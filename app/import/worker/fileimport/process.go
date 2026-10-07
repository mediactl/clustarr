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
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// videoProbeTimeout bounds probeVideo's probe (the container, then the first
// frame, read in-process by the domain's Prober). A healthy file answers in well under a second, even over a
// network mount; one that hangs -- a stalled mount, a pathological file --
// must not hold the import handler past its delivery's acknowledgement
// deadline, ConsumerImportFile's BackOff[0] (30s; HeartbeatInterval says
// why not its AckWait), so a timeout is a probe failure like any other and
// the file imports under its name. probeVideo heartbeats immediately before
// it probes, and TestTheImportFitsTheFileConsumersAckDeadline holds this
// and HeartbeatInterval to that deadline together.
const videoProbeTimeout = 15 * time.Second

// metricKindMovie is the bounded `kind` label on the import metrics, mirroring
// app/import/worker/rescan's identical constant.
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
	// existing is every MediaFile the movie has now (existingMovieFiles),
	// empty when this is the first file imported for it.
	existing             []catalogv1alpha1.MediaFile
	baseContext          naming.Context
	originalLanguageName string
}

// importOutcome accumulates one Download's import result across every file
// the walk visited.
type importOutcome struct {
	imported   []*downloadac.ImportedFileApplyConfiguration
	rejections []rejection
}

