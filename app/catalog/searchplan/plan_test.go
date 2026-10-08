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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const ns = "media"

// clock is the test's time, shared by the planner's view and the real
// records.Pacer.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func admitAll(string, int64) dispatch.Decision { return dispatch.Decision{Admitted: true} }

// itemView is an Episode due its first search: wanted, monitored, a
// profile, nothing dispatched.
func itemView(name string, now time.Time) searchplan.View {
	ref := schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: name, UID: "uid-" + name}}
	return searchplan.View{
		Item: ref, Container: &schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: ns, Name: "breaking-bad", UID: "uid-bb"}},
		Kind: commonv1.MediaKindEpisode, Now: now, Monitored: true, Searchable: true, Wanted: true,
		Reason: schema.SearchReasonMissing, Profile: "hd-1080p",
		Protocols: []commonv1.Protocol{commonv1.ProtocolTorrent},
		MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: name},
		Key:       dispatch.Key{Kind: "Episode", Namespace: ns, Name: name, Sub: "search"},
	}
}

// pacerFor is the real live-search pacer at perMinute, with the item's
// reservation key as the search stage passes it.
func pacerFor(c *clock, perMinute int) (*records.Pacer, func(v searchplan.View) func() time.Time) {
	p := records.NewPacer(map[string]int{records.PaceSearch: perMinute}, c.Now)
	return p, func(v searchplan.View) func() time.Time {
		return func() time.Time { return p.Reserve(records.SearchClass(v.Item.Namespace), v.Item.UID) }
	}
}

// answer is the search agent's record answering the dispatch in flight.
func answer(p searchplan.Plan, at time.Time, allPaced bool) *schema.SearchRecord {
	return &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{Seq: p.Dispatch.Seq, State: records.StateAnswered},
		FinishedAt:   at, AllPaced: allPaced,
	}
}

// carry is the next pass's view of v after plan was applied.
func carry(v searchplan.View, p searchplan.Plan, now time.Time) searchplan.View {
	v.Now = now
	v.Dispatch, v.LastSearched = p.Dispatch, p.LastSearchedAt
	v.Attempts, v.DonorAttempts = p.Attempts, p.DonorAttempts
	if p.Publish != nil {
		cp := *p.Publish
		v.InFlight = &cp
	}
	return v
}

func ago(d time.Duration) *metav1.Time {
	t := metav1.NewTime(t0.Add(-d))
	return &t
}

// TestTheSearchPlannerFollowsTheBackoffLadder is P11: an item searched n
// times waits 6 h · 2^(n-1), capped at 7 days, from its latest attempt.
func TestTheSearchPlannerFollowsTheBackoffLadder(t *testing.T) {
	for n, wait := range map[int32]time.Duration{1: 6 * time.Hour, 2: 12 * time.Hour, 3: 24 * time.Hour, 5: 96 * time.Hour, 6: 7 * 24 * time.Hour, 40: 7 * 24 * time.Hour} {
		t.Run(fmt.Sprintf("%d attempts wait %s", n, wait), func(t *testing.T) {
			require.Equal(t, wait, wantedcron.Backoff(commonv1.Attempts{Count: n}))
			v := itemView("ep", t0)
			v.Attempts = commonv1.Attempts{Count: n, Initial: ago(30 * 24 * time.Hour), Latest: ago(wait - time.Minute)}
			v.LastSearched = v.Attempts.Latest
			require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish, "a minute short of the backoff")

			v.Attempts.Latest = ago(wait + time.Minute)
			v.LastSearched = v.Attempts.Latest
			p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
			require.NotNil(t, p.Publish, "a minute past it")
			require.True(t, p.Publish.Payload.IndexOnly, "an item searched before searches the release index")
		})
	}
	t.Run("a never-searched item is due at once", func(t *testing.T) {
		p := searchplan.Decide(itemView("ep", t0), admitAll, func() time.Time { return t0 })
		require.NotNil(t, p.Publish)
		require.Equal(t, t0.Add(searchplan.RepublishEvery), p.Due)
	})
	t.Run("an unmonitored, unwanted or profileless item is never due", func(t *testing.T) {
		for _, f := range []func(*searchplan.View){
			func(v *searchplan.View) { v.Monitored = false },
			func(v *searchplan.View) { v.Wanted = false },
			func(v *searchplan.View) { v.Profile = "" },
			func(v *searchplan.View) { v.Searchable = false },
		} {
			v := itemView("ep", t0)
			f(&v)
			require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish)
		}
	})
}

