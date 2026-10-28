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

package mediux_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata/clients/mediux"
)

// edgerunners is the recorded show: 14 sets, four without a show poster,
// two of titlecards alone.
func edgerunners(t *testing.T) []mediux.ShowSet {
	t.Helper()
	c, _, _ := recorded(t)
	show, err := c.ShowSets(context.Background(), "105248")
	require.NoError(t, err)
	return show.Sets
}

// series is the policy for Edgerunners as the catalog has it: two
// seasons of ten episodes, images in English.
func series() mediux.Policy {
	return mediux.Policy{Languages: []string{"English"}, Seasons: []int32{1, 2}, Episodes: map[int32]int32{1: 10, 2: 10}}
}

// from is, by slot, the set each slot's image came from.
func from(c *mediux.Composition) map[mediux.Slot]string {
	out := map[mediux.Slot]string{}
	for _, p := range c.Picks {
		out[p.Slot] = p.Set
	}
	return out
}

func cards(season int32, n int32) []mediux.Slot {
	var out []mediux.Slot
	for e := int32(1); e <= n; e++ {
		out = append(out, mediux.Slot{Kind: mediux.SlotTitlecard, Season: season, Episode: e})
	}
	return out
}

var (
	poster   = mediux.Slot{Kind: mediux.SlotPoster}
	backdrop = mediux.Slot{Kind: mediux.SlotBackdrop}
	season1  = mediux.Slot{Kind: mediux.SlotSeasonPoster, Season: 1}
	season2  = mediux.Slot{Kind: mediux.SlotSeasonPoster, Season: 2}
)

// The preferred creator's set anchors the look, and what it lacks comes
// from the other sets: Praythea has nothing for season 2, so its poster
// and all ten titlecards come from sharivari's set, the most popular of
// those that have them.
func TestASlotTheAnchorLacksIsFilledFromAnotherSet(t *testing.T) {
	p := series()
	p.PreferredCreators = []string{"Praythea"}
	c := mediux.ComposeShow(edgerunners(t), p, "")
	require.NotNil(t, c)
	require.Equal(t, "8776", c.Anchor.ID, "of Praythea's two sets, the one with titlecards")
	got := from(c)
	for _, s := range append([]mediux.Slot{poster, backdrop, season1}, cards(1, 10)...) {
		require.Equal(t, "8776", got[s], "%+v", s)
	}
	require.Equal(t, "41471", got[season2])
	for _, s := range cards(2, 10) {
		require.Equal(t, "41471", got[s], "%+v", s)
	}
	require.Len(t, c.Picks, 24)
	require.Equal(t, []mediux.SetUse{
		{SetRef: refOf(c, "8776"), Images: 13},
		{SetRef: refOf(c, "41471"), Images: 11},
	}, c.Sets, "the anchor first, then by images supplied")
}

// A season's titlecards follow the anchor's creator, and an episode the
// creator has no card for takes one from the set that covers the season;
// a backdrop the anchor's creator never made comes from the most complete
// poster set with one (sharivari's, over creshaytions' more popular set,
// which has one season poster to its two).
func TestASeasonsTitlecardsFillTheirGapsFromOneSet(t *testing.T) {
	p := series()
	p.PreferredCreators = []string{"RandalPink87"}
	c := mediux.ComposeShow(edgerunners(t), p, "")
	require.NotNil(t, c)
	require.Equal(t, "38985", c.Anchor.ID)
	got := from(c)
	require.Equal(t, "38985", got[poster])
	require.Equal(t, "38985", got[season1])
	require.Equal(t, "38985", got[season2])
	require.Equal(t, "41471", got[backdrop])
	for i, s := range cards(2, 10) {
		want := "41471"
		if i < 3 {
			want = "38985" // RandalPink87 made episodes 1 to 3
		}
		require.Equal(t, want, got[s], "%+v", s)
	}
	require.Equal(t, []string{"38985", "41471"}, ids(c.Sets))
	require.Equal(t, 16, c.Sets[0].Images)
	require.Equal(t, 8, c.Sets[1].Images)
	require.Contains(t, c.Reason, "preferred creator RandalPink87")
}

// MediUX's popularity is a rank: 1 is the most popular set. Of two
// poster-only sets, Ludacris' (10th, popularity_global 5916) anchors over
// sharivari's (11th, 6342) -- the first cut took the higher number, the
// less popular.
func TestPopularityIsARankAndOneIsTheMostPopular(t *testing.T) {
	var keep []mediux.ShowSet
	for _, s := range edgerunners(t) {
		if s.ID == "29735" || s.ID == "41472" {
			keep = append(keep, s)
		}
	}
	require.Len(t, keep, 2)
	c := mediux.ComposeShow(keep, mediux.Policy{Languages: []string{"English"}}, "")
	require.NotNil(t, c)
	require.Equal(t, "29735", c.Anchor.ID)
	require.Equal(t, map[mediux.Slot]string{poster: "29735"}, from(c))
}

