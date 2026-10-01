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

// Package limits is spec §5's clustarr-indexer-limits: per Indexer, a
// timestamp ring of the queries and one of the grabs made against it in the
// spec.limits window, "<indexer-uid>.query, .grab timestamp rings (CAS), TTL
// 2d".
//
// The rings are the source of truth for an Indexer's limits, and every
// decision reads them, never status. status.queriesInWindow and
// status.grabsInWindow are the Indexer reconciler's projection of them for an
// operator, refreshed when it reconciles; a decision gated on that
// projection -- as the search fan-out's was until 2026-10-01 -- refuses an
// indexer that reached its limit and then went quiet for good, because
// nothing that counts ever runs again to bring the projection down.
//
// [ReserveQuery] and [ReserveGrab] are the gate: one compare-and-swap that
// either refuses -- the window is at the limit, nothing is appended, and
// [Reservation.RetryAt] says when it next has room -- or appends and
// allows. Every path that contacts an indexer reserves BEFORE the request:
// the search fan-out and the RSS poll a query each (Prowlarr counts
// IndexerQuery and IndexerRss together against QueryLimit, one per request),
// and the download verb and catalogarr's grab path a grab. An Indexer with
// no limit, or Prowlarr's "0 is none", is always allowed and still counted:
// the count is observability whether or not a limit is set.
//
// It is a leaf -- the Indexer type, the bus and nothing of indexarr's -- so
// app/catalog/worker/grab can reserve a grab before it creates the Download,
// which is where a grab limit has to hold: a direct-source Download never
// reaches the download verb at all.
package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

const (
	// maxQueryEntries caps the query ring so a busy indexer cannot turn a KV
	// value into a megabyte; at this size the JSON is roughly 45KB. The
	// count SATURATES here, so a queryLimit above it is never reached. Real
	// Torznab query limits are in the hundreds per day.
	maxQueryEntries = 4096

	// maxGrabEntries caps the grab ring the same way.
	maxGrabEntries = 512

	// casAttempts bounds the optimistic loop. Contention is two requests to
	// one indexer at the same instant; a failure is the caller's to judge.
	casAttempts = 3
)

// QueryKey is the query ring's key. The UID must come from the LIVE Indexer,
// never from a caller-supplied ref: a stale UID would open a second ring for
// one indexer and halve its apparent traffic.
//
// events.KVKeyToken is not optional. A NATS KV key must match
// ^[-/_=\.a-zA-Z0-9]+$ with no leading or trailing "." and no "..", nothing
// in the Go types enforces it, and an illegal key fails Put and Delete alike.
func QueryKey(uid string) string { return events.KVKeyToken(uid) + ".query" }

// GrabKey is the grab ring's key; see [QueryKey].
func GrabKey(uid string) string { return events.KVKeyToken(uid) + ".grab" }

// Window is idx's limit window. Prowlarr counts over 1h or 24h
// (docs/research/indexers.md §6); the CRD's default unit is day, and a nil
// spec.limits still has one.
func Window(idx *indexv1alpha1.Indexer) time.Duration {
	if idx.Spec.Limits != nil && idx.Spec.Limits.Unit == indexv1alpha1.LimitUnitHour {
		return time.Hour
	}
	return 24 * time.Hour
}

// QueryLimit and GrabLimit are idx's configured limits, nil for none. A limit
// of 0 or less is none, as Prowlarr's IndexerLimitService reads it
// (QueryLimit > 0).
func QueryLimit(idx *indexv1alpha1.Indexer) *int32 {
	if idx.Spec.Limits == nil {
		return nil
	}
	return positive(idx.Spec.Limits.QueryLimit)
}

// GrabLimit is idx's grab limit; see [QueryLimit].
func GrabLimit(idx *indexv1alpha1.Indexer) *int32 {
	if idx.Spec.Limits == nil {
		return nil
	}
	return positive(idx.Spec.Limits.GrabLimit)
}

func positive(l *int32) *int32 {
	if l == nil || *l <= 0 {
		return nil
	}
	return l
}

