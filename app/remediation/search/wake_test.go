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

package search_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

// wakeQueue records what a handler enqueues and at which priority.
type wakeQueue struct {
	priorityqueue.PriorityQueue[remediation.Key]
	added map[remediation.Key]int
}

func (q *wakeQueue) Add(k remediation.Key) { q.added[k] = 0 }

func (q *wakeQueue) AddWithOpts(o priorityqueue.AddOpts, ks ...remediation.Key) {
	for _, k := range ks {
		p := 0
		if o.Priority != nil {
			p = *o.Priority
		}
		q.added[k] = p
	}
}

// wakes feeds one update of old to cur through every source the stage
// declares for objects of cur's type, and returns what they enqueued.
func wakes(t *testing.T, srcs []remediation.Source, old, cur client.Object) map[remediation.Key]int {
	t.Helper()
	o, n := old.DeepCopyObject().(client.Object), cur.DeepCopyObject().(client.Object)
	o.SetResourceVersion("1")
	n.SetResourceVersion("2")
	q := &wakeQueue{added: map[remediation.Key]int{}}
	for _, s := range srcs {
		if s.Object == nil || s.Handler == nil {
			continue
		}
		if typeName(s.Object) != typeName(cur) {
			continue
		}
		s.Handler.Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: o, ObjectNew: n}, q)
	}
	return q.added
}

func typeName(o client.Object) string {
	switch o.(type) {
	case *catalogv1alpha1.Movie:
		return "Movie"
	case *catalogv1alpha1.Episode:
		return "Episode"
	case *catalogv1alpha1.Album:
		return "Album"
	case *catalogv1alpha1.Book:
		return "Book"
	case *catalogv1alpha1.Audiobook:
		return "Audiobook"
	case *catalogv1alpha1.Issue:
		return "Issue"
	case *catalogv1alpha1.Search:
		return "Search"
	}
	return "?"
}

