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
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

var saga = schema.ItemRef{Kind: "Comic", Ref: schema.Ref{Namespace: ns, Name: "saga", UID: "uid-saga"}}

// volumeView is Saga's container view: one season, the volume, with three
// monitored issues missing and never searched.
func volumeView(now time.Time) searchplan.SeasonView {
	return searchplan.SeasonView{
		Series: saga, Kind: commonv1.MediaKindComic, Now: now, Monitored: true, Profile: "comic",
		Protocols: []commonv1.Protocol{commonv1.ProtocolUsenet},
		Scope:     schema.BlockScopeOf(saga),
		Seasons:   []searchplan.SeasonFacts{{Number: searchplan.VolumeNumber, Monitored: true, Wanted: 3, PackFirst: true, FirstSearch: 3}},
		Records:   map[int32]*schema.SearchRecord{},
		Key:       dispatch.Key{Kind: "Comic", Namespace: ns, Name: "saga", Sub: "season-search"},
	}
}

// TestAComicVolumeIsSearchedAsOneSeason is 2026-11-10 design D10: a Comic
// with two or more issues missing is asked for as a volume pack first, the
// Series' season machinery with the season layer removed -- one automatic
// state over the one season VolumeNumber, its task naming the Comic (never
// an Issue) on the high lane under the season Msg-Id, its answer finishing
// the state, and the cooldown before the next.
func TestAComicVolumeIsSearchedAsOneSeason(t *testing.T) {
	now := t0
	v := volumeView(now)
	p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.NotNil(t, p.State)
	require.True(t, searchplan.Automatic(p.State), "no planned request waiting: the automatic volume search")
	require.Equal(t, map[int32]catalogv1alpha1.SeasonSearchPhase{searchplan.VolumeNumber: catalogv1alpha1.SeasonSearchSearching}, phases(p.State))
	require.NotNil(t, p.Publish)
	task := p.Publish
	require.Equal(t, commonv1.MediaKindComic, task.Payload.MediaRef.Kind, "the task names the Comic, so the agent answers the volume")
	require.Equal(t, "saga", task.Payload.MediaRef.Name)
	require.Equal(t, saga, task.Payload.Item)
	require.Equal(t, searchplan.VolumeNumber, *task.Payload.Season)
	require.Equal(t, schema.SearchReasonSeason, task.Payload.Reason)
	require.False(t, task.Payload.UserInvoked)
	require.False(t, task.Payload.IndexOnly)
	require.True(t, task.Live)
	require.Equal(t, events.WorkSearchSubject(events.PriorityHigh, events.MediaKey("comic", ns, "saga")), task.Subject)
	require.Equal(t, events.MsgIDForSeasonSearch("uid-saga", searchplan.VolumeNumber, task.Payload.Seq), task.MsgID)
	require.Equal(t, searchplan.SeasonDurable, p.State.Dispatch.Destination)

	// The answer finishes the state; the next volume search waits out the
	// cooldown.
	v = next(v, p, now.Add(time.Minute))
	v.Records[searchplan.VolumeNumber] = seasonAnswer(p, now.Add(30*time.Second), records.StateAnswered, false, 2)
	done := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.True(t, done.Answered)
	require.NotNil(t, done.State.FinishedAt)
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, done.State.Seasons[0].Phase)
	require.EqualValues(t, 2, done.State.Seasons[0].Approved)
	require.Len(t, done.Finals, 1)

	v = next(v, done, v.Now.Add(time.Minute))
	v.ServedSettled = true
	held := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.Nil(t, held.Publish)
	require.Equal(t, done.State.FinishedAt.Add(searchplan.AutomaticCooldown), held.Due)

	t.Run("nothing for an unmonitored comic, one issue missing, or a grab holding the volume", func(t *testing.T) {
		v := volumeView(now)
		v.Monitored = false
		require.Nil(t, searchplan.DecideSeasons(v, admitAll, paceNow(now)).State)

		v = volumeView(now)
		v.Seasons[0].PackFirst, v.Seasons[0].Wanted, v.Seasons[0].FirstSearch = false, 1, 1
		require.Nil(t, searchplan.DecideSeasons(v, admitAll, paceNow(now)).State)

		v = volumeView(now)
		v.Seasons[0].Downloading = 1
		require.Nil(t, searchplan.DecideSeasons(v, admitAll, paceNow(now)).State)

		v = volumeView(now)
		v.Seasons[0].FirstSearch = 0
		require.Nil(t, searchplan.DecideSeasons(v, admitAll, paceNow(now)).State, "every issue was searched before")
	})
	t.Run("a view naming no kind is a Series", func(t *testing.T) {
		v := seasonView(now)
		v.Seasons = packFirstSeasons()
		p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
		require.Equal(t, commonv1.MediaKindSeries, p.Publish.Payload.MediaRef.Kind)
	})
}

