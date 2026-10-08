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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

var bb = schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: ns, Name: "breaking-bad", UID: "uid-bb"}}

func request(name string, season *int32, created time.Time) searchplan.SeasonRequest {
	return searchplan.SeasonRequest{
		Search: catalogv1alpha1.SeasonSearchRequest{Name: name, UID: "uid-" + name}, Season: season, Created: created,
	}
}

func seasonN(n int32) *int32 { return &n }

// seasons are Breaking Bad's specials and three seasons, all monitored,
// seven wanted episodes each.
func seasons() []searchplan.SeasonFacts {
	return []searchplan.SeasonFacts{
		{Number: 0, Monitored: true, Wanted: 2},
		{Number: 1, Monitored: true, Wanted: 7},
		{Number: 2, Monitored: true, Wanted: 7},
		{Number: 3, Monitored: true, Wanted: 7},
	}
}

func seasonView(now time.Time, reqs ...searchplan.SeasonRequest) searchplan.SeasonView {
	return searchplan.SeasonView{
		Series: bb, Now: now, Monitored: true, Profile: "hd-1080p",
		Protocols: []commonv1.Protocol{commonv1.ProtocolTorrent},
		Scope:     schema.BlockScopeOf(bb), Requests: reqs, Seasons: seasons(),
		Records: map[int32]*schema.SearchRecord{},
		Key:     dispatch.Key{Kind: "Series", Namespace: ns, Name: "breaking-bad", Sub: "season-search"},
	}
}

func paceNow(now time.Time) func(int32) time.Time { return func(int32) time.Time { return now } }

// next is the following pass's view after p was applied, at now.
func next(v searchplan.SeasonView, p searchplan.SeasonPlan, now time.Time) searchplan.SeasonView {
	v.Now, v.State, v.LastSearched = now, p.State, p.LastSearchedAt
	if p.Publish != nil {
		cp := *p.Publish
		v.InFlight = &cp
	}
	return v
}

// seasonAnswer is the search agent's record of the season in flight.
func seasonAnswer(p searchplan.SeasonPlan, at time.Time, state string, allPaced bool, approved int) *schema.SearchRecord {
	season := *p.Publish.Payload.Season
	rec := &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{Seq: p.Publish.Payload.Seq, State: state, Sub: schema.SeasonSub(season)},
		FinishedAt:   at, AllPaced: allPaced, Season: &season,
	}
	for i := range approved + 1 {
		rec.Candidates = append(rec.Candidates, schema.ScoredRelease{Decision: commonv1.ReleaseDecision{Approved: i < approved}})
	}
	return rec
}

func phases(st *catalogv1alpha1.SeasonSearchState) map[int32]catalogv1alpha1.SeasonSearchPhase {
	out := map[int32]catalogv1alpha1.SeasonSearchPhase{}
	for _, s := range st.Seasons {
		out[s.Number] = s.Phase
	}
	return out
}

func searchingCount(st *catalogv1alpha1.SeasonSearchState) int {
	n := 0
	for _, s := range st.Seasons {
		if s.Phase == catalogv1alpha1.SeasonSearchSearching {
			n++
		}
	}
	return n
}