// Reservation is one ring decision.
type Reservation struct {
	// Allowed is false when the window was at the limit: nothing was
	// appended, and the caller must not contact the indexer.
	Allowed bool

	// Counted is true when this call appended an entry. An allowed grab of a
	// GUID already in the ring, or a direct grab older than the window, is
	// not counted.
	Counted bool

	// Count is how many entries the window holds after this call.
	Count int32

	// RetryAt is the earliest instant the window has room for one more
	// entry, zero when it has room now or no limit is configured.
	RetryAt time.Time
}

// Crossed reports whether this call is the one that filled the window --
// the "limited" transition IndexerEvent announces once.
func (r Reservation) Crossed(limit *int32) bool {
	limit = positive(limit)
	return r.Counted && limit != nil && r.Count == *limit
}

// ReserveQuery reserves one query against idx at now: refused when the
// window already holds spec.limits.queryLimit, appended otherwise. There is
// no idempotency key -- a repeated search is a genuinely repeated request to
// the indexer.
func ReserveQuery(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, now time.Time) (Reservation, error) {
	return reserve(ctx, kv, idx, queryRing, QueryLimit(idx), "", now, now, true)
}

// ReserveGrab reserves the grab of guid against idx at now: refused when the
// window already holds spec.limits.grabLimit, appended otherwise. A GUID
// already in the window holds its slot and is allowed without counting
// again, so every path that touches one grab -- catalogarr's grab before
// the Download, the download verb's fetch, the direct-grab counter -- counts
// it once between them.
func ReserveGrab(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, guid string, now time.Time) (Reservation, error) {
	return reserve(ctx, kv, idx, grabRing, GrabLimit(idx), guid, now, now, true)
}

// CountGrabAt counts a grab that already happened, at `at`, and never
// refuses: the direct-grab counter meets a Download after it exists. A grab
// older than the window is not counted -- the counter meets every existing
// Download again each time indexarr starts -- and a creation stamp ahead of
// now is clock skew, counted at now.
func CountGrabAt(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, guid string, at, now time.Time) (Reservation, error) {
	if at.After(now) {
		at = now
	}
	return reserve(ctx, kv, idx, grabRing, GrabLimit(idx), guid, at, now, false)
}

// Usage is a ring's window as it stands, for the reconciler's projection.
type Usage struct {
	Count   int32
	RetryAt time.Time
}

// Queries reads idx's query window at now without changing it.
func Queries(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, now time.Time) (Usage, error) {
	return usage(ctx, kv, idx, queryRing, QueryLimit(idx), now)
}

// Grabs reads idx's grab window at now without changing it.
func Grabs(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, now time.Time) (Usage, error) {
	return usage(ctx, kv, idx, grabRing, GrabLimit(idx), now)
}

// entry is one ring entry. The query ring stores the timestamp alone; the
// grab ring stores the GUID too, which is what makes counting a grab
// idempotent.
type entry struct {
	GUID string `json:"g"`
	At   int64  `json:"t"` // unix seconds
}

// ring is one of the two rings' key, cap and wire shape. The shapes predate
// this package and are kept, so a ring written before it reads the same.
type ring struct {
	what   string
	key    func(uid string) string
	max    int
	decode func([]byte) ([]entry, error)
	encode func([]entry) ([]byte, error)
}

var queryRing = ring{
	what: "query",
	key:  QueryKey,
	max:  maxQueryEntries,
	decode: func(b []byte) ([]entry, error) {
		var ats []int64
		if err := json.Unmarshal(b, &ats); err != nil {
			return nil, err
		}
		out := make([]entry, len(ats))
		for i, at := range ats {
			out[i] = entry{At: at}
		}
		return out, nil
	},
	encode: func(es []entry) ([]byte, error) {
		ats := make([]int64, len(es))
		for i, e := range es {
			ats[i] = e.At
		}
		return json.Marshal(ats)
	},
}

var grabRing = ring{
	what: "grab",
	key:  GrabKey,
	max:  maxGrabEntries,
	decode: func(b []byte) ([]entry, error) {
		var es []entry
		err := json.Unmarshal(b, &es)
		return es, err
	},
	encode: func(es []entry) ([]byte, error) { return json.Marshal(es) },
}

