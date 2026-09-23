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

package issue

import (
	"context"
	"time"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// publishItem publishes one catalog ItemEvent for iss -- spec §5's
// clustarr.evt.catalog.<kind>.<added|updated|deleted>.<uid>, which the
// history sink (catalogarr/history) turns into an Event on the Issue. It is
// built by rollup.ItemEvent, so its envelope id is the same function of the
// edge for every kind, and it is best effort, as the Movie reconciler's is:
// the event is history and the status is the record, so a failed publish is
// logged and the reconcile carries on. A nil Bus publishes nothing.
func (r *Reconciler) publishItem(ctx context.Context, iss *catalogv1alpha1.Issue, action string, now time.Time) {
	if r.Bus == nil {
		return
	}
	// The Issue's own provider id (status.sourceID) is keyed by its Comic's
	// source, which this object does not carry, so no IDs are claimed.
	f := rollup.ItemFields{
		Monitored: ptr.Deref(iss.Spec.Monitored, true),
		Title:     iss.Status.Title,
	}
	if iss.Status.Date != nil {
		f.Year = int32(iss.Status.Date.UTC().Year())
	}
	log := logging.FromContext(ctx)
	subject, env, err := rollup.ItemEvent(iss, commonv1.MediaKindIssue, action, f, now)
	if err != nil {
		log.Warn("issue: could not encode a catalog item event", "action", action, "error", err)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("issue: could not publish a catalog item event", "action", action, "subject", subject, "error", err)
	}
}
