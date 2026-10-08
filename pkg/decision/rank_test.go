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

package decision_test

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func TestRankPrimaryKey(t *testing.T) {
	t.Run("lower QualityIndex (better quality) ranks first", func(t *testing.T) {
		worse := decision.Decision{Release: common.ReleaseInfo{GUID: "worse"}, Rank: decision.RankKey{QualityIndex: 1}}
		better := decision.Decision{Release: common.ReleaseInfo{GUID: "better"}, Rank: decision.RankKey{QualityIndex: 0}}
		ranked := decision.Rank([]decision.Decision{worse, better}, decision.Options{})
		require.Equal(t, "better", ranked[0].Release.GUID)
	})

	t.Run("same quality: higher revision (proper/repack) ranks first when PreferRevision", func(t *testing.T) {
		v1 := decision.Decision{Release: common.ReleaseInfo{GUID: "v1"}, Rank: decision.RankKey{PreferRevision: true, Revision: common.Revision{Version: 1}}}
		v2 := decision.Decision{Release: common.ReleaseInfo{GUID: "v2-proper"}, Rank: decision.RankKey{PreferRevision: true, Revision: common.Revision{Version: 2}}}
		ranked := decision.Rank([]decision.Decision{v1, v2}, decision.Options{})
		require.Equal(t, "v2-proper", ranked[0].Release.GUID)
	})

	t.Run("PreferRevision false: revision is not a tiebreak (doNotPrefer)", func(t *testing.T) {
		v1 := decision.Decision{Release: common.ReleaseInfo{GUID: "v1"}, Rank: decision.RankKey{PreferRevision: false, Revision: common.Revision{Version: 1}}}
		v2 := decision.Decision{Release: common.ReleaseInfo{GUID: "v2"}, Rank: decision.RankKey{PreferRevision: false, Revision: common.Revision{Version: 2}}}
		ranked := decision.Rank([]decision.Decision{v2, v1}, decision.Options{}) // stable: original order preserved
		require.Equal(t, "v2", ranked[0].Release.GUID)
	})

	t.Run("same quality and revision: higher custom-format score ranks first", func(t *testing.T) {
		low := decision.Decision{Release: common.ReleaseInfo{GUID: "low"}, Rank: decision.RankKey{FormatScore: 10}}
		high := decision.Decision{Release: common.ReleaseInfo{GUID: "high"}, Rank: decision.RankKey{FormatScore: 50}}
		ranked := decision.Rank([]decision.Decision{low, high}, decision.Options{})
		require.Equal(t, "high", ranked[0].Release.GUID)
	})
}

