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

package fileimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// dedupFingerprint is the value recorded at [DedupKey] once a Download has
// been imported. It exists so a redelivered or duplicate ImportTask is a
// no-op even after the Download itself is gone -- grabarr deletes an
// imported, seed-goal-met Download when spec.removeDataOnDelete's sibling
// RemoveOnImport fires, which can race a redelivery inside the consumer's
// AckWait/backoff window, and by then there is no status.import left to
// consult.
type dedupFingerprint struct {
	// ImportedAt is when this worker finished the import.
	ImportedAt time.Time `json:"importedAt"`

	// MediaFileRefs lists the MediaFiles the import created, for operators
	// reading the raw KV entry; nothing in this package re-reads it.
	MediaFileRefs []string `json:"mediaFileRefs,omitempty"`
}

// DedupKey is the events.BucketDedup key one Download's import is recorded
// under, following that bucket's "import fingerprints for re-import no-ops"
// purpose (topology.go) and the repo-wide KVKeyToken discipline every KV key
// here uses. uid is the Download's UID: stable across a redelivery of the
// same task, and distinct from a same-named Download recreated later.
func DedupKey(downloadUID string) string { return "import." + events.KVKeyToken(downloadUID) }

// alreadyImported reports whether uid has a recorded dedup fingerprint. A
// missing key is not an error -- it is the overwhelmingly common case, a
// Download being imported for the first time.
func alreadyImported(ctx context.Context, kv events.KV, uid string) (bool, error) {
	if _, err := kv.Get(ctx, DedupKey(uid)); err != nil {
		if errors.Is(err, events.ErrKeyNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// recordImport writes uid's dedup fingerprint. It is best-effort: a failure
// here does not undo an import that already succeeded and is already
// reflected in Download.status.import, so the caller logs and continues
// rather than treating it as fatal. A future redelivery within the bucket's
// TTL that misses this write simply re-derives the same idempotent outcome
// from status.import.state == imported instead.
func recordImport(ctx context.Context, kv events.KV, uid string, mediaFileRefs []string) error {
	fp := dedupFingerprint{ImportedAt: time.Now().UTC(), MediaFileRefs: mediaFileRefs}
	data, err := json.Marshal(fp)
	if err != nil {
		return fmt.Errorf("fileimport: encode dedup fingerprint: %w", err)
	}
	if _, err := kv.Put(ctx, DedupKey(uid), data); err != nil {
		return fmt.Errorf("fileimport: record dedup fingerprint: %w", err)
	}
	return nil
}
