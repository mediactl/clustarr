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
	"encoding/json"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// Result is the worker's outcome for one ImportList sync (every requested
// kind combined), checkpointed to a clustarr-progress key. The ImportList
// controller polls that key and projects it into status -- the same split
// app/import/worker/rescan.Progress uses for LibraryScan, and for the same
// reason: the controller is the single writer of ImportList.status (see
// k8s.ManagerImportarr's doc comment), so the worker reports through a KV
// checkpoint instead of an apiserver write of its own.
type Result struct {
	// SyncedAt is when this sync attempt finished, successfully or not. The
	// key holds only the most recently finished attempt, which the
	// controller projects as status.lastSyncAt; the worker also stamps it
	// on the ImportList as AnnotationSyncedAt.
	SyncedAt time.Time `json:"syncedAt"`

	// Error is non-empty when any requested kind failed: the provider
	// could not be built or cannot yield the kind, the fetch failed, no
	// catalog writer exists for the kind, or a syncLevel action could not
	// be carried out. Each kind's failure is joined in, so a partial
	// failure is still visible without hiding the kinds that did work.
	Error string `json:"error,omitempty"`

	// Fetched is how many items the remote list(s) returned, summed across
	// every requested kind.
	Fetched int32 `json:"fetched"`

	// Added is how many catalog items were created or updated.
	Added int32 `json:"added"`

	// Excluded is how many fetched entries an ImportExclusion suppressed.
	Excluded int32 `json:"excluded"`

	// Removed is how many previously-added catalog items were unmonitored
	// or removed by spec.syncLevel.
	Removed int32 `json:"removed"`
}

// Encode marshals r for a KV Put.
func (r Result) Encode() ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("importlist: encode result: %w", err)
	}
	return data, nil
}

// DecodeResult unmarshals a KV value written by [Result.Encode].
func DecodeResult(data []byte) (Result, error) {
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, fmt.Errorf("importlist: decode result: %w", err)
	}
	return r, nil
}

// ResultKey is the clustarr-progress key one ImportList's worker
// checkpoints to and its controller polls, following that bucket's existing
// "<kind>.<uid>" convention (the same one app/import/worker/rescan.ProgressKey
// uses for LibraryScan). The UID, not the name, keys it, so an ImportList
// deleted and recreated under the same name never reads a stale checkpoint
// left by the deleted one.
func ResultKey(listUID string) string { return "importlist." + events.KVKeyToken(listUID) }
