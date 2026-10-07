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

// Package specwrite is the MediaFile spec's Kubernetes writers, split from
// app/import/mediafilespec (ADR-0019 A3.8) so the import agent links none:
// Apply and ApplyFacts (the one render of importarr-worker's MediaFileSpec)
// and RenameFile. The manager's import materialisation and rename actuator
// call them; the rescan until A6.1 moves its writes to the manager.
package specwrite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// Apply applies importarr's complete MediaFileSpec for one MediaFile
// under [mediafilespec.FieldManager]: ref, path, the size and mtime info observed, and
// every frozen field f carries. It is the one render of this manager's set
// on a MediaFile -- the rescan and the rename both go through it, so neither
// can become a second, narrower apply that releases what the other sends.
//
// A nil info sends no size or mtime: a transcoded file's are catalogarr's
// (see [RenameFile]). A non-empty rv is the resourceVersion the caller read,
// sent as a precondition, and a refusal for that reason comes back as a
// *[mediafilespec.StaleReadError].
//
// It returns the applied MediaFile's UID: the rescan seeds its probe record
// under it (spec 2026-10-06 §6.6).
func Apply(
	ctx context.Context, c client.Client, namespace, name, rv string,
	ref commonv1.MediaRef, path string, info os.FileInfo, f mediafilespec.Frozen,
) (types.UID, error) {
	var (
		size int64
		mod  time.Time
	)
	if info != nil {
		size, mod = info.Size(), info.ModTime()
	}
	return applyFacts(ctx, c, namespace, name, rv, ref, path, info != nil, size, mod, f)
}

// ApplyFacts is Apply with the observed size and mtime as values, for a
// caller that holds no os.FileInfo: the manager materialising a placed file
// from an import's execute record (ADR-0019 §6.9).
func ApplyFacts(
	ctx context.Context, c client.Client, namespace, name, rv string, ref commonv1.MediaRef,
	path string, sizeBytes int64, modTime time.Time, f mediafilespec.Frozen,
) (types.UID, error) {
	return applyFacts(ctx, c, namespace, name, rv, ref, path, true, sizeBytes, modTime, f)
}

func applyFacts(
	ctx context.Context, c client.Client, namespace, name, rv string, ref commonv1.MediaRef,
	path string, observed bool, sizeBytes int64, modTime time.Time, f mediafilespec.Frozen,
) (types.UID, error) {
	spec := catalogac.MediaFileSpec().
		WithMediaRef(ref).
		WithPath(path)
	if observed {
		spec = spec.WithSizeBytes(sizeBytes).WithModTime(metav1.NewTime(modTime))
	}
	if f.Quality != nil {
		spec = spec.WithQuality(*f.Quality)
	}
	if f.Revision != nil {
		spec = spec.WithRevision(*f.Revision)
	}
	if f.ReleaseType != "" {
		spec = spec.WithReleaseType(f.ReleaseType)
	}
	if f.ReleaseGroup != nil {
		spec = spec.WithReleaseGroup(*f.ReleaseGroup)
	}
	if f.Edition != nil {
		spec = spec.WithEdition(*f.Edition)
	}
	if len(f.Languages) > 0 {
		spec = spec.WithLanguages(f.Languages...)
	}
	if f.ImportedFrom != nil {
		spec = spec.WithImportedFrom(f.ImportedFrom)
	}
	if f.FormatScore != nil {
		spec = spec.WithFormatScore(*f.FormatScore)
	}
	if len(f.MatchedFormats) > 0 {
		spec = spec.WithMatchedFormats(f.MatchedFormats...)
	}
	if f.ProfileHash != "" {
		spec = spec.WithProfileHash(f.ProfileHash)
	}
	if f.Original != nil {
		spec = spec.WithOriginal(*f.Original)
	}

	ac := catalogac.MediaFile(name, namespace).WithSpec(spec)
	if rv != "" {
		ac = ac.WithResourceVersion(rv)
	}
	applied, err := k8s.Apply(ctx, c, mediafilespec.FieldManager, ac)
	if err != nil {
		if rv != "" && apierrors.IsConflict(err) {
			return "", &mediafilespec.StaleReadError{
				Key: types.NamespacedName{Namespace: namespace, Name: name}, RV: rv,
				Err: fmt.Errorf("rescan: apply media file %s: %w", name, err),
			}
		}
		return "", fmt.Errorf("rescan: apply media file %s: %w", name, err)
	}
	return ptr.Deref(applied.UID, ""), nil
}

