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

	err := fsops.Walk(ctx, pc.download.Status.ContentRoot, func(srcPath string, info os.FileInfo, class fsops.FileClass) error {
		if err := pc.worker.beat(ctx, pc.message, &lastHeartbeat); err != nil {
			return err
		}
		if class != fsops.ClassMedia {
			return nil
		}

		imported, rejection, err := pc.processFile(ctx, srcPath, info, &recycledOld)
		if err != nil {
			return err
		}
		if rejection != "" {
			out.rejections = append(out.rejections, rejection)
			return nil
		}
		out.imported = append(out.imported, imported)
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, nil
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
	parsed.Group = releaseGroupOrEmpty(parsed)

	if !pc.profile.Allowed(parsed.Quality) {
		return nil, fmt.Sprintf("%s: quality %s is not allowed by the quality profile", rel, parsed.Quality.Name), nil
	}

	ic := catalogue.ItemContext{OriginalLanguageName: pc.originalLanguageName, ReleaseType: parsed.ReleaseType}
	score, matched := pc.profile.Score(ctx, pc.worker.Catalogue, parsed, ic)

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
	if err := fsops.Import(ctx, srcPath, dest, mode); err != nil {
		return nil, "", fmt.Errorf("fileimport: import %s: %w", rel, err)
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
		if _, err := fsops.Recycle(pc.rootFolder.Spec.RecycleBin.Path, pc.existing.Spec.Path); err != nil {
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
