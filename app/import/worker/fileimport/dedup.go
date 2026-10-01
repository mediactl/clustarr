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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
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

// ownEarlierAttempt reports whether mf is the file this very import placed
// on an earlier delivery of the same task: the delivery applied mf, then
// died -- the pod was killed, or the status.import write failed -- before
// status.import or the dedup record above was written, so the redelivery
// finds no trace of its own work but mf among the item's existing files.
//
// Such a file is not one the import would replace, so the transcoded and
// upgrade gates must not compare against it. Compared against itself, a
// file is never an upgrade: every file was rejected, status.import read
// downloadv1alpha1.ImportMessageEveryFileRejected, and grabarr took that
// for a bad release -- blocklisted it, searched again and had the engine
// delete the download's data.
//
// Identity is the Download, not the path: mf records the Download that
// imported it (spec.importedFrom.downloadRef), and a redelivery may render
// another path for the same file (a probe that timed out on the first
// attempt and answers on the second names the codec). A Download's name is
// reused when a release is grabbed again for the same target
// (k8s.ChildName(target, guid)), so the file must also have been imported
// no earlier than this Download was created: one an earlier Download of the
// same name imported is compared like any other. Both stamps are whole
// seconds, so the same second counts as after.
//
// A file squasharr has transcoded since -- catalogarr took spec.original
// over as false, or its probe read the CLUSTARR_PROFILE tag -- is no longer
// what the import placed, and stays under the gates: the transcode is final,
// and re-placing the source over it would undo it.
func ownEarlierAttempt(mf *catalogv1alpha1.MediaFile, dl *downloadv1alpha1.Download) bool {
	src := mf.Spec.ImportedFrom
	if src == nil || src.DownloadRef == "" || src.DownloadRef != dl.Name {
		return false
	}
	if src.ImportedAt.IsZero() || src.ImportedAt.Before(&dl.CreationTimestamp) {
		return false
	}
	if mf.Spec.Original != nil && !*mf.Spec.Original {
		return false
	}
	if mi := mf.Status.MediaInfo; mi != nil && mi.TranscodeProfile != "" {
		return false
	}
	return true
}

// comparedFiles is existing without the files ownEarlierAttempt claims for
// dl: the files an import's gates judge it against. It never aliases
// existing, which the movie and episode replacement steps still walk whole,
// so an earlier attempt's file at a path this attempt no longer renders is
// replaced like any other.
func comparedFiles(existing []catalogv1alpha1.MediaFile, dl *downloadv1alpha1.Download) []catalogv1alpha1.MediaFile {
	out := make([]catalogv1alpha1.MediaFile, 0, len(existing))
	for i := range existing {
		if !ownEarlierAttempt(&existing[i], dl) {
			out = append(out, existing[i])
		}
	}
	return out
}
