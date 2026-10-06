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

package grab

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/version"
)

// Decide is spec §8.2's "delay" step: grab the approved release now, or hold
// it for the DelayProfile's window first.
//
// It is a pure entry point in the sense that matters: the caller has already
// resolved the quality.Profile and the catalogv1alpha1.DelayProfileSpec
// governing the item, so this package never resolves a profile itself and
// never needs to know how the caller found one. Both the search worker and the
// RSS matcher call it, which is what makes §8.7's "approved -> the same
// delay/lease/grab path (pending CAS keep-best updates a delayed grab)" true
// by construction rather than by two copies agreeing.
//
// On the delayed branch it does three things, in this order:
//
//  1. CAS keep-best into clustarr-pending. A release arriving while another is
//     already waiting replaces it only if it wins profile.UpgradeDecision, and
//     the window's anchor (firstSeen) survives the replacement.
//  2. Publish a GrabTask scheduled for firstSeen+delay, with Msg-Id
//     "<mediaKey>:<firstSeen unix>". The Msg-Id is what makes a second, better
//     candidate inside the same window NOT schedule a second delivery: the
//     anchor is unchanged, so the broker's deduplication window absorbs it.
//  3. Record status.pendingGrab on every status target, under
//     k8s.ManagerCatalogarrGrab. It never writes status.phase -- the Movie
//     and Episode reconcilers recompute Phase=Delayed from pendingGrab under
//     k8s.ManagerCatalogarr, woken by the status.pendingGrab arm of their own
//     predicates.
//
// A grab its Indexer's spec.limits.grabLimit refuses is held the same way,
// until the instant the window next has room (holdForGrabLimit), so a caller
// with one release -- the RSS matcher -- loses nothing to a full window. The
// Sink, which has the ranked list, tries the next release on another indexer
// first (decide with holdOnLimit false).
func Decide(
	ctx context.Context,
	d Deps,
	profile quality.Profile,
	delay catalogv1alpha1.DelayProfileSpec,
	a Approved,
) error {
	return decide(ctx, d, profile, delay, a, true)
}

// decide is Decide, with a grab-limit refusal either held for its retry
// instant (holdOnLimit) or returned as the *GrabLimitError for the caller.
func decide(
	ctx context.Context,
	d Deps,
	profile quality.Profile,
	delay catalogv1alpha1.DelayProfileSpec,
	a Approved,
	holdOnLimit bool,
) error {
	ctx, span := tracing.Start(ctx, "grab.Decide")
	defer span.End()

	idx, ok := profile.Index(a.Release.Quality)
	atTopTier := ok && idx == 0
	wait := DelayFor(delay, a.Release.Protocol)

	if wait <= 0 || Bypasses(delay, atTopTier, a.Release.FormatScore) {
		err := performGrab(ctx, d, a.Namespace, a.Target, a.Keys, a.Release, a.GrabbedBy, a.Purpose)
		var limited *GrabLimitError
		if holdOnLimit && errors.As(err, &limited) {
			return holdForGrabLimit(ctx, d, profile, a, limited.RetryAt)
		}
		return err
	}

	if err := hold(ctx, d, profile, a, func(firstSeen time.Time) time.Time { return firstSeen.Add(wait) }, ""); err != nil {
		return err
	}
	metrics.SearchDecisionsTotal.WithLabelValues(string(a.Target.Kind), "delayed", string(a.Release.Protocol)).Inc()
	return nil
}

// holdForGrabLimit holds a grab its Indexer refused until retryAt, when the
// grab window next has room: the same pending entry, scheduled GrabTask and
// status.pendingGrab as a DelayProfile's hold, so the item reads Delayed with
// the instant it will be grabbed, and the scheduled delivery re-reads the
// entry -- by then possibly a better release -- and grabs it.
//
// The Msg-Id carries the retry instant: the broker would otherwise absorb
// this schedule into a delay's own "<mediaKey>:<firstSeen>" within its
// dedup window, and nothing would fire at retryAt.
func holdForGrabLimit(ctx context.Context, d Deps, profile quality.Profile, a Approved, retryAt time.Time) error {
	logging.FromContext(ctx).Info("grab: the indexer is at its grab limit; holding the grab until its window has room",
		"indexer", a.Release.IndexerRef, "release", a.Release.Title, "retryAt", retryAt)
	return hold(ctx, d, profile, a, func(time.Time) time.Time { return retryAt }, grabLimitMsgIDSuffix(retryAt))
}

