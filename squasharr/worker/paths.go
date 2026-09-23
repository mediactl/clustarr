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

package worker

import (
	"fmt"
	"path/filepath"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// LogicalDataRoot is where every path stored in a CRD lives: RootFolder
// paths must start with /data/media/ (a CEL rule), and MediaFile and
// TranscodeJob paths are under a root folder. [Options.DataDir] is where
// that volume is mounted in THIS process -- /data in a Job pod, so the
// mapping is the identity in production.
const LogicalDataRoot = "/data"

// defaultRecycleBin is RecycleBin.Path's CRD default, used when a
// RootFolder's is somehow empty.
const defaultRecycleBin = "/data/.recycle"

// localPath maps a logical /data path to this process's filesystem. A path
// outside /data is refused rather than guessed at.
func localPath(dataDir, logical string) (string, error) {
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

// within reports whether path is strictly inside dir (both logical, both
// already cleaned by the caller or the CRD).
func within(dir, path string) bool {
	dir = filepath.Clean(dir)
	return strings.HasPrefix(filepath.Clean(path), dir+"/")
}

// rootFolderFor picks the RootFolder whose path contains source, the
// deepest one if root folders nest. nil means none does.
func rootFolderFor(folders []catalogv1alpha1.RootFolder, source string) *catalogv1alpha1.RootFolder {
	var best *catalogv1alpha1.RootFolder
	for i := range folders {
		rf := &folders[i]
		if !within(rf.Spec.Path, source) {
			continue
		}
		if best == nil || len(filepath.Clean(rf.Spec.Path)) > len(filepath.Clean(best.Spec.Path)) {
			best = rf
		}
	}
	return best
}