// sampleIsIncidental downgrades a suspected sample's rejection once the walk
// has found real media beside it: then it is the release's promo clip and
// decides nothing; alone, a size cannot tell it from a short film, and a
// person must say (needsPerson).
func (o *importOutcome) sampleIsIncidental(candidates int) {
	if candidates == 0 {
		return
	}
	for i := range o.rejections {
		if o.rejections[i].sample {
			o.rejections[i].class = ""
		}
	}
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
			if !rejection.none() {
				out.rejections = append(out.rejections, rejection)
			}
			return nil
		}
		c := fileCandidate{path: srcPath, info: info}
		if p, err := parseMediaFile(srcPath, pc.download.Spec.Release.Title, commonv1.MediaKindMovie); err == nil {
			c.ranked(pc.profile, p.Quality, p.Revision, true)
		}
		cands = append(cands, c)
		return nil
	}, out.unreadable(root))
	if err != nil {
		return out, err
	}
	out.sampleIsIncidental(len(cands))

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
		if !rejection.none() {
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
		o.rejections = append(o.rejections, transientRejection("%s: could not be read, so nothing in it was imported: %v",
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
) (r rejection, candidate bool) {
	switch class {
	case fsops.ClassMedia:
		return rejection{}, true
	case fsops.ClassSuspectedSample:
		switch {
		case manual && !besideMedia:
			return rejection{}, true
		case manual:
			return incidentalRejection("%s: %s; left behind by this manual import because the download also holds real "+
				"media, and this is the promo clip beside it", relPath(root, path), SuspectedSampleReason(info.Size(), w.SampleMaxBytes)), false
		}
		r := needsPersonRejection("%s: %s; only a manual import (spec.manual, or %s=true) imports it",
			relPath(root, path), SuspectedSampleReason(info.Size(), w.SampleMaxBytes), importtarget.AnnotationImportOverride)
		r.sample = true
		return r, false
	default:
		return rejection{}, false
	}
}

// parseMediaFile parses a media file's name, and when that names nothing
// -- an obfuscated post's "2ef6f194995e4a11b055d0f2354ef0ba.mp4", the
// first grab on the owner's cluster (2026-09-24) -- the release title the
// Download carries: Radarr's ImportDecisionMaker falls back from the
// file's name (FileMovieInfo) to the download client item's title, and
// Sonarr does the same for a single episode. The Download already names
// the item, so the parse serves the quality, revision, group and
// languages, which the release title carries as well as any file name.
// An empty releaseTitle (a pack, whose title names none of its files) is
// no fallback, and the file's own parse error is the one reported.
func parseMediaFile(srcPath, releaseTitle string, kind commonv1.MediaKind) (*release.ParsedRelease, error) {
	p, err := release.ParsePath(srcPath, release.Options{Kind: kind})
	if err == nil || releaseTitle == "" {
		return p, err
	}
	fp, ferr := release.Parse(releaseTitle, release.Options{Kind: kind})
	if ferr != nil {
		return nil, err
	}
	return fp, nil
}

// processFile imports one media file, or explains why it was rejected.
// A non-nil error means the whole walk must abort; see [processConfig.run].
func (pc *processConfig) processFile(
	ctx context.Context, srcPath string, info os.FileInfo, recycledOld *bool,
) (imported *downloadac.ImportedFileApplyConfiguration, rejected rejection, fatal error) {
	log := logging.FromContext(ctx)
	rel := relPath(pc.download.Status.ContentRoot, srcPath)

	parsed, perr := parseMediaFile(srcPath, pc.download.Spec.Release.Title, commonv1.MediaKindMovie)
	if perr != nil {
		return nil, needsPersonRejection("%s: could not parse the filename: %v", rel, perr), nil
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

	// The probe corrects the name's resolution (and a false remux) before
	// the profile judges the quality, so a "2160p" name on a 1080p stream
	// is admitted, compared and frozen as the 1080p it is.
	mi, err := pc.worker.probeVideo(ctx, pc.message, srcPath, rel)
	if err != nil {
		return nil, rejection{}, err
	}
	parsed.Quality, _ = quality.AugmentFromMediaInfo(parsed.Quality, mi)

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

	// The gates judge the file against every one the movie has but this
	// import's own, placed by an earlier delivery that died before it could
	// say so (ownEarlierAttempt): against itself, a file is never an upgrade.
	compared := comparedFiles(pc.existing, pc.download)
	// A transcoded file is final: only a person's choice replaces it
	// (transcoded.go), whichever of the movie's files it is. Checked before
	// the upgrade comparison, so the rejection says why rather than
	// reporting a quality verdict.
	for i := range compared {
		if r := transcodedRejection(rel, &compared[i], pc.download, pc.manual); !r.none() {
			return nil, r, nil
		}
	}
	// The file replaces every one the movie has, so it must be an upgrade
	// over each, as an episode file must over each file it replaces.
	if !pc.manual {
		candidate := quality.Candidate{Quality: parsed.Quality, Revision: parsed.Revision, FormatScore: score}
		originalTag := ""
		if pc.movie.Status.Metadata != nil {
			originalTag = pc.movie.Status.Metadata.OriginalLanguage
		}
		for i := range compared {
			mf := &compared[i]
			current := quality.Candidate{
				Quality: mf.Spec.Quality, Revision: mf.Spec.Revision, FormatScore: int(mf.Spec.FormatScore),
			}
			// A file whose audio lacks the profile's language is replaced by
			// one whose probe carries it, upgrade or not (anime dual-audio
			// spec §5.3): the search grabbed it for that.
			if verdict := pc.profile.UpgradeDecision(current, candidate); verdict != quality.Upgrade &&
				!replacesWrongLanguage(pc.profile, originalTag, mf, mi) {
				text := fmt.Sprintf("%s: %s", rel, verdictMessage(verdict))
				if len(compared) > 1 {
					text = fmt.Sprintf("%s (MediaFile %s)", text, mf.Name)
				}
				return nil, verdictRejection(pc.profile, pc.download, parsed.Quality, text), nil
			}
		}
	}

	frozen := frozenRelease(parsed, matched, pc.download.Spec.Release.Title)
	nctx := catalogctx.File(ctx, pc.baseContext, frozen, mi)
	dest, derr := catalogctx.MovieFilePath(pc.rootFolder, pc.movie, nctx, catalogctx.ContainerExt(mi, srcPath))
	if derr != nil {
		// A path that cannot be rendered is the item's or the root folder's
		// fault (a folder override that climbs out of the library, a
		// dot-only name), not the release's: blocked, as the non-video
		// paths already are, never a rejection that grabarr would read as
		// a bad release to blocklist and delete.
		return nil, rejection{}, blocked("%s: could not render a destination path: %v", rel, derr)
	}

	needed := info.Size() + pc.rootFolder.Spec.MinFreeBytes
	if err := fsops.EnsureFreeSpace(pc.rootFolder.Spec.Path, needed); err != nil {
		return nil, rejection{}, fmt.Errorf("fileimport: %w", err)
	}

	mode := fsops.ImportHardlink
	if pc.download.Status.CanMoveFiles {
		mode = fsops.ImportMove
	}
	if err := placeFile(ctx, pc.rootFolder.Spec.Path, pc.rootFolder.Spec.RecycleBin.Path, srcPath, info, dest, mode); err != nil {
		if errors.Is(err, errWouldOverwrite) {
			return nil, needsPersonRejection("%s: %v", rel, err), nil
		}
		return nil, rejection{}, err
	}

	destInfo, serr := os.Stat(dest)
	if serr != nil {
		return nil, rejection{}, fmt.Errorf("fileimport: stat imported file %s: %w", dest, serr)
	}

	mfName := k8s.ChildName(pc.movie.Name, "mediafile", dest)
	spec := catalogac.MediaFileSpec().
		WithMediaRef(pc.target).
		WithPath(dest).
		WithSizeBytes(destInfo.Size()).
		WithModTime(metav1.NewTime(destInfo.ModTime())).
		WithQuality(frozen.Quality).
		WithRevision(frozen.Revision).
		WithReleaseType(parsed.ReleaseType).
		WithReleaseGroup(frozen.ReleaseGroup).
		WithEdition(frozen.Edition).
		WithFormatScore(int32(score)). //nolint:gosec // a custom-format score is a small bounded sum, never near int32's range
		WithProfileHash(pc.profile.Hash).
		WithOriginal(true).
		WithImportedFrom(catalogac.ImportSource().
			WithDownloadRef(pc.download.Name).
			WithReleaseTitle(frozen.ImportedFrom.ReleaseTitle).
			WithIndexerName(importText(pc.download.Spec.Release.IndexerName, catalogv1alpha1.MaxIndexerNameLength)).
			WithProtocol(pc.download.Spec.Release.Protocol).
			WithImportedAt(metav1.NewTime(pc.worker.now())).
			WithManual(pc.manual))
	if len(frozen.MatchedFormats) > 0 {
		spec = spec.WithMatchedFormats(frozen.MatchedFormats...)
	}
	if len(parsed.Languages) > 0 {
		spec = spec.WithLanguages(parsed.Languages...)
	}

	uid, err := pc.worker.applyMediaFile(ctx, mfName, spec, pc.movie.Namespace)
	if err != nil {
		return nil, rejection{}, err
	}
	pc.worker.seedProbe(ctx, pc.movie.Namespace, mfName, uid, dest, destInfo, mi)

	// Every file the movie had is replaced (a movie holds one file), as the
	// episode path replaces each file of the episodes it covers. A file
	// already gone from disk still has its MediaFile removed.
	if !*recycledOld {
		bin := fsops.RecycleBinPath(pc.rootFolder.Spec.RecycleBin.Path)
		for i := range pc.existing {
			old := &pc.existing[i]
			if old.Spec.Path == dest || old.Name == mfName {
				continue
			}
			if _, err := fsops.Recycle(bin, old.Spec.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Warn("fileimport: could not recycle the replaced file; leaving it in place",
					"path", old.Spec.Path, "error", err)
				continue
			}
			if err := pc.worker.Client.Delete(ctx, old); client.IgnoreNotFound(err) != nil {
				log.Warn("fileimport: could not delete the replaced media file object",
					"mediaFile", old.Name, "error", err)
			}
		}
		*recycledOld = true
	}

	metrics.ImportFilesTotal.WithLabelValues(metricKindMovie, mode.String(), "imported").Inc()
	log.Info("fileimport: imported a file", "source", rel, "dest", dest, "mediaFile", mfName, "formatScore", score)

	return downloadac.ImportedFile().
		WithSourcePath(rel).
		WithDestPath(dest).
		WithMediaFileRef(mfName), rejection{}, nil
}

// probeVideo probes a video file an import is about to judge: its
// technical description corrects the name-derived quality
// (quality.AugmentFromMediaInfo) and names the file for its codec and
// dynamic range (catalogctx.File). A probe failure never fails the import
// -- an unprobeable file imports under its name-derived quality and its
// source extension, exactly as it did before imports probed, since every
// MediaInfo block of a preset is optional -- so it is logged and nil
// returned. The probe gets videoProbeTimeout, and running out of it is
// such a failure. It heartbeats on m immediately before it probes, so the
// probe's whole bound lies inside the delivery's ack deadline however long
// the file loop has gone since its last beat; a failed heartbeat is the
// error, which aborts the import as the loop's own heartbeat failure does.
// It probes through w.Prober, the import domain's one prober (spec
// 2026-10-06 §6.6); a worker with none probes nothing and returns nil, nil,
// so the file imports under its name-derived quality.
func (w *Worker) probeVideo(ctx context.Context, m events.Message, srcPath, rel string) (*commonv1.MediaInfo, error) {
	if w.Prober == nil {
		return nil, nil
	}
	if err := heartbeat(ctx, m); err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, videoProbeTimeout)
	defer cancel()
	mi, _, err := w.Prober.Probe(pctx, srcPath)
	if err != nil {
		logging.FromContext(ctx).Warn("fileimport: could not probe the file; importing it under its name-derived quality",
			"source", rel, "error", err)
		return nil, nil
	}
	return mi, nil
}

// frozenRelease is the release-time half of the MediaFileSpec an import
// freezes -- quality, revision, group, edition, matched formats and the
// release title -- built once, so the spec the worker applies and the
// naming context catalogctx.File renders the destination from read the same
// values, and a later rename of the file renders the name the import did.
func frozenRelease(parsed *release.ParsedRelease, matched []string, releaseTitle string) *catalogv1alpha1.MediaFileSpec {
	return &catalogv1alpha1.MediaFileSpec{
		Quality: parsed.Quality, Revision: parsed.Revision,
		ReleaseGroup:   importText(parsed.Group, catalogv1alpha1.MaxReleaseGroupLength),
		Edition:        importText(parsed.Edition, catalogv1alpha1.MaxEditionLength),
		MatchedFormats: capMatchedFormats(matched),
		ImportedFrom:   &catalogv1alpha1.ImportSource{ReleaseTitle: importText(releaseTitle, catalogv1alpha1.MaxReleaseTitleLength)},
	}
}

// importText is s as a MediaFile spec field bounded at maxBytes holds it:
// the runes a server-side apply cannot carry replaced, then cut on a rune
// boundary (loop spec §2.11.2). A Download's release fields are unbounded,
// and the apiserver refuses an over-long one whole, which would fail the
// import.
func importText(s string, maxBytes int) string {
	return k8s.ClampText(k8s.SanitizeText(s), maxBytes)
}

// relPath renders srcPath relative to root, matching
// ImportedFile.SourcePath's documented contract ("the path the file had
// inside the download"). It falls back to the absolute path when the two
// are unrelated, mirroring app/import/worker/rescan.relPath.
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
