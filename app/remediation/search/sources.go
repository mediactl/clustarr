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

package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Sources implements remediation.Watcher: S29, clustarr-searches (A4.2
// step 3). A record whose item is a searchable kind wakes it -- the search
// stage incorporates the answer -- and its container too (an Episode's
// Series, an Issue's Comic), whose grab stage reads it (R7); a season
// record's item is its Series (Wave S), whose search stage incorporates it
// and whose grab stage reads it. A record of a Search (an interactive
// search) wakes no item: the Search controller has its own source.
// S31: a planned Search wakes the Series it names. And S32: a searchable
// item's own status write that turns it wanted, or not, wakes it.
func (s *Stage) Sources(ctrl.Manager) ([]remediation.Source, error) {
	src := recordsource.NewItems(s.o.Bus, events.BucketSearches, func(item schema.ItemRef, value []byte) []remediation.Key {
		var keys []remediation.Key
		if k, ok := itemKey(item); ok {
			keys = append(keys, k)
		}
		var rec schema.SearchRecord
		if err := json.Unmarshal(value, &rec); err == nil && rec.Container != nil {
			if k, ok := itemKey(*rec.Container); ok {
				keys = append(keys, k)
			}
		}
		return keys
	})
	out := []remediation.Source{
		{Name: "S29/" + events.BucketSearches, Raw: src},
		// S31 (Wave S): a planned Search -- a series or season search with
		// grabBest -- created, changed (its phase settling is the
		// handshake) or deleted wakes the Series it names, at once.
		{Name: "S31/Search", Object: &catalogv1alpha1.Search{}, Handler: plannedSearchWakes()},
	}
	// S32: a searchable item's own status write that moves what this stage
	// reads of it wakes it (wantedWakes).
	for _, w := range []struct {
		kind remediation.KeyKind
		obj  client.Object
	}{
		{remediation.KindMovie, &catalogv1alpha1.Movie{}},
		{remediation.KindEpisode, &catalogv1alpha1.Episode{}},
		{remediation.KindAlbum, &catalogv1alpha1.Album{}},
		{remediation.KindBook, &catalogv1alpha1.Book{}},
		{remediation.KindAudiobook, &catalogv1alpha1.Audiobook{}},
		{remediation.KindIssue, &catalogv1alpha1.Issue{}},
	} {
		out = append(out, remediation.Source{Name: "S32/" + string(w.kind), Object: w.obj, Handler: wantedWakes(w.kind, s.o.Clock)})
	}
	return out, nil
}

// wantedWakes is S32's handler (smoke finding O1): an item whose own status
// write moved what the search stage reads of it -- its sweep reason
// (missing, cutoff unmet or neither) or whether an audio donor is wanted,
// both wantedcron.CandidateOf's -- is woken, so the stage plans over what
// the kind's pass just wrote. The stage runs before the kind's own pass in
// one item pass (remediation.StageOrder), so the pass that turns an item Wanted -- its
// metadata arriving, its availability or air date passing, its file going --
// searched it from the phase before; and the kinds' own watches never wake
// an item for its own phase (moviePredicate's self-loop rule), so a new
// Movie waited for the twelve-hourly sweep. The next pass reads the same
// phase, so the wake settles after one pass.
//
// Updates only: a create moves nothing, and the cache's initial list wakes
// every item through the kinds' own watches already. At low priority, the
// wanted sweep's: a cold start or an import list that turns many items
// Wanted at once queues behind a person's work, and each item's first,
// live, search still reserves the namespace's pace slot before it
// dispatches (searchplan.Decide, ADR-0019 §5.4) -- an item searched before
// goes IndexOnly -- so the wakes release no indexer burst.
func wantedWakes(kind remediation.KeyKind, clock func() time.Time) handler.TypedEventHandler[client.Object, remediation.Key] {
	if clock == nil {
		clock = time.Now
	}
	return handler.TypedFuncs[client.Object, remediation.Key]{
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return
			}
			now := clock()
			if searchReading(e.ObjectOld, now) == searchReading(e.ObjectNew, now) {
				return
			}
			k := remediation.Key{Kind: kind, Namespace: e.ObjectNew.GetNamespace(), Name: e.ObjectNew.GetName()}
			if pq, ok := q.(priorityqueue.PriorityQueue[remediation.Key]); ok {
				pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(handler.LowPriority)}, k)
				return
			}
			q.Add(k)
		},
	}
}