// TestAnIndexOnlySearchIsNotPacedAndALiveOneIs is P12 and §5.4: an item
// never searched gets one live search, which reserves the namespace's pace
// slot on the normal lane; an item searched before asks the release index
// on the low lane and is never paced.
func TestAnIndexOnlySearchIsNotPacedAndALiveOneIs(t *testing.T) {
	c := &clock{now: t0}
	pacer, pace := pacerFor(c, 1)
	// Another item holds the minute's only slot.
	pacer.Reserve(records.SearchClass(ns), "uid-other")

	live := itemView("never-searched", t0)
	p := searchplan.Decide(live, admitAll, pace(live))
	require.Nil(t, p.Publish, "the live search waits its slot")
	require.Equal(t, catalogv1alpha1.DeliveryReasonPaced, p.Waiting)
	require.Equal(t, t0.Add(time.Minute), p.Due)

	indexOnly := itemView("searched-before", t0)
	indexOnly.Attempts = commonv1.Attempts{Count: 1, Initial: ago(7 * time.Hour), Latest: ago(7 * time.Hour)}
	reserved := false
	q := searchplan.Decide(indexOnly, admitAll, func() time.Time { reserved = true; return t0.Add(time.Hour) })
	require.False(t, reserved, "an index-only search never reserves a pace slot")
	require.NotNil(t, q.Publish)
	require.True(t, q.Publish.Payload.IndexOnly)
	require.False(t, q.Publish.Live)
	require.Equal(t, events.WorkSearchSubject(events.PriorityLow, events.MediaKey("episode", ns, "searched-before")), q.Publish.Subject)

	c.now = t0.Add(time.Minute)
	live.Now = c.now
	p = searchplan.Decide(live, admitAll, pace(live))
	require.NotNil(t, p.Publish, "its slot came")
	require.True(t, p.Publish.Live)
	require.False(t, p.Publish.Payload.IndexOnly)
	require.Equal(t, events.WorkSearchSubject(events.PriorityNormal, events.MediaKey("episode", ns, "never-searched")), p.Publish.Subject)
	require.Equal(t, events.MsgIDForSearch("uid-never-searched", p.Dispatch.Seq), p.Publish.MsgID)
	require.Equal(t, searchplan.Durable, p.Dispatch.Destination)
}

// TestAPacedAnswerDoesNotCountAnAttempt is P6: a record whose every indexer
// was paced closes the dispatch without counting an attempt, and since no
// indexer was asked, the item's next search is still its first live one.
func TestAPacedAnswerDoesNotCountAnAttempt(t *testing.T) {
	v := itemView("ep", t0)
	first := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.True(t, first.Publish.Live)

	v = carry(v, first, t0.Add(time.Minute))
	v.Record = answer(first, t0.Add(30*time.Second), true)
	paced := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
	require.True(t, paced.Answered)
	require.Equal(t, first.Dispatch.Seq, paced.AnsweredSeq)
	require.Zero(t, paced.Attempts.Count, "an all-paced answer is not an attempt")
	require.Nil(t, paced.Publish, "nor is the item searched again before the backoff's floor")

	v = carry(v, paced, t0.Add(wantedcron.MinimumGap+time.Minute))
	next := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
	require.NotNil(t, next.Publish)
	require.True(t, next.Publish.Live, "no indexer was asked: the next search is still the item's live one")

	t.Run("a counted answer is an attempt", func(t *testing.T) {
		v := carry(itemView("ep", t0), first, t0.Add(time.Minute))
		v.Record = answer(first, t0.Add(30*time.Second), false)
		p := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
		require.EqualValues(t, 1, p.Attempts.Count)
		require.Equal(t, t0.Add(30*time.Second), p.LastSearchedAt.UTC())
	})
}

