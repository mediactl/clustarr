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

package records

import (
	"context"
	"fmt"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Requester is the loop's half (§4.5): it reads records, requests and
// withdraws. Only the remediation loop calls Request and Withdraw.
type Requester[R Record] struct{ s store[R] }

// NewRequester binds spec to kv, the remediation's records bucket.
func NewRequester[R Record](kv events.KV, spec Spec[R]) *Requester[R] {
	return &Requester[R]{s: newStore(kv, spec)}
}

// Get reads key: the record, its revision (0 when absent), and whether it
// decoded. An unreadable record is absent at its revision.
func (q *Requester[R]) Get(ctx context.Context, key string) (R, uint64, bool, error) {
	return q.s.get(ctx, key)
}

// Request writes rec as the requested record at rec.Header().Seq, over the
// record the caller read at rev (0: absent). rec's Seq must be above the
// record's. A record holding an answered fact the caller has not incorporated
// is never replaced (ErrUnincorporatedFact). A record no longer at rev is
// ErrRaced: the planner requeues and decides again. It returns the new
// revision.
func (q *Requester[R]) Request(ctx context.Context, key string, rev uint64, rec R, opts ...Option) (uint64, error) {
	cur, ok, err := q.s.at(ctx, key, rev)
	if err != nil {
		return 0, err
	}
	h := rec.Header()
	if ok && h.Seq <= cur.Header().Seq {
		return 0, fmt.Errorf("records: request %s/%s at seq %d, not above the record's %d", q.s.spec.Bucket, key, h.Seq, cur.Header().Seq)
	}
	if err := q.s.refuseFact(cur, ok, opts); err != nil {
		return 0, err
	}
	h.State = StateRequested
	if h.RequestedAt.IsZero() {
		h.RequestedAt = q.s.now()
	}
	h.ClaimedAt, h.DeferredUntil, h.AnsweredAt = nil, nil, nil
	h.Writer, h.WriterVersion, h.Failure, h.Transient = "", "", "", false
	if q.s.spec.Carry != nil {
		q.s.spec.Carry(cur, ok, rec)
	}
	return q.s.write(ctx, key, rev, rec, "request")
}

// Withdraw closes seq over the record read at rev, by §4.7's table:
// absent, or any state below seq, becomes withdrawn{seq} (an answered fact
// below seq the caller has not incorporated is ErrUnincorporatedFact);
// requested, claimed or deferred at seq becomes withdrawn{seq}; withdrawn,
// answered or failed at seq, and anything above seq, is left alone. It
// returns the new revision, 0 when it wrote nothing.
func (q *Requester[R]) Withdraw(ctx context.Context, key string, rev uint64, seq int64, opts ...Option) (uint64, error) {
	cur, ok, err := q.s.at(ctx, key, rev)
	if err != nil {
		return 0, err
	}
	var next R
	switch {
	case ok && cur.Header().Seq > seq:
		return 0, nil
	case ok && cur.Header().Seq == seq:
		if st := cur.Header().State; st == StateWithdrawn || q.s.spec.Answered(st) {
			return 0, nil
		}
		if next, err = q.s.clone(cur); err != nil {
			return 0, err
		}
	default:
		if ferr := q.s.refuseFact(cur, ok, opts); ferr != nil {
			return 0, ferr
		}
		uid, sub, perr := keyParts(key)
		if perr != nil {
			return 0, fmt.Errorf("records: withdraw %s/%s: %w", q.s.spec.Bucket, key, perr)
		}
		next = q.s.spec.New()
		nh := next.Header()
		nh.MediaFile, nh.Sub, nh.RequestedAt = schema.Ref{UID: uid}, sub, q.s.now()
		if ok {
			nh.MediaFile = cur.Header().MediaFile
		}
	}
	nh := next.Header()
	nh.Seq, nh.State = seq, StateWithdrawn
	nh.Writer, nh.WriterVersion = "", ""
	return q.s.write(ctx, key, rev, next, "withdraw")
}
