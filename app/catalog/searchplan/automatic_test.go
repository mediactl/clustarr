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
	"github.com/mediactl/clustarr/pkg/records"
)

// packFirstSeasons are Breaking Bad's specials and three seasons: season 1
// fully aired with every episode waiting for its first search, season 2
// already searched, season 3 not PackFirst (still airing, or one missing).
func packFirstSeasons() []searchplan.SeasonFacts {
	return []searchplan.SeasonFacts{
		{Number: 0, Monitored: true, Wanted: 2, PackFirst: false, FirstSearch: 2},
		{Number: 1, Monitored: true, Wanted: 7, PackFirst: true, FirstSearch: 7},
		{Number: 2, Monitored: true, Wanted: 7, PackFirst: true, FirstSearch: 0},
		{Number: 3, Monitored: true, Wanted: 7, PackFirst: false, FirstSearch: 7},
	}
}

// TestAPackFirstSeasonIsSearchedAutomatically: with no person's Search
// waiting, a fully aired season with two or more wanted episodes, one of
// them never searched, is asked for as a season -- a pack before its
// episodes (owner, 2026-10-27; series search design §17) -- in a state that
// names no Search, its task not user-invoked.
func TestAPackFirstSeasonIsSearchedAutomatically(t *testing.T) {
	now := t0
	v := seasonView(now)
	v.Seasons = packFirstSeasons()
	p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.NotNil(t, p.State)
	require.True(t, searchplan.Automatic(p.State))
	require.Equal(t, map[int32]catalogv1alpha1.SeasonSearchPhase{1: catalogv1alpha1.SeasonSearchSearching}, phases(p.State),
		"only season 1: season 2's episodes were searched, season 3 is no PackFirst season, and specials never are")
	require.NotNil(t, p.Publish)
	require.Equal(t, int32(1), *p.Publish.Payload.Season)
	require.False(t, p.Publish.Payload.UserInvoked, "an automatic season search is no person's")

	// The answer finishes the state; the next automatic state waits out the
	// cooldown, and the pass asks to be woken when it ends.
	v = next(v, p, now.Add(time.Minute))
	v.Records[1] = seasonAnswer(p, now.Add(30*time.Second), records.StateAnswered, false, 1)
	done := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.NotNil(t, done.State.FinishedAt)
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, done.State.Seasons[0].Phase)

	v = next(v, done, v.Now.Add(time.Minute))
	v.ServedSettled = true
	held := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.Nil(t, held.Publish)
	require.Equal(t, done.State.FinishedAt.Add(searchplan.AutomaticCooldown), held.Due)

	v.Now = done.State.FinishedAt.Add(searchplan.AutomaticCooldown + time.Second)
	again := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.NotNil(t, again.Publish, "past the cooldown a season still waiting is asked again")
	require.True(t, again.Started)

	t.Run("a person's Search comes first", func(t *testing.T) {
		v := seasonView(now, request("pick", seasonN(2), now))
		v.Seasons = packFirstSeasons()
		p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
		require.False(t, searchplan.Automatic(p.State))
		require.Equal(t, "pick", p.State.Request.Name)
	})
	t.Run("nothing for an unmonitored series or no waiting episode", func(t *testing.T) {
		v := seasonView(now)
		v.Seasons = packFirstSeasons()
		v.Monitored = false
		require.Nil(t, searchplan.DecideSeasons(v, admitAll, paceNow(now)).State)

		v = seasonView(now)
		v.Seasons = packFirstSeasons()
		v.Seasons[1].FirstSearch = 0
		require.Nil(t, searchplan.DecideSeasons(v, admitAll, paceNow(now)).State)

		v.Seasons[1].FirstSearch, v.Seasons[1].Downloading = 7, 7
		require.Nil(t, searchplan.DecideSeasons(v, admitAll, paceNow(now)).State, "every wanted episode is downloading")
	})
	t.Run("an automatic state is never canceled for a missing Search", func(t *testing.T) {
		v := seasonView(now)
		v.Seasons = packFirstSeasons()
		p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
		v = next(v, p, now.Add(time.Second))
		v.ServedGone = true
		p = searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
		require.Equal(t, catalogv1alpha1.SeasonSearchSearching, p.State.Seasons[0].Phase)
	})
}

// PackFirst reads the Series' status.seasons: not the specials, monitored,
// aired to its end, two or more episodes without a file.
func TestPackFirst(t *testing.T) {
	next := metav1.NewTime(t0.Add(24 * time.Hour))
	for name, c := range map[string]struct {
		s    catalogv1alpha1.SeasonStatus
		want bool
	}{
		"aired, two missing":     {catalogv1alpha1.SeasonStatus{Number: 1, Monitored: true, EpisodeCount: 10, EpisodeFileCount: 8}, true},
		"one missing":            {catalogv1alpha1.SeasonStatus{Number: 1, Monitored: true, EpisodeCount: 10, EpisodeFileCount: 9}, false},
		"still airing":           {catalogv1alpha1.SeasonStatus{Number: 1, Monitored: true, EpisodeCount: 10, NextAiring: &next}, false},
		"unmonitored":            {catalogv1alpha1.SeasonStatus{Number: 1, EpisodeCount: 10}, false},
		"specials":               {catalogv1alpha1.SeasonStatus{Number: 0, Monitored: true, EpisodeCount: 10}, false},
		"aired, nothing on disk": {catalogv1alpha1.SeasonStatus{Number: 2, Monitored: true, EpisodeCount: 6}, true},
	} {
		require.Equal(t, c.want, searchplan.PackFirst(c.s), name)
	}
}

// TestAnEpisodeWaitsForItsPackFirstSeason: an Episode never searched whose
// season is PackFirst starts no search of its own -- the automatic season
// search asks for its season first -- while one searched before searches on
// its backoff as ever, and a redownload never waits.
func TestAnEpisodeWaitsForItsPackFirstSeason(t *testing.T) {
	v := itemView("breaking-bad-s01e05", t0)
	v.SeasonPackFirst = true
	require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish)

	failed := t0.Add(-time.Minute)
	v.Redownload = &failed
	require.NotNil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish, "a redownload does not wait")
	v.Redownload = nil

	searched := metav1.NewTime(t0.Add(-8 * 24 * time.Hour))
	v.LastSearched = &searched
	v.Attempts = commonv1.Attempts{Count: 1, Initial: &searched, Latest: &searched}
	p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.NotNil(t, p.Publish, "an episode searched before is no longer held for its season")
	require.True(t, p.Publish.Payload.IndexOnly)
}
