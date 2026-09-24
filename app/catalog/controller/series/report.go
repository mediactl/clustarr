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

package series

import (
	"context"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// publishItem publishes one ItemEvent for s. Best effort, like the movie
// package's: the event is history, the status is the record.
func (r *Reconciler) publishItem(ctx context.Context, s *catalogv1alpha1.Series, action string, now time.Time) {
	if r.Bus == nil {
		return
	}
	log := logging.FromContext(ctx)
	f := rollup.ItemFields{
		Monitored: ptr.Deref(s.Spec.Monitored, true),
		AddSource: s.Spec.Source,
		IDs:       map[string]string{metadata.KeyTVDB: strconv.FormatInt(s.Spec.TvdbID, 10)},
	}
	if md := s.Status.Metadata; md != nil {
		f.Title, f.Year = md.Title, md.Year
	}
	subject, env, err := rollup.ItemEvent(s, commonv1.MediaKindSeries, action, f, now)
	if err != nil {
		log.Warn("series: could not encode a catalog event", "action", action, "error", err)
		return
	}
	tracing.Inject(ctx, env)
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		log.Warn("series: could not publish a catalog event", "action", action, "subject", subject, "error", err)
	}
}

// normal and warn emit a Kubernetes Event on s, on an edge only; see
// rollup.Transitioned.
func (r *Reconciler) normal(s *catalogv1alpha1.Series, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(s, nil, corev1.EventTypeNormal, reason, "Reconcile", format, args...)
	}
}

func (r *Reconciler) warn(s *catalogv1alpha1.Series, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(s, nil, corev1.EventTypeWarning, reason, "Reconcile", format, args...)
	}
}