// TestASearchWithNoIndexerDoesNotCountAnAttempt is smoke finding 7: an item
// searched before any Indexer existed -- or while every one was disabled --
// was answered by no indexer, so that answer is no attempt and sets no
// lastSearchedAt, and once an indexer exists the item's next search is
// still its one live search, not IndexOnly on the backoff.
func TestASearchWithNoIndexerDoesNotCountAnAttempt(t *testing.T) {
	for _, kind := range []string{"video", "donor"} {
		t.Run(kind, func(t *testing.T) {
			v := itemView("ep", t0)
			if kind == "donor" {
				v.Wanted = false
				v.Donor = &schema.DonorWant{Languages: []string{"en"}, Anchor: "ja"}
				v.DonorBudget = true
			}
			first := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
			require.NotNil(t, first.Publish)
			require.True(t, first.Publish.Live)

			// No Indexer in the namespace: the agent answers with none asked.
			v = carry(v, first, t0.Add(time.Minute))
			v.Record = answer(first, t0.Add(30*time.Second), false)
			v.Record.NoIndexer = true
			if kind == "donor" {
				v.Record.Purpose = schema.SearchPurposeAudioDonor
			}
			empty := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
			require.True(t, empty.Answered, "the answer closes the dispatch")
			require.Zero(t, empty.Attempts.Count, "no indexer was asked: not an attempt")
			require.Zero(t, empty.DonorAttempts.Count, "nor a donor attempt")
			require.Nil(t, empty.LastSearchedAt, "nor a search")
			require.Nil(t, empty.Publish, "the backoff's floor still holds the dispatch")

			// An Indexer is added; the next pass past the floor searches live.
			v = carry(v, empty, t0.Add(wantedcron.MinimumGap+time.Minute))
			next := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
			require.NotNil(t, next.Publish)
			require.True(t, next.Publish.Live, "the item's one live search")
			require.False(t, next.Publish.Payload.IndexOnly)
		})
	}
}

// TestTheDonorBudgetCapsFirstTimeLiveDonorSearchesPerSweep is P4 and §7.3:
// an item's first donor search is live, and at most
// DonorSearchesPerSweep of them go per namespace per sweep; a donor
// searched before is index-only and not capped.
func TestTheDonorBudgetCapsFirstTimeLiveDonorSearchesPerSweep(t *testing.T) {
	budget := searchplan.NewDonorBudget()
	budget.Refill(ns, searchplan.DonorSearchesPerSweep)
	donorView := func(i int) searchplan.View {
		v := itemView(fmt.Sprintf("ep-%02d", i), t0)
		v.Wanted = false // the video is fine; only a dub is missing
		v.Donor = &schema.DonorWant{Languages: []string{"en"}, Anchor: "ja"}
		v.DonorBudget = budget.Room(ns)
		return v
	}
	live := 0
	for i := range 25 {
		p := searchplan.Decide(donorView(i), admitAll, func() time.Time { return t0 })
		if p.Publish == nil {
			continue
		}
		require.Equal(t, schema.SearchPurposeAudioDonor, p.Publish.Payload.Purpose)
		require.True(t, p.Publish.FirstLiveDonor)
		require.True(t, budget.Take(ns), "the stage spends what Decide granted")
		live++
	}
	require.Equal(t, searchplan.DonorSearchesPerSweep, live)

	searched := donorView(30)
	searched.DonorAttempts = commonv1.Attempts{Count: 1, Initial: ago(7 * time.Hour), Latest: ago(7 * time.Hour)}
	p := searchplan.Decide(searched, admitAll, func() time.Time { return t0 })
	require.NotNil(t, p.Publish, "a donor searched before is not capped")
	require.True(t, p.Publish.Payload.IndexOnly)
	require.False(t, p.Publish.FirstLiveDonor)

	budget.Refill(ns, searchplan.DonorSearchesPerSweep)
	require.NotNil(t, searchplan.Decide(donorView(24), admitAll, func() time.Time { return t0 }).Publish, "the next sweep refills it")
	require.False(t, searchplan.NewDonorBudget().Room("other"), "a namespace never refilled has no budget")
}

