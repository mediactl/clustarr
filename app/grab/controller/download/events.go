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

package download

import (
	"context"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// publishDownloadEvent publishes one clustarr.evt.download.download.<action>
// transition for dl -- the DownloadEventSubject producer design spec §5
// assigns to grabarr and the history sink (catalogarr/history) turns into
// an Event on the Download. Until this, the subject had no producer and a
// Download's history showed nothing.
//
// It is called on the reconcile that OBSERVES the transition, before the
// status apply that records it, with an Envelope id of "<uid>:<action>":
// a crash or a failed apply after the publish re-observes the same edge on
// the next reconcile and republishes the same id, which the EVENTS
// stream's duplicate window absorbs. Publishing after the apply would lose
// the event on exactly that crash instead.
//
// It is best effort, the same as captionarr's subtitle events: the event is
// history, the status is the record, and a lost event must not hold a
// Download's phase back. A nil Bus (a test that exercises nothing past
// assignment) publishes nothing.
//
// It produces every §5 action: queued (the engine pin), started,
// completed, seedGoalMet (the engine's status.seedGoalReached, first seen),
// failed (a failure first recorded, whatever its reason), blocklisted,
// imported and removed. seedGoalMet had no producer until gap fix Y2 gave
// the engine a way to report the goal apart from status.canBeRemoved, which
// also waits for the import.
func (r *Reconciler) publishDownloadEvent(ctx context.Context, dl *downloadv1alpha1.Download, action, reason string) {
	if r.Bus == nil {
		return
	}
	log := logging.FromContext(ctx)

	size := dl.Status.TotalBytes
	if size <= 0 {
		size = dl.Spec.Release.SizeBytes
	}
	evt := schema.DownloadEvent{
		DownloadRef: schema.Ref{Namespace: dl.Namespace, Name: dl.Name, UID: string(dl.UID)},
		Media:       dl.Spec.Target,
		Action:      action,
		ClientID:    dl.Status.DownloadID,
		Protocol:    dl.Spec.Protocol,
		Title:       dl.Spec.Release.Title,
		InfoHash:    dl.Spec.Release.InfoHash,
		SizeBytes:   size,
		Reason:      reason,
		OutputPath:  dl.Status.OutputPath,
		At:          r.now(),
	}
	if dl.Spec.ClientRef != "" {
		evt.ClientRef = &schema.Ref{Namespace: dl.Namespace, Name: dl.Spec.ClientRef}
	}
	schemaName, data, err := schema.Encode(evt)
	if err != nil {
		log.Warn("download: could not encode the download event", "action", action, "error", err)
		return
	}
	env := &events.Envelope{
		ID:     string(dl.UID) + ":" + action,
		Type:   "download.DownloadEvent",
		Schema: schemaName,
		Source: "grabarr-controller@" + version.String(),
		Key:    dl.Namespace + "/" + dl.Name,
		Time:   evt.At,
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, events.DownloadEventSubject(action, string(dl.UID)), env); err != nil {
		log.Warn("download: could not publish the download event", "action", action, "error", err)
	}
}

// phaseActions maps a phase this controller moves a Download INTO onto the
// §5 action announcing it. Only the phases that are events in their own
// right appear; Queued/Downloading/Paused are progress, and Completed and
// Seeding are both announced once, as "completed", by the Downloaded edge.
//
// Failed is deliberately absent. failed is announced when a failure is
// first RECORDED, not when the phase reads Failed: a release fault goes
// straight to Blocklisted and never reads Failed at all, and a Download
// whose blocklist label an operator removes reads Failed again without
// having failed again. See advancePhase.
var phaseActions = map[downloadv1alpha1.DownloadPhase]string{
	downloadv1alpha1.DownloadPhaseImported:    events.ActionImported,
	downloadv1alpha1.DownloadPhaseBlocklisted: events.ActionBlocklisted,
}
