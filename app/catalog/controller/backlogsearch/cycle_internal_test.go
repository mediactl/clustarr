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

package backlogsearch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/backlog"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

const ns = "media"

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *metav1.Time { t := metav1.NewTime(now.Add(-d)); return &t }

func searched(d time.Duration) commonv1.Attempts {
	return commonv1.Attempts{Count: 2, Initial: at(60 * 24 * time.Hour), Latest: at(d)}
}

func movie(name string, phase catalogv1alpha1.MoviePhase, live *metav1.Time, attempts commonv1.Attempts) *catalogv1alpha1.Movie {
	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	m.Status.Phase, m.Status.LastLiveSearchAt, m.Status.SearchAttempts = phase, live, attempts
	return m
}

func episode(name, series string, season int32, phase catalogv1alpha1.EpisodePhase, live *metav1.Time) *catalogv1alpha1.Episode {
	e := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	e.Spec.SeriesRef, e.Spec.SeasonNumber = series, season
	e.Status.Phase, e.Status.LastLiveSearchAt, e.Status.SearchAttempts = phase, live, searched(24*time.Hour)
	return e
}

func series(name string, monitored bool) *catalogv1alpha1.Series {
	s := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	s.Spec.Monitored = ptr.To(monitored)
	return s
}

func newReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...)
	for _, o := range []client.Object{
		&catalogv1alpha1.Movie{}, &catalogv1alpha1.Episode{}, &catalogv1alpha1.Album{},
		&catalogv1alpha1.Book{}, &catalogv1alpha1.Audiobook{}, &catalogv1alpha1.Issue{},
	} {
		b = b.WithIndex(o, itemread.BacklogIndex, itemread.BacklogValues)
	}
	return &Reconciler{Client: b.Build(), Book: backlog.NewBook(), Clock: func() time.Time { return now }}
}

func drain(b *backlog.Book) []string {
	var out []string
	for {
		select {
		case ref := <-b.Wakes():
			out = append(out, ref.Kind+"/"+ref.Name)
		default:
			return out
		}
	}
}

// TestACycleGrantsTheOldestDueAndWakesThem: a cycle lists the backlog
// through the index, leaves out what is not a candidate (never searched,
// unmonitored series, cooling down, not wanted), takes the oldest due ones
// up to perCycle, grants them and wakes their keys, a season's Series first.
func TestACycleGrantsTheOldestDueAndWakesThem(t *testing.T) {
	r := newReconciler(t,
		movie("never-live", catalogv1alpha1.MoviePhaseWanted, nil, searched(time.Hour)),
		movie("old-live", catalogv1alpha1.MoviePhaseWanted, at(30*24*time.Hour), searched(time.Hour)),
		movie("cooling", catalogv1alpha1.MoviePhaseWanted, at(24*time.Hour), searched(time.Hour)),
		movie("first-search-owed", catalogv1alpha1.MoviePhaseWanted, nil, commonv1.Attempts{}),
		movie("has-it", catalogv1alpha1.MoviePhaseImported, nil, searched(time.Hour)),
		movie("upgrade", catalogv1alpha1.MoviePhaseCutoffUnmet, at(30*24*time.Hour), searched(time.Hour)),
		series("show", true), series("off", false),
		episode("show-s01e01", "show", 1, catalogv1alpha1.EpisodePhaseWanted, nil),
		episode("show-s01e02", "show", 1, catalogv1alpha1.EpisodePhaseWanted, nil),
		episode("off-s01e01", "off", 1, catalogv1alpha1.EpisodePhaseWanted, nil),
	)
	bs := &catalogv1alpha1.BacklogSearch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default", Generation: 1}}
	bs.Spec.Missing.PerCycle = ptr.To[int32](2)
	st := &catalogv1alpha1.BacklogSearchStatus{}

	units, err := r.cycle(context.Background(), bs, st, now)
	require.NoError(t, err)
	require.Len(t, units, 3)

	require.Equal(t, []string{"Series/show", "Episode/show-s01e01", "Episode/show-s01e02", "Movie/never-live", "Movie/upgrade"}, drain(r.Book),
		"no live search recorded, the older lastSearchedAt first: the season as one unit, then the movie; then the cutoff queue")
	for _, uid := range []string{"uid-never-live", "uid-show-s01e01", "uid-show-s01e02", "uid-upgrade"} {
		_, ok := r.Book.Granted(uid, now)
		assert.True(t, ok, uid)
	}
	for _, uid := range []string{"uid-old-live", "uid-cooling", "uid-first-search-owed", "uid-has-it", "uid-off-s01e01"} {
		_, ok := r.Book.Granted(uid, now)
		assert.False(t, ok, uid)
	}
	g, _ := r.Book.Granted("uid-never-live", now)
	assert.Equal(t, now.Add(GrantLapse), g.Until, "past the automatic season search's cooldown")

	require.NotNil(t, st.Missing)
	assert.EqualValues(t, 5, st.Missing.Candidates, "never-live, old-live, cooling and the two monitored episodes")
	assert.EqualValues(t, 4, st.Missing.Due)
	assert.EqualValues(t, 1, st.CutoffUnmet.Due)
	assert.EqualValues(t, 3, st.SearchesLastHour)
	require.Len(t, st.Recent, 3)
	assert.Equal(t, commonv1.MediaKindSeries, st.Recent[0].Kind)
	assert.EqualValues(t, 1, *st.Recent[0].Season)
	assert.EqualValues(t, 2, st.Recent[0].Items)
	assert.Equal(t, commonv1.MediaKindMovie, st.Recent[1].Kind)
	assert.Equal(t, "never-live", st.Recent[1].Name)
	assert.Equal(t, "cutoffUnmet", st.Recent[2].Reason)
	assert.Equal(t, now.Add(15*time.Minute), st.NextCycleAt.Time)
}