// TestDecideSeasonsDispatchesOneSeasonAtATime is design §4.4: three queued
// seasons publish one task per pass, each only after the previous one's
// answer; the specials are not part of a whole-series request; the state
// finishes with the newest answer as lastSearchedAt.
func TestDecideSeasonsDispatchesOneSeasonAtATime(t *testing.T) {
	now := t0
	v := seasonView(now, request("all", nil, now.Add(-time.Minute)))
	p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.True(t, p.Started)
	require.Len(t, p.State.Seasons, 3, "season 0 is not part of a whole-series request")
	require.NotNil(t, p.Publish)

	var published []int32
	for want := int32(1); want <= 3; want++ {
		require.NotNil(t, p.Publish, "season %d", want)
		task := p.Publish
		published = append(published, *task.Payload.Season)
		require.Equal(t, want, *task.Payload.Season)
		require.Equal(t, 1, searchingCount(p.State), "one season in flight")
		require.Equal(t, schema.SearchReasonSeason, task.Payload.Reason)
		require.True(t, task.Payload.UserInvoked)
		require.False(t, task.Payload.IndexOnly)
		require.Equal(t, bb, task.Payload.Item)
		require.Equal(t, events.WorkSearchSubject(events.PriorityHigh, events.MediaKey("series", ns, "breaking-bad")), task.Subject)
		require.Equal(t, events.MsgIDForSeasonSearch("uid-bb", want, task.Payload.Seq), task.MsgID)
		require.Equal(t, searchplan.SeasonDurable, p.State.Dispatch.Destination)

		// A pass with no answer publishes nothing new.
		now = now.Add(30 * time.Second)
		v = next(v, p, now)
		idle := searchplan.DecideSeasons(v, admitAll, paceNow(now))
		require.Nil(t, idle.Publish)
		require.Equal(t, 1, searchingCount(idle.State))

		// The answer closes the season and dispatches the next one.
		now = now.Add(30 * time.Second)
		v = next(v, idle, now)
		v.Records[want] = seasonAnswer(p, now.Add(-10*time.Second), records.StateAnswered, false, 3)
		p = searchplan.DecideSeasons(v, admitAll, paceNow(now))
		require.True(t, p.Answered)
		require.Equal(t, v.State.Dispatch.Seq, p.AnsweredSeq)
		got := p.State.Seasons[want-1]
		require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, got.Phase)
		require.EqualValues(t, 4, got.Candidates)
		require.EqualValues(t, 3, got.Approved)
	}
	require.Nil(t, p.Publish)
	require.Equal(t, []int32{1, 2, 3}, published)
	require.NotNil(t, p.State.FinishedAt)
	require.Equal(t, now.Add(-10*time.Second).Truncate(time.Second), p.LastSearchedAt.UTC())
	require.Len(t, p.Finals, 1, "season 3 became final this pass")
}

// TestDecideSeasonsWaitsForItsPaceSlot is design §4.4 step 3: a season
// reserves the namespace's live-search slot (the real Pacer); a slot more
// than a second away leaves the season Queued, Paced, due at the slot.
func TestDecideSeasonsWaitsForItsPaceSlot(t *testing.T) {
	c := &clock{now: t0}
	pacer := records.NewPacer(map[string]int{records.PaceSearch: 1}, c.Now)
	class := records.SearchClass(ns)
	pacer.Reserve(class, "uid-another-item")
	pace := func(season int32) time.Time { return pacer.Reserve(class, searchplan.SeasonPaceKey("uid-bb", season)) }

	v := seasonView(t0, request("s1", seasonN(1), t0))
	p := searchplan.DecideSeasons(v, admitAll, pace)
	require.Nil(t, p.Publish)
	require.Equal(t, catalogv1alpha1.DeliveryReasonPaced, p.Waiting)
	require.Equal(t, t0.Add(time.Minute), p.Due)
	require.Equal(t, catalogv1alpha1.SeasonSearchQueued, p.State.Seasons[0].Phase)

	c.now = t0.Add(time.Minute)
	p = searchplan.DecideSeasons(next(v, p, c.now), admitAll, pace)
	require.NotNil(t, p.Publish, "its slot came")
	require.Equal(t, catalogv1alpha1.SeasonSearchSearching, p.State.Seasons[0].Phase)
}