// read returns the ring's live entries at cutoff, its revision and whether
// the key exists. A value that will not decode is replaced: failing shut
// would wedge counting for the bucket's whole 2d TTL.
func (r ring) read(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, cutoff time.Time) ([]entry, uint64, bool, error) {
	ent, err := kv.Get(ctx, r.key(string(idx.UID)))
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		return nil, 0, false, nil
	case err != nil:
		return nil, 0, false, err
	}
	es, uerr := r.decode(ent.Value)
	if uerr != nil {
		logging.FromContext(ctx).Warn("app/indexer/limits: replacing an undecodable ring",
			"ring", r.what, "indexer", idx.Name, "err", uerr)
		es = nil
	}
	return prune(es, cutoff, r.max), ent.Revision, true, nil
}

func reserve(
	ctx context.Context,
	kv events.KV,
	idx *indexv1alpha1.Indexer,
	r ring,
	limit *int32,
	guid string,
	at, now time.Time,
	enforce bool,
) (Reservation, error) {
	window := Window(idx)
	cutoff := now.Add(-window)

	var lastErr error
	for range casAttempts {
		es, rev, have, err := r.read(ctx, kv, idx, cutoff)
		if err != nil {
			return Reservation{}, err
		}
		held := Reservation{Allowed: true, Count: int32(len(es)), RetryAt: retryAt(es, limit, window)} //nolint:gosec // capped at maxQueryEntries
		if guid != "" && slices.ContainsFunc(es, func(e entry) bool { return e.GUID == guid }) {
			return held, nil
		}
		if at.Before(cutoff) {
			return held, nil
		}
		if enforce && limit != nil && len(es) >= int(*limit) {
			held.Allowed = false
			return held, nil
		}

		es = prune(append(es, entry{GUID: guid, At: at.Unix()}), cutoff, r.max)
		val, err := r.encode(es)
		if err != nil {
			return Reservation{}, err
		}
		key := r.key(string(idx.UID))
		if have {
			_, err = kv.Update(ctx, key, val, rev)
		} else {
			_, err = kv.Create(ctx, key, val)
		}
		switch {
		case err == nil:
			return Reservation{
				Allowed: true, Counted: true,
				Count:   int32(len(es)), //nolint:gosec // capped at maxQueryEntries
				RetryAt: retryAt(es, limit, window),
			}, nil
		case errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyExists):
			lastErr = err
		default:
			return Reservation{}, err
		}
	}
	return Reservation{}, fmt.Errorf("app/indexer/limits: %s ring CAS gave up after %d attempts: %w",
		r.what, casAttempts, lastErr)
}

func usage(ctx context.Context, kv events.KV, idx *indexv1alpha1.Indexer, r ring, limit *int32, now time.Time) (Usage, error) {
	window := Window(idx)
	es, _, _, err := r.read(ctx, kv, idx, now.Add(-window))
	if err != nil {
		return Usage{}, err
	}
	return Usage{Count: int32(len(es)), RetryAt: retryAt(es, limit, window)}, nil //nolint:gosec // capped at maxQueryEntries
}

// prune drops entries older than cutoff and keeps the newest maxEntries. It
// is pure so the window arithmetic is testable without a bus.
func prune(es []entry, cutoff time.Time, maxEntries int) []entry {
	out := es[:0:0]
	for _, e := range es {
		if e.At >= cutoff.Unix() {
			out = append(out, e)
		}
	}
	if len(out) > maxEntries {
		out = out[len(out)-maxEntries:]
	}
	return out
}

// retryAt is when the window next has room for one more entry: once all but
// limit-1 of its entries have aged out. An entry stays while it is no older
// than the window (prune keeps At >= cutoff), so it is gone a second after.
// Zero means there is room now, or no limit.
func retryAt(es []entry, limit *int32, window time.Duration) time.Time {
	limit = positive(limit)
	if limit == nil || len(es) < int(*limit) {
		return time.Time{}
	}
	ats := make([]int64, len(es))
	for i, e := range es {
		ats[i] = e.At
	}
	slices.Sort(ats) // a direct grab is counted at its creation time, so not always last
	return time.Unix(ats[len(ats)-int(*limit)], 0).UTC().Add(window + time.Second)
}