// TestVolumePackFirstReadsTheMissingCount is design D10's test, from the
// Comic's container rollup: monitored with two or more monitored, released
// issues missing.
// A planned volume Search -- the comic page's "Search Volume", a Search
// naming the Comic with spec.season VolumeNumber and grabBest (design
// §9.4, D10) -- is served as a Series' season search is: the state names
// the request, one season task goes out for the volume as a person's, its
// answer finishes the state, and the next request waits for the handshake.
// A request naming another season is skipped, since a comic has one
// volume, and one naming none asks for the volume.
func TestAPlannedVolumeSearchIsServedAsASeasonSearch(t *testing.T) {
	now := t0
	v := volumeView(now)
	v.Seasons[0].FirstSearch = 0 // every issue searched before: nothing automatic to serve
	v.Requests = []searchplan.SeasonRequest{request("volume", seasonN(searchplan.VolumeNumber), now.Add(-time.Minute))}
	p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.NotNil(t, p.State)
	require.True(t, p.Started)
	require.False(t, searchplan.Automatic(p.State), "the state serves the Search")
	require.Equal(t, catalogv1alpha1.SeasonSearchRequest{Name: "volume", UID: "uid-volume"}, p.State.Request)
	require.Equal(t, map[int32]catalogv1alpha1.SeasonSearchPhase{searchplan.VolumeNumber: catalogv1alpha1.SeasonSearchSearching}, phases(p.State))
	require.NotNil(t, p.Publish)
	task := p.Publish
	require.Equal(t, commonv1.MediaKindComic, task.Payload.MediaRef.Kind)
	require.Equal(t, "saga", task.Payload.MediaRef.Name)
	require.Equal(t, saga, task.Payload.Item, "the Comic is the item: no Issue search")
	require.Equal(t, searchplan.VolumeNumber, *task.Payload.Season)
	require.Equal(t, schema.SearchReasonSeason, task.Payload.Reason)
	require.True(t, task.Payload.UserInvoked, "a person asked")
	require.True(t, task.Live)
	require.Equal(t, events.MsgIDForSeasonSearch("uid-saga", searchplan.VolumeNumber, task.Payload.Seq), task.MsgID)

	v = next(v, p, now.Add(time.Minute))
	v.Records[searchplan.VolumeNumber] = seasonAnswer(p, now.Add(30*time.Second), records.StateAnswered, false, 2)
	done := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.True(t, done.Answered)
	require.NotNil(t, done.State.FinishedAt)
	require.Equal(t, "uid-volume", done.State.Request.UID)
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, done.State.Seasons[0].Phase)
	require.EqualValues(t, 2, done.State.Seasons[0].Approved)
	require.Len(t, done.Finals, 1)
	require.Equal(t, "uid-volume", done.Finals[0].Request.UID)

	// The handshake: the next request waits for the Search to copy its
	// outcome, then is served; a request for another season is skipped.
	v = next(v, done, v.Now.Add(time.Minute))
	v.Requests = append(v.Requests, request("second", seasonN(2), v.Now))
	held := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.False(t, held.Started, "the served Search has not settled")
	v.ServedSettled = true
	served := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
	require.True(t, served.Started)
	require.Equal(t, "uid-second", served.State.Request.UID)
	require.Equal(t, catalogv1alpha1.SeasonSearchSkipped, served.State.Seasons[0].Phase)
	require.Equal(t, catalogv1alpha1.SeasonSearchNoSuchSeason, served.State.Seasons[0].Reason, "a comic has one volume")
	require.Nil(t, served.Publish)

	t.Run("a request naming no season asks for the volume", func(t *testing.T) {
		v := volumeView(now)
		v.Requests = []searchplan.SeasonRequest{request("whole", nil, now)}
		p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
		require.Equal(t, "uid-whole", p.State.Request.UID)
		require.Equal(t, map[int32]catalogv1alpha1.SeasonSearchPhase{searchplan.VolumeNumber: catalogv1alpha1.SeasonSearchSearching}, phases(p.State))
	})
	t.Run("nothing wanted is skipped at once, as a season is", func(t *testing.T) {
		v := volumeView(now)
		v.Seasons[0].Wanted = 0
		v.Requests = []searchplan.SeasonRequest{request("volume", seasonN(searchplan.VolumeNumber), now)}
		p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
		require.Equal(t, catalogv1alpha1.SeasonSearchSkipped, p.State.Seasons[0].Phase)
		require.Equal(t, catalogv1alpha1.SeasonSearchNothingWanted, p.State.Seasons[0].Reason)
		require.NotNil(t, p.State.FinishedAt)
		require.Nil(t, p.Publish)
	})
}

func TestVolumePackFirstReadsTheMissingCount(t *testing.T) {
	comic := func(monitored *bool, missing int32) *catalogv1alpha1.Comic {
		co := &catalogv1alpha1.Comic{}
		co.Spec.Monitored = monitored
		co.Status.MissingChildCount = missing
		return co
	}
	for _, tc := range []struct {
		name string
		co   *catalogv1alpha1.Comic
		want bool
	}{
		{"two missing, monitored by default", comic(nil, 2), true},
		{"many missing", comic(ptr.To(true), 12), true},
		{"one missing is searched as an issue", comic(ptr.To(true), 1), false},
		{"nothing missing", comic(ptr.To(true), 0), false},
		{"unmonitored", comic(ptr.To(false), 5), false},
		{"no comic", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, searchplan.VolumePackFirst(tc.co))
		})
	}
}