// TestDecideSeasonsWaitsForAdmission is design §4.4 step 3: refused by the
// dispatch ledger, the season stays Queued and the plan carries the
// ledger's reason and retry.
func TestDecideSeasonsWaitsForAdmission(t *testing.T) {
	refuse := func(durable string, _ int64) dispatch.Decision {
		require.Equal(t, searchplan.SeasonDurable, durable)
		return dispatch.Decision{Reason: catalogv1alpha1.DeliveryReasonBudget, Message: "waiting", RetryAfter: 10 * time.Second}
	}
	v := seasonView(t0, request("s1", seasonN(1), t0))
	p := searchplan.DecideSeasons(v, refuse, paceNow(t0))
	require.Nil(t, p.Publish)
	require.Equal(t, catalogv1alpha1.DeliveryReasonBudget, p.Waiting)
	require.Equal(t, t0.Add(10*time.Second), p.Due)
	require.Equal(t, catalogv1alpha1.SeasonSearchQueued, p.State.Seasons[0].Phase)
	require.Nil(t, p.State.Dispatch)

	now := t0.Add(10 * time.Second)
	p = searchplan.DecideSeasons(next(v, p, now), admitAll, paceNow(now))
	require.NotNil(t, p.Publish)
}

// TestDecideSeasonsSkipsWhatCannotGrab is design §4.3: a season a query
// could grab nothing for is Skipped at once with its reason -- unmonitored,
// nothing wanted, everything wanted already downloading, no such season --
// and a whole-series request leaves out the specials.
func TestDecideSeasonsSkipsWhatCannotGrab(t *testing.T) {
	v := seasonView(t0, request("all", nil, t0))
	v.Seasons = []searchplan.SeasonFacts{
		{Number: 0, Monitored: true, Wanted: 2},
		{Number: 1, Monitored: false, Wanted: 7},
		{Number: 2, Monitored: true, Wanted: 0},
		{Number: 3, Monitored: true, Wanted: 3, Downloading: 3},
		{Number: 4, Monitored: true, Wanted: 3, Downloading: 1},
	}
	p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
	reasons := map[int32]catalogv1alpha1.SeasonSearchReason{}
	for _, s := range p.State.Seasons {
		reasons[s.Number] = s.Reason
	}
	require.Equal(t, map[int32]catalogv1alpha1.SeasonSearchReason{
		1: catalogv1alpha1.SeasonSearchNotMonitored,
		2: catalogv1alpha1.SeasonSearchNothingWanted,
		3: catalogv1alpha1.SeasonSearchAllDownloading,
		4: "",
	}, reasons)
	require.Equal(t, catalogv1alpha1.SeasonSearchSearching, phases(p.State)[4])
	require.Equal(t, int32(4), *p.Publish.Payload.Season)
	require.Len(t, p.Finals, 3, "the skipped seasons are final at once")

	t.Run("no such season finishes the state at once", func(t *testing.T) {
		v := seasonView(t0, request("s9", seasonN(9), t0))
		p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
		require.Equal(t, catalogv1alpha1.SeasonSearchNoSuchSeason, p.State.Seasons[0].Reason)
		require.NotNil(t, p.State.FinishedAt)
		require.Nil(t, p.Publish)
	})
	t.Run("an unmonitored series skips every season", func(t *testing.T) {
		v := seasonView(t0, request("all", nil, t0))
		v.Monitored = false
		p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
		for _, s := range p.State.Seasons {
			require.Equal(t, catalogv1alpha1.SeasonSearchNotMonitored, s.Reason)
		}
		require.Nil(t, p.Publish)
	})
	t.Run("the specials when named", func(t *testing.T) {
		v := seasonView(t0, request("s0", seasonN(0), t0))
		p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
		require.Equal(t, int32(0), *p.Publish.Payload.Season)
	})
}

