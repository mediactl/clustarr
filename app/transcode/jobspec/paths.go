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

package jobspec

import (
	"fmt"
	"path/filepath"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// LogicalDataRoot is where every path stored in a CRD lives: RootFolder
// paths must start with /data/media/ (a CEL rule), and MediaFile paths are
// under a root folder. app/transcode/worker's
// Options.DataDir is where that volume is mounted in THIS process -- /data in
// a Job pod, so the mapping is the identity in production.
const LogicalDataRoot = "/data"

// defaultRecycleBin is RecycleBin.Path's CRD default, used when a
// RootFolder's is somehow empty.
const defaultRecycleBin = "/data/.recycle"

// LocalPath maps a logical /data path to this process's filesystem. A path
// outside /data is refused rather than guessed at.
func LocalPath(dataDir, logical string) (string, error) {
	if !filepath.IsAbs(logical) {
		return "", fmt.Errorf("path %q is not absolute", logical)
	}
	clean := filepath.Clean(logical)
	if clean != LogicalDataRoot && !strings.HasPrefix(clean, LogicalDataRoot+"/") {
		return "", fmt.Errorf("path %q is outside %s", logical, LogicalDataRoot)
	}
	rel := strings.TrimPrefix(clean, LogicalDataRoot)
	return filepath.Join(dataDir, rel), nil
}

// Within reports whether path is strictly inside dir (both logical, both
// already cleaned by the caller or the CRD).
func Within(dir, path string) bool {
	dir = filepath.Clean(dir)
	return strings.HasPrefix(filepath.Clean(path), dir+"/")
}

// RootFolderFor picks the RootFolder whose path contains source, the
// deepest one if root folders nest. nil means none does.
func RootFolderFor(folders []catalogv1alpha1.RootFolder, source string) *catalogv1alpha1.RootFolder {
	var best *catalogv1alpha1.RootFolder
	for i := range folders {
		rf := &folders[i]
		if !Within(rf.Spec.Path, source) {
			continue
		}
		if best == nil || len(filepath.Clean(rf.Spec.Path)) > len(filepath.Clean(best.Spec.Path)) {
			best = rf
		}
	}
	return best
}

// OutputPath is where a transcode of source under profileName finally
// lives: the output location gap-fix ruling R-11 asks for, from design spec
// §4.5 and §6.4 ("atomic rename over the source path (source -> recycle
// bin)"; "replaces the library hardlink only").
//
//   - replaceSource=true: <stem>.<container> beside the source. For a
//     same-container profile that IS the source path and the output replaces
//     it in place; for a container change (every MP4 output of an .mkv) it is
//     the new name, and the source is retired to the recycle bin once the
//     output is in place.
//   - replaceSource=false: "<stem> - <profile>.<container>" beside the
//     source. The source itself would be the same-container name, so a kept
//     source needs another; the " - <label>" suffix is the multiple-version
//     convention Jellyfin and Plex both read (docs/research/naming.md §A3),
//     so a media server shows the transcode as a version of the same title
//     rather than as a second one.
//
// The extension comparison ignores case: Film.MKV under an mkv profile is
// replaced in place, not renamed. The transcode planner (which records the
// plan and builds the task) and the worker's tests call this, so they agree
// on the path by construction.
func OutputPath(source, profileName string, container transcodev1alpha1.Container, replaceSource bool) (string, error) {
	ext := strings.ToLower(string(container))
	if ext == "" {
		ext = string(transcodev1alpha1.ContainerMKV)
	}
	source = filepath.Clean(source)
	if !filepath.IsAbs(source) {
		return "", fmt.Errorf("source %q is not absolute", source)
	}
	srcExt := filepath.Ext(source)
	stem := strings.TrimSuffix(source, srcExt)
	if replaceSource {
		if strings.EqualFold(strings.TrimPrefix(srcExt, "."), ext) {
			return source, nil
		}
		return stem + "." + ext, nil
	}
	return stem + " - " + profileName + "." + ext, nil
}

// RecycleBinOf is the recycle bin a RootFolder's swaps link into.
func RecycleBinOf(rf *catalogv1alpha1.RootFolder) string {
	if rf.Spec.RecycleBin.Path != "" {
		return rf.Spec.RecycleBin.Path
	}
	return defaultRecycleBin
}
