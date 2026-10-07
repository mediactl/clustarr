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

package indexer

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/rssschedule"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/app/indexer/status/statuspatch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Indexer health and RSS scheduling in the manager (ADR-0019 §7.5, W34, W35,
// P87, P91; rulings R18, R19). The index agent reports every query's outcome
// in clustarr-indexer-health and writes no status; this controller runs the
// escalation ladder from the record, writes the worker field set under
// k8s.ManagerIndexWorker, and publishes every scheduled RSS poll.

const (
	// rssPollGrace is how long a reached slot may go without a reported
	// outcome before the controller treats its poll as skipped (inside a
	// backoff window, at the query limit) or lost and schedules the next:
	// longer than one poll (maxPages requests of spec.timeout each).
	rssPollGrace = 5 * time.Minute

	// rssClockSkew is how far before a slot a reported outcome may be
	// stamped and still count as that slot's poll: the agent and the
	// manager read different clocks, and status keeps whole seconds.
	rssClockSkew = 30 * time.Second
)

// healthSeen is what this leader last incorporated from an Indexer's health
// record: the failure and success instants (whole seconds, as status keeps
// them) and the record's indexedReleases counter.
//
// R19 incorporates by timestamps, and two of them need this memo. Without
// it a success after the run's last failure would step the ladder down on
// every pass (RecordSuccess keeps status.lastFailureAt while the level is
// above zero), and once the ladder reached zero (which clears
// status.lastFailureAt) the run's last failure would read as new and
// escalate again. The memo is leader-local, like the caps memo: after a
// leader change it starts empty, and a record's last failure and success
// are applied once more -- a clean status stays clean (the failure and the
// success cancel), and an indexer mid-ladder steps down one extra rung.
type healthSeen struct {
	failureAt, successAt time.Time
	indexed              int64
	known                bool
}

// rssSlot is the last RSS poll this leader scheduled for an Indexer, at the
// generation it was scheduled for.
type rssSlot struct {
	at         time.Time
	generation int64
}

// HealthRecordSource wakes an Indexer on its clustarr-indexer-health record
// (the record's item is the Indexer). It is leader-only, as every
// controller source is.
func HealthRecordSource(bus events.Bus) *recordsource.Source[reconcile.Request] {
	return recordsource.NewItems(bus, events.BucketIndexerHealth, func(item schema.ItemRef, _ []byte) []reconcile.Request {
		if item.Kind != "Indexer" || item.Name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: item.Namespace, Name: item.Name}}}
	})
}

// healthReader is the manager's read half of clustarr-indexer-health.
func (r *Reconciler) healthReader() *records.Reader[*schema.IndexerHealthRecord] {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.health == nil {
		r.health = records.NewReader(r.Bus.KV(events.BucketIndexerHealth), agentrecords.IndexerHealth())
	}
	return r.health
}