func TestRankSecondaryKeys(t *testing.T) {
	t.Run("preferred protocol match ranks first at equal quality", func(t *testing.T) {
		torrentPref := decision.Decision{Release: common.ReleaseInfo{GUID: "torrent"}, Rank: decision.RankKey{PreferredProtocolMatch: true}}
		usenetOther := decision.Decision{Release: common.ReleaseInfo{GUID: "usenet"}, Rank: decision.RankKey{PreferredProtocolMatch: false}}
		ranked := decision.Rank([]decision.Decision{usenetOther, torrentPref}, decision.Options{})
		require.Equal(t, "torrent", ranked[0].Release.GUID)
	})

	// Rewritten in the test batch (D2, Wave S S3): Sonarr's
	// CompareEpisodeCount ranks a whole season first, but between two
	// partial releases a standard series prefers FEWER episodes, so an
	// eight-episode release no longer outranks a single on a tie.
	t.Run("a whole season ranks above a single episode; a standard series prefers the single over a partial multi-episode release", func(t *testing.T) {
		single := decision.Decision{Release: common.ReleaseInfo{GUID: "single"}, Rank: decision.RankKey{EpisodeCount: 1}}
		multi := decision.Decision{Release: common.ReleaseInfo{GUID: "multi"}, Rank: decision.RankKey{EpisodeCount: 8}}
		pack := decision.Decision{Release: common.ReleaseInfo{GUID: "pack"}, Rank: decision.RankKey{EpisodeCount: math.MaxInt32}}
		ranked := decision.Rank([]decision.Decision{multi, single, pack}, decision.Options{})
		require.Equal(t, []string{"pack", "single", "multi"}, guids(ranked))
	})

	t.Run("lower indexer-priority number wins -- indexers.md: Priority 1 (highest) .. 50, default 25", func(t *testing.T) {
		a := decision.Decision{Release: common.ReleaseInfo{GUID: "a", IndexerRef: "prio-5"}}
		b := decision.Decision{Release: common.ReleaseInfo{GUID: "b", IndexerRef: "prio-30"}}
		o := decision.Options{IndexerPriority: map[string]int{"prio-5": 5, "prio-30": 30}}
		ranked := decision.Rank([]decision.Decision{b, a}, o)
		require.Equal(t, "a", ranked[0].Release.GUID)
	})

	t.Run("an indexer absent from IndexerPriority defaults to 25", func(t *testing.T) {
		known := decision.Decision{Release: common.ReleaseInfo{GUID: "known", IndexerRef: "prio-10"}}
		unknown := decision.Decision{Release: common.ReleaseInfo{GUID: "unknown", IndexerRef: "no-entry"}}
		o := decision.Options{IndexerPriority: map[string]int{"prio-10": 10}}
		ranked := decision.Rank([]decision.Decision{unknown, known}, o) // 10 < 25
		require.Equal(t, "known", ranked[0].Release.GUID)
	})

	t.Run("freeleech (+2) outranks halfleech (+1) outranks no flags", func(t *testing.T) {
		free := decision.Decision{Release: common.ReleaseInfo{GUID: "free", IndexerFlags: []string{common.IndexerFlagFreeleech}}}
		half := decision.Decision{Release: common.ReleaseInfo{GUID: "half", IndexerFlags: []string{common.IndexerFlagHalfleech}}}
		none := decision.Decision{Release: common.ReleaseInfo{GUID: "none"}}
		ranked := decision.Rank([]decision.Decision{none, half, free}, decision.Options{})
		require.Equal(t, []string{"free", "half", "none"}, []string{ranked[0].Release.GUID, ranked[1].Release.GUID, ranked[2].Release.GUID})
	})

	t.Run("more seeders wins for torrent", func(t *testing.T) {
		many := seeders(200)
		few := seeders(2)
		a := decision.Decision{Release: common.ReleaseInfo{GUID: "many", Protocol: common.ProtocolTorrent, Seeders: &many}}
		b := decision.Decision{Release: common.ReleaseInfo{GUID: "few", Protocol: common.ProtocolTorrent, Seeders: &few}}
		ranked := decision.Rank([]decision.Decision{b, a}, decision.Options{})
		require.Equal(t, "many", ranked[0].Release.GUID)
	})

	t.Run("size: closest to the profile's preferred size wins when the quality has a real preference", func(t *testing.T) {
		closer := decision.Decision{Release: common.ReleaseInfo{GUID: "closer"}, Rank: decision.RankKey{SizeDeltaBucket: 200 * 1024 * 1024}}
		farther := decision.Decision{Release: common.ReleaseInfo{GUID: "farther"}, Rank: decision.RankKey{SizeDeltaBucket: 800 * 1024 * 1024}}
		ranked := decision.Rank([]decision.Decision{farther, closer}, decision.Options{})
		require.Equal(t, "closer", ranked[0].Release.GUID)
	})

	t.Run("size: largest wins when the quality's preferred size is the TRaSH \"biggest\" sentinel", func(t *testing.T) {
		bigger := decision.Decision{Release: common.ReleaseInfo{GUID: "bigger"}, Rank: decision.RankKey{PreferLargestSize: true, SizeBytes: 9_000_000_000}}
		smaller := decision.Decision{Release: common.ReleaseInfo{GUID: "smaller"}, Rank: decision.RankKey{PreferLargestSize: true, SizeBytes: 4_000_000_000}}
		ranked := decision.Rank([]decision.Decision{smaller, bigger}, decision.Options{})
		require.Equal(t, "bigger", ranked[0].Release.GUID)
	})
}

func seeders(n int32) int32 { return n }

func guids(ds []decision.Decision) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Release.GUID)
	}
	return out
}

