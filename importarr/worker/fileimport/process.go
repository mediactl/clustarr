/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package fileimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// metricKindMovie is the bounded `kind` label on the import metrics, mirroring
// importarr/worker/rescan's identical constant.
const metricKindMovie = "movie"

// processConfig is everything one Download's import needs, so [processConfig.run]
// takes no parameter besides ctx.
type processConfig struct {
	worker   *Worker
	message  events.Message
	download *downloadv1alpha1.Download
	// target is the MediaFile's spec.mediaRef: the Download's spec.target,
	// or what its import-target annotation redirected the import to.
	target commonv1.MediaRef
	// manual is DownloadSpec.Manual or an import-override=true annotation.
	manual     bool
	movie      *catalogv1alpha1.Movie
	rootFolder *catalogv1alpha1.RootFolder
	profile    quality.Profile
	// existing is the target's current MediaFile, or nil when this is the
	// first file imported for it.
	existing             *catalogv1alpha1.MediaFile
	engine               naming.Engine
	baseContext          naming.Context
	originalLanguageName string
}

// importOutcome accumulates one Download's import result across every file
// the walk visited.
type importOutcome struct {
	imported   []*downloadac.ImportedFileApplyConfiguration
	rejections []string
}

// run walks the Download's content root and imports every media file it can
// confidently attribute and clear, in the never-guess sense: a file this
// worker cannot parse, or that the quality profile rejects, is recorded in
// outcome.rejections rather than imported speculatively.
//
// A non-nil error means the walk was aborted by an environmental failure
// (insufficient disk space, a filesystem error moving or linking a file) --
// not by an individual file being unsuitable, which is a rejection, not an
// abort. The caller decides whether that is worth retrying based on
// [Worker.finalAttempt].
func (pc *processConfig) run(ctx context.Context) (importOutcome, error) {
	var out importOutcome
	var lastHeartbeat time.Time
	recycledOld := false

	root := pc.download.Status.ContentRoot
	classifier := ClassifierFor(commonv1.MediaKindMovie, root, pc.worker.SampleMaxBytes)
	besideMedia := false
	if pc.manual {
		n, err := countMedia(ctx, classifier, root, 1)
		if err != nil {
			return out, err
		}
		besideMedia = n > 0
	}
	var cands []fileCandidate
	err := classifier.Walk(ctx, root, func(srcPath string, info os.FileInfo, class fsops.FileClass) error {
		if err := pc.worker.beat(ctx, pc.message, &lastHeartbeat); err != nil {
			return err
		}
		if rejection, candidate := pc.worker.admit(root, srcPath, info, class, pc.manual, besideMedia); !candidate {
			if rejection != "" {
				out.rejections = append(out.rejections, rejection)
			}
			return nil
		}
		c := fileCandidate{path: srcPath, info: info}
		if p, err := release.ParsePath(srcPath, release.Options{Kind: commonv1.MediaKindMovie}); err == nil {
			c.ranked(pc.profile, p.Quality, p.Revision, true)
		}
		cands = append(cands, c)
		return nil
	}, out.unreadable(root))
	if err != nil {
		return out, err
	}

	// A movie holds one file: the best candidate is imported and every
	// other is rejected (order.go).
	sortCandidates(cands)
	filledBy := ""
	for _, c := range cands {
		if err := pc.worker.beat(ctx, pc.message, &lastHeartbeat); err != nil {
			return out, err
		}
		rel := relPath(root, c.path)
		if filledBy != "" {
			out.rejections = append(out.rejections, c.lateRejection(rel, "movie "+pc.target.Name, filledBy))
			continue
		}
		imported, rejection, err := pc.processFile(ctx, c.path, c.info, &recycledOld)
		if err != nil {
			return out, err
		}
		if rejection != "" {
			out.rejections = append(out.rejections, rejection)
			continue
		}
		out.imported = append(out.imported, imported)
		filledBy = rel
	}
	return out, nil
}

// unreadable is the fsops.UnreadableFunc both import walks use: an entry of
// the download the walk could not read is a rejection naming it, and the
// walk carries on with the rest -- one unreadable folder in a release (a
// permissions slip on an extras folder) must not keep its readable files
// out of the library. The content root itself failing is still the walk's
// error, which a redelivery retries.
func (o *importOutcome) unreadable(root string) fsops.UnreadableFunc {
	return func(path string, err error) error {
		o.rejections = append(o.rejections, fmt.Sprintf("%s: could not be read, so nothing in it was imported: %v",
			relPath(root, path), err))
		return nil
	}
}