// With no series shape to measure against, a set is as complete as its
// own season posters and titlecards: sharivari's two seasons, twenty
// cards and backdrop anchor over creshaytions' more popular set of one.
func TestWithoutTheSeriesShapeASetsOwnImagesMeasureIt(t *testing.T) {
	c := mediux.ComposeShow(edgerunners(t), mediux.Policy{}, "")
	require.NotNil(t, c)
	require.Equal(t, "41471", c.Anchor.ID)
}

// With the series' shape known, the anchor is the most complete set:
// sharivari's has both season posters, all twenty titlecards and a
// backdrop; Hikari's lacks the backdrop.
func TestTheMostCompleteSetAnchors(t *testing.T) {
	c := mediux.ComposeShow(edgerunners(t), series(), "")
	require.NotNil(t, c)
	require.Equal(t, "41471", c.Anchor.ID)
	require.Len(t, c.Sets, 1, "it fills every slot itself")
	require.Contains(t, c.Reason, "2 of 2 season posters, 20 titlecards")
}

// A pinned set anchors even with no poster of its own: artificial_grey's
// set of season 1 titlecards keeps them, and the poster group follows the
// set that supplies the poster.
func TestAPinnedSetAnchorsAndTheOthersFillItsGaps(t *testing.T) {
	c := mediux.ComposeShow(edgerunners(t), series(), "22523")
	require.NotNil(t, c)
	require.Equal(t, "22523", c.Anchor.ID)
	require.Equal(t, "artificial_grey", c.Anchor.Creator)
	got := from(c)
	for _, s := range cards(1, 10) {
		require.Equal(t, "22523", got[s], "%+v", s)
	}
	for _, s := range []mediux.Slot{poster, backdrop, season1, season2} {
		require.Equal(t, "41471", got[s], "the poster's set supplies the poster group: %+v", s)
	}
	for _, s := range cards(2, 10) {
		require.Equal(t, "41471", got[s], "%+v", s)
	}
	require.Equal(t, []string{"22523", "41471"}, ids(c.Sets))
}

// A specials poster is season 0's, never the show's own.
func TestASpecialsPosterIsASeasonsPoster(t *testing.T) {
	var sets []mediux.ShowSet
	require.NoError(t, json.Unmarshal([]byte(`[{"id":"1","set_title":"A","user_created":{"username":"a"},"date_updated":"2026-01-01T00:00:00Z",
		"show_poster":[{"id":"p","modified_on":"2026-01-01T00:00:00Z","src":"/p?v=1"}],
		"season_posters":[{"id":"s0","modified_on":"2026-01-01T00:00:00Z","src":"/s0?v=1","season":{"season_number":0}}]}]`), &sets))
	c := mediux.ComposeShow(sets, mediux.Policy{}, "")
	require.NotNil(t, c)
	got := map[mediux.Slot]string{}
	for _, p := range c.Picks {
		got[p.Slot] = p.File.ID
	}
	require.Equal(t, map[mediux.Slot]string{poster: "p", {Kind: mediux.SlotSeasonPoster, Season: 0}: "s0"}, got)
}

// A set of titlecards alone still answers: the anchor is the best set
// there is, and the show poster is left to the providers.
func TestSetsWithoutAPosterStillSupplyTheirImages(t *testing.T) {
	var keep []mediux.ShowSet
	for _, s := range edgerunners(t) {
		if s.ID == "22523" || s.ID == "61211" {
			keep = append(keep, s)
		}
	}
	c := mediux.ComposeShow(keep, series(), "")
	require.NotNil(t, c)
	got := from(c)
	_, hasPoster := got[poster]
	require.False(t, hasPoster)
	require.Len(t, c.Picks, 20)
	require.Nil(t, mediux.ComposeShow(nil, series(), ""))
}

// A film's most complete set anchors: sexten's 59100 has the only
// backdrop, so it supplies both images though CmdrRiker's is ranked 1.
func TestAFilmsMostCompleteSetAnchors(t *testing.T) {
	c, _, _ := recorded(t)
	m, err := c.MovieSets(context.Background(), "328387")
	require.NoError(t, err)
	comp := mediux.ComposeMovie(m, mediux.Policy{Languages: []string{"English"}}, "")
	require.NotNil(t, comp)
	require.Equal(t, "59100", comp.Anchor.ID)
	require.Equal(t, mediux.SetMovie, comp.Anchor.Kind)
	require.Equal(t, map[mediux.Slot]string{poster: "59100", backdrop: "59100"}, from(comp))
}

