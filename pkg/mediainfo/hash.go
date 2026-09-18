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

package mediainfo

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"
)

// ProbeHash is sha1(path|size|mtime), matching
// api/catalog/v1alpha1.MediaFileStatus.ProbeHash's doc comment: a change
// in any of the three makes downstream services (transcode compliance,
// subtitle sidecar rescans) replan.
func ProbeHash(path string, size int64, mtime time.Time) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", path, size, mtime.UnixNano())))
	return hex.EncodeToString(sum[:])
}
