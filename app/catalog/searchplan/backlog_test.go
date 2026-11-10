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

package searchplan_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// searchedView is an Episode searched before, an hour ago: its next search
// is IndexOnly, and its backoff has not elapsed.
func searchedView(name string) searchplan.View {
	v := itemView(name, t0)
	last := metav1.NewTime(t0.Add(-time.Hour))
	v.LastSearched = &last
	v.Attempts = commonv1.Attempts{Count: 3, Initial: ago(30 * 24 * time.Hour), Latest: &last}
	v.Dispatch = &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 7, DispatchedAt: last}
	return v
}

// TestABacklogGrantIsOneLiveSearchNow: a granted, wanted item searched
// before is searched live at once, past its backoff and the six-hour floor,
// behind the pace; without the grant it waits its backoff and then asks
// only the release index.
func TestABacklogGrantIsOneLiveSearchNow(t *testing.T) {
	v := searchedView("breaking-bad-s02e03")
	require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish, "not granted: its backoff holds")

	v.Backlog = true
	p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.NotNil(t, p.Publish)
	require.False(t, p.Publish.Payload.IndexOnly, "a backlog search asks the indexers")
	require.True(t, p.Publish.Live)
	require.True(t, p.Publish.Backlog, "the stage spends the grant")
	require.Equal(t, schema.SearchReasonMissing, p.Publish.Payload.Reason)
	require.NotNil(t, p.LastLiveSearchAt)
	require.Equal(t, t0, p.LastLiveSearchAt.Time, "the cooldown runs from the dispatch")

	paced := searchplan.Decide(v, admitAll, func() time.Time { return t0.Add(time.Minute) })
	require.Nil(t, paced.Publish, "it waits for the namespace's live-search pace like any live search")
	require.Equal(t, catalogv1alpha1.DeliveryReasonPaced, paced.Waiting)

	t.Run("not when no longer wanted", func(t *testing.T) {
		v := searchedView("ep")
		v.Backlog, v.Wanted = true, false
		require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish)
	})
	t.Run("a cutoff-unmet item keeps its reason", func(t *testing.T) {
		v := searchedView("ep")
		v.Backlog, v.Reason = true, schema.SearchReasonCutoffUnmet
		p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
		require.Equal(t, schema.SearchReasonCutoffUnmet, p.Publish.Payload.Reason)
	})
	t.Run("waits for its season", func(t *testing.T) {
		v := searchedView("ep")
		v.Backlog, v.SeasonBacklog = true, true
		require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish)
	})
	t.Run("an IndexOnly search leaves lastLiveSearchAt alone", func(t *testing.T) {
		v := searchedView("ep")
		v.Now = t0.Add(30 * 24 * time.Hour)
		kept := metav1.NewTime(t0.Add(-40 * 24 * time.Hour))
		v.LastLiveSearch = &kept
		p := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
		require.NotNil(t, p.Publish)
		require.True(t, p.Publish.Payload.IndexOnly)
		require.Equal(t, kept, *p.LastLiveSearchAt)
	})
}

// TestASeasonAnswerIsTheEpisodesLiveSearch: a season answer folded into an
// Episode moves its lastLiveSearchAt too, and spends a grant waiting for it.
func TestASeasonAnswerIsTheEpisodesLiveSearch(t *testing.T) {
	answered := metav1.NewTime(t0.Add(-10 * time.Minute))
	v := searchedView("breaking-bad-s02e03")
	v.Backlog, v.SeasonBacklog = true, true
	v.SeasonAnsweredAt = &answered
	p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.True(t, p.SeasonFolded)
	require.Equal(t, answered, *p.LastLiveSearchAt)
	require.Nil(t, p.Publish, "the season was its search")
}

// TestASeasonOfGrantedEpisodesIsSearchedAsAPack: two or more granted
// episodes of a season, no grab holding it, ask for the season.
func TestASeasonOfGrantedEpisodesIsSearchedAsAPack(t *testing.T) {
	v := seasonView(t0)
	v.Seasons = []searchplan.SeasonFacts{
		{Number: 1, Monitored: true, Wanted: 7, BacklogDue: 3},
		{Number: 2, Monitored: true, Wanted: 7, BacklogDue: 1},
		{Number: 3, Monitored: true, Wanted: 7, BacklogDue: 3, Downloading: 1},
	}
	p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
	require.NotNil(t, p.State)
	require.True(t, searchplan.Automatic(p.State))
	require.Equal(t, map[int32]catalogv1alpha1.SeasonSearchPhase{1: catalogv1alpha1.SeasonSearchSearching}, phases(p.State),
		"season 2 has one granted episode, which searches alone; a grab holds season 3")
}
