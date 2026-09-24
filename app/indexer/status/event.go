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

package status

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// The IndexerEvent producer: design spec §5's
// clustarr.evt.index.indexer.<disabled|recovered|limited>.<uid>, which the
// catalogarr history sink turns into Kubernetes Events on the Indexer.
//
// It lives here for the reason the ladder does. The transitions it reports
// ARE the ladder's (RecordFailure opening a backoff window, RecordSuccess
// closing one) and the counters' (a query or grab window reaching
// spec.limits), and the three writers that apply them -- the search fan-out,
// the RSS poll and the download verb -- all already import this package. Each
// publishes AFTER its status apply lands, best effort: the status is the
// record and the event is history, and failing a poll or a grab over a lost
// event would redo work the indexer already did.

// EscalationAction names the event an escalation transition calls for, or ""
// for none.
//
//   - disabled: a FAILURE opened a backoff window on an indexer that was
//     queryable (no window, or one that had expired). A failure while a
//     window is still open does not re-announce it, and the ladder's zero
//     first rung is not a disable.
//   - recovered: a SUCCESS cleared a backoff window. RecordSuccess drops
//     disabledUntil on the first success after a disable, so this fires once
//     per episode, however many rungs the level still has to climb down.
func EscalationAction(cur indexv1alpha1.IndexerStatus, esc Escalation, failed bool, now time.Time) string {
	switch {
	case failed && esc.DisabledUntil != nil && Healthy(cur, now):
		return events.ActionDisabled
	case !failed && esc.Changed && esc.DisabledUntil == nil && cur.DisabledUntil != nil:
		return events.ActionRecovered
	}
	return ""
}

// LimitCrossed reports whether a window count moving from prev to next has
// just reached limit, which is the "limited" transition. A nil limit is no
// limit; a count already at or past it before this move was announced when
// it got there.
func LimitCrossed(limit *int32, prev, next int32) bool {
	return limit != nil && prev < *limit && next >= *limit
}

// LimitMessage renders a crossed limit for an event's reason, in the same
// words the Indexer reconciler's RateLimited condition uses.
func LimitMessage(what string, count int32, limits *indexv1alpha1.Limits) string {
	unit := indexv1alpha1.LimitUnitDay
	var limit int32
	if limits != nil {
		if limits.Unit != "" {
			unit = limits.Unit
		}
		switch what {
		case "queries":
			if limits.QueryLimit != nil {
				limit = *limits.QueryLimit
			}
		case "grabs":
			if limits.GrabLimit != nil {
				limit = *limits.GrabLimit
			}
		}
	}
	return fmt.Sprintf("%s %d/%d per %s", what, count, limit, unit)
}

// IndexerEvent is one event to publish about idx.
type IndexerEvent struct {
	Action   string
	Reason   string
	Until    *time.Time
	Failures int32
	At       time.Time
}

// EscalationEvent builds the event for an escalation action returned by
// [EscalationAction].
func EscalationEvent(action string, esc Escalation, at time.Time) IndexerEvent {
	e := IndexerEvent{Action: action, At: at}
	if action == events.ActionDisabled {
		e.Reason = esc.LastFailureMsg
		e.Failures = esc.FailureLevel
		if esc.DisabledUntil != nil {
			until := esc.DisabledUntil.UTC()
			e.Until = &until
		}
	}
	return e
}

// PublishIndexerEvent publishes e about idx on
// clustarr.evt.index.indexer.<action>.<uid>. It is best effort -- a nil bus,
// an encode failure or a refused publish is logged and dropped -- because the
// caller has already applied the status that is the real record.
func PublishIndexerEvent(ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer, e IndexerEvent) {
	if bus == nil || idx == nil || e.Action == "" || idx.UID == "" {
		return
	}
	ctx, span := tracing.Start(ctx, "indexarr.status.publish_event")
	defer span.End()
	log := logging.FromContext(ctx)

	name, data, err := schema.Encode(schema.IndexerEvent{
		IndexerRef: schema.Ref{Namespace: idx.Namespace, Name: idx.Name, UID: string(idx.UID)},
		Action:     e.Action,
		Reason:     e.Reason,
		Until:      e.Until,
		Failures:   e.Failures,
		At:         e.At.UTC(),
	})
	if err != nil {
		tracing.RecordError(span, err)
		log.Warn("indexarr: could not encode the indexer event", "action", e.Action, "err", err)
		return
	}
	env := &events.Envelope{
		// One id per transition: the same transition redelivered collapses in
		// the stream's dedup window, a later one of the same kind does not,
		// and two at one instant -- the query and the grab window filling
		// together -- are told apart by their reason.
		ID: strings.Join([]string{
			string(idx.UID), e.Action, strconv.FormatInt(e.At.UnixNano(), 10), reasonKey(e.Reason),
		}, "/"),
		Type:   "index.IndexerEvent",
		Schema: name,
		Source: "indexarr@" + version.String(),
		Key:    idx.Namespace + "/" + idx.Name,
		Time:   e.At.UTC(),
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := bus.Publish(ctx, events.IndexerEventSubject(e.Action, string(idx.UID)), env); err != nil {
		tracing.RecordError(span, err)
		log.Warn("indexarr: could not publish the indexer event", "action", e.Action, "err", err)
	}
}

// reasonKey is a short, fixed-width digest of an event's reason for its
// envelope id: the reason itself is indexer-supplied text of any length.
func reasonKey(reason string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(reason))
	return strconv.FormatUint(uint64(h.Sum32()), 36)
}

// Transition is what one worker apply just changed, as the event producer
// needs it. Prev is the status the apply was seeded from -- the live one the
// writer re-read, never a pre-work snapshot -- and the rest is what the apply
// set.
type Transition struct {
	// Prev is the status before the apply.
	Prev indexv1alpha1.IndexerStatus

	// Escalation is the ladder step the apply made, nil when it made none.
	Escalation *Escalation

	// Failed is true when Escalation came from RecordFailure.
	Failed bool

	// Queries and Grabs are the window counts the apply set, nil for a count
	// it did not move.
	Queries *int32
	Grabs   *int32

	// At is when the transition happened.
	At time.Time
}

// PublishTransitions publishes every event t made true about idx: disabled
// or recovered from the ladder, and limited when a query or grab window
// reached its spec.limits. Call it only after the status apply landed.
func PublishTransitions(ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer, t Transition) {
	if bus == nil || idx == nil {
		return
	}
	if t.Escalation != nil {
		if action := EscalationAction(t.Prev, *t.Escalation, t.Failed, t.At); action != "" {
			PublishIndexerEvent(ctx, bus, idx, EscalationEvent(action, *t.Escalation, t.At))
		}
	}
	limits := idx.Spec.Limits
	if limits == nil {
		return
	}
	if t.Queries != nil && LimitCrossed(limits.QueryLimit, t.Prev.QueriesInWindow, *t.Queries) {
		PublishIndexerEvent(ctx, bus, idx, IndexerEvent{
			Action: events.ActionLimited, Reason: LimitMessage("queries", *t.Queries, limits), At: t.At,
		})
	}
	if t.Grabs != nil && LimitCrossed(limits.GrabLimit, t.Prev.GrabsInWindow, *t.Grabs) {
		PublishIndexerEvent(ctx, bus, idx, IndexerEvent{
			Action: events.ActionLimited, Reason: LimitMessage("grabs", *t.Grabs, limits), At: t.At,
		})
	}
}
