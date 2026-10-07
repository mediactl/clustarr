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
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Verdict is what a worker does after Claim, Defer or Answer (§4.8).
type Verdict int

const (
	// Wrote: the write landed.
	Wrote Verdict = iota + 1
	// NotYet: the record is absent or older than the task. Do not start; nak
	// with NotYetDelay, and ack on the task's last delivery.
	NotYet
	// Dropped: superseded, withdrawn or already answered. Ack; the first
	// answer stands.
	Dropped
	// DroppedFact: a fact over a newer request. Nothing was written; log it
	// at Warn with the task's Seq and inputs, since the loop recovers it from
	// disk (§5.9).
	DroppedFact
)

func (v Verdict) String() string {
	switch v {
	case Wrote:
		return "wrote"
	case NotYet:
		return "not-yet"
	case Dropped:
		return "dropped"
	case DroppedFact:
		return "dropped-fact"
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}

// Answerer is the workers' half (§4.5): Superseded, Claim, Defer, Answer, and
// Seed for the probe importer. Every write re-reads immediately before its
// compare-and-swap (the lost-update rule).
type Answerer[R Record] struct {
	s               store[R]
	writer, version string
}

// NewAnswerer binds spec to kv. writer and writerVersion (the pod name and
// version.String()) are stamped on every claim, deferral and answer; the
// probe passes "" for both, since its Prober carries the pod.
func NewAnswerer[R Record](kv events.KV, spec Spec[R], writer, writerVersion string) *Answerer[R] {
	return &Answerer[R]{s: newStore(kv, spec), writer: writer, version: writerVersion}
}

// Superseded reports whether a task at seq is already superseded, withdrawn
// or answered, so the worker acks it without working.
func (a *Answerer[R]) Superseded(ctx context.Context, key string, seq int64) (bool, error) {
	cur, _, ok, err := a.s.get(ctx, key)
	if err != nil {
		return false, err
	}
	return superseded(a.s.spec, cur, ok, seq), nil
}

func superseded[R Record](spec Spec[R], cur R, ok bool, seq int64) bool {
	if !ok {
		return false
	}
	h := cur.Header()
	switch {
	case h.Seq > seq:
		return true
	case h.Seq < seq:
		return false
	}
	return h.State == StateWithdrawn || spec.Answered(h.State)
}

// Claim marks seq claimed. It writes over requested at seq, over claimed at
// seq only when reclaim says this is a redelivery with no live lease, and
// over deferred at seq once DeferredUntil has passed.
func (a *Answerer[R]) Claim(ctx context.Context, key string, seq int64, reclaim bool) (Verdict, error) {
	now := a.s.now()
	return a.progress(ctx, key, seq, StateClaimed, "claim", func(h *schema.RecordHeader) bool {
		switch h.State {
		case StateClaimed:
			return reclaim
		case StateDeferred:
			return h.DeferredUntil == nil || !now.Before(*h.DeferredUntil)
		}
		return false
	}, func(h *schema.RecordHeader) { h.ClaimedAt, h.DeferredUntil = &now, nil })
}

// Defer postpones seq to until (markers): over requested at seq, or over
// deferred at seq once its DeferredUntil has passed.
func (a *Answerer[R]) Defer(ctx context.Context, key string, seq int64, until time.Time) (Verdict, error) {
	now, until := a.s.now(), until.UTC()
	return a.progress(ctx, key, seq, StateDeferred, "claim", func(h *schema.RecordHeader) bool {
		return h.State == StateDeferred && (h.DeferredUntil == nil || !now.Before(*h.DeferredUntil))
	}, func(h *schema.RecordHeader) { h.DeferredUntil = &until })
}

// progress is Claim's and Defer's loop: a record absent or below seq is
// NotYet (no write), one superseded is Dropped, one requested at seq or
// allowed by may is written in state, stamped.
func (a *Answerer[R]) progress(ctx context.Context, key string, seq int64, state, op string,
	may func(*schema.RecordHeader) bool, stamp func(*schema.RecordHeader),
) (Verdict, error) {
	for range CASAttempts {
		cur, rev, ok, err := a.s.get(ctx, key)
		if err != nil {
			return 0, err
		}
		if !ok || cur.Header().Seq < seq {
			return NotYet, nil
		}
		if superseded(a.s.spec, cur, ok, seq) {
			return Dropped, nil
		}
		if h := cur.Header(); h.State != StateRequested && !may(h) {
			return Dropped, nil
		}
		next, err := a.s.clone(cur)
		if err != nil {
			return 0, err
		}
		nh := next.Header()
		nh.State, nh.Writer, nh.WriterVersion = state, a.writer, a.version
		stamp(nh)
		_, err = a.s.write(ctx, key, rev, next, op)
		if err == nil {
			return Wrote, nil
		}
		if !errorsIsRaced(err) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("records: %s %s/%s: %w", state, a.s.spec.Bucket, key, ErrRaced)
}

// Answer records ans, whose header carries the task's Seq and whose state is
// one Spec.Answered accepts, by §4.8's table: written over an absent or older
// record (the task's inputs with it), over requested, claimed or deferred at
// its Seq, and, for a fact only, over withdrawn at its Seq; Dropped over an
// answer at its Seq, over withdrawn at its Seq, and over a newer record;
// DroppedFact for a fact over a newer record. Failure is clipped to
// MaxFailure. Timestamps of the request it answers are carried.
func (a *Answerer[R]) Answer(ctx context.Context, key string, ans R) (Verdict, error) {
	ah := ans.Header()
	if !a.s.spec.Answered(ah.State) {
		return 0, fmt.Errorf("records: a %s answer cannot be %q", a.s.spec.Remediation, ah.State)
	}
	fact := a.s.spec.Fact != nil && a.s.spec.Fact(ans)
	seq := ah.Seq
	for range CASAttempts {
		cur, rev, ok, err := a.s.get(ctx, key)
		if err != nil {
			return 0, err
		}
		if ok {
			h := cur.Header()
			switch {
			case h.Seq > seq && fact:
				return DroppedFact, nil
			case h.Seq > seq:
				return Dropped, nil
			case h.Seq == seq && a.s.spec.Answered(h.State):
				return Dropped, nil
			case h.Seq == seq && h.State == StateWithdrawn && !fact:
				return Dropped, nil
			}
		}
		next, err := a.s.clone(ans)
		if err != nil {
			return 0, err
		}
		nh := next.Header()
		nh.Failure = clip(nh.Failure, MaxFailure)
		nh.Writer, nh.WriterVersion = a.writer, a.version
		if nh.AnsweredAt == nil {
			at := a.s.now()
			nh.AnsweredAt = &at
		}
		if ok && cur.Header().Seq == seq {
			if nh.RequestedAt.IsZero() {
				nh.RequestedAt = cur.Header().RequestedAt
			}
			if nh.ClaimedAt == nil {
				nh.ClaimedAt = cur.Header().ClaimedAt
			}
		}
		if a.s.spec.Carry != nil {
			a.s.spec.Carry(cur, ok, next)
		}
		_, err = a.s.write(ctx, key, rev, next, "answer")
		if err == nil {
			return Wrote, nil
		}
		if !errorsIsRaced(err) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("records: answer %s/%s: %w", a.s.spec.Bucket, key, ErrRaced)
}

// Seed writes rec unsolicited (the probe importer, §4.12): at the current
// record's Seq (0 when absent), unless keep says the current record already
// answers for rec's inputs. It reports whether it wrote.
func (a *Answerer[R]) Seed(ctx context.Context, key string, rec R, keep func(cur R, ok bool) bool) (bool, error) {
	for range CASAttempts {
		cur, rev, ok, err := a.s.get(ctx, key)
		if err != nil {
			return false, err
		}
		if keep != nil && keep(cur, ok) {
			return false, nil
		}
		next, err := a.s.clone(rec)
		if err != nil {
			return false, err
		}
		nh := next.Header()
		nh.Seq = 0
		if ok {
			nh.Seq = cur.Header().Seq
		}
		nh.Failure = clip(nh.Failure, MaxFailure)
		nh.Writer, nh.WriterVersion = a.writer, a.version
		if a.s.spec.Carry != nil {
			a.s.spec.Carry(cur, ok, next)
		}
		_, err = a.s.write(ctx, key, rev, next, "answer")
		if err == nil {
			return true, nil
		}
		if !errorsIsRaced(err) {
			return false, err
		}
	}
	return false, fmt.Errorf("records: seed %s/%s: %w", a.s.spec.Bucket, key, ErrRaced)
}

func errorsIsRaced(err error) bool { return errors.Is(err, ErrRaced) }
