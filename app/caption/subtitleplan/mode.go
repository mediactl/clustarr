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

package subtitleplan

import (
	"cmp"
	"io/fs"
	"strconv"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// DefaultSidecarMode is RootFolderSpec.Permissions.FileMode's default, 0664:
// sidecars sit beside the video in a group-shared library. It is the mode of
// a sidecar whose video lies under no RootFolder, or under one whose
// fileMode does not parse.
const DefaultSidecarMode = fs.FileMode(0o664)

// sidecarMode is the file mode a sidecar beside path's video is written
// with: spec.permissions.fileMode of the deepest RootFolder whose spec.path
// contains path (both logical /data paths) -- the mode import gave the
// video itself, so a media server that can read the one can read the
// other -- or fallback, itself defaulting to DefaultSidecarMode, when none
// does or its fileMode is not the CRD's "0[0-7]{3}". The RootFolder is found
// by path, not through the item's rootFolderRef: the sidecar is written
// beside the file, so the folder that contains the file is the one whose
// permissions apply.
func sidecarMode(roots []catalogv1alpha1.RootFolder, path string, fallback fs.FileMode) fs.FileMode {
	best, bestLen := -1, 0
	for i := range roots {
		root := strings.TrimRight(roots[i].Spec.Path, "/")
		if root == "" || !strings.HasPrefix(path, root+"/") || len(root) <= bestLen {
			continue
		}
		best, bestLen = i, len(root)
	}
	if best >= 0 {
		if m, ok := parseFileMode(roots[best].Spec.Permissions.FileMode); ok {
			return m
		}
	}
	return cmp.Or(fallback, DefaultSidecarMode)
}

// parseFileMode parses a RootFolder fileMode, which the CRD holds to
// ^0[0-7]{3}$. Anything else -- including the empty string of an object
// written before the default existed -- is not a mode.
func parseFileMode(s string) (fs.FileMode, bool) {
	if len(s) != 4 || s[0] != '0' {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, false
	}
	return fs.FileMode(n), true
}
