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

package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxRingEntries caps the ring. Spec §5 pins <indexer-uid>.grab as a timestamp
// ring under CAS in clustarr-indexer-limits (TTL 2d); an uncapped ring is how
// a busy indexer turns a KV value into a megabyte.
const maxRingEntries = 512

// casAttempts bounds the optimistic loop. Contention here is one process
// grabbing twice at once; three attempts is generous and a failure is
// non-fatal to the caller.
const casAttempts = 3

// grabEntry is one grab. The GUID is stored, not just the timestamp, and that
// is what makes counting idempotent: a redelivered download for a GUID already
// in the ring changes nothing.
type grabEntry struct {
	GUID string `json:"g"`
	At   int64  `json:"t"` // unix seconds
}

// GrabRingKey is the key spec §5's KV table names. The UID must come from the
// LIVE object, never from DownloadRequest.IndexerRef.UID: the request's UID is
// caller-supplied and a stale one would open a second ring for one indexer.
//
// It is exported so D1-9's e2e can assert the ring without reimplementing the
// escape. KVKeyToken is not optional: a NATS KV key must match
// ^[-/_=\.a-zA-Z0-9]+$ with no leading or trailing "." and no "..", nothing in
// the Go types enforces it, and an illegal key fails Put and Delete alike.
func GrabRingKey(uid string) string { return events.KVKeyToken(uid) + ".grab" }

// grabWindow is the limit window. Prowlarr counts ReleaseGrabbed events over
// 1h or 24h (docs/research/indexers.md §6); the CRD's default unit is day, and
// a nil spec.limits still counts -- the counter is observability whether or
// not a limit is configured.
func grabWindow(idx *indexv1alpha1.Indexer) time.Duration {
	if idx.Spec.Limits != nil && idx.Spec.Limits.Unit == indexv1alpha1.LimitUnitHour {
		return time.Hour
	}
	return 24 * time.Hour
}

// pruneRing drops entries older than cutoff and keeps the newest maxEntries.
// It is pure so the window arithmetic is testable without a bus.
func pruneRing(ring []grabEntry, cutoff time.Time, maxEntries int) []grabEntry {
	out := ring[:0:0]
	for _, e := range ring {
		if e.At >= cutoff.Unix() {
			out = append(out, e)
		}
	}
	if len(out) > maxEntries {
		out = out[len(out)-maxEntries:]
	}
	return out
}

// CountGrab records one grab of guid against idx and returns the number of
// grabs in the current window. counted is false when guid was already in the
// ring -- a redelivery, which must not double count.
//
// The caller treats an error as NON-FATAL: an accounting outage must never
// strand a grab that already has its bytes.
func CountGrab(
	ctx context.Context,
	kv events.KV,
	idx *indexv1alpha1.Indexer,
	guid string,
	now time.Time,
) (int32, bool, error) {
	return CountGrabAt(ctx, kv, idx, guid, now, now)
}

// CountGrabAt is CountGrab for a grab that happened at `at` rather than now:
// the direct-grab reconciler counts a Download at its creation time, and
// meets every existing Download again each time indexarr starts. A grab older
// than the window is therefore not counted at all (counted false) -- it would
// be pruned from the ring by the next count anyway, and counting it now would
// report a grab the window no longer holds.
func CountGrabAt(
	ctx context.Context,
	kv events.KV,
	idx *indexv1alpha1.Indexer,
	guid string,
	at, now time.Time,
) (int32, bool, error) {
	key := GrabRingKey(string(idx.UID))
	cutoff := now.Add(-grabWindow(idx))
	if at.After(now) {
		at = now // a creation stamp ahead of this clock is skew, not the future
	}

	var lastErr error
	for range casAttempts {
		var (
			ring []grabEntry
			rev  uint64
			have bool
		)
		ent, err := kv.Get(ctx, key)
		switch {
		case err == nil:
			have, rev = true, ent.Revision
			// A value we cannot decode is a value we replace. Failing shut
			// here would wedge counting for this indexer for the bucket's
			// whole 2d TTL.
			if uerr := json.Unmarshal(ent.Value, &ring); uerr != nil {
				logging.FromContext(ctx).Warn(
					"indexarr/download: replacing an undecodable grab ring",
					"indexer", idx.Name, "err", uerr)
				ring = nil
			}
		case errors.Is(err, events.ErrKeyNotFound):
		default:
			return 0, false, err
		}

		ring = pruneRing(ring, cutoff, maxRingEntries)
		for _, e := range ring {
			if e.GUID == guid {
				return int32(len(ring)), false, nil
			}
		}
		if at.Before(cutoff) {
			return int32(len(ring)), false, nil
		}
		ring = pruneRing(append(ring, grabEntry{GUID: guid, At: at.Unix()}), cutoff, maxRingEntries)

		val, err := json.Marshal(ring)
		if err != nil {
			return 0, false, err
		}
		if have {
			_, err = kv.Update(ctx, key, val, rev)
		} else {
			_, err = kv.Create(ctx, key, val)
		}
		switch {
		case err == nil:
			return int32(len(ring)), true, nil
		case errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyExists):
			lastErr = err
		default:
			return 0, false, err
		}
	}
	return 0, false, fmt.Errorf("indexarr/download: grab ring CAS gave up after %d attempts: %w",
		casAttempts, lastErr)
}