// TestABlocklistedEntryMakesItsItemsDueAtOnce is R25: a Failed or
// Blocklisted entry asks a search of every item it covered at once, on the
// item's backoff or not, while the failure is newer than the item's last
// dispatch and younger than RedownloadWindow. A failed pack does so for each
// of its episodes.
func TestABlocklistedEntryMakesItsItemsDueAtOnce(t *testing.T) {
	failedAt := t0.Add(-time.Minute)
	for _, e := range []int{1, 2, 3} {
		v := itemView(fmt.Sprintf("breaking-bad-s01e%02d", e), t0)
		v.Attempts = commonv1.Attempts{Count: 2, Initial: ago(2 * time.Hour), Latest: ago(time.Hour)}
		v.LastSearched = v.Attempts.Latest
		v.Dispatch = &catalogv1alpha1.Dispatch{Seq: 5, AnsweredSeq: 5, DispatchedAt: *ago(time.Hour)}
		require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish, "inside its backoff")

		v.Redownload = &failedAt
		p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
		require.NotNil(t, p.Publish, "episode %d of the failed pack", e)
		require.Equal(t, schema.SearchReasonRedownload, p.Publish.Payload.Reason)
		require.True(t, p.Publish.Live)

		// Once that search is dispatched, the same failure asks no more.
		again := carry(v, p, t0.Add(time.Minute))
		again.Record = answer(p, t0.Add(30*time.Second), false)
		require.Nil(t, searchplan.Decide(again, admitAll, func() time.Time { return again.Now }).Publish)
	}

	t.Run("a failure older than the window asks nothing", func(t *testing.T) {
		v := itemView("ep", t0)
		v.Attempts = commonv1.Attempts{Count: 2, Initial: ago(2 * time.Hour), Latest: ago(time.Hour)}
		old := t0.Add(-lifecycle.RedownloadWindow - time.Minute)
		v.Redownload = &old
		require.Nil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish)
	})
}

// TestAnEpisodeWaitsForItsPendingSeason is series search design §7: while
// its season is Queued or Searching in the Series' season search, an
// Episode starts no search of its own -- except a redownload.
func TestAnEpisodeWaitsForItsPendingSeason(t *testing.T) {
	v := itemView("breaking-bad-s01e05", t0)
	v.SeasonPending = true
	p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.Nil(t, p.Publish)
	require.True(t, p.Due.IsZero(), "the Series' wake brings the pass, not a timer")

	failed := t0.Add(-time.Minute)
	v.Redownload = &failed
	p = searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.NotNil(t, p.Publish, "a redownload does not wait")
	require.Equal(t, schema.SearchReasonRedownload, p.Publish.Payload.Reason)

	v.Redownload, v.SeasonPending = nil, false
	require.NotNil(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Publish, "once the season settles it searches")
}

// TestTheEpisodeFoldsASeasonAnswerOnce is design §7: a season answer newer
// than the Episode's lastSearchedAt counts once as its search -- one
// attempt at answeredAt -- so its next search is IndexOnly, and the next
// pass, seeing lastSearchedAt equal, does not count it again.
func TestTheEpisodeFoldsASeasonAnswerOnce(t *testing.T) {
	answered := metav1.NewTime(t0.Add(-10 * time.Minute))
	v := itemView("breaking-bad-s01e05", t0)
	v.SeasonAnsweredAt = &answered

	p := searchplan.Decide(v, admitAll, func() time.Time { return t0 })
	require.EqualValues(t, 1, p.Attempts.Count)
	require.Equal(t, answered, *p.LastSearchedAt)
	require.Nil(t, p.Publish, "the fold is the search; the backoff starts from it")

	v = carry(v, p, t0.Add(time.Minute))
	again := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
	require.EqualValues(t, 1, again.Attempts.Count, "folded once")

	v = carry(v, again, answered.Add(wantedcron.MinimumGap+time.Minute))
	next := searchplan.Decide(v, admitAll, func() time.Time { return v.Now })
	require.NotNil(t, next.Publish)
	require.True(t, next.Publish.Payload.IndexOnly, "the season query fed the release index")

	t.Run("not while its own search is in flight", func(t *testing.T) {
		v := itemView("ep", t0)
		v.Dispatch = &catalogv1alpha1.Dispatch{Seq: 9, AnsweredSeq: 8, DispatchedAt: *ago(time.Minute)}
		v.SeasonAnsweredAt = &answered
		require.Zero(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Attempts.Count)
	})
	t.Run("not when no longer wanted", func(t *testing.T) {
		v := itemView("ep", t0)
		v.Wanted = false
		v.SeasonAnsweredAt = &answered
		require.Zero(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Attempts.Count)
	})
	t.Run("an older season answer is not folded", func(t *testing.T) {
		v := itemView("ep", t0)
		v.LastSearched = ago(time.Minute)
		v.SeasonAnsweredAt = &answered
		require.Zero(t, searchplan.Decide(v, admitAll, func() time.Time { return t0 }).Attempts.Count)
	})
}
