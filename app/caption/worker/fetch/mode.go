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

package fetch

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// sidecarModeFor is the file mode a sidecar beside mf's video is written
// with: spec.permissions.fileMode of the RootFolder the video lies under --
// the mode importarr gave the video itself, so a media server that can read
// the one can read the other -- else [Worker.SidecarMode], else
// [DefaultSidecarMode]. Until gap-fix X11b every sidecar was written 0664
// whatever the root folder said.
//
// The RootFolder is found by path, not through the catalog owner's
// rootFolderRef: the sidecar is written beside the file, so the folder that
// contains the file is the one whose permissions apply, even while an owner
// moved to another root folder still has its file in the old one. Only a
// List failure is an error; the caller redelivers.
func (w *Worker) sidecarModeFor(ctx context.Context, mf *catalogv1alpha1.MediaFile) (os.FileMode, error) {
	var roots catalogv1alpha1.RootFolderList
	if err := w.Client.List(ctx, &roots, client.InNamespace(mf.Namespace)); err != nil {
		return 0, fmt.Errorf("list root folders in %s: %w", mf.Namespace, err)
	}
	return sidecarMode(roots.Items, mf.Spec.Path, w.SidecarMode), nil
}

// sidecarMode is [Worker.sidecarModeFor]'s rule, pure: the fileMode of the
// deepest root folder whose spec.path contains path (both logical /data
// paths), or fallback -- itself defaulting to [DefaultSidecarMode] -- when
// none does or its fileMode is not the CRD's "0[0-7]{3}".
func sidecarMode(roots []catalogv1alpha1.RootFolder, path string, fallback os.FileMode) os.FileMode {
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
func parseFileMode(s string) (os.FileMode, bool) {
	if len(s) != 4 || s[0] != '0' {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, false
	}
	return os.FileMode(n), true
}
