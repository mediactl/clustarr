/*
Copyright (C) 2026 The Clustarr Authors

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

package grabplan_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// lastResort is v under a profile whose 720p tier is taken only as a last
// resort (quality.Profile.LastResort): hdProfile with its last tier marked.
func lastResort(v grabplan.View) grabplan.View {
	prof := *v.Profile
	prof.LastResortFrom = len(prof.Tiers) - 1
	v.Profile = &prof
	return v
}

// The owner, 2026-11-26: "The only time a 720p release should be allowed is
// if there are literally no other releases that exist. But in this scenario,
// season packs, episode packs, etc should take precedence and block an
// episode release if one is found that is 1080p".
func TestALastResortReleaseIsGrabbedOnlyWhenNothingBetterCoversItsItems(t *testing.T) {
	t.Run("an RSS match alone says nothing of what else exists", func(t *testing.T) {
		match := parked(bbRef, "rss-1", seasonCandidates(t, 1, 7, single1)[0], false, false)
		p := grabplan.Decide(lastResort(seriesView(t, season(1, 7), match)))
		require.Empty(t, p.NewEntries)
		require.Equal(t, intake.Refused, p.Settle["rss-1"])

		p = grabplan.Decide(seriesView(t, season(1, 7), match))
		require.Equal(t, []string{single1}, titles(p), "without the mark the match is grabbed")
	})
	t.Run("a search that finds nothing better takes it", func(t *testing.T) {
		p := grabplan.Decide(lastResort(seriesView(t, season(1, 7), seasonCandidates(t, 1, 7, single1)...)))
		require.Equal(t, []string{single1}, titles(p))
	})
	t.Run("a person's pick is not held to the rule", func(t *testing.T) {
		pick := parked(bbRef, "pick-1", seasonCandidates(t, 1, 7, single1)[0], true, false)
		p := grabplan.Decide(lastResort(seriesView(t, season(1, 7), pick)))
		require.Equal(t, []string{single1}, titles(p))
	})
	t.Run("a 1080p pack covering the episode blocks the 720p single", func(t *testing.T) {
		cands := seasonCandidates(t, 1, 7, single1, single2, pack1080)
		p := grabplan.Decide(lastResort(seriesView(t, season(1, 7), cands...)))
		require.Equal(t, []string{pack1080}, titles(p))
	})
	t.Run("and does while it waits on its indexer's grab limit", func(t *testing.T) {
		// The pack's indexer is at its grab limit, so the pass holds it for
		// later; the 720p single, from an indexer with room, was grabbed in
		// its place.
		target := seasonSearch(1, 7, nil)
		limitedPack := rel(pack1080)
		limitedPack.IndexerRef = "limited"
		rec := record(t, target, rel(single1), limitedPack)
		cands := grabplan.FromSeasonRecord(rec, 1, commonv1.GrabSourceSearch)
		require.Len(t, cands, 2)
		at := now.Add(time.Hour)

		v := lastResort(seriesView(t, season(1, 7), cands...))
		v.GrabLimited = map[string]time.Time{"limited": at}
		p := grabplan.Decide(v)
		require.Empty(t, p.NewEntries, "the single is not grabbed in the pack's place")
		require.Len(t, p.Pending, 1)
		require.Equal(t, pack1080, p.Pending[0].ReleaseTitle)

		v = seriesView(t, season(1, 7), cands...)
		v.GrabLimited = map[string]time.Time{"limited": at}
		require.Equal(t, []string{single1}, titles(grabplan.Decide(v)), "without the mark the single is taken meanwhile")
	})
	t.Run("a 1080p single blocks a 720p pack covering its episode", func(t *testing.T) {
		cands := seasonCandidates(t, 1, 7, pack720, bdSingle1)
		p := grabplan.Decide(lastResort(seriesView(t, season(1, 7), cands...)))
		require.Equal(t, []string{bdSingle1}, titles(p))
	})
}

// A transcode of a last-resort tier is not final: a grab of a better tier
// in flight is still wanted over it.
func TestStillWantsABetterTierOverATranscodedLastResortFile(t *testing.T) {
	claim := schema.TransferClaim{
		Owner: heatRef, Entry: schema.EntryRef{ID: "heat-grab", UID: "uid-grab"},
		InfoHash: "0123456789abcdef0123456789abcdef01234567", Indexer: indexer, GUID: "g1",
		Title: "Heat.1995.1080p.BluRay.x264-GRP",
	}
	cur := &decision.Current{Quality: q(t, "HDTV-720p"), Transcoded: true}
	ok, _ := grabplan.StillWants(lastResort(movieView(t, cur)), claim)
	require.True(t, ok)
	ok, _ = grabplan.StillWants(movieView(t, cur), claim)
	require.False(t, ok, "without the mark a transcoded file is final")
}