// TestCompareEpisodeCountIsSonarrs is Sonarr's
// DownloadDecisionComparer.CompareEpisodeCount (series search design §6.4):
// a whole season first; then an anime series prefers more episodes and any
// other series fewer.
func TestCompareEpisodeCountIsSonarrs(t *testing.T) {
	const whole = true
	require.Negative(t, decision.CompareEpisodeCount(whole, 0, !whole, 1, false), "a pack beats a single")
	require.Positive(t, decision.CompareEpisodeCount(!whole, 1, whole, 0, false))
	require.Negative(t, decision.CompareEpisodeCount(whole, 0, !whole, 2, true), "and beats an anime batch short of the season")
	require.Negative(t, decision.CompareEpisodeCount(!whole, 1, !whole, 2, false), "a standard series prefers the single")
	require.Positive(t, decision.CompareEpisodeCount(!whole, 2, !whole, 1, false))
	require.Negative(t, decision.CompareEpisodeCount(!whole, 2, !whole, 1, true), "anime prefers the two-episode release")
	require.Positive(t, decision.CompareEpisodeCount(!whole, 1, !whole, 2, true))
	require.Zero(t, decision.CompareEpisodeCount(!whole, 2, !whole, 2, false))
	require.Zero(t, decision.CompareEpisodeCount(whole, 0, whole, 0, true))
}

// seasonRank evaluates titles as a season search of Breaking Bad's first
// seven episodes through the REAL Evaluate (so the rank keys, the pack
// sentinel included, are Evaluate's own) and returns them ranked.
func seasonRank(t *testing.T, anime bool, titles ...string) []decision.Decision {
	t.Helper()
	tg := seasonTarget([]string{"Breaking Bad"}, 1, seq(1, 7), nil, nil)
	tg.Anime = anime
	rels := make([]common.ReleaseInfo, 0, len(titles))
	for _, title := range titles {
		rels = append(rels, common.ReleaseInfo{GUID: title, IndexerRef: textIndexer, Title: title, Protocol: common.ProtocolTorrent})
	}
	ds := decision.Evaluate(context.Background(), tg, identityProfile(t), &catalogue.Catalogue{}, rels, identityOptions())
	for _, d := range ds {
		require.True(t, d.Approved, "%s: %+v", d.Release.Title, d.Rejections)
	}
	return decision.Rank(ds, identityOptions())
}

// TestRankPutsAPackFirstOnATie is design §6.4 through Evaluate: at the same
// quality a season pack ranks above its singles and multi-episode releases,
// for a standard and an anime series alike, and below it the standard
// series takes the single and the anime one the multi-episode release.
func TestRankPutsAPackFirstOnATie(t *testing.T) {
	const (
		single = "Breaking.Bad.S01E05.720p.HDTV.x264-GRP"
		double = "Breaking.Bad.S01E05E06.720p.HDTV.x264-GRP"
		pack   = "Breaking.Bad.S01.720p.HDTV.x264-GRP"
	)
	require.Equal(t, []string{pack, single, double}, guids(seasonRank(t, false, single, double, pack)))
	require.Equal(t, []string{pack, double, single}, guids(seasonRank(t, true, single, double, pack)))
}

// TestRankKeepsQualityAheadOfPacks: the episode count is a tie-break, never
// a reason to take a worse quality -- a 1080p single outranks a 720p pack.
func TestRankKeepsQualityAheadOfPacks(t *testing.T) {
	const (
		single1080 = "Breaking.Bad.S01E05.1080p.BluRay.x264-GRP"
		pack720    = "Breaking.Bad.S01.720p.HDTV.x264-GRP"
	)
	tg := seasonTarget([]string{"Breaking Bad"}, 1, seq(1, 7), nil, nil)
	var tier1080, tier720 []quality.Definition
	for name, tier := range map[string]*[]quality.Definition{"Bluray-1080p": &tier1080, "HDTV-720p": &tier720} {
		d, ok := quality.Lookup("video", name)
		require.True(t, ok, name)
		*tier = append(*tier, d)
	}
	p := quality.Profile{Tiers: [][]quality.Definition{tier1080, tier720}, LanguageName: "any", ProperPolicy: "preferAndUpgrade"}
	rels := []common.ReleaseInfo{
		{GUID: pack720, IndexerRef: textIndexer, Title: pack720, Protocol: common.ProtocolTorrent},
		{GUID: single1080, IndexerRef: textIndexer, Title: single1080, Protocol: common.ProtocolTorrent},
	}
	ds := decision.Evaluate(context.Background(), tg, p, &catalogue.Catalogue{}, rels, identityOptions())
	for _, d := range ds {
		require.True(t, d.Approved, "%s: %+v", d.Release.Title, d.Rejections)
	}
	require.True(t, ds[0].Rank.WholeSeason(), "the pack carries the whole-season sentinel")
	require.Equal(t, []string{single1080, pack720}, guids(decision.Rank(ds, identityOptions())))
}
