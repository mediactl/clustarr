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
	"slices"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	cataloghistory "github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// sources are the item path's sources beyond the kinds' own watches
// (A3.3 step 4): S24, the intent wakes at PriorityUser, S30, the delivery
// wakes, and each stage that is a Watcher.
func (ir *ItemReconciler) sources(mgr ctrl.Manager) ([]Source, error) {
	var out []Source
	if len(ir.Stages) == 0 {
		return out, nil
	}
	c := mgr.GetClient()
	out = append(out,
		Source{Name: "S24/Series", Object: &catalogv1alpha1.Series{}, Handler: containerWakes(c, KindSeries)},
		Source{Name: "S24/Comic", Object: &catalogv1alpha1.Comic{}, Handler: containerWakes(c, KindComic)},
	)
	for _, kind := range []KeyKind{KindMovie, KindSeries, KindAlbum, KindBook, KindAudiobook, KindComic} {
		obj, _ := newItemObject(kind)
		out = append(out, Source{Name: "intent/" + string(kind), Object: obj, Handler: intentWakes(kind)})
	}
	if ir.Inbox != nil {
		out = append(out, Source{Name: "S30", Raw: &chanSource[schema.ItemRef]{
			ch: ir.Inbox.Wakes(), toKey: itemRefKey, priority: PriorityUser,
		}})
	}
	if ir.DeliveryWakes != nil {
		out = append(out, Source{Name: "delivery", Raw: &chanSource[cataloghistory.Target]{
			ch: ir.DeliveryWakes, toKey: targetKey, priority: 0,
		}})
	}
	for _, s := range ir.Stages {
		w, ok := s.(Watcher)
		if !ok {
			continue
		}
		srcs, err := w.Sources(mgr)
		if err != nil {
			return nil, err
		}
		out = append(out, srcs...)
	}
	return out, nil
}

// itemRefKey is an ItemRef's item key, false for a kind the loop has no key
// for.
func itemRefKey(ref schema.ItemRef) (Key, bool) {
	kind := KeyKind(ref.Kind)
	if _, ok := newItemObject(kind); !ok || ref.Name == "" {
		return Key{}, false
	}
	return Key{Kind: kind, Namespace: ref.Namespace, Name: ref.Name}, true
}

// targetKey is a history target's item key, false for any other kind (a
// MediaFile's delivery wakes the file path through its records).
func targetKey(t cataloghistory.Target) (Key, bool) {
	kind := KeyKind(t.Kind)
	if _, ok := newItemObject(kind); !ok || t.Name == "" {
		return Key{}, false
	}
	return Key{Kind: kind, Namespace: t.Namespace, Name: t.Name}, true
}

// intentWakes enqueues an owner at PriorityUser when one of its download
// intent annotations changed (§6.10): a person is waiting on it.
func intentWakes(kind KeyKind) handler.TypedEventHandler[client.Object, Key] {
	return handler.TypedFuncs[client.Object, Key]{
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return
			}
			oa, na := e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations()
			for _, a := range catalogv1alpha1.DownloadIntentAnnotations() {
				ov, ook := oa[a]
				nv, nok := na[a]
				if ov != nv || ook != nok {
					addWithPriority(q, Key{Kind: kind, Namespace: e.ObjectNew.GetNamespace(), Name: e.ObjectNew.GetName()}, PriorityUser)
					return
				}
			}
		},
	}
}

