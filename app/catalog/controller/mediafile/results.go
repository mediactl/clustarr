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

package mediafile

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	clustarrevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// probeRecordsSource wakes the reconciler for every answered probe: S16 of
// the remediation loop to come (loop spec §3.18, §4.9), a records source over
// clustarr-probes that enqueues the MediaFile a probed or failed record names
// and skips requests, deletes and undecodable values. It writes nothing:
// Reconcile reads the record itself. Controller sources start only on the
// leader; a probe answered while no leader ran is read by the pass the
// informer's initial list enqueues.
func (r *Reconciler) probeRecordsSource() source.Source {
	return recordsource.New(r.Probes.Bus(), clustarrevents.BucketProbes, mediaFileRequest,
		recordsource.OnRecreated[reconcile.Request](r.wakePendingProbes))
}

func mediaFileRequest(ref schema.Ref) (reconcile.Request, bool) {
	if ref.Name == "" {
		return reconcile.Request{}, false
	}
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}}, true
}

// wakePendingProbes enqueues every MediaFile whose status says a probe is
// pending (Probed False, ProbePending): its request went with a recreated
// bucket (loop spec §4.9, §5.15 "records bucket lost").
func (r *Reconciler) wakePendingProbes(ctx context.Context, enqueue func(reconcile.Request)) {
	var list catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Warn("mediafile: could not list MediaFiles to wake their pending probes", "error", err)
		return
	}
	for i := range list.Items {
		mf := &list.Items[i]
		c := k8s.FindCondition(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionProbed)
		if c != nil && c.Status == metav1.ConditionFalse && c.Reason == catalogv1alpha1.MediaFileReasonProbePending {
			enqueue(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Name}})
		}
	}
}