// apiReader is the uncached reader the incorporation's compare-and-swap
// reads through: a cached read lags the write it raced.
func (r *Reconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// incorporateHealth applies idx's health record to its worker field set
// under k8s.ManagerIndexWorker, as a compare-and-swap from a fresh read, and
// copies the result onto idx.Status so the rest of the pass derives Healthy,
// Ready, the requeue and the RSS schedule from it. It publishes the ladder's
// disabled and recovered events after the apply lands. A missing record is
// nothing to incorporate.
func (r *Reconciler) incorporateHealth(ctx context.Context, idx *indexv1alpha1.Indexer) error {
	if r.Bus == nil || idx.UID == "" {
		return nil
	}
	rec, _, ok, err := r.healthReader().Get(ctx, agentrecords.IndexerHealthKey(string(idx.UID)))
	if err != nil {
		return fmt.Errorf("indexer: read the health record of %s: %w", client.ObjectKeyFromObject(idx), err)
	}
	seen := r.seenHealth(idx.UID)
	if !ok {
		// Nothing reported yet, or the record is gone (its TTL, a
		// recreated bucket): the next record's counter starts from zero.
		seen.indexed, seen.known = 0, true
		r.markHealth(idx.UID, seen)
		return nil
	}
	var out incorporation
	prev, _, err := statuspatch.PatchCAS(ctx, r.apiReader(), r.Client, k8s.ManagerIndexWorker, client.ObjectKeyFromObject(idx),
		func(fresh *indexv1alpha1.Indexer, ac *indexac.IndexerStatusApplyConfiguration) bool {
			out = incorporate(fresh.Status, rec, seen)
			if !out.changed {
				return true
			}
			workerAC(ac, out.next)
			return false
		})
	if err != nil {
		return fmt.Errorf("indexer: incorporate the health record of %s: %w", client.ObjectKeyFromObject(idx), err)
	}
	r.markHealth(idx.UID, out.seen)
	if !out.changed {
		return nil
	}
	copyWorkerFields(&idx.Status, out.next)
	if out.esc != nil {
		// After the apply, never before: the status is the record, the
		// event is history. prev is the read the applied escalation was
		// measured from.
		idxstatus.PublishTransitions(ctx, r.Bus, prev, idxstatus.Transition{
			Prev: prev.Status, Escalation: out.esc, Failed: out.failed, At: out.at,
		})
	}
	logging.FromContext(ctx).Debug("indexer: incorporated the health record",
		"indexer", client.ObjectKeyFromObject(idx), "escalationLevel", out.next.EscalationLevel,
		"failed", out.failed, "seq", rec.Seq)
	return nil
}

// incorporation is one pass's result of [incorporate].
type incorporation struct {
	// next is the status with the worker fields after the record.
	next indexv1alpha1.IndexerStatus
	// esc is the last ladder step applied, nil when none was; failed is
	// true when it came from RecordFailure, and at is when it happened.
	esc    *idxstatus.Escalation
	failed bool
	at     time.Time
	// seen is the memo after the record.
	seen healthSeen
	// changed is false when next equals the status on every worker field.
	changed bool
}

// incorporate is R19 over cur: a record failure newer than status'
// lastFailureAt (and than anything this leader incorporated) applies
// RecordFailure at the failure's instant; then a record success newer than
// the record's and status' last failure (and than the last success this
// leader incorporated) applies RecordSuccess. Several failures, or several
// successes, between two passes are one rung. indexedReleases grows by the
// record counter's growth since the last pass, lastRssAt and lastRssNewCount
// follow the record's last read of the feed. It is pure.
func incorporate(cur indexv1alpha1.IndexerStatus, rec *schema.IndexerHealthRecord, seen healthSeen) incorporation {
	st := *cur.DeepCopy()
	out := incorporation{seen: seen}

	failedAt := wholeSecond(rec.LastFailureAt)
	if !failedAt.IsZero() && failedAt.After(statusTime(st.LastFailureAt)) &&
		failedAt.After(seen.failureAt) && failedAt.After(seen.successAt) {
		e := idxstatus.RecordFailure(st, failedAt, rec.LastFailure)
		st = stepped(st, e)
		out.esc, out.failed, out.at = &e, true, failedAt
		out.seen.failureAt = failedAt
	}
	if okAt := wholeSecond(rec.LastSuccessAt); !okAt.IsZero() && okAt.After(failedAt) &&
		okAt.After(statusTime(st.LastFailureAt)) && okAt.After(out.seen.successAt) {
		if e := idxstatus.RecordSuccess(st, okAt); e.Changed {
			st = stepped(st, e)
			out.esc, out.failed, out.at = &e, false, okAt
		}
		out.seen.successAt = okAt
	}

	// indexedReleases accumulates in status, which survives the record: the
	// record's counter restarts with a new record (its TTL, a recreated
	// bucket), so only its growth since the last pass is added. A leader
	// that meets a record it has not seen takes its counter as already
	// incorporated, unless it is the record's first write.
	switch {
	case !seen.known:
		if rec.Seq <= 1 {
			st.IndexedReleases += rec.IndexedReleases
		}
	case rec.IndexedReleases >= seen.indexed:
		st.IndexedReleases += rec.IndexedReleases - seen.indexed
	default:
		st.IndexedReleases += rec.IndexedReleases
	}
	out.seen.indexed, out.seen.known = rec.IndexedReleases, true

	if readAt := wholeSecond(rec.LastRssAt); !readAt.IsZero() && readAt.After(statusTime(st.LastRssAt)) {
		st.LastRssAt = new(metav1.NewTime(readAt))
		st.LastRssNewCount = rec.LastRssNewCount
	}

	out.next = st
	out.changed = !sameWorkerFields(cur, st)
	return out
}

// stepped is idxstatus.ApplyEscalation onto a status rather than an apply.
func stepped(st indexv1alpha1.IndexerStatus, e idxstatus.Escalation) indexv1alpha1.IndexerStatus {
	st.InitialFailureAt = e.InitialFailure(st)
	st.EscalationLevel = e.FailureLevel
	st.LastFailure = e.LastFailureMsg
	st.DisabledUntil = e.DisabledUntil
	st.LastFailureAt = e.LastFailureAt
	return st
}

// workerAC writes next's eight worker fields onto a WorkerFields-seeded
// apply, ASSIGNING the pointers so a cleared one is removed from the seed
// rather than carried forward (idxstatus.ApplyEscalation's reason).
func workerAC(ac *indexac.IndexerStatusApplyConfiguration, next indexv1alpha1.IndexerStatus) {
	ac.WithEscalationLevel(next.EscalationLevel).
		WithLastFailure(next.LastFailure).
		WithLastRssNewCount(next.LastRssNewCount).
		WithIndexedReleases(next.IndexedReleases)
	ac.DisabledUntil = next.DisabledUntil
	ac.InitialFailureAt = next.InitialFailureAt
	ac.LastFailureAt = next.LastFailureAt
	ac.LastRssAt = next.LastRssAt
}

// copyWorkerFields copies the worker field set from src onto dst.
func copyWorkerFields(dst *indexv1alpha1.IndexerStatus, src indexv1alpha1.IndexerStatus) {
	dst.EscalationLevel = src.EscalationLevel
	dst.DisabledUntil = src.DisabledUntil
	dst.InitialFailureAt = src.InitialFailureAt
	dst.LastFailureAt = src.LastFailureAt
	dst.LastFailure = src.LastFailure
	dst.LastRssAt = src.LastRssAt
	dst.LastRssNewCount = src.LastRssNewCount
	dst.IndexedReleases = src.IndexedReleases
}

// sameWorkerFields reports whether a and b agree on every worker field, at
// status' whole-second precision.
func sameWorkerFields(a, b indexv1alpha1.IndexerStatus) bool {
	return a.EscalationLevel == b.EscalationLevel && a.LastFailure == b.LastFailure &&
		a.LastRssNewCount == b.LastRssNewCount && a.IndexedReleases == b.IndexedReleases &&
		statusTime(a.DisabledUntil).Equal(statusTime(b.DisabledUntil)) &&
		statusTime(a.InitialFailureAt).Equal(statusTime(b.InitialFailureAt)) &&
		statusTime(a.LastFailureAt).Equal(statusTime(b.LastFailureAt)) &&
		statusTime(a.LastRssAt).Equal(statusTime(b.LastRssAt))
}

// wholeSecond is a record instant at status' precision: metav1.Time
// serialises whole seconds, so a record time compared unrounded with the
// status it was written into would read as newer on every pass.
func wholeSecond(t *time.Time) time.Time {
	if t == nil || t.IsZero() {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Second)
}

// statusTime is a status instant, the zero time when absent.
func statusTime(t *metav1.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Second)
}

