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

// Package datapath maps the logical /data paths stored in CRDs to where the
// media volume is mounted in this process (--data-dir).
//
// Both captionarr roles read the media file's directory: the SubtitleRequest
// controller lists it for sidecars, the fetch worker stats the video and
// writes the sidecar beside it. They share this one mapping so --data-dir
// means the same thing to both. Until plan task F-6 only the worker mapped;
// the controller read spec.path literally, so a non-default --data-dir left
// every request Blocked on a directory that does not exist in its process.
package datapath

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Root is where every path stored in a CRD lives. It is the same constant
// app/squash/worker uses, for the same reason: a CRD path is a logical /data
// path, and each process maps it through its own mount point. In the
// Deployments the volume is mounted at /data and the mapping is the
// identity; in a test it is a temp dir.
const Root = "/data"

// Local maps logical to where the volume is mounted in this process, dataDir
// (empty means [Root]). A path outside /data, or a relative one, is refused
// rather than guessed at: a MediaFile whose path is not on the data volume
// can never be served, however often it is retried.
func Local(dataDir, logical string) (string, error) {
	if dataDir == "" {
		dataDir = Root
	}
	if !filepath.IsAbs(logical) {
		return "", fmt.Errorf("path %q is not absolute", logical)
	}
	clean := filepath.Clean(logical)
	if clean != Root && !strings.HasPrefix(clean, Root+"/") {
		return "", fmt.Errorf("path %q is outside %s", logical, Root)
	}
	return filepath.Join(dataDir, strings.TrimPrefix(clean, Root)), nil
}
