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

package remediation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/probestore"
	"github.com/mediactl/clustarr/pkg/records"
)

// EffectKind labels an effect in clustarr_remediation_effects_total.
type EffectKind string

// The effect kinds.
const (
	EffectRecord   EffectKind = "record"
	EffectPublish  EffectKind = "publish"
	EffectWithdraw EffectKind = "withdraw"
	EffectHistory  EffectKind = "history"
	EffectGraftJob EffectKind = "graftJob" // F7.2
	EffectLedger   EffectKind = "ledger"   // F6.3
	EffectObserve  EffectKind = "observe"  // F6.3
)

// Effect is one write a pass owes after its status apply, derived from status
// plus record, so a pass after a crash owes it again (§3.8).
type Effect interface{ Kind() EffectKind }

// RecordWrite creates (Revision 0) or updates (at Revision, the one Gather
// read) one record. Remediation and Lane, when set, count the write into
// clustarr_record_requests_total once it lands; Pace, when set, is the
// records-pacer class whose reservation for this file the write consumes
// once it lands (§4.7).
type RecordWrite struct {
	Bucket, Key       string
	Revision          uint64
	Value             []byte
	Remediation, Lane string
	Pace              string
}

// Publish puts one task on the bus under its Msg-Id, which carries the
// remediation's sequence; At, when set, schedules it.
type Publish struct {
	Subject, MsgID, ExpectStream string
	Envelope                     *events.Envelope
	At                           time.Time
}

// Withdraw closes one remediation's dispatch at Seq through the emitting
// planner's Withdrawer (§4.7's one table). Sub is a subtitle's langKey,
// empty for a one-record remediation.
type Withdraw struct {
	UID string
	Seq int64
	Sub string
}

// Withdrawer is a planner that withdraws its own dispatches.
type Withdrawer interface {
	Withdraw(ctx context.Context, env *Env, w Withdraw) error
}

// History publishes a clustarr.evt.* event, on transitions only.
type History struct {
	Subject, MsgID string
	Envelope       *events.Envelope
}

// Kind implements Effect.
func (RecordWrite) Kind() EffectKind { return EffectRecord }

// Kind implements Effect.
func (Publish) Kind() EffectKind { return EffectPublish }

// Kind implements Effect.
func (Withdraw) Kind() EffectKind { return EffectWithdraw }

// Kind implements Effect.
func (History) Kind() EffectKind { return EffectHistory }

// IsCASMiss reports a record compare-and-swap another writer won.
func IsCASMiss(err error) bool {
	return errors.Is(err, events.ErrKeyExists) || errors.Is(err, events.ErrRevisionMismatch) ||
		errors.Is(err, probestore.ErrConflict) || errors.Is(err, records.ErrRaced) || errors.Is(err, records.ErrUnincorporatedFact)
}

// effectsOutcome is one planner's batch.
type effectsOutcome struct {
	casMiss, transient bool
	failed             outcome // error or panic: remembered for the next pass
}

// runEffects runs one planner's effects in order under isolate; the first
// failure stops the rest of that planner's batch only (§3.6, §3.8).
func (r *Reconciler) runEffects(ctx context.Context, uid string, p Bound, effs []Effect) effectsOutcome {
	var res effectsOutcome
	for _, eff := range effs {
		o := r.isolate(ctx, p.Name(), "effect", 0, func(ctx context.Context) error { return r.runEffect(ctx, uid, p, eff) })
		label := "ok"
		switch {
		case !o.failed():
		case o.casMiss():
			label, res.casMiss = failCASMiss, true
		case o.transient():
			label, res.transient = failTransient, true
		default:
			label, res.failed = o.reason, outcome{reason: o.reason, message: "effect: " + o.message}
		}
		metrics.RemediationEffectsTotal.WithLabelValues(string(p.Name()), string(eff.Kind()), label).Inc()
		if o.failed() {
			break
		}
	}
	return res
}

func (r *Reconciler) runEffect(ctx context.Context, uid string, p Bound, eff Effect) error {
	switch e := eff.(type) {
	case RecordWrite:
		kv := r.env.Bus.KV(e.Bucket)
		var err error
		if e.Revision == 0 {
			_, err = kv.Create(ctx, e.Key, e.Value)
		} else {
			_, err = kv.Update(ctx, e.Key, e.Value, e.Revision)
		}
		if err != nil && !IsCASMiss(err) {
			return Transient(fmt.Errorf("record %s/%s: %w", e.Bucket, e.Key, err))
		}
		if err != nil {
			return err
		}
		if e.Remediation != "" {
			metrics.RecordRequestsTotal.WithLabelValues(e.Remediation, e.Lane).Inc()
		}
		if e.Pace != "" && r.env.Pacer != nil {
			r.env.Pacer.Consume(e.Pace, uid)
		}
		return nil
	case Publish:
		opts := []events.PublishOption{events.WithMsgID(e.MsgID)}
		if e.ExpectStream != "" {
			opts = append(opts, events.WithExpectStream(e.ExpectStream))
		}
		if !e.At.IsZero() {
			opts = append(opts, events.WithScheduleAt(e.At))
		}
		if _, err := r.env.Bus.Publish(ctx, e.Subject, e.Envelope, opts...); err != nil {
			return Transient(fmt.Errorf("publish %s: %w", e.Subject, err))
		}
		return nil
	case History:
		if _, err := r.env.Bus.Publish(ctx, e.Subject, e.Envelope, events.WithMsgID(e.MsgID)); err != nil {
			return Transient(fmt.Errorf("history %s: %w", e.Subject, err))
		}
		return nil
	case Withdraw:
		w, ok := p.unwrap().(Withdrawer)
		if !ok {
			return fmt.Errorf("planner %s emitted a Withdraw and withdraws nothing", p.Name())
		}
		return w.Withdraw(ctx, r.env, e)
	}
	return fmt.Errorf("unknown effect %T", eff)
}
