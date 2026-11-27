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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A fansub episode whose title ends in a season marker is one episode, read
// the same however its separators are written and whether or not a group
// stands in front (no caller names a series type, so a name with none goes
// to the standard parser first). The names are talos's of
// 2026-11-27: the files of packs held at import as "a whole season's", and
// one-episode usenet titles a season search took for packs.
func TestAnimeEpisodeAfterASeasonMarkerIsOneEpisode(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		absolute    []int
		titleSeason int
	}{
		{"[SubsPlease] Pop Team Epic S2 - 01v2 (1080p) [112E43D1]", "Pop Team Epic S2", []int{1}, 2},
		{"Kemono Friends S2 - 01 (1920x1080 HEVC2 AAC)", "Kemono Friends S2", []int{1}, 2},
		{"Seirei.Gensouki.S2.-.01", "Seirei Gensouki S2", []int{1}, 2},
		{"_Re-Dive_S2_-_01_V2", "Re-Dive S2", []int{1}, 2},
		{"[SubsPlease].Kingdom.S5-04.1080p.[30564E75]", "Kingdom S5", []int{4}, 5},
		{"[SubsPlease].Kingdom.S5-06.1080p.[914E20BC]", "Kingdom S5", []int{6}, 5},
		{"[SubsPlease] Kingdom S6-13 [1080p] [93D389B5]", "Kingdom S6", []int{13}, 6},
		{"[Moozzi2] Hoozuki no Reitetsu S2-13 END [BD 1920x1080 x.264 FLACx2]", "Hoozuki no Reitetsu S2", []int{13}, 2},
		{"[Toom.Nyard].Lupin.III.S3-46.[BDrip.X265.AAC.720P]", "Lupin III S3", []int{46}, 3},
		{"Lupin III S3 - 13 - Play a Joke on the Variation [neo1024][3B7DFDCB]", "Lupin III S3", []int{13}, 3},
		{"[HorribleSubs].Tsugumomo.S2-05.[1080p]", "Tsugumomo S2", []int{5}, 2},
		{"[SubsPlease] Show S02 - 05 (1080p) [ABCD1234]", "Show S02", []int{5}, 2},
		// The marker in words was read as an episode already; it now says
		// which season as well.
		{"[Erai-raws] Kingdom 3rd Season - 26 END [1080p][Multiple Subtitle][3DC8CB0C]", "Kingdom 3rd Season", []int{26}, 3},
		{"[Erai-raws].Shiguang.Dailiren.Season.3-05.[CA].[1080p.CR.WEBRip.HEVC.AAC]", "Shiguang.Dailiren.Season.3", []int{5}, 3},
		{"[SubsPlease] Frieren - 28 (1080p) [ABCD1234]", "Frieren", []int{28}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseKind(tt.name, commonv1.MediaKindEpisode)
			require.NoError(t, err)
			assert.Equal(t, tt.title, p.Title)
			assert.Equal(t, tt.absolute, p.Absolute)
			assert.Equal(t, tt.titleSeason, p.TitleSeason)
			assert.Empty(t, p.Seasons, "one episode, not a season or a range of seasons")
			assert.Empty(t, p.Episodes)
			assert.False(t, p.FullSeason)
			assert.False(t, p.MultiSeason)
			assert.Equal(t, commonv1.ReleaseTypeSingle, p.ReleaseType)
		})
	}
}

// What the shape must not take: a range of seasons, a pack named for its
// season, and a season and episode written out.
func TestAnimeSeasonMarkerShapesThatAreNotOneAbsoluteEpisode(t *testing.T) {
	// No caller names a series type: every name goes through the default
	// order, the standard parser ahead of the anime one.
	anime := Options{Kind: commonv1.MediaKindEpisode}

	p, err := Parse("[Anime Time] Tsugumomo (Uncensored) S01-02 + OVA [BD][Dual Audio][1080p][HEVC 10bit x265][AAC][Eng Sub]", anime)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, p.Seasons)
	assert.True(t, p.FullSeason)
	assert.True(t, p.MultiSeason)
	assert.Empty(t, p.Absolute)
	assert.Zero(t, p.TitleSeason)

	p, err = Parse("Kemono Friends S2 01-12 (1920x1080 HEVC2 AAC) (Batch)", anime)
	require.NoError(t, err)
	assert.Empty(t, p.Episodes)
	assert.NotEqual(t, []int{1}, p.Absolute, "a batch's range is not its first episode")

	p, err = Parse("[SubsPlease] Pop Team Epic S2 (01-12) (1080p) [Batch]", anime)
	require.NoError(t, err)
	assert.NotEqual(t, commonv1.ReleaseTypeSingle, p.ReleaseType)

	p, err = Parse("Kingdom.S05-E01.1080p.WEB.H264-GRP", anime)
	require.NoError(t, err)
	assert.Equal(t, "Kingdom", p.Title)
	assert.Equal(t, []int{5}, p.Seasons)
	assert.Equal(t, []int{1}, p.Episodes)
	assert.Empty(t, p.Absolute)
	assert.Zero(t, p.TitleSeason)

	p, err = Parse("[Judas] Honzuki no Gekokujou - S04E01v2", anime)
	require.NoError(t, err)
	assert.Equal(t, []int{4}, p.Seasons)
	assert.Equal(t, []int{1}, p.Episodes)
	assert.Zero(t, p.TitleSeason)

	p, err = Parse("[JRx7] Full-Time Magister S6 Special - 01 (1080p HEVC SDR) [5C7D879C]", anime)
	require.NoError(t, err)
	assert.Empty(t, p.Absolute, "a season's special is not that season's episode")
}
