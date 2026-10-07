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

package rssschedule

import (
	"context"
	"fmt"
	"time"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// DefaultInterval mirrors spec.rssInterval's +kubebuilder:default="15m". It
// is restated here, rather than relied on from the apiserver, because that
// default reaches far fewer objects than it looks like it does -- see
// Interval. TestRssIntervalDefaultMatchesTheGeneratedCRD reads the generated
// schema and fails if the two drift, so this is a mirror and not a second
// source of truth.
const DefaultInterval = 15 * time.Minute

// Interval is how long until the next poll of idx, floored at the CRD's
// own default.
//
// The floor is load-bearing, and the obvious reading of why it is not needed
// is wrong. An apiserver default fills a field that is ABSENT FROM THE
// SUBMITTED JSON. metav1.Duration is a struct and `omitempty` does nothing to
// a struct field, so a typed Go client ALWAYS marshals it: an Indexer created
// through client-go sends `"rssInterval":"0s"` explicitly and is never
// defaulted. Only YAML and unstructured creates -- kubectl apply, the chart
// -- omit the key and get 15m.
//
// Unfloored, that zero schedules the next poll at `now`, which is
// immediately redeliverable: one indexer would be polled as fast as the
// broker could hand the task back.
//
// This is the opposite call from spec.requestDelay, which the Indexer
// reconciler deliberately does NOT floor, because ratelimit.Config documents
// RPS <= 0 as unlimited and an explicit `requestDelay: 0s` is therefore a
// supported "do not pace me". A zero poll interval has no such reading --
// nobody is asking to poll an indexer infinitely often -- so the two must not
// be generalised into one rule.
func Interval(idx *indexv1alpha1.Indexer) time.Duration {
	if d := idx.Spec.RssInterval.Duration; d > 0 {
		return d
	}
	return DefaultInterval
}

// TaskMsgID is the deduplication id for one scheduled poll slot.
//
// It carries the slot, not just the object, on purpose: CLUSTARR_WORK_INDEXARR
// dedups for 1h, so a constant per-object id would make every schedule after
// the first within that hour a no-op and the indexer would silently stop
// polling. Two schedulers converging on the same slot -- the reconciler
// seeding one while a poll schedules the next -- collapse to one delivery,
// which is exactly what we want.
func TaskMsgID(uid string, generation int64, slot time.Time) string {
	return events.MsgIDForObject(uid, generation,
		"rss:"+slot.UTC().Truncate(time.Second).Format(time.RFC3339))
}

// NextPollAt is the slot a poll of idx should be scheduled for, read off the
// status the last poll left behind. It exists so the Indexer reconciler can
// seed the chain (ruling R36) without inventing a cadence of its own.
//
// The arithmetic is chosen so a reconciler seed and the worker's own
// reschedule CONVERGE ON ONE SLOT rather than fighting. The worker stamps
// status.lastRssAt with the same instant it schedules from, so
// lastRssAt + Interval is byte-for-byte the slot the worker already asked
// for; [TaskMsgID] quantises to the second and the work stream deduplicates
// for an hour, so the reconciler's publish stores nothing at all. Seeding at
// `now` on every pass instead would look equally correct and would quietly
// override spec.rssInterval with reprobeInterval -- polling a tracker that
// asked for hourly every fifteen minutes.
//
// A never-polled Indexer, or one whose slot has already passed because the
// chain died, gets `now`: that is the seed, and it is also the repair.
//
// The one case the convergence does not cover is an rssInterval with a
// sub-second component, where the two slots can truncate to different
// seconds. The consequence is a stored duplicate rather than a lost poll --
// a scheduled publish carries Nats-Rollup: sub, so the newer slot REPLACES
// the pending one instead of queuing beside it, and there is still exactly
// one pending poll per indexer.
func NextPollAt(idx *indexv1alpha1.Indexer, now time.Time) time.Time {
	if at := idx.Status.LastRssAt; at != nil && !at.Time.IsZero() {
		if next := at.Add(Interval(idx)); next.After(now) {
			return next
		}
	}
	return now
}

// ScheduleNext publishes the next RssTask for idx, held on the schedule
// subject until at.
//
// Two callers, one function, so the subject and the msg-id cannot be built
// two ways. The Indexer reconciler seeds the chain for an enabled, healthy
// Indexer at [NextPollAt]; the worker schedules the next one at the end of
// EVERY poll, success or failure. Both are needed. Without the worker's,
// polling would stop until the next reconcile; without the reconciler's,
// nothing would ever start it -- which is exactly what shipped before ruling
// R36, so the release firehose never began in a real cluster, and an indexer
// disabled and re-enabled never polled again.
func ScheduleNext(ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer, at time.Time) error {
	if idx.UID == "" {
		// The work subject is keyed by uid. An empty token would publish to
		// a subject no stream claims, which the bus reports only as a
		// publish error at the far end of a poll that already succeeded.
		return fmt.Errorf("rss: schedule %s/%s: the Indexer has no UID", idx.Namespace, idx.Name)
	}
	name, data, err := schema.Encode(schema.RssTask{
		IndexerRef: schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
		Categories: idx.Spec.Categories,
		Since:      newestSeen(idx),
	})
	if err != nil {
		return fmt.Errorf("rss: encode schedule for %s/%s: %w", idx.Namespace, idx.Name, err)
	}
	id := TaskMsgID(string(idx.UID), idx.Generation, at)
	env := &events.Envelope{
		ID:     id,
		Type:   "index.RssTask",
		Schema: name,
		Source: "indexarr@" + version.String(),
		// The work-task envelope key is the same <namespace>/<name> shape as
		// the firehose's, for the same reason: whoever handles it cuts it to
		// recover the namespace.
		Key:  idx.Namespace + "/" + idx.Name,
		Time: at,
		Data: data,
	}
	tracing.Inject(ctx, env)

	// Publish to the TARGET subject; the bus rewrites it onto the schedule
	// holding subject and asks the broker to republish it to the target when
	// the schedule fires. Publishing to the holding subject directly would
	// make the message re-trigger itself.
	_, err = bus.Publish(ctx, events.WorkRSSSubject(string(idx.UID)), env,
		events.WithMsgID(id), events.WithScheduleAt(at))
	if err != nil {
		return fmt.Errorf("rss: schedule %s/%s at %s: %w", idx.Namespace, idx.Name, at, err)
	}
	return nil
}

// newestSeen is the publish time the next poll may stop paging at. Only
// status.lastRssAt records how far this indexer has been read; there is no
// field carrying the newest PUBLISH time, and inventing one would be a CRD
// change no Phase D1 task owns.
func newestSeen(idx *indexv1alpha1.Indexer) *time.Time {
	if at := idx.Status.LastRssAt; at != nil && !at.Time.IsZero() {
		return new(at.Time)
	}
	return nil
}
