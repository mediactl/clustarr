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

package importliststate

import (
	"strconv"

	"github.com/mediactl/clustarr/pkg/events"
)

// SnapshotChunkBytes is the most one snapshot chunk's encoded value may
// hold: a list whose snapshot (schema.ListSnapshot) encodes larger is
// written as several chunks (ADR-0019 §7.6, ruling R17). The import agent
// packs items into chunks under it; the ImportList controller reads every
// chunk and diffs only a complete set.
const SnapshotChunkBytes = 512 << 10

// SnapshotKey is the events.BucketImportList key of chunk n of one list's
// snapshot: "snapshot.<KVKeyToken(list uid)>" for chunk 0 and
// "snapshot.<token>.<n>" for every later one (ruling R17). The UID, not the
// name, keys it, so a list deleted and recreated under one name never reads
// the deleted one's snapshot.
//
// It cannot collide with an [ItemsKey]: one of those has three segments
// ending in a kind ("movie", "series", ...), and a chunk key's third
// segment is a number.
func SnapshotKey(listUID string, chunk int) string {
	key := "snapshot." + events.KVKeyToken(listUID)
	if chunk > 0 {
		key += "." + strconv.Itoa(chunk)
	}
	return key
}
