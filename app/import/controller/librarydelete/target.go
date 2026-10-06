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

// Package librarydelete carries out a library delete request: an item
// annotated catalog.clustarr.io/delete is removed with its MediaFile
// records and, for "files", its folder on disk (docs/superpowers/specs/
// 2026-09-30-library-delete-design.md).
package librarydelete

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// ErrRefused is a delete that would reach outside the item: nothing is
// removed, and the reason goes on the item until the request is renewed.
var ErrRefused = errors.New("librarydelete: refused")

// Target is everything one delete acts on.
type Target struct {
	// Root is the item's RootFolder spec.path; "" when it has none.
	Root string
	// Folder is the item's status.path; "" when never resolved.
	Folder string
	// Keys are TargetKey of the item and of each of its children.
	Keys map[string]bool
	// Files are the MediaFiles of the item and its children.
	Files []catalogv1alpha1.MediaFile
	// Donors are the item's and its children's audio donor folders,
	// <Root>/.clustarr/donors/<uid> (anime dual-audio spec §6.2).
	Donors []string
}

// TargetKey is the "<kind>/<name>" a MediaRef names.
func TargetKey(kind commonv1.MediaKind, name string) string { return string(kind) + "/" + name }

// Owns reports whether a MediaFile with ref belongs to the item: it names
// the item or a child, or -- a multi-episode file -- lists one in keys.
func (t Target) Owns(ref commonv1.MediaRef) bool {
	if t.Keys[TargetKey(ref.Kind, ref.Name)] {
		return true
	}
	return slices.ContainsFunc(ref.Keys, func(k string) bool { return t.Keys[TargetKey(ref.Kind, k)] })
}

// paths is every file path and sidecar the target's MediaFiles record.
func (t Target) paths() []string {
	var out []string
	for _, mf := range t.Files {
		out = append(out, mf.Spec.Path)
		for _, sc := range mf.Status.Sidecars {
			out = append(out, sc.Path)
		}
	}
	return out
}

// Occupant is a path something other than the item stands on: another
// item's folder, or a RootFolder.
type Occupant struct {
	// What names it in a refusal, e.g. "movie media/heat-2".
	What string
	Path string
}

// Check refuses, wrapping ErrRefused, a delete of files that would reach
// outside the item: no RootFolder, a folder or path not strictly under it,
// a folder holding a MediaFile of another item (all is every MediaFile in
// the namespace), or a folder at or above an occupant -- another item's
// folder or a RootFolder, which may hold files no MediaFile records yet.
func Check(t Target, all []catalogv1alpha1.MediaFile, occupants []Occupant) error {
	if t.Root == "" {
		return fmt.Errorf("%w: the item's root folder is unknown", ErrRefused)
	}
	if t.Folder != "" && !fsops.StrictlyUnder(t.Folder, t.Root) {
		return fmt.Errorf("%w: folder %q is not inside root folder %q", ErrRefused, t.Folder, t.Root)
	}
	for _, p := range t.paths() {
		if !fsops.StrictlyUnder(p, t.Root) {
			return fmt.Errorf("%w: file %q is not inside root folder %q", ErrRefused, p, t.Root)
		}
	}
	if t.Folder == "" {
		return nil
	}
	for i := range all {
		mf := &all[i]
		if t.Owns(mf.Spec.MediaRef) {
			continue
		}
		if atOrUnder(mf.Spec.Path, t.Folder) {
			return fmt.Errorf("%w: folder %q also holds media file %s of %s %s",
				ErrRefused, t.Folder, mf.Name, mf.Spec.MediaRef.Kind, mf.Spec.MediaRef.Name)
		}
	}
	for _, o := range occupants {
		if atOrUnder(o.Path, t.Folder) {
			return fmt.Errorf("%w: folder %q also holds %s (%q)", ErrRefused, t.Folder, o.What, o.Path)
		}
	}
	return nil
}

// atOrUnder reports whether path is dir or lies inside it.
func atOrUnder(path, dir string) bool {
	return path != "" && (filepath.Clean(path) == filepath.Clean(dir) || fsops.StrictlyUnder(path, dir))
}

// RemoveFromDisk removes the folder, recursively, and each recorded path
// outside it, permanently, then prunes the directories that leaves empty up
// to (never including) the root. A path already gone is done, so a retry
// after a partial run completes. Symlinks are never followed: a symlinked
// folder loses the link, not its target's contents (fsops.SafeRemove).
func RemoveFromDisk(ctx context.Context, t Target) error {
	var removed []string
	remove := func(p string) error {
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			removed = append(removed, p)
			return nil
		}
		if err := fsops.SafeRemove(ctx, t.Root, p); err != nil {
			return err
		}
		removed = append(removed, p)
		return nil
	}
	if t.Folder != "" {
		if err := remove(t.Folder); err != nil {
			return err
		}
	}
	for _, p := range t.paths() {
		if t.Folder != "" && fsops.StrictlyUnder(p, t.Folder) {
			continue
		}
		if err := remove(p); err != nil {
			return err
		}
	}
	for _, d := range t.Donors {
		if err := remove(d); err != nil {
			return err
		}
	}
	for _, p := range removed {
		fsops.PruneEmptyDirs(t.Root, filepath.Dir(p))
	}
	return nil
}