// TestDecideSeasonsRequeuesAnAllPacedSeasonOnce is design §4.4 step 1: a
// season every indexer paced goes once to the back of the queue; a second
// all-paced answer is Answered, AllPaced.
func TestDecideSeasonsRequeuesAnAllPacedSeasonOnce(t *testing.T) {
	now := t0
	v := seasonView(now, request("s12", nil, now))
	v.Seasons = v.Seasons[:3] // specials, 1 and 2
	p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.Equal(t, int32(1), *p.Publish.Payload.Season)

	answerPass := func(allPaced bool) {
		now = now.Add(time.Minute)
		v = next(v, p, now)
		v.Records[*p.Publish.Payload.Season] = seasonAnswer(p, now, records.StateAnswered, allPaced, 0)
		p = searchplan.DecideSeasons(v, admitAll, paceNow(now))
	}
	answerPass(true) // season 1 paced: back of the queue
	require.Equal(t, catalogv1alpha1.SeasonSearchQueued, p.State.Seasons[0].Phase)
	require.Equal(t, int32(2), *p.Publish.Payload.Season, "season 2 goes before season 1's retry")
	answerPass(false)
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, p.State.Seasons[1].Phase)
	require.Equal(t, int32(1), *p.Publish.Payload.Season)
	require.EqualValues(t, 2, p.State.Seasons[0].Attempts)
	answerPass(true) // paced again: answered, counting for no episode
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, p.State.Seasons[0].Phase)
	require.Equal(t, catalogv1alpha1.SeasonSearchAllPaced, p.State.Seasons[0].Reason)
	require.Nil(t, p.Publish)
	require.NotNil(t, p.State.FinishedAt)
}

// TestDecideSeasonsAnswersANoIndexerSeasonForNoEpisode is smoke finding 7
// on a season: a season answered with no indexer to ask is Answered,
// NoIndexer -- not requeued, since asking again asks no one -- and counts
// for no episode (the Episode's fold skips it, as it skips AllPaced).
func TestDecideSeasonsAnswersANoIndexerSeasonForNoEpisode(t *testing.T) {
	now := t0
	v := seasonView(now, request("s1", seasonN(1), now))
	p := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.Equal(t, int32(1), *p.Publish.Payload.Season)

	now = now.Add(time.Minute)
	v = next(v, p, now)
	rec := seasonAnswer(p, now, records.StateAnswered, false, 0)
	rec.NoIndexer = true
	v.Records[1] = rec
	p = searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.Equal(t, catalogv1alpha1.SeasonSearchAnswered, p.State.Seasons[0].Phase)
	require.Equal(t, catalogv1alpha1.SeasonSearchNoIndexer, p.State.Seasons[0].Reason)
	require.EqualValues(t, 1, p.State.Seasons[0].Attempts, "not requeued")
	require.Nil(t, p.Publish)
	require.NotNil(t, p.State.FinishedAt)
}

// TestDecideSeasonsTimesOutAndGoesOn is design §4.4 step 2: a season no
// record answers within Timeout fails TimedOut, the ledger is told, and the
// next season is dispatched in the same pass; in between, the task is
// republished under its Msg-Id.
func TestDecideSeasonsTimesOutAndGoesOn(t *testing.T) {
	v := seasonView(t0, request("s12", nil, t0))
	v.Seasons = v.Seasons[:3]
	p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
	first := p.Publish

	now := t0.Add(searchplan.RepublishEvery)
	v = next(v, p, now)
	rep := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.NotNil(t, rep.Publish)
	require.True(t, rep.Publish.Republish)
	require.Equal(t, first.MsgID, rep.Publish.MsgID)

	now = t0.Add(searchplan.Timeout)
	v = next(v, rep, now)
	p = searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.True(t, p.Answered)
	require.Equal(t, first.Payload.Seq, p.AnsweredSeq)
	require.Equal(t, catalogv1alpha1.SeasonSearchFailed, p.State.Seasons[0].Phase)
	require.Equal(t, catalogv1alpha1.SeasonSearchTimedOut, p.State.Seasons[0].Reason)
	require.NotNil(t, p.Publish)
	require.Equal(t, int32(2), *p.Publish.Payload.Season)
	require.False(t, p.Publish.Republish)

	t.Run("a republish after a leader change rebuilds the task", func(t *testing.T) {
		v := next(seasonView(t0, request("s12", nil, t0)), searchplan.DecideSeasons(seasonView(t0, request("s12", nil, t0)), admitAll, paceNow(t0)), t0.Add(time.Minute))
		v.InFlight = nil
		p := searchplan.DecideSeasons(v, admitAll, paceNow(v.Now))
		require.NotNil(t, p.Publish)
		require.Equal(t, first.MsgID, p.Publish.MsgID)
	})
}