// RenameFile moves one library file to the canonical path catalogarr
// proposes in status.naming, and applies importarr's complete MediaFileSpec
// with the new path, the moved file's size and mtime, and the
// probe-corrected quality status.naming carries (spec §5).
//
// It re-reads the MediaFile through api -- the uncached reader --
// immediately before deciding (CLAUDE.md's lost-update rule), and refuses,
// touching nothing, when there is nothing to do or it may not move the file:
// see the Rename* reasons. The file's size and mtime must still match spec
// and nothing may exist at the proposed path, both checked immediately
// before the move; the move itself is [fsops.MoveNoReplace], which refuses
// a file that appears at the proposed path after the check rather than
// replacing it. A dry run returns after the checks with [mediafilespec.RenameDryRun].
//
// The apply goes through [Apply], the render the rescan uses, under
// [mediafilespec.FieldManager], carrying the resourceVersion just read. If it fails, the
// MediaFile is read again -- an apply whose response was lost may still
// have landed -- and only if it does not already name the new path is the
// file moved back, so spec.path never names a path with no file; a
// MediaFile that changed in the meantime reads [mediafilespec.RenameChanged]. A rename
// that moved the file and then died before its apply is rolled forward on
// the next call: spec.path is gone and the proposed path holds a file with
// spec's fingerprint, so the apply is made without a move.
//
// With keepFolder, the target is the proposed file name in the file's own
// folder, and a proposed folder that differs is not a reason to hold it:
// the rename controller's renameTranscoded mode, which renames a transcoded
// file without splitting its season or film folder. Without it, a proposed
// path in another folder is [mediafilespec.RenameHeld], as it has always been.
//
// A transcoded file (spec.original false) has its size, mtime and original
// flag left out of the apply: catalogarr owns those once it incorporates a
// swap.
//
// Once the file has moved, each sidecar in status.sidecars sharing its stem
// is moved to the new stem, keeping its language and flag suffix. That is
// best effort: a sidecar that cannot be moved is logged, never a failed
// rename. catalogarr re-observes everything else on its own.
func RenameFile(
	ctx context.Context, c client.Client, api client.Reader, mf *catalogv1alpha1.MediaFile, dryRun, keepFolder bool,
) (mediafilespec.RenameOutcome, error) {
	var fresh catalogv1alpha1.MediaFile
	if err := api.Get(ctx, client.ObjectKeyFromObject(mf), &fresh); err != nil {
		return mediafilespec.RenameOutcome{}, fmt.Errorf("rescan: rename: read media file %s: %w", mf.Name, err)
	}
	out := mediafilespec.RenameOutcome{From: fresh.Spec.Path}
	n := fresh.Status.Naming
	switch {
	case n == nil || n.ExpectedPath == "" || n.Current || filepath.Clean(fresh.Spec.Path) == n.ExpectedPath:
		out.Reason = mediafilespec.RenameNotCurrent
		return out, nil
	case n.Reason != "":
		out.Reason = mediafilespec.RenameHeld
		return out, nil
	}
	out.To = n.ExpectedPath
	if keepFolder {
		// The canonical file name in the file's own folder: a canonical
		// folder that differs ("Season 03" for "Season 3") is left alone.
		out.To = filepath.Join(filepath.Dir(filepath.Clean(out.From)), filepath.Base(n.ExpectedPath))
		if out.To == filepath.Clean(out.From) {
			out.Reason = mediafilespec.RenameNotCurrent
			return out, nil
		}
	}
	if filepath.Dir(out.To) != filepath.Dir(filepath.Clean(out.From)) {
		out.Reason = mediafilespec.RenameHeld
		return out, nil
	}

	fromInfo, fromErr := os.Stat(out.From)
	if fromErr != nil && !errors.Is(fromErr, fs.ErrNotExist) {
		return out, fmt.Errorf("rescan: rename: stat %s: %w", out.From, fromErr)
	}
	toInfo, toErr := os.Lstat(out.To)
	switch {
	case toErr != nil && !errors.Is(toErr, fs.ErrNotExist):
		return out, fmt.Errorf("rescan: rename: check %s: %w", out.To, toErr)
	case toErr == nil && fromErr != nil:
		// Only the proposed path exists. If it holds the file spec records,
		// an earlier rename moved it and never recorded the move.
		if !toInfo.Mode().IsRegular() || !mediafilespec.SameFingerprint(&fresh, toInfo) {
			out.Reason = mediafilespec.RenameCollision
			return out, nil
		}
		if dryRun {
			out.Reason = mediafilespec.RenameDryRun
			return out, nil
		}
		logging.FromContext(ctx).Info("the file is already at its proposed path; recording the rename",
			"mediaFile", fresh.Name, "from", out.From, "to", out.To)
		return recordRename(ctx, c, api, &fresh, out, toInfo)
	case toErr == nil && !os.SameFile(fromInfo, toInfo):
		// A hard link of the same file is a MoveNoReplace cut short between
		// its link and its unlink, which the move below finishes.
		out.Reason = mediafilespec.RenameCollision
		return out, nil
	case fromErr != nil:
		return out, fmt.Errorf("rescan: rename: stat %s: %w", out.From, fromErr)
	}

	if renameBeforeMove != nil {
		renameBeforeMove()
	}
	info, err := os.Stat(out.From)
	if err != nil {
		return out, fmt.Errorf("rescan: rename: stat %s: %w", out.From, err)
	}
	if !mediafilespec.SameFingerprint(&fresh, info) {
		out.Reason = mediafilespec.RenameChanged
		return out, nil
	}
	if dryRun {
		out.Reason = mediafilespec.RenameDryRun
		return out, nil
	}

	if err := fsops.MoveNoReplace(out.From, out.To); err != nil {
		if errors.Is(err, fsops.ErrExists) {
			out.Reason = mediafilespec.RenameCollision
			return out, nil
		}
		return out, fmt.Errorf("rescan: rename: %w", err)
	}
	return recordRename(ctx, c, api, &fresh, out, info)
}