func (r *Reconciler) seenHealth(uid types.UID) healthSeen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.healthSeen[uid]
}

func (r *Reconciler) markHealth(uid types.UID, seen healthSeen) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.healthSeen == nil {
		r.healthSeen = map[types.UID]healthSeen{}
	}
	r.healthSeen[uid] = seen
}

// scheduleRSS publishes idx's next RSS poll (ruling R18; P91): the agent's
// poll no longer chains itself, so every poll is this controller's.
//
//   - With nothing scheduled by this leader, or a new generation (a spec
//     edit, a re-enable), it seeds at rssschedule.NextPollAt -- the slot the
//     last read of the feed implies, or now -- held past any backoff window
//     and any full query window.
//   - While the slot is pending, it moves it only later: a backoff window or
//     a full query window that ends after it. The scheduled publish carries
//     Nats-Rollup: sub, so the later slot replaces the pending one.
//   - Once the slot is reached and the poll's outcome is in the record --
//     status.lastRssAt or status.lastFailureAt at or after it -- the next
//     poll is one interval after the slot (or the last read), held as above.
//     A failed poll therefore waits an interval like a successful one, and
//     the ladder's window when that is longer.
//   - A reached slot with no outcome after rssPollGrace was skipped (backoff,
//     the query limit) or lost; the cadence resumes one interval after it.
//
// It returns when this controller should look again for the third and
// fourth cases, zero when a record or the tick will do.
func (r *Reconciler) scheduleRSS(ctx context.Context, idx *indexv1alpha1.Indexer, queryRetry, now time.Time) (time.Time, error) {
	if !ptr.Deref(idx.Spec.Enabled, true) || !ptr.Deref(idx.Spec.EnableRss, true) {
		r.forgetSlot(idx.UID)
		return time.Time{}, nil
	}
	if r.Bus == nil {
		logging.FromContext(ctx).Warn(
			"no bus is wired; this Indexer's RSS polls cannot be scheduled and it will never be polled",
			"indexer", client.ObjectKeyFromObject(idx))
		return time.Time{}, nil
	}
	last, ok := r.slot(idx.UID)
	at, recheck, publish := nextRSSSlot(idx, last, ok, queryRetry, now)
	if !publish {
		return recheck, nil
	}
	if err := rssschedule.ScheduleNext(ctx, r.Bus, idx, at); err != nil {
		return time.Time{}, err
	}
	r.setSlot(idx.UID, rssSlot{at: at, generation: idx.Generation})
	return time.Time{}, nil
}