// reading is what the search stage reads of an item's own status.
type reading struct {
	reason schema.SearchReason
	donor  bool
}

// searchReading is o's reading at now.
func searchReading(o client.Object, now time.Time) reading {
	c, _ := wantedcron.CandidateOf(o, now)
	return reading{reason: c.Reason, donor: c.Donor}
}

// plannedSearchWakes is S31's handler: the Series a planned Search names,
// at PriorityUser, on any create, update or delete (an update's old or new
// spec may be the planned one).
func plannedSearchWakes() handler.TypedEventHandler[client.Object, remediation.Key] {
	add := func(q workqueue.TypedRateLimitingInterface[remediation.Key], objs ...client.Object) {
		for _, o := range objs {
			srch, ok := o.(*catalogv1alpha1.Search)
			if !ok || !srch.Spec.PlannedBySeries() || srch.Spec.MediaRef.Name == "" {
				continue
			}
			k := remediation.Key{Kind: remediation.KindSeries, Namespace: srch.Namespace, Name: srch.Spec.MediaRef.Name}
			if pq, ok := q.(priorityqueue.PriorityQueue[remediation.Key]); ok {
				pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(remediation.PriorityUser)}, k)
				continue
			}
			q.Add(k)
		}
	}
	return handler.TypedFuncs[client.Object, remediation.Key]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
			add(q, e.Object)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
			add(q, e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[remediation.Key]) {
			add(q, e.Object)
		},
	}
}

// itemKey is a record item's loop key: a searchable kind, or a Series or
// Comic container.
func itemKey(ref schema.ItemRef) (remediation.Key, bool) {
	kind := ref.Kind
	if kind == "" || ref.Name == "" {
		return remediation.Key{}, false
	}
	kind = strings.ToUpper(kind[:1]) + kind[1:]
	if !itemread.Searchable(kind) && kind != "Series" && kind != "Comic" {
		return remediation.Key{}, false
	}
	return remediation.Key{Kind: remediation.KeyKind(kind), Namespace: ref.Namespace, Name: ref.Name}, true
}

// LedgerSource rebuilds catalog-search-normal's outstanding dispatches
// from every searchable item's searchDispatch (seq > answeredSeq) in the
// cache, after a leader change (A4.2 step 4).
type LedgerSource struct{}

var _ dispatch.Source = LedgerSource{}

// Durable implements dispatch.Source.
func (LedgerSource) Durable() string { return searchplan.Durable }

// Rebuild implements dispatch.Source.
func (LedgerSource) Rebuild(ctx context.Context, r client.Reader) ([]dispatch.Outstanding, error) {
	var out []dispatch.Outstanding
	add := func(o client.Object) {
		if d := itemread.SearchDispatch(o); d.InFlight() {
			out = append(out, dispatch.Outstanding{
				Key: dispatch.Key{Kind: itemread.Kind(o), Namespace: o.GetNamespace(), Name: o.GetName(), Sub: history.DispatchSubSearch},
				Seq: d.Seq,
			})
		}
	}
	lists := []client.ObjectList{
		&catalogv1alpha1.MovieList{}, &catalogv1alpha1.EpisodeList{}, &catalogv1alpha1.AlbumList{},
		&catalogv1alpha1.BookList{}, &catalogv1alpha1.AudiobookList{}, &catalogv1alpha1.IssueList{},
	}
	for _, l := range lists {
		if err := r.List(ctx, l); err != nil {
			return nil, fmt.Errorf("search: rebuild the ledger: %w", err)
		}
		switch t := l.(type) {
		case *catalogv1alpha1.MovieList:
			for i := range t.Items {
				add(&t.Items[i])
			}
		case *catalogv1alpha1.EpisodeList:
			for i := range t.Items {
				add(&t.Items[i])
			}
		case *catalogv1alpha1.AlbumList:
			for i := range t.Items {
				add(&t.Items[i])
			}
		case *catalogv1alpha1.BookList:
			for i := range t.Items {
				add(&t.Items[i])
			}
		case *catalogv1alpha1.AudiobookList:
			for i := range t.Items {
				add(&t.Items[i])
			}
		case *catalogv1alpha1.IssueList:
			for i := range t.Items {
				add(&t.Items[i])
			}
		}
	}
	return out, nil
}
