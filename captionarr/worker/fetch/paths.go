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
	"fmt"
	"path/filepath"
	"strings"
)

// LogicalDataRoot is where every path stored in a CRD lives. It is the same
// constant squasharr/worker uses, for the same reason: a CRD path is a
// logical /data path, and the process maps it through its own mount point
// ([Worker.DataDir]). In the worker Deployment the volume is mounted at
// /data and the mapping is the identity; in a test it is a temp dir.
const LogicalDataRoot = "/data"

// localPath maps a logical /data path to where the volume is mounted in this
// process. A path outside /data, or a relative one, is refused rather than
// guessed at: a MediaFile whose path is not on the data volume can never be
// served by this worker, however often it is redelivered.
func localPath(dataDir, logical string) (string, error) {
	if !filepath.IsAbs(logical) {
		return "", fmt.Errorf("path %q is not absolute", logical)
	}
	clean := filepath.Clean(logical)
	if clean != LogicalDataRoot && !strings.HasPrefix(clean, LogicalDataRoot+"/") {
		return "", fmt.Errorf("path %q is outside %s", logical, LogicalDataRoot)
	}
	return filepath.Join(dataDir, strings.TrimPrefix(clean, LogicalDataRoot)), nil
}