// nextRSSSlot is scheduleRSS's decision, pure: the slot to publish and
// whether to, or when to look again.
func nextRSSSlot(
	idx *indexv1alpha1.Indexer, last rssSlot, scheduled bool, queryRetry, now time.Time,
) (at, recheck time.Time, publish bool) {
	hold := queryRetry
	if d := idx.Status.DisabledUntil; d != nil && d.After(hold) {
		hold = d.Time
	}
	if !scheduled || last.generation != idx.Generation {
		return later(rssschedule.NextPollAt(idx, now), hold), time.Time{}, true
	}
	if now.Before(last.at) {
		if hold.After(last.at) {
			return hold, time.Time{}, true
		}
		return time.Time{}, time.Time{}, false
	}
	interval := rssschedule.Interval(idx)
	from := last.at.Add(-rssClockSkew)
	polled := !statusTime(idx.Status.LastRssAt).Before(from) || !statusTime(idx.Status.LastFailureAt).Before(from)
	if !polled && now.Before(last.at.Add(rssPollGrace)) {
		return time.Time{}, last.at.Add(rssPollGrace), false
	}
	next := last.at.Add(interval)
	if t := idx.Status.LastRssAt; t != nil {
		next = later(next, t.Add(interval))
	}
	return later(later(next, hold), now), time.Time{}, true
}

// later is the later of a and b.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (r *Reconciler) slot(uid types.UID) (rssSlot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.rssSlots[uid]
	return s, ok
}

func (r *Reconciler) setSlot(uid types.UID, s rssSlot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rssSlots == nil {
		r.rssSlots = map[types.UID]rssSlot{}
	}
	r.rssSlots[uid] = s
}

func (r *Reconciler) forgetSlot(uid types.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rssSlots, uid)
}
