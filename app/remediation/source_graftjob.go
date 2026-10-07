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

package remediation

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
)

// GraftJobSource is S15 (loop spec §3.3): a graft Job's completion, failure
// or deletion enqueues the MediaFile that controls it. The manager caches
// Jobs through pool.PoolJobCache, which graft Jobs pass by their managed-by
// label. The graft planner's adapter declares it.
func GraftJobSource(c cache.Cache) source.TypedSource[Key] {
	return source.TypedKind[*batchv1.Job, Key](c, &batchv1.Job{},
		handler.TypedEnqueueRequestsFromMapFunc(GraftJobKeys), graftJobPredicate())
}

// GraftJobKeys maps a graft Job (labelled grafttask.LabelGraftOf) to its
// controlling MediaFile's file key: by owner reference, since a label value
// cannot hold every MediaFile name.
func GraftJobKeys(_ context.Context, j *batchv1.Job) []Key {
	if _, ok := j.Labels[grafttask.LabelGraftOf]; !ok {
		return nil
	}
	ref := metav1.GetControllerOf(j)
	if ref == nil || ref.Kind != "MediaFile" || ref.APIVersion != catalogv1alpha1.GroupVersion.String() {
		return nil
	}
	return []Key{{Kind: KindMediaFile, Namespace: j.Namespace, Name: ref.Name}}
}

// graftJobPredicate passes a graft Job finishing and a graft Job going;
// its creation is the loop's own act and its progress nothing the planner
// reads.
func graftJobPredicate() predicate.TypedPredicate[*batchv1.Job] {
	finished := func(j *batchv1.Job) bool { done, _, _ := pool.JobFinished(j); return done }
	return predicate.TypedFuncs[*batchv1.Job]{
		CreateFunc: func(event.TypedCreateEvent[*batchv1.Job]) bool { return false },
		UpdateFunc: func(e event.TypedUpdateEvent[*batchv1.Job]) bool {
			return finished(e.ObjectOld) != finished(e.ObjectNew)
		},
		DeleteFunc:  func(event.TypedDeleteEvent[*batchv1.Job]) bool { return true },
		GenericFunc: func(event.TypedGenericEvent[*batchv1.Job]) bool { return false },
	}
}