// The collection sets' images of a film join its own sets: with a film
// set of a poster alone, a collection set with the film's poster and
// backdrop is the more complete and anchors; with both equally complete,
// the film's own set wins.
func TestACollectionSetsImagesOfAFilmJoinItsSets(t *testing.T) {
	var m mediux.Movie
	require.NoError(t, json.Unmarshal([]byte(`{"id":"1","title":"F",
		"movie_sets":[{"id":"m1","set_title":"F","user_created":{"username":"a"},"popularity":1,"date_updated":"2026-01-01T00:00:00Z",
			"movie_poster":[{"id":"mp","modified_on":"2026-01-01T00:00:00Z","src":"/mp?v=1"}]}],
		"collectionSets":[{"id":"c1","set_title":"F Collection","user_created":{"username":"b"},"popularity":1,"date_updated":"2026-01-01T00:00:00Z",
			"movie_poster":[{"id":"cp","modified_on":"2026-01-01T00:00:00Z","src":"/cp?v=1"}],
			"movie_backdrop":[{"id":"cb","modified_on":"2026-01-01T00:00:00Z","src":"/cb?v=1"}]}]}`), &m))
	c := mediux.ComposeMovie(&m, mediux.Policy{}, "")
	require.NotNil(t, c)
	require.Equal(t, "c1", c.Anchor.ID)
	require.Equal(t, mediux.SetCollection, c.Anchor.Kind)
	require.Equal(t, map[mediux.Slot]string{poster: "c1", backdrop: "c1"}, from(c))

	m.CollectionSets[0].Backdrop = nil
	c = mediux.ComposeMovie(&m, mediux.Policy{}, "")
	require.Equal(t, "m1", c.Anchor.ID, "a film's own set over a collection's")
}

func ids(sets []mediux.SetUse) []string {
	out := make([]string, len(sets))
	for i, s := range sets {
		out[i] = s.ID
	}
	return out
}

func refOf(c *mediux.Composition, id string) mediux.SetRef {
	for _, s := range c.Sets {
		if s.ID == id {
			return s.SetRef
		}
	}
	return mediux.SetRef{ID: id}
}

// only is the recorded sets with ids, in that order.
func only(t *testing.T, ids ...string) []mediux.ShowSet {
	t.Helper()
	by := map[string]mediux.ShowSet{}
	for _, s := range edgerunners(t) {
		by[s.ID] = s
	}
	var out []mediux.ShowSet
	for _, id := range ids {
		s, ok := by[id]
		require.True(t, ok, id)
		out = append(out, s)
	}
	return out
}

// A pinned set keeps every image it has, though it has no poster: ReaperOf
// Burgers' backdrop and season 1 titlecards stay its own, and only what it
// lacks comes from the others.
func TestAPinnedSetKeepsItsOwnImages(t *testing.T) {
	c := mediux.ComposeShow(edgerunners(t), series(), "11388")
	require.NotNil(t, c)
	require.Equal(t, "11388", c.Anchor.ID)
	require.Contains(t, c.Reason, "pinned")
	got := from(c)
	require.Equal(t, "11388", got[backdrop], "the pin's own backdrop")
	for _, s := range cards(1, 10) {
		require.Equal(t, "11388", got[s], "%+v", s)
	}
	require.Equal(t, "41471", got[poster])

	// A pin of season posters alone keeps them.
	sets := edgerunners(t)
	for i := range sets {
		if sets[i].ID == "61200" {
			sets[i].Poster = nil
		}
	}
	c = mediux.ComposeShow(sets, series(), "61200")
	require.Equal(t, "61200", c.Anchor.ID)
	got = from(c)
	require.Equal(t, "61200", got[season1])
	require.Equal(t, "61200", got[season2])

	// A pin tied with its creator's other set wins the tie.
	twin := only(t, "61211")[0]
	twin.ID = "61212"
	c = mediux.ComposeShow(append(only(t, "61211"), twin), series(), "61212")
	require.Equal(t, "61212", c.Anchor.ID)
	for _, s := range append(cards(1, 10), cards(2, 10)...) {
		require.Equal(t, "61212", from(c)[s], "%+v", s)
	}
}

// Images for seasons and episodes the series does not have are left out,
// and a season's titlecard lead is judged on the episodes it has: a set
// with cards past the season's end does not outrank an exact one.
func TestOnlyTheSeriesSlotsAreFilled(t *testing.T) {
	p := mediux.Policy{Languages: []string{"English"}, Seasons: []int32{1}, Episodes: map[int32]int32{1: 5}}
	c := mediux.ComposeShow(only(t, "41471"), p, "")
	require.NotNil(t, c)
	got := from(c)
	require.Len(t, got, 2+1+5, "poster, backdrop, season 1's poster and its five episodes")
	_, s2 := got[season2]
	require.False(t, s2)

	// 61226 (unranked) and 41471 (popularity 2) both have season 1's ten
	// cards; given ten more past the end, the unranked set would lead on
	// the raw count.
	long := only(t, "61226")[0]
	for _, tc := range long.Titlecards {
		if tc.Season == 1 {
			extra := tc
			extra.Episode += 10
			long.Titlecards = append(long.Titlecards, extra)
		}
	}
	c = mediux.ComposeShow([]mediux.ShowSet{long, only(t, "41471")[0]}, mediux.Policy{Seasons: []int32{1}, Episodes: map[int32]int32{1: 10}}, "")
	for _, s := range cards(1, 10) {
		require.Equal(t, "41471", from(c)[s], "%+v", s)
	}
}