// TestDecideSeasonsCancelsWhenTheSearchIsDeleted is design §4.3: the
// served Search gone, the dispatch closes withdrawn, the searching and
// queued seasons are Canceled, SearchDeleted, and an answer arriving later
// is not incorporated.
func TestDecideSeasonsCancelsWhenTheSearchIsDeleted(t *testing.T) {
	v := seasonView(t0, request("s12", nil, t0))
	v.Seasons = v.Seasons[:3]
	p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
	inflight := *p.Publish

	now := t0.Add(time.Minute)
	v = next(v, p, now)
	v.Requests, v.ServedGone = nil, true
	p = searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.True(t, p.Answered)
	require.Equal(t, inflight.Payload.Seq, p.AnsweredSeq)
	require.True(t, p.State.Dispatch.Withdrawn)
	require.NotNil(t, p.State.FinishedAt)
	for _, s := range p.State.Seasons {
		require.Equal(t, catalogv1alpha1.SeasonSearchCanceled, s.Phase)
		require.Equal(t, catalogv1alpha1.SeasonSearchSearchDeleted, s.Reason)
	}
	require.Nil(t, p.Publish)

	now = now.Add(time.Minute)
	v = next(v, p, now)
	v.Records[1] = seasonAnswer(searchplan.SeasonPlan{Publish: &inflight}, now, records.StateAnswered, false, 2)
	late := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.False(t, late.Answered)
	require.Equal(t, catalogv1alpha1.SeasonSearchCanceled, late.State.Seasons[0].Phase, "a late answer is not incorporated")
	require.Nil(t, late.Publish)
}

// TestDecideSeasonsServesRequestsInOrderAfterTheHandshake is design §4.3:
// requests are served oldest first, one at a time, and the next is taken
// only once the served Search has copied its outcome (Completed or Failed)
// or is gone.
func TestDecideSeasonsServesRequestsInOrderAfterTheHandshake(t *testing.T) {
	first := request("first", seasonN(1), t0.Add(-2*time.Minute))
	second := request("second", seasonN(2), t0.Add(-time.Minute))
	v := seasonView(t0, first, second)
	p := searchplan.DecideSeasons(v, admitAll, paceNow(t0))
	require.Equal(t, "first", p.State.Request.Name)

	now := t0.Add(time.Minute)
	v = next(v, p, now)
	v.Records[1] = seasonAnswer(p, now, records.StateFailed, false, 0)
	p = searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.Equal(t, catalogv1alpha1.SeasonSearchFailed, p.State.Seasons[0].Phase)
	require.Equal(t, catalogv1alpha1.SeasonSearchAgentFailed, p.State.Seasons[0].Reason)
	require.NotNil(t, p.State.FinishedAt)

	now = now.Add(time.Minute)
	v = next(v, p, now)
	held := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.False(t, held.Started, "the first Search has not copied its outcome yet")
	require.Equal(t, "first", held.State.Request.Name)

	v.ServedSettled = true
	v.Requests = []searchplan.SeasonRequest{second} // the first reads Failed: no longer open
	taken := searchplan.DecideSeasons(v, admitAll, paceNow(now))
	require.True(t, taken.Started)
	require.Equal(t, "second", taken.State.Request.Name)
	require.Equal(t, int32(2), *taken.Publish.Payload.Season)

	t.Run("a deleting request is passed over", func(t *testing.T) {
		gone := first
		gone.Deleting = true
		p := searchplan.DecideSeasons(seasonView(t0, gone, second), admitAll, paceNow(t0))
		require.Equal(t, "second", p.State.Request.Name)
	})
}