// admit decides what one walked file's class means for an import, for the
// movie walk and the non-video walk alike: candidate is true for a file the
// import goes on to attribute and clear.
//
//   - media: a candidate.
//   - a suspected sample (fsops.ClassSuspectedSample, a video file only the
//     size floor flags): never dropped silently, because a size alone cannot
//     tell a promo clip from a real short film. It is a rejection naming its
//     size and the threshold, so status.import says why the file was left
//     behind -- and a candidate under a manual import, a person's
//     instruction to import this download's files, exactly as a manual
//     import accepts a non-video quality this worker cannot determine. But
//     not when the download also holds real media (besideMedia): a person
//     importing a release imports the release, and the small video beside
//     it is its promo clip, which a manual import used to take along. It
//     stays a rejection then, saying so.
//   - a part, an extra, a name-marked sample or a non-media file: neither a
//     candidate nor reported. Those are the release's own packaging -- a
//     "-sample" file ships in nearly every scene release beside the real
//     one, and its name is the releaser's declaration, not this worker's
//     inference -- and listing each would bury the rejections a person has
//     to act on.
func (w *Worker) admit(
	root, path string, info os.FileInfo, class fsops.FileClass, manual, besideMedia bool,
) (rejection string, candidate bool) {
	switch class {
	case fsops.ClassMedia:
		return "", true
	case fsops.ClassSuspectedSample:
		switch {
		case manual && !besideMedia:
			return "", true
		case manual:
			return fmt.Sprintf("%s: %s; left behind by this manual import because the download also holds real "+
				"media, and this is the promo clip beside it", relPath(root, path), SuspectedSampleReason(info.Size(), w.SampleMaxBytes)), false
		}
		return fmt.Sprintf("%s: %s; only a manual import (spec.manual, or %s=true) imports it",
			relPath(root, path), SuspectedSampleReason(info.Size(), w.SampleMaxBytes), AnnotationImportOverride), false
	default:
		return "", false
	}
}

// processFile imports one media file, or explains why it was rejected.
// A non-nil error means the whole walk must abort; see [processConfig.run].
func (pc *processConfig) processFile(
	ctx context.Context, srcPath string, info os.FileInfo, recycledOld *bool,
) (imported *downloadac.ImportedFileApplyConfiguration, rejection string, fatal error) {
	log := logging.FromContext(ctx)
	rel := relPath(pc.download.Status.ContentRoot, srcPath)

	parsed, perr := release.ParsePath(srcPath, release.Options{Kind: commonv1.MediaKindMovie})
	if perr != nil {
		return nil, fmt.Sprintf("%s: could not parse the filename: %v", rel, perr), nil
	}
	// A file whose name names no language takes the movie's original
	// language, as Radarr's AggregateLanguages does ("Use movie language as
	// fallback if we couldn't parse a language"). Resolved once, here, so
	// the score below and the languages frozen into the spec agree -- the
	// order pkg/decision.Evaluate uses for a release. parsed.Group is the
	// parser's own: pkg/release ports Radarr's ReleaseGroupParser, so the
	// quality-token guard this worker once carried would only drop a real
	// group that shares a quality word.
	parsed.Languages = parsed.LanguagesFor(pc.originalLanguageName)

	if !pc.profile.Allowed(parsed.Quality) {
		return nil, notAllowedRejection(rel, parsed.Quality), nil
	}

	// ReleaseTitle custom formats (repack/proper, HDR, codecs, streaming
	// services) read the release's full name and the file's: Radarr's
	// LocalMovie input, the scene name -- the Download's release title --
	// else the file name, and the file name as Filename.
	releaseTitle := pc.download.Spec.Release.Title
	if releaseTitle == "" {
		releaseTitle = filepath.Base(srcPath)
	}
	ic := catalogue.ItemContext{
		OriginalLanguageName: pc.originalLanguageName, ReleaseType: parsed.ReleaseType,
		ReleaseTitle: releaseTitle, Filename: filepath.Base(srcPath),
	}
	score, matched := pc.profile.Score(ctx, pc.worker.Catalogue, parsed, ic)

	// A transcoded file is final: only a person's choice replaces it
	// (transcoded.go). Checked before the upgrade comparison, so the
	// rejection says why rather than reporting a quality verdict.
	if pc.existing != nil {
		if r := transcodedRejection(rel, pc.existing, pc.download, pc.manual); r != "" {
			return nil, r, nil
		}
	}
	if pc.existing != nil && !pc.manual {
		current := quality.Candidate{
			Quality:     pc.existing.Spec.Quality,
			Revision:    pc.existing.Spec.Revision,
			FormatScore: int(pc.existing.Spec.FormatScore),
		}
		candidate := quality.Candidate{Quality: parsed.Quality, Revision: parsed.Revision, FormatScore: score}
		if verdict := pc.profile.UpgradeDecision(current, candidate); verdict != quality.Upgrade {
			return nil, fmt.Sprintf("%s: %s", rel, verdictMessage(verdict)), nil
		}
	}

	nctx := pc.baseContext
	nctx.Quality = parsed.Quality
	nctx.Revision = parsed.Revision
	nctx.ReleaseGroup = parsed.Group
	nctx.Edition = parsed.Edition
	nctx.CustomFormats = matched

	dest, derr := destinationPath(pc.rootFolder.Spec.Path, pc.movie.Spec.Folder, srcPath, pc.engine, nctx)
	if derr != nil {
		return nil, fmt.Sprintf("%s: could not render a destination path: %v", rel, derr), nil
	}

	needed := info.Size() + pc.rootFolder.Spec.MinFreeBytes
	if err := fsops.EnsureFreeSpace(pc.rootFolder.Spec.Path, needed); err != nil {
		return nil, "", fmt.Errorf("fileimport: %w", err)
	}

	mode := fsops.ImportHardlink
	if pc.download.Status.CanMoveFiles {
		mode = fsops.ImportMove
	}
	if err := placeFile(ctx, pc.rootFolder.Spec.RecycleBin.Path, srcPath, info, dest, mode); err != nil {
		if errors.Is(err, errWouldOverwrite) {
			return nil, fmt.Sprintf("%s: %v", rel, err), nil
		}
		return nil, "", err
	}

	destInfo, serr := os.Stat(dest)
	if serr != nil {
		return nil, "", fmt.Errorf("fileimport: stat imported file %s: %w", dest, serr)
	}

	mfName := k8s.ChildName(pc.movie.Name, "mediafile", dest)
	spec := catalogac.MediaFileSpec().
		WithMediaRef(pc.target).
		WithPath(dest).
		WithSizeBytes(destInfo.Size()).
		WithModTime(metav1.NewTime(destInfo.ModTime())).
		WithQuality(parsed.Quality).
		WithRevision(parsed.Revision).
		WithReleaseType(parsed.ReleaseType).
		WithReleaseGroup(parsed.Group).
		WithEdition(parsed.Edition).
		WithFormatScore(int32(score)). //nolint:gosec // a custom-format score is a small bounded sum, never near int32's range
		WithProfileHash(pc.profile.Hash).
		WithOriginal(true).
		WithImportedFrom(catalogac.ImportSource().
			WithDownloadRef(pc.download.Name).
			WithReleaseTitle(pc.download.Spec.Release.Title).
			WithIndexerName(pc.download.Spec.Release.IndexerName).
			WithProtocol(pc.download.Spec.Release.Protocol).
			WithImportedAt(metav1.NewTime(pc.worker.now())).
			WithManual(pc.manual))
	if len(matched) > 0 {
		spec = spec.WithMatchedFormats(capMatchedFormats(matched)...)
	}
	if len(parsed.Languages) > 0 {
		spec = spec.WithLanguages(parsed.Languages...)
	}

	if err := pc.worker.applyMediaFile(ctx, mfName, spec, pc.movie.Namespace); err != nil {
		return nil, "", err
	}

	if pc.existing != nil && !*recycledOld && pc.existing.Spec.Path != dest {
		if _, err := fsops.Recycle(fsops.RecycleBinPath(pc.rootFolder.Spec.RecycleBin.Path), pc.existing.Spec.Path); err != nil {
			log.Warn("fileimport: could not recycle the replaced file; leaving it in place",
				"path", pc.existing.Spec.Path, "error", err)
		} else if err := pc.worker.Client.Delete(ctx, pc.existing); err != nil {
			log.Warn("fileimport: could not delete the replaced media file object",
				"mediaFile", pc.existing.Name, "error", err)
		}
		*recycledOld = true
	}

	metrics.ImportFilesTotal.WithLabelValues(metricKindMovie, mode.String(), "imported").Inc()
	log.Info("fileimport: imported a file", "source", rel, "dest", dest, "mediaFile", mfName, "formatScore", score)

	return downloadac.ImportedFile().
		WithSourcePath(rel).
		WithDestPath(dest).
		WithMediaFileRef(mfName), "", nil
}