func grabLimitMsgIDSuffix(retryAt time.Time) string {
	return fmt.Sprintf(":grablimit:%d", retryAt.Unix())
}

// hold is the delayed half of Decide: CAS keep-best a into clustarr-pending,
// schedule the GrabTask for grabAt(firstSeen) under Msg-Id
// "<mediaKey>:<firstSeen unix><idSuffix>", and record status.pendingGrab.
func hold(
	ctx context.Context,
	d Deps,
	profile quality.Profile,
	a Approved,
	grabAt func(firstSeen time.Time) time.Time,
	idSuffix string,
) error {
	now := d.now()
	// Reject an ungrabbable target before touching the KV bucket: writing a
	// pending entry nothing will ever grab is worse than refusing.
	statusTargets, err := StatusTargets(a.Target, a.Keys)
	if err != nil {
		return err
	}

	// MediaKeyFor, not MediaKey: a pack's pending entry and Msg-Id must be
	// scoped to the episodes it covers, or two seasons of one series collide
	// on one entry.
	mediaKey := MediaKeyFor(a.Namespace, a.Target, a.Keys)
	kept, err := casKeepBest(ctx, d.Bus.KV(events.BucketPending), events.PendingKey(mediaKey), profile,
		pendingValue{Target: a.Target, Keys: a.Keys, Release: a.Release, GrabbedBy: a.GrabbedBy}, now)
	if err != nil {
		return err
	}
	return schedule(ctx, d, a.Namespace, statusTargets, mediaKey, kept, grabAt(kept.FirstSeen), idSuffix)
}

// schedule publishes the GrabTask for the pending entry kept, held until at,
// under Msg-Id "<mediaKey>:<firstSeen unix><idSuffix>", and records
// status.pendingGrab on every status target.
func schedule(
	ctx context.Context,
	d Deps,
	ns string,
	statusTargets []commonv1.MediaRef,
	mediaKey string,
	kept pendingValue,
	at time.Time,
	idSuffix string,
) error {
	now := d.now()
	schemaName, data, err := schema.Encode(schema.GrabTask{MediaRef: kept.Target, Keys: kept.Keys})
	if err != nil {
		return err
	}
	msgID := fmt.Sprintf("%s:%d%s", mediaKey, kept.FirstSeen.Unix(), idSuffix)
	env := &events.Envelope{
		ID:     msgID,
		Type:   "catalog.GrabTask",
		Schema: schemaName,
		Source: "catalogarr-worker@" + version.String(),
		Key:    ns + "/" + kept.Target.Name,
		Time:   now,
		Data:   data,
	}
	// A delayed grab is the longest causal gap in the system -- minutes to
	// hours between the search that chose the release and the delivery that
	// grabs it. Without this the grab's span is orphaned from the search that
	// caused it, which is precisely the trace an operator asks for.
	tracing.Inject(ctx, env)
	if _, err := d.Bus.Publish(ctx, events.WorkGrabSubject(mediaKey), env,
		events.WithScheduleAt(at), events.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("grab: publish scheduled grab for %s: %w", mediaKey, err)
	}

	pg := &catalogv1alpha1.PendingGrab{
		ReleaseTitle: kept.Release.Title,
		Protocol:     kept.Release.Protocol,
		GrabAt:       metav1.NewTime(at),
	}
	for _, st := range statusTargets {
		// The whole worker-owned status set is re-declared, not just
		// pendingGrab, and only if nothing wrote the object since it was
		// read: see workerStatus and updateWorkerStatus.
		err := updateWorkerStatus(ctx, d.Client, ns, st, func(ws *workerStatus) bool {
			ws.PendingGrab = pg
			return true
		})
		if err != nil {
			return fmt.Errorf("grab: record pendingGrab on %s/%s: %w", st.Kind, st.Name, err)
		}
	}

	return nil
}
