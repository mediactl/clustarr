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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
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
func Decide(
	ctx context.Context,
	d Deps,
	profile quality.Profile,
	delay catalogv1alpha1.DelayProfileSpec,
	a Approved,
) error {
	ctx, span := tracing.Start(ctx, "grab.Decide")
	defer span.End()

	now := d.now()
	idx, ok := profile.Index(a.Release.Quality)
	atTopTier := ok && idx == 0
	wait := DelayFor(delay, a.Release.Protocol)

	if wait <= 0 || Bypasses(delay, atTopTier, a.Release.FormatScore) {
		return performGrab(ctx, d, a.Namespace, a.Target, a.Keys, a.Release, a.GrabbedBy)
	}

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
	grabAt := kept.FirstSeen.Add(wait)

	schemaName, data, err := schema.Encode(schema.GrabTask{MediaRef: a.Target, Keys: a.Keys})
	if err != nil {
		return err
	}
	msgID := fmt.Sprintf("%s:%d", mediaKey, kept.FirstSeen.Unix())
	env := &events.Envelope{
		ID:     msgID,
		Type:   "catalog.GrabTask",
		Schema: schemaName,
		Source: "catalogarr-worker@" + version.String(),
		Key:    a.Namespace + "/" + a.Target.Name,
		Time:   now,
		Data:   data,
	}
	// A delayed grab is the longest causal gap in the system -- minutes to
	// hours between the search that chose the release and the delivery that
	// grabs it. Without this the grab's span is orphaned from the search that
	// caused it, which is precisely the trace an operator asks for.
	tracing.Inject(ctx, env)
	if _, err := d.Bus.Publish(ctx, events.WorkGrabSubject(mediaKey), env,
		events.WithScheduleAt(grabAt), events.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("grab: publish scheduled grab for %s: %w", mediaKey, err)
	}

	pg := &catalogv1alpha1.PendingGrab{
		ReleaseTitle: kept.Release.Title,
		Protocol:     kept.Release.Protocol,
		GrabAt:       metav1.NewTime(grabAt),
	}
	for _, st := range statusTargets {
		ops, err := kindOpsFor(st.Kind)
		if err != nil {
			return err
		}
		if !ops.recordsPendingGrab() {
			// An Issue has nowhere to show the wait. The grab is still
			// scheduled above and still happens; only the object's view
			// of it is missing.
			continue
		}
		// The whole worker-owned status set is re-declared, not just
		// pendingGrab, and only if nothing wrote the object since it was
		// read: see workerStatus and updateWorkerStatus.
		err = updateWorkerStatus(ctx, d.Client, a.Namespace, st, func(ws *workerStatus) bool {
			ws.PendingGrab = pg
			return true
		})
		if err != nil {
			return fmt.Errorf("grab: record pendingGrab on %s/%s: %w", st.Kind, st.Name, err)
		}
	}

	metrics.SearchDecisionsTotal.WithLabelValues(string(a.Target.Kind), "delayed", string(a.Release.Protocol)).Inc()
	return nil
}