// relPath renders srcPath relative to root, matching
// ImportedFile.SourcePath's documented contract ("the path the file had
// inside the download"). It falls back to the absolute path when the two
// are unrelated, mirroring importarr/worker/rescan.relPath.
func relPath(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

// capMatchedFormats caps the matched-formats list at MediaFileSpec.
// MatchedFormats' own MaxItems (200), keeping the highest-scoring surprise
// out of the list is not this function's job -- catalogue.Profile.Score
// already returns every format that matched, and 200 formats matching one
// release would itself be a catalogue bug, not a normal case this needs to
// handle gracefully beyond not exceeding the CRD's cap.
func capMatchedFormats(matched []string) []string {
	const maxMatchedFormats = 200
	if len(matched) <= maxMatchedFormats {
		return matched
	}
	return matched[:maxMatchedFormats]
}

// verdictMessage renders a non-Upgrade quality.Verdict as a rejection
// reason. It is a small, local mapping rather than a reuse of
// pkg/decision.VerdictReason, which that function's own doc comment reserves
// for pkg/decision's tests.
func verdictMessage(v quality.Verdict) string {
	switch v {
	case quality.ExistingBetterQuality:
		return "the existing file already has a better quality"
	case quality.UpgradesNotAllowed:
		return "the quality profile does not allow upgrades"
	case quality.ExistingBetterRevision:
		return "the existing file already has a better proper/repack revision"
	case quality.QualityCutoffMet:
		return "the existing file already meets the quality profile's cutoff"
	case quality.FormatScoreNotHigher:
		return "the candidate's custom-format score is not higher than the existing file's"
	case quality.FormatCutoffMet:
		return "the existing file already meets the quality profile's custom-format cutoff"
	case quality.FormatIncrementTooSmall:
		return "the candidate's custom-format score improvement is below the profile's minimum increment"
	default:
		return fmt.Sprintf("not an upgrade over the existing file (verdict %d)", v)
	}
}
