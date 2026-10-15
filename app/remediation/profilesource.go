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
	"cmp"
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// QualityProfileSource is S10's arm for the file planners that read an
// item's profile: a QualityProfile whose audio policy or language changed
// wakes the files of every item ranked against it -- its Movies, and its
// Series' Episodes -- through the .spec.qualityProfileRef indexes and the
// files-by-item index. The graft planner plans a file's graft block from
// the policy (GatherItem), and the naming planner names a file's audio
// languages against the item's configured language, which both fields set
// (catalogctx.ConfiguredLanguage). S10 itself wakes only the items, whose
// status changes never wake a file (§3.3's S1'), so without this a profile
// that stopped grafting left every graft block it had planned standing (T2
// finding). Both planners return this one source, deduplicated by Name.
// Files are enqueued at handler.LowPriority, as the other profile fan-outs
// are (§3.11).
func QualityProfileSource(c client.Reader) Source {
	enqueue := func(ctx context.Context, o client.Object, q workqueue.TypedRateLimitingInterface[Key]) {
		keys := filesOfProfile(ctx, c, o.GetName())
		if len(keys) == 0 {
			return
		}
		if pq, ok := q.(priorityqueue.PriorityQueue[Key]); ok {
			pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(handler.LowPriority)}, keys...)
			return
		}
		for _, k := range keys {
			q.Add(k)
		}
	}
	return Source{
		Name: "S10'/QualityProfile", Object: &catalogv1alpha1.QualityProfile{},
		Handler: handler.TypedFuncs[client.Object, Key]{
			CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
				enqueue(ctx, e.Object, q)
			},
			UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
				enqueue(ctx, e.ObjectNew, q)
			},
			DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
				enqueue(ctx, e.Object, q)
			},
		},
		Predicates: []predicate.Predicate{languagePolicyChanged()},
	}
}

// languagePolicyChanged passes a QualityProfile whose spec.audio -- whether
// it grafts, its default language, the languages it wants -- or
// spec.language changed, one deleted, and one created after the cache's
// initial list (the files' initial list already wakes every file at start).
func languagePolicyChanged() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return !e.IsInInitialList },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return languageOf(e.ObjectOld) != languageOf(e.ObjectNew) ||
				!equality.Semantic.DeepEqual(audioPolicyOf(e.ObjectOld), audioPolicyOf(e.ObjectNew))
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// audioPolicyOf is o's spec.audio, nil for none or another type.
func audioPolicyOf(o client.Object) *catalogv1alpha1.AudioPolicy {
	if qp, ok := o.(*catalogv1alpha1.QualityProfile); ok {
		return qp.Spec.Audio
	}
	return nil
}

// languageOf is o's spec.language, "" for another type.
func languageOf(o client.Object) string {
	if qp, ok := o.(*catalogv1alpha1.QualityProfile); ok {
		return qp.Spec.Language
	}
	return ""
}

// filesOfProfile is the file keys of every Movie and every Series' Episode
// ranked against QualityProfile profile, through the cache's indexes (never
// a namespace List, CLAUDE.md). A QualityProfile is cluster-scoped, so the
// item Lists carry no namespace. A failed List is logged and skipped: a
// dropped wake costs latency, and the file's next pass reads the profile.
func filesOfProfile(ctx context.Context, c client.Reader, profile string) []Key {
	set := map[types.NamespacedName]bool{}
	var movies catalogv1alpha1.MovieList
	if err := c.List(ctx, &movies, client.MatchingFields{movie.QualityProfileIndex: profile}, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("remediation: list a QualityProfile's movies to wake their files", "qualityProfile", profile, "error", err)
	}
	for i := range movies.Items {
		for _, nn := range mediafile.FilesForMovie(ctx, c, &movies.Items[i]) {
			set[nn] = true
		}
	}
	var series catalogv1alpha1.SeriesList
	if err := c.List(ctx, &series, client.MatchingFields{episode.SeriesByQualityProfileIndex: profile}, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("remediation: list a QualityProfile's series to wake their files", "qualityProfile", profile, "error", err)
	}
	for i := range series.Items {
		for _, nn := range mediafile.FilesForSeries(ctx, c, &series.Items[i]) {
			set[nn] = true
		}
	}
	keys := make([]Key, 0, len(set))
	for nn := range set {
		keys = append(keys, Key{Kind: KindMediaFile, Namespace: nn.Namespace, Name: nn.Name})
	}
	slices.SortFunc(keys, func(a, b Key) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return keys
}