// TestTheHourlyLimitCountsTheRecentSearches: after a leader change the
// status' recent searches are the hour's count.
func TestTheHourlyLimitCountsTheRecentSearches(t *testing.T) {
	r := newReconciler(t,
		movie("a", catalogv1alpha1.MoviePhaseWanted, nil, searched(time.Hour)),
		movie("b", catalogv1alpha1.MoviePhaseWanted, nil, searched(time.Hour)),
	)
	bs := &catalogv1alpha1.BacklogSearch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default"}}
	bs.Spec.HourlyLimit = ptr.To[int32](3)
	st := &catalogv1alpha1.BacklogSearchStatus{Recent: []catalogv1alpha1.BacklogSearchUnit{
		{Kind: commonv1.MediaKindMovie, Name: "x", Reason: "missing", At: *at(10 * time.Minute)},
		{Kind: commonv1.MediaKindMovie, Name: "y", Reason: "missing", At: *at(50 * time.Minute)},
		{Kind: commonv1.MediaKindMovie, Name: "z", Reason: "missing", At: *at(2 * time.Hour)},
	}}
	units, err := r.cycle(context.Background(), bs, st, now)
	require.NoError(t, err)
	require.Len(t, units, 1, "two of the hour's three were spent")
	assert.EqualValues(t, 3, st.SearchesLastHour)
	assert.Len(t, st.Recent, 4)
}

// TestTheOldestBacklogSearchWins: one BacklogSearch per namespace runs.
func TestTheOldestBacklogSearchWins(t *testing.T) {
	older := &catalogv1alpha1.BacklogSearch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "z-first", CreationTimestamp: *at(time.Hour)}}
	newer := &catalogv1alpha1.BacklogSearch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "a-second", CreationTimestamp: *at(time.Minute)}}
	r := newReconciler(t, older, newer)
	w, err := r.winner(context.Background(), ns)
	require.NoError(t, err)
	assert.Equal(t, "z-first", w)
}

// TestAKindsListLimitsTheBacklog: spec.kinds leaves the other kinds out.
func TestAKindsListLimitsTheBacklog(t *testing.T) {
	r := newReconciler(t,
		movie("m", catalogv1alpha1.MoviePhaseWanted, nil, searched(time.Hour)),
		series("show", true),
		episode("show-s01e01", "show", 1, catalogv1alpha1.EpisodePhaseWanted, nil),
	)
	bs := &catalogv1alpha1.BacklogSearch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default"}}
	bs.Spec.Kinds = []commonv1.MediaKind{commonv1.MediaKindEpisode}
	cands, err := r.candidates(context.Background(), bs, now)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: "show-s01e01", UID: "uid-show-s01e01"}}, cands[0].Item)
}
