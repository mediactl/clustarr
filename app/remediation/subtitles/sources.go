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

package subtitles

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
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/subtitleplan"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Sources: S13 (a SubtitleProfile's generation or Invalid changed: every
// movie and episode file, at low priority), S14 (an embedded
// SubtitleProvider's generation: the files with a subtitles block in its
// namespace) and S19 (a clustarr-subtitles answer), loop spec §3.3, §6.4.7.
// S2' and S4' are the naming planner's Movie and Series sources, whose
// predicates read status.metadata.originalLanguage too.
func (a *Adapter) Sources(mgr ctrl.Manager) ([]remediation.Source, error) {
	c := mgr.GetClient()
	low := func(ctx context.Context, q workqueue.TypedRateLimitingInterface[remediation.Key]) {
		videoFilesLow(ctx, c, q)
	}
	return []remediation.Source{
		{
			Name: "S13/SubtitleProfile", Object: &subtitlev1alpha1.SubtitleProfile{},
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
			Predicates: []predicate.Predicate{k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(subtitleProfileInvalid))},
		},
		{
			Name: "S14/SubtitleProvider", Object: &subtitlev1alpha1.SubtitleProvider{},
			Handler: handler.TypedEnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []remediation.Key {
				return filesWithSubtitles(ctx, c, o)
			}),
			Predicates: []predicate.Predicate{k8s.And(predicate.NewPredicateFuncs(isLocalProvider), k8s.GenerationChanged())},
		},
		{Name: "S19/clustarr-subtitles", Raw: recordsource.New(a.bus(), events.BucketSubtitles, remediation.FileKey)},
	}, nil
}

// videoFilesLow enqueues every movie and episode file at handler.LowPriority:
// a profile event enqueues the library, and only files whose block changes
// are written, through the status-write limiter (§6.4.7).
func videoFilesLow(ctx context.Context, c client.Reader, q workqueue.TypedRateLimitingInterface[remediation.Key]) {
	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Error("subtitles: list MediaFiles for a SubtitleProfile change", "err", err)
		return
	}
	keys := make([]remediation.Key, 0, len(files.Items))
	for i := range files.Items {
		mf := &files.Items[i]
		if subtitleplan.EligibleKind(mf.Spec.MediaRef.Kind) {
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

// filesWithSubtitles is S14's map: the files in o's namespace whose status
// carries a subtitles block, through remediation.mediafile.subtitles.
func filesWithSubtitles(ctx context.Context, c client.Reader, o client.Object) []remediation.Key {
	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.InNamespace(o.GetNamespace()), client.MatchingFields{mfindex.Subtitles: "true"},
		client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Error("subtitles: list MediaFiles for a local provider change", "err", err)
		return nil
	}
	out := make([]remediation.Key, 0, len(files.Items))
	for i := range files.Items {
		out = append(out, remediation.Key{Kind: remediation.KindMediaFile, Namespace: files.Items[i].Namespace, Name: files.Items[i].Name})
	}
	return out
}

// subtitleProfileInvalid is a profile's Invalid condition, for S13's
// predicate.
func subtitleProfileInvalid(o client.Object) bool {
	p, ok := o.(*subtitlev1alpha1.SubtitleProfile)
	return ok && subtitleplan.Invalid(p)
}

// isLocalProvider reports whether o is an embedded or an ocr
// SubtitleProvider: the types whose presence changes a plan
// (extractableStreams, imageWants). The type is immutable, so an object
// never moves in or out of this set.
func isLocalProvider(o client.Object) bool {
	sp, ok := o.(*subtitlev1alpha1.SubtitleProvider)
	return ok && (sp.Spec.Type == subtitlev1alpha1.SubtitleProviderEmbedded || sp.Spec.Type == subtitlev1alpha1.SubtitleProviderOCR)
}
