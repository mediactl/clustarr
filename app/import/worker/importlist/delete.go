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

package importlist

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ErrOutsideRootFolder is a MediaFile whose path does not lie strictly under
// the RootFolder its item names. removeAndDelete refuses to move it: a path
// it cannot place inside the library is not one it may take out of it.
var ErrOutsideRootFolder = errors.New("importlist: file is not under the item's root folder")

// ErrNoRecycleBin is a RootFolder with an empty spec.recycleBin.path (the
// CRD defaults it, so this is a hand-cleared value). removeAndDelete never
// falls back to unlinking.
var ErrNoRecycleBin = errors.New("importlist: root folder has no recycle bin path")

// removeWithFiles is SyncActionRemoveAndDelete: the catalog item is removed
// and so are its files. It is Radarr's and Sonarr's "remove and delete"
// (the list-sync clean-up calls DeleteMovie/DeleteSeries with deleteFiles),
// whose file half goes through the recycle bin, never a bare unlink -- and
// so does this: every file goes through fsops.Recycle into the item's
// RootFolder spec.recycleBin.path, the same path app/import/worker/fileimport
// sends a replaced file down, where spec.recycleBin.cleanupDays governs how
// long it stays.
//
// "Its files" is what Clustarr recorded, not what a directory walk finds:
// each MediaFile attributed to the item (spec.mediaRef) and each sidecar on
// its status. Anything else in the folder -- a file nobody imported -- stays,
// and so does the folder it keeps non-empty. Directories left empty are
// removed up to, never including, the root folder.
//
// Order makes a retry safe: files first, then each MediaFile object, then
// the item. A failure anywhere returns an error, the caller keeps the item
// in the list's snapshot, and the next sync repeats the whole call, which
// finds recycled files already gone and deleted objects already absent. An
// item that no longer exists is done: its root folder cannot be named
// without it, and guessing one would be the never-guess rule broken.
func removeWithFiles(ctx context.Context, c client.Client, namespace string, si StoredItem) error {
	if si.ObjectKind == string(commonv1.MediaKindSeries) {
		return removeSeriesWithFiles(ctx, c, namespace, si.ObjectName)
	}
	return removeMovieWithFiles(ctx, c, namespace, si.ObjectName)
}

func removeMovieWithFiles(ctx context.Context, c client.Client, namespace, name string) error {
	var m catalogv1alpha1.Movie
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &m); err != nil {
		return client.IgnoreNotFound(err)
	}
	belongs := func(ref commonv1.MediaRef) bool {
		return ref.Kind == commonv1.MediaKindMovie && ref.Name == name
	}
	if err := recycleMediaFiles(ctx, c, namespace, m.Spec.RootFolderRef, belongs); err != nil {
		return fmt.Errorf("importlist: delete files of movie %s: %w", name, err)
	}
	return deleteMovie(ctx, c, namespace, name)
}

func removeSeriesWithFiles(ctx context.Context, c client.Client, namespace, name string) error {
	var s catalogv1alpha1.Series
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
		return client.IgnoreNotFound(err)
	}
	var eps catalogv1alpha1.EpisodeList
	if err := c.List(ctx, &eps, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("importlist: list episodes of series %s: %w", name, err)
	}
	episodes := make(map[string]bool)
	for i := range eps.Items {
		if eps.Items[i].Spec.SeriesRef == name {
			episodes[eps.Items[i].Name] = true
		}
	}
	belongs := func(ref commonv1.MediaRef) bool {
		switch ref.Kind {
		case commonv1.MediaKindSeries:
			return ref.Name == name
		case commonv1.MediaKindEpisode:
			// MediaRef.keys lists the episodes a pack covers; any of
			// them belonging to this series claims the file.
			return episodes[ref.Name] || slices.ContainsFunc(ref.Keys, func(k string) bool { return episodes[k] })
		default:
			return false
		}
	}
	if err := recycleMediaFiles(ctx, c, namespace, s.Spec.RootFolderRef, belongs); err != nil {
		return fmt.Errorf("importlist: delete files of series %s: %w", name, err)
	}
	return deleteSeries(ctx, c, namespace, name)
}

// recycleMediaFiles recycles the file and sidecars of every MediaFile in
// namespace whose mediaRef belongs reports true, deleting each MediaFile
// object once its files are in the bin.
func recycleMediaFiles(
	ctx context.Context, c client.Client, namespace, rootFolderRef string, belongs func(commonv1.MediaRef) bool,
) error {
	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("list media files: %w", err)
	}
	var owned []*catalogv1alpha1.MediaFile
	for i := range files.Items {
		if belongs(files.Items[i].Spec.MediaRef) {
			owned = append(owned, &files.Items[i])
		}
	}
	if len(owned) == 0 {
		return nil
	}

	var rf catalogv1alpha1.RootFolder
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: rootFolderRef}, &rf); err != nil {
		return fmt.Errorf("root folder %s: %w", rootFolderRef, err)
	}
	root, bin := rf.Spec.Path, rf.Spec.RecycleBin.Path
	if bin == "" {
		return fmt.Errorf("%w: %s", ErrNoRecycleBin, rf.Name)
	}

	log := logging.FromContext(ctx)
	for _, mf := range owned {
		paths := []string{mf.Spec.Path}
		for _, sc := range mf.Status.Sidecars {
			paths = append(paths, sc.Path)
		}
		// Check every path before moving any, so a MediaFile with one
		// out-of-root sidecar is left whole rather than half recycled.
		for _, p := range paths {
			if !strictlyUnder(p, root) {
				return fmt.Errorf("%w: media file %s path %q, root folder %s is %q",
					ErrOutsideRootFolder, mf.Name, p, rf.Name, root)
			}
		}
		for _, p := range paths {
			dest, err := recycle(bin, p)
			if err != nil {
				return fmt.Errorf("recycle %s: %w", p, err)
			}
			if dest != "" {
				log.Info("importlist: recycled a file of an item that fell off the list",
					"mediaFile", mf.Name, "path", p, "recycledTo", dest)
			}
		}
		if err := c.Delete(ctx, mf); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete media file %s: %w", mf.Name, err)
		}
		pruneEmptyDirs(root, filepath.Dir(mf.Spec.Path))
	}
	return nil
}

// recycle moves path into bin, or does nothing when path is already gone (a
// retry after a partial run). It returns the destination, or "" when there
// was nothing to move.
func recycle(bin, path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return fsops.Recycle(bin, path)
}

// strictlyUnder reports whether path names something inside root, and not
// root itself, once both are cleaned. Both must be absolute.
func strictlyUnder(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pruneEmptyDirs removes dir and then each parent while it is empty, stopping
// at the first directory that is not empty (os.Remove refuses one) and never
// removing root or anything outside it.
func pruneEmptyDirs(root, dir string) {
	for strictlyUnder(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
