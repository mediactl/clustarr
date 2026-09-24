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

package search

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

// The query ring, spec §5's other half of clustarr-indexer-limits:
// "<indexer-uid>.query, .grab timestamp rings (CAS), TTL 2d". indexarr's
// download verb owns the .grab ring; this owns .query.
//
// It exists because status.queriesInWindow is a WINDOW, and a status field
// alone cannot express one: there is no window-start field on IndexerStatus
// and nothing in the system ever lowers a counter. A plain increment would
// therefore climb forever, and the first time it passed spec.limits.queryLimit
// the indexer would be skipped with "query limit reached" for good -- the
// Indexer reconciler reads the same field for its RateLimited condition and
// would latch that too. The ring is the source of truth and
// status.queriesInWindow is its projection, exactly as the grab ring and
// status.grabsInWindow relate.
//
// Both of an indexer's query paths count here: the search fan-out, and the
// RSS poll, which makes up to four Torznab requests per poll. Prowlarr counts
// IndexerQuery and IndexerRss together against QueryLimit. The poll cannot
// import this package (this package imports app/indexer/worker/rss for
// ProjectRelease), so app/indexer/run.go hands it [CountQuery] through
// rss.Deps.CountQuery -- one ring, one key, one window for both.
const (
	// maxQueryRingEntries caps the ring so a busy indexer cannot turn a KV
	// value into a megabyte. At this size the JSON is roughly 45KB.
	//
	// The count SATURATES here: an indexer answering more than this many
	// queries in its window reports maxQueryRingEntries, so a queryLimit
	// above it can never be reached. Real Torznab query limits are in the
	// hundreds per day, so the ceiling is far above any configured limit;
	// the alternative -- an uncapped ring -- is how an operator's etcd and
	// broker both suffer for one misconfigured indexer.
	maxQueryRingEntries = 4096

	// queryCASAttempts bounds the optimistic loop. Contention is two of this
	// process's own fan-out goroutines touching one indexer at once, which
	// needs a duplicated IndexerRef in the request; three attempts is
	// generous and a failure is non-fatal to the search.
	queryCASAttempts = 3
)

// QueryRingKey is the key spec §5 names for the query ring.
//
// The UID comes from the LIVE Indexer object, never from a caller-supplied
// ref: a stale UID would open a second ring for one indexer and halve its
// apparent traffic. It is exported so D1-9's e2e can assert the ring without
// reimplementing the escape.
//
// events.KVKeyToken is not optional. A NATS KV key must match
// ^[-/_=\.a-zA-Z0-9]+$ with no leading or trailing "." and no "..", nothing
// in the Go types enforces it, and an illegal key fails Put and Delete alike
// -- which in Phase C left an object undeletable, because the finalizer's
// Delete validated the key exactly as the Put had.
func QueryRingKey(uid string) string { return events.KVKeyToken(uid) + ".query" }

// queryWindow is the limit window. Prowlarr counts queries over 1h or 24h
// (docs/research/indexers.md §6); the CRD's default unit is day, and a nil
// spec.limits still counts -- the counter is observability whether or not a
// limit is configured.
func queryWindow(idx *indexv1alpha1.Indexer) time.Duration {
	if idx.Spec.Limits != nil && idx.Spec.Limits.Unit == indexv1alpha1.LimitUnitHour {
		return time.Hour
	}
	return 24 * time.Hour
}

// pruneQueryRing drops entries older than cutoff and keeps the newest
// maxEntries. It is pure so the window arithmetic is testable without a bus.
func pruneQueryRing(ring []int64, cutoff time.Time, maxEntries int) []int64 {
	out := ring[:0:0]
	for _, at := range ring {
		if at >= cutoff.Unix() {
			out = append(out, at)
		}
	}
	if len(out) > maxEntries {
		out = out[len(out)-maxEntries:]
	}
	return out
}

// CountQuery records one query against idx in kv (the
// clustarr-indexer-limits bucket) and returns how many are in the current
// window. It is the RSS poll's entry point to the ring the search fan-out
// counts into; see the note above maxQueryRingEntries.
func CountQuery(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, now time.Time) (int32, error) {
	return countQuery(ctx, kv, idx, now)
}

// countQuery records one query against idx and returns how many are in the
// current window.
//
// Unlike the grab ring there is no idempotency key: a search RPC is
// request/reply, so a repeated search is a genuinely repeated query against
// the indexer and must count again.
//
// The caller treats an error as NON-FATAL and leaves status.queriesInWindow
// alone: an accounting outage must not fail a search that the indexer
// answered, and the projection catches up at the next query.
func countQuery(
	ctx context.Context,
	kv events.KV,
	idx *indexv1alpha1.Indexer,
	now time.Time,
) (int32, error) {
	key := QueryRingKey(string(idx.UID))
	cutoff := now.Add(-queryWindow(idx))

	var lastErr error
	for range queryCASAttempts {
		var (
			ring []int64
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
					"app/indexer/search: replacing an undecodable query ring",
					"indexer", idx.Name, "err", uerr)
				ring = nil
			}
		case errors.Is(err, events.ErrKeyNotFound):
		default:
			return 0, err
		}

		ring = pruneQueryRing(append(pruneQueryRing(ring, cutoff, maxQueryRingEntries), now.Unix()),
			cutoff, maxQueryRingEntries)

		val, err := json.Marshal(ring)
		if err != nil {
			return 0, err
		}
		if have {
			_, err = kv.Update(ctx, key, val, rev)
		} else {
			_, err = kv.Create(ctx, key, val)
		}
		switch {
		case err == nil:
			return int32(len(ring)), nil
		case errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyExists):
			lastErr = err
		default:
			return 0, err
		}
	}
	return 0, fmt.Errorf("app/indexer/search: query ring CAS gave up after %d attempts: %w",
		queryCASAttempts, lastErr)
}