// TestANewlyWantedItemWakesItsOwnSearch is smoke finding O1: the search
// stage runs before the kind's own pass, so the pass that turns an item
// Wanted (its metadata arriving, its availability date passing) searched
// it from the phase before; and the kinds' own watches never wake an item
// for its own phase, so a new Movie waited for the twelve-hourly sweep.
// Every searchable kind's own status write that moves what the stage reads
// -- wanted, cutoff unmet, or a donor wanted -- wakes it, at low priority
// (the sweep's), and one that moves nothing the stage reads wakes nothing.
func TestANewlyWantedItemWakesItsOwnSearch(t *testing.T) {
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	srcs, err := search.New(search.Options{Bus: bus}).Sources(nil)
	require.NoError(t, err)

	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "media", Name: name} }
	cutoffFalse := []metav1.Condition{{Type: catalogv1alpha1.IssueConditionCutoffMet, Status: metav1.ConditionFalse}}
	past := metav1.NewTime(time.Now().Add(-48 * time.Hour))
	cases := []struct {
		kind               remediation.KeyKind
		before, wanted, ok client.Object
	}{{
		kind:   remediation.KindMovie,
		before: &catalogv1alpha1.Movie{ObjectMeta: meta("x"), Status: catalogv1alpha1.MovieStatus{Phase: catalogv1alpha1.MoviePhasePending}},
		wanted: &catalogv1alpha1.Movie{ObjectMeta: meta("x"), Status: catalogv1alpha1.MovieStatus{Phase: catalogv1alpha1.MoviePhaseWanted}},
		ok:     &catalogv1alpha1.Movie{ObjectMeta: meta("x"), Status: catalogv1alpha1.MovieStatus{Phase: catalogv1alpha1.MoviePhasePending, ObservedGeneration: 2}},
	}, {
		kind:   remediation.KindEpisode,
		before: &catalogv1alpha1.Episode{ObjectMeta: meta("x"), Status: catalogv1alpha1.EpisodeStatus{Phase: catalogv1alpha1.EpisodePhaseUnaired}},
		wanted: &catalogv1alpha1.Episode{ObjectMeta: meta("x"), Status: catalogv1alpha1.EpisodeStatus{Phase: catalogv1alpha1.EpisodePhaseWanted}},
		ok:     &catalogv1alpha1.Episode{ObjectMeta: meta("x"), Status: catalogv1alpha1.EpisodeStatus{Phase: catalogv1alpha1.EpisodePhaseUnaired, ObservedGeneration: 2}},
	}, {
		kind:   remediation.KindAlbum,
		before: &catalogv1alpha1.Album{ObjectMeta: meta("x"), Status: catalogv1alpha1.AlbumStatus{}},
		wanted: &catalogv1alpha1.Album{ObjectMeta: meta("x"), Status: catalogv1alpha1.AlbumStatus{Phase: catalogv1alpha1.AlbumPhaseWanted}},
		ok:     &catalogv1alpha1.Album{ObjectMeta: meta("x"), Status: catalogv1alpha1.AlbumStatus{ObservedGeneration: 2}},
	}, {
		kind:   remediation.KindBook,
		before: &catalogv1alpha1.Book{ObjectMeta: meta("x"), Status: catalogv1alpha1.BookStatus{}},
		wanted: &catalogv1alpha1.Book{ObjectMeta: meta("x"), Status: catalogv1alpha1.BookStatus{Phase: catalogv1alpha1.BookPhaseWanted}},
		ok:     &catalogv1alpha1.Book{ObjectMeta: meta("x"), Status: catalogv1alpha1.BookStatus{ObservedGeneration: 2}},
	}, {
		kind:   remediation.KindAudiobook,
		before: &catalogv1alpha1.Audiobook{ObjectMeta: meta("x"), Status: catalogv1alpha1.AudiobookStatus{}},
		wanted: &catalogv1alpha1.Audiobook{ObjectMeta: meta("x"), Status: catalogv1alpha1.AudiobookStatus{Phase: catalogv1alpha1.AudiobookPhaseCutoffUnmet}},
		ok:     &catalogv1alpha1.Audiobook{ObjectMeta: meta("x"), Status: catalogv1alpha1.AudiobookStatus{ObservedGeneration: 2}},
	}, {
		kind: remediation.KindIssue,
		before: &catalogv1alpha1.Issue{ObjectMeta: meta("x"), Spec: catalogv1alpha1.IssueSpec{Monitored: ptr.To(true)},
			Status: catalogv1alpha1.IssueStatus{State: catalogv1alpha1.IssueStateDownloaded}},
		wanted: &catalogv1alpha1.Issue{ObjectMeta: meta("x"), Spec: catalogv1alpha1.IssueSpec{Monitored: ptr.To(true)},
			Status: catalogv1alpha1.IssueStatus{State: catalogv1alpha1.IssueStateDownloaded, Conditions: cutoffFalse}},
		ok: &catalogv1alpha1.Issue{ObjectMeta: meta("x"), Spec: catalogv1alpha1.IssueSpec{Monitored: ptr.To(true)},
			Status: catalogv1alpha1.IssueStatus{State: catalogv1alpha1.IssueStateDownloaded, Date: &past}},
	}}
	for _, tc := range cases {
		key := remediation.Key{Kind: tc.kind, Namespace: "media", Name: "x"}
		got := wakes(t, srcs, tc.before, tc.wanted)
		p, woke := got[key]
		require.True(t, woke, "%s: its own status turning it wanted woke no search pass", tc.kind)
		assert.Equal(t, handler.LowPriority, p, "%s: a wanted wake is the sweep's priority, never ahead of a person's work", tc.kind)
		assert.Len(t, got, 1, "%s: only the item itself", tc.kind)

		assert.Empty(t, wakes(t, srcs, tc.before, tc.ok), "%s: a status write the search reads nothing of wakes nothing", tc.kind)
	}
}
