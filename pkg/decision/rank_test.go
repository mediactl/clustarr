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
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
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

	t.Run("higher episode count (season pack) ranks above a single episode", func(t *testing.T) {
		single := decision.Decision{Release: common.ReleaseInfo{GUID: "single"}, Rank: decision.RankKey{EpisodeCount: 1}}
		pack := decision.Decision{Release: common.ReleaseInfo{GUID: "pack"}, Rank: decision.RankKey{EpisodeCount: 8}}
		ranked := decision.Rank([]decision.Decision{single, pack}, decision.Options{})
		require.Equal(t, "pack", ranked[0].Release.GUID)
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
