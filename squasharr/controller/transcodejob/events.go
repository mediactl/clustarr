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

package transcodejob

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// ReasonStarted is the Event reason for a job whose Job was admitted to a
// slot and started running.
const ReasonStarted = "Started"

// transition is one edge a reconcile moved a TranscodeJob across, as both
// history (the §5 transcode.JobEvent, action) and a Kubernetes Event
// (eventType, reason, message).
type transition struct {
	action    string // "" for an edge §5 has no action for (Planned)
	eventType string
	reason    string
	message   string
}

// transitions lists the edges between the status this reconcile read (old)
// and the one it is about to apply (st), in lifecycle order. Each is keyed
// on the field that records it, so a level re-run over unchanged state finds
// none:
//
//   - planned:   phase left Pending with a plan (Planned=True)
//   - skipped:   phase became Skipped (§5 "skipped")
//   - queued:    jobRef was set -- the suspended Job was created (§5 "queued")
//   - started:   startedAt was set -- the Job was admitted (§5 "started")
//   - succeeded: phase became Succeeded (§5 "succeeded")
//   - failed:    phase became Failed (§5 "failed")
func transitions(old, st *transcodev1alpha1.TranscodeJobStatus) []transition {
	var out []transition
	phaseBecame := func(p transcodev1alpha1.TranscodeJobPhase) bool {
		return st.Phase == p && old.Phase != p
	}

	if old.Plan == nil && st.Plan != nil && st.Phase != transcodev1alpha1.TranscodeJobPhaseSkipped {
		out = append(out, transition{
			eventType: corev1.EventTypeNormal, reason: ReasonPlanned,
			message: fmt.Sprintf("planned %s with %s: %s", st.Plan.Mode, st.Plan.Encoder, st.Message),
		})
	}
	if phaseBecame(transcodev1alpha1.TranscodeJobPhaseSkipped) {
		out = append(out, transition{
			action: events.ActionSkipped, eventType: corev1.EventTypeNormal, reason: ReasonSkipped, message: st.Message,
		})
	}
	if old.JobRef == nil && st.JobRef != nil {
		out = append(out, transition{
			action: events.ActionQueued, eventType: corev1.EventTypeNormal, reason: ReasonJobCreated,
			message: fmt.Sprintf("created Job %s suspended; %s", *st.JobRef, st.Message),
		})
	}
	if old.StartedAt == nil && st.StartedAt != nil {
		msg := "the encode started"
		if st.JobRef != nil {
			msg = fmt.Sprintf("Job %s was admitted to a slot and started", *st.JobRef)
		}
		out = append(out, transition{
			action: events.ActionStarted, eventType: corev1.EventTypeNormal, reason: ReasonStarted, message: msg,
		})
	}
	if phaseBecame(transcodev1alpha1.TranscodeJobPhaseSucceeded) {
		out = append(out, transition{
			action: events.ActionSucceeded, eventType: corev1.EventTypeNormal, reason: ReasonJobSucceeded, message: st.Message,
		})
	}
	if phaseBecame(transcodev1alpha1.TranscodeJobPhaseFailed) {
		reason := ReasonJobFailed
		if c := k8s.FindCondition(st.Conditions, transcodev1alpha1.TranscodeJobConditionFailed); c != nil && c.Reason != "" {
			reason = c.Reason
		}
		out = append(out, transition{
			action: events.ActionFailed, eventType: corev1.EventTypeWarning, reason: reason, message: st.Message,
		})
	}
	return out
}

// publishJobEvents publishes each transition that §5 names as one
// clustarr.evt.transcode.job.<action>.<uid> -- the TranscodeJobSubject
// producer §5 assigns to squasharr, which the history sink
// (catalogarr/history) turns into an Event and the DLQ projector resolves
// back to this TranscodeJob. Until this, the subject had no producer.
//
// It runs on the reconcile that OBSERVES the edge, before the status apply
// that records it, with an Envelope id of "<uid>:<action>" -- the shape
// grabarr's DownloadEvent producer uses: a crash or a failed apply after the
// publish re-observes the same edge next time and republishes the same id,
// which the EVENTS stream's duplicate window absorbs. Publishing after the
// apply would lose the event on exactly that crash instead.
//
// It is best effort. The event is history and status is the record, so a
// lost event must not hold a transcode back. A nil Bus publishes nothing.
//
// result is the worker's status.result as read fresh for a job that just
// finished, or nil; it supplies the output figures of a "succeeded".
func (r *Reconciler) publishJobEvents(ctx context.Context, tj *transcodev1alpha1.TranscodeJob,
	st *transcodev1alpha1.TranscodeJobStatus, result *transcodev1alpha1.Result, edges []transition,
) {
	if r.Bus == nil {
		return
	}
	for _, e := range edges {
		if e.action == "" {
			continue
		}
		r.publishJobEvent(ctx, tj, st, result, e)
	}
}

func (r *Reconciler) publishJobEvent(ctx context.Context, tj *transcodev1alpha1.TranscodeJob,
	st *transcodev1alpha1.TranscodeJobStatus, result *transcodev1alpha1.Result, e transition,
) {
	log := logging.FromContext(ctx)
	evt := schema.JobEvent{
		JobRef:    schema.Ref{Namespace: tj.Namespace, Name: tj.Name, UID: string(tj.UID)},
		Action:    e.action,
		InputPath: tj.Spec.SourcePath,
		At:        r.now().Time,
	}
	if tj.Spec.ProfileRef != "" {
		evt.ProfileRef = &schema.Ref{Name: tj.Spec.ProfileRef}
	}
	if st.Plan != nil {
		evt.Mode = string(st.Plan.Mode)
		evt.Encoder = st.Plan.Encoder
	}
	if e.action == events.ActionSkipped || e.action == events.ActionFailed {
		evt.Reason = e.message
	}
	if e.action == events.ActionSucceeded {
		if result != nil {
			evt.OutputPath = result.OutputPath
			evt.OutputBytes = result.OutputSizeBytes
			if p := result.OutputToSourcePercent; p > 0 && p <= 100 {
				evt.SavedPercent = 100 - p
			}
		}
		if st.StartedAt != nil && st.FinishedAt != nil {
			if d := st.FinishedAt.Sub(st.StartedAt.Time); d > 0 {
				evt.DurationSeconds = int64(d.Seconds())
			}
		}
	}

	schemaName, data, err := schema.Encode(evt)
	if err != nil {
		log.Warn("transcodejob: could not encode the job event", "action", e.action, "error", err)
		return
	}
	env := &events.Envelope{
		ID:     string(tj.UID) + ":" + e.action,
		Type:   "transcode.JobEvent",
		Schema: schemaName,
		Source: "squasharr-controller@" + version.String(),
		Key:    tj.Namespace + "/" + tj.Name,
		Time:   evt.At,
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, events.TranscodeJobSubject(e.action, string(tj.UID)), env); err != nil {
		log.Warn("transcodejob: could not publish the job event", "action", e.action, "error", err)
	}
}

// recordEvents emits each transition as a Kubernetes Event on the
// TranscodeJob, after the status apply that recorded it landed, so
// `kubectl describe transcodejob` tells the job's story. A nil Recorder
// records nothing.
func (r *Reconciler) recordEvents(tj *transcodev1alpha1.TranscodeJob, edges []transition) {
	if r.Recorder == nil {
		return
	}
	for _, e := range edges {
		r.Recorder.Eventf(tj, nil, e.eventType, e.reason, "Reconcile", "%s", e.message)
	}
}
