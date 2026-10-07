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

package transcode

import (
	"context"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Sources: S12 (a TranscodeProfile's create, delete or generation: every
// movie and episode file, at low priority, loop spec §3.3, §3.11; only files
// whose block changes are written, through the status-write limiter) and
// S22 (admission's grants, directives and changed wait reasons, at
// admission.GrantPriority).
func (a *Adapter) Sources(mgr ctrl.Manager) ([]remediation.Source, error) {
	c := mgr.GetClient()
	low := func(ctx context.Context, q workqueue.TypedRateLimitingInterface[remediation.Key]) {
		videoFilesLow(ctx, c, q)
	}
	var srcs []remediation.Source
	if a.o.LoopSource != nil {
		srcs = append(srcs, remediation.Source{Name: "S22/admission", Raw: a.o.LoopSource})
	}
	return append(srcs, []remediation.Source{
		{
			Name: "S12/TranscodeProfile", Object: &transcodev1alpha1.TranscodeProfile{},
			Handler: handler.TypedFuncs[client.Object, remediation.Key]{
				CreateFunc: func(ctx context.Context, _ event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
					low(ctx, q)
				},
				UpdateFunc: func(ctx context.Context, _ event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
					low(ctx, q)
				},
				DeleteFunc: func(ctx context.Context, _ event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
					low(ctx, q)
				},
			},
			Predicates: []predicate.Predicate{k8s.GenerationChanged()},
		},
	}...), nil
}

// videoFilesLow enqueues every movie and episode file at handler.LowPriority.
func videoFilesLow(ctx context.Context, c client.Reader, q workqueue.TypedRateLimitingInterface[remediation.Key]) {
	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Error("transcode: list MediaFiles for a TranscodeProfile change", "err", err)
		return
	}
	keys := make([]remediation.Key, 0, len(files.Items))
	for i := range files.Items {
		mf := &files.Items[i]
		if transcodeplan.EligibleKind(mf.Spec.MediaRef.Kind) {
			keys = append(keys, remediation.Key{Kind: remediation.KindMediaFile, Namespace: mf.Namespace, Name: mf.Name})
		}
	}
	if pq, ok := q.(priorityqueue.PriorityQueue[remediation.Key]); ok {
		pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(handler.LowPriority)}, keys...)
		return
	}
	for _, k := range keys {
		q.Add(k)
	}
}