// recordRename applies the rename of a file now at out.To, and moves its
// sidecars after it. When the apply fails it re-reads the MediaFile: one
// that already names out.To took the apply after all; otherwise the file is
// moved back to out.From.
func recordRename(
	ctx context.Context, c client.Client, api client.Reader, fresh *catalogv1alpha1.MediaFile,
	out mediafilespec.RenameOutcome, info os.FileInfo,
) (mediafilespec.RenameOutcome, error) {
	if err := applyRenamed(ctx, c, fresh, out.To, info); err != nil {
		var now catalogv1alpha1.MediaFile
		if getErr := api.Get(ctx, client.ObjectKeyFromObject(fresh), &now); getErr == nil && now.Spec.Path == out.To {
			logging.FromContext(ctx).Info("the rename's apply reported an error but landed", "mediaFile", fresh.Name, "error", err)
		} else {
			if undoErr := moveBack(out.To, out.From); undoErr != nil {
				logging.FromContext(ctx).Error("a renamed file's MediaFile could not be updated, nor the file moved back; it names a path that no longer exists",
					"mediaFile", fresh.Name, "from", out.From, "to", out.To, "error", err, "undoError", undoErr)
				return out, errors.Join(err, undoErr)
			}
			var stale *mediafilespec.StaleReadError
			if errors.As(err, &stale) {
				out.Reason = mediafilespec.RenameChanged
				return out, nil
			}
			return out, err
		}
	}
	out.Moved = true
	moveSidecars(ctx, fresh.Status.Sidecars, out.From, out.To)
	return out, nil
}

// applyRenamed applies mf's complete spec as [Apply] renders it for an
// existing MediaFile -- every frozen field re-asserted -- with the path
// moved to to, the size and mtime of the moved file, and the quality
// status.naming proposes.
func applyRenamed(ctx context.Context, c client.Client, mf *catalogv1alpha1.MediaFile, to string, before os.FileInfo) error {
	f := mediafilespec.ReassertFrozen(&mf.Spec)
	if q := mf.Status.Naming.Quality; q != nil {
		f.Quality = q
	}
	info := before
	if moved, err := os.Stat(to); err == nil {
		info = moved
	}
	if mf.Spec.Original != nil && !*mf.Spec.Original {
		info, f.Original = nil, nil
	}
	_, err := Apply(ctx, c, mf.Namespace, mf.Name, mf.ResourceVersion, mf.Spec.MediaRef, to, info, f)
	return err
}

// moveBack undoes a move whose apply failed. It never replaces a file that
// has taken the original path since.
func moveBack(to, from string) error {
	if err := fsops.MoveNoReplace(to, from); err != nil {
		return fmt.Errorf("rescan: rename: move %s back: %w", to, err)
	}
	return nil
}

// moveSidecars moves each sidecar next to from whose name is from's stem
// plus a suffix -- ".en.srt", ".en.forced.srt", ".nfo", as
// subtitles.SidecarName writes them -- to to's stem with the same suffix.
// A sidecar that cannot be moved, or whose new name is taken, is logged and
// left where it is.
func moveSidecars(ctx context.Context, sidecars []catalogv1alpha1.Sidecar, from, to string) {
	oldStem := strings.TrimSuffix(from, filepath.Ext(from))
	newStem := strings.TrimSuffix(to, filepath.Ext(to))
	if oldStem == newStem {
		return
	}
	log := logging.FromContext(ctx)
	// The recorded sidecars, and every subtitle beside the file its stem
	// names -- squasharr's own, which no MediaFile records (MP4 standard
	// §4.1).
	paths := map[string]bool{}
	for _, s := range sidecars {
		paths[s.Path] = true
	}
	found, err := subtitles.SidecarsOf(from)
	if err != nil {
		log.Warn("could not list a renamed file's folder for its sidecars", "file", from, "error", err)
	}
	for _, p := range found {
		paths[p] = true
	}
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		if filepath.Dir(p) != filepath.Dir(from) || !strings.HasPrefix(p, oldStem+".") {
			continue
		}
		dst := newStem + strings.TrimPrefix(p, oldStem)
		if err := fsops.MoveNoReplace(p, dst); errors.Is(err, fsops.ErrExists) {
			log.Warn("a sidecar's renamed path is taken; leaving it under the old name", "sidecar", p, "to", dst)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("could not move a sidecar with its renamed file", "sidecar", p, "to", dst, "error", err)
		}
	}
}

// renameBeforeMove runs after RenameFile has read the MediaFile and found
// the proposed path free, and before it checks the file's fingerprint and
// moves it. It is nil in production; a test sets it to stand in for a
// second writer -- of the file, of the proposed path, or of the MediaFile.
var renameBeforeMove func()