// containerWakes is S24 (§6.11): a Series or Comic status change whose
// DownloadsSignature changed enqueues every covered Episode or Issue of the
// old and the new object, so each re-renders its view of the container's
// entries.
func containerWakes(c client.Reader, kind KeyKind) handler.TypedEventHandler[client.Object, Key] {
	return handler.TypedFuncs[client.Object, Key]{
		UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Key]) {
			switch kind {
			case KindSeries:
				o, ok1 := e.ObjectOld.(*catalogv1alpha1.Series)
				n, ok2 := e.ObjectNew.(*catalogv1alpha1.Series)
				if !ok1 || !ok2 {
					return
				}
				if rollup.DownloadsSignature(o.Status.Downloads, o.Status.PendingGrabs) == rollup.DownloadsSignature(n.Status.Downloads, n.Status.PendingGrabs) {
					return
				}
				covered := rollup.CoveredEpisodes(o.Status.Downloads, o.Status.PendingGrabs)
				for _, ep := range rollup.CoveredEpisodes(n.Status.Downloads, n.Status.PendingGrabs) {
					if !slices.Contains(covered, ep) {
						covered = append(covered, ep)
					}
				}
				for _, k := range coveredEpisodeKeys(ctx, c, n, covered) {
					q.Add(k)
				}
			case KindComic:
				o, ok1 := e.ObjectOld.(*catalogv1alpha1.Comic)
				n, ok2 := e.ObjectNew.(*catalogv1alpha1.Comic)
				if !ok1 || !ok2 {
					return
				}
				if rollup.DownloadsSignature(o.Status.Downloads, o.Status.PendingGrabs) == rollup.DownloadsSignature(n.Status.Downloads, n.Status.PendingGrabs) {
					return
				}
				covered := rollup.CoveredIssues(o.Status.Downloads, o.Status.PendingGrabs)
				for _, iss := range rollup.CoveredIssues(n.Status.Downloads, n.Status.PendingGrabs) {
					if !slices.Contains(covered, iss) {
						covered = append(covered, iss)
					}
				}
				for _, k := range coveredIssueKeys(ctx, c, n, covered) {
					q.Add(k)
				}
			}
		},
	}
}

// coveredEpisodeKeys lists s's Episodes through .spec.seriesRef and keeps
// those whose numbers are covered.
func coveredEpisodeKeys(ctx context.Context, c client.Reader, s *catalogv1alpha1.Series, covered []catalogv1alpha1.EpisodeNumber) []Key {
	if len(covered) == 0 {
		return nil
	}
	var list catalogv1alpha1.EpisodeList
	if err := c.List(ctx, &list, client.InNamespace(s.Namespace), client.MatchingFields{series.EpisodeBySeriesRefIndex: s.Name}); err != nil {
		logging.FromContext(ctx).Warn("remediation: S24 could not list a series' episodes", "series", s.Name, "error", err)
		return nil
	}
	var out []Key
	for i := range list.Items {
		ep := &list.Items[i]
		n := catalogv1alpha1.EpisodeNumber{Season: ep.Spec.SeasonNumber, Number: ep.Spec.EpisodeNumber}
		if slices.Contains(covered, n) {
			out = append(out, Key{Kind: KindEpisode, Namespace: ep.Namespace, Name: ep.Name})
		}
	}
	return out
}

// coveredIssueKeys lists c's Issues through .spec.comicRef and keeps those
// whose numbers are covered.
func coveredIssueKeys(ctx context.Context, c client.Reader, co *catalogv1alpha1.Comic, covered []string) []Key {
	if len(covered) == 0 {
		return nil
	}
	var list catalogv1alpha1.IssueList
	if err := c.List(ctx, &list, client.InNamespace(co.Namespace), client.MatchingFields{comic.IssueByComicRefIndex: co.Name}); err != nil {
		logging.FromContext(ctx).Warn("remediation: S24 could not list a comic's issues", "comic", co.Name, "error", err)
		return nil
	}
	var out []Key
	for i := range list.Items {
		iss := &list.Items[i]
		if slices.Contains(covered, iss.Spec.Number) {
			out = append(out, Key{Kind: KindIssue, Namespace: iss.Namespace, Name: iss.Name})
		}
	}
	return out
}

// chanSource adapts a leader-local wake channel to a loop source: each
// value maps to a key, added at priority.
type chanSource[T any] struct {
	ch       <-chan T
	toKey    func(T) (Key, bool)
	priority int
}

var _ source.TypedSource[Key] = (*chanSource[schema.ItemRef])(nil)

// Start implements source.TypedSource.
func (s *chanSource[T]) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[Key]) error {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-s.ch:
				if !ok {
					return
				}
				if k, ok := s.toKey(v); ok {
					addWithPriority(q, k, s.priority)
				}
			}
		}
	}()
	return nil
}

// String implements fmt.Stringer for the controller's logs.
func (s *chanSource[T]) String() string { return "remediation channel source" }
