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

// Titles an episode search on talos could not parse (2026-10-21), each the
// right series: the German and French scene's anime episodes name an "E01"
// with no season, numbered absolutely as the series runs; fansub season
// packs name "Season 1" (or "(S01)", "Season 1-2") in brackets, often beside
// an alternate title after "|" or in parentheses; and an absolute episode
// can follow the series' year. Titles keeps every name the release gives, so
// identity matches the series by any of them.
func TestParseSeriesAnimeTitlesTalosCouldNotParse(t *testing.T) {
	tests := []struct {
		title    string
		titles   []string
		seasons  []int
		absolute []int
		year     int
		group    string
		kind     commonv1.ReleaseType
	}{
		{"Monster.E01.Herr.Dr.Tenma.German.2004.ANiME.DL.FS.1080p.BluRay.x264-STARS-xpost",
			[]string{"Monster"}, nil, []int{1}, 0, "STARS", commonv1.ReleaseTypeSingle},
		{"86.Eighty.Six.E01.Undertaker.German.ANiME.1080P.WebHD.X264-MRW-xpost",
			[]string{"86 Eighty Six"}, nil, []int{1}, 0, "MRW", commonv1.ReleaseTypeSingle},
		{"Kanata.No.Astra.E01.1080p.WEB.x264.1-URANiME-xpost",
			[]string{"Kanata No Astra"}, nil, []int{1}, 0, "URANiME", commonv1.ReleaseTypeSingle},
		{"Medalist.E01.MULTi.1080p.WEB.H264-AMB3R-xpost",
			[]string{"Medalist"}, nil, []int{1}, 0, "AMB3R", commonv1.ReleaseTypeSingle},
		{"[koala] 86: Eighty-Six Season 1 (BD 1080p HEVC Opus) [Dual Audio]",
			[]string{"86: Eighty-Six"}, []int{1}, nil, 0, "koala", commonv1.ReleaseTypeSeasonPack},
		{"[Judas] Apocalypse Hotel (Season 01) [1080p][HEVC x265 10bit][Multi-Subs] (Batch)",
			[]string{"Apocalypse Hotel"}, []int{1}, nil, 0, "Judas", commonv1.ReleaseTypeSeasonPack},
		{"[LostYears] Eighty Six - Season 01 (S01) (WEB 1080p x264 10-bit AAC-EAC3) [Dual-Audio] (86 Part 1 & Part 2)",
			[]string{"Eighty Six"}, []int{1}, nil, 0, "LostYears", commonv1.ReleaseTypeSeasonPack},
		{"[TOPKEK] Astra Lost In Space (Kanata no Astra) - Season 1 - [1080p] [x264 AAC] [Dual Audio]",
			[]string{"Astra Lost In Space (Kanata no Astra)", "Astra Lost In Space", "Kanata no Astra"}, []int{1}, nil, 0, "TOPKEK", commonv1.ReleaseTypeSeasonPack},
		{"[Sylvar] Medalist Season 2 (BD Remux 1080p x264 8-bit FLAC) [Dual Audio] | Medalist 2nd Season",
			[]string{"Medalist"}, []int{2}, nil, 0, "Sylvar", commonv1.ReleaseTypeSeasonPack},
		{"[DB] 86 | 86 Eighty-Six (Season 1-2+Specials) [Dual Audio 10bit BD1080p][HEVC-x265]",
			[]string{"86 | 86 Eighty-Six", "86", "86 Eighty-Six"}, []int{1, 2}, nil, 0, "DB", commonv1.ReleaseTypeSeasonPack},
		{"[Judas] 86 (Eighty Six) (2021) (Season 1 Part 2 + Specials) [1080p][HEVC x265 10bit][Multi-Subs] (Batch)",
			[]string{"86 (Eighty Six)", "86", "Eighty Six"}, []int{1}, nil, 2021, "Judas", commonv1.ReleaseTypeSeasonPack},
		{"[Some-Stuffs] Pocket Monsters (2023) 152 (1080p) [B3545811]",
			[]string{"Pocket Monsters"}, nil, []int{152}, 2023, "Some-Stuffs", commonv1.ReleaseTypeSingle},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			p, err := ParseKind(tt.title, commonv1.MediaKindEpisode)
			require.NoError(t, err)
			assert.Equal(t, tt.titles, p.Titles)
			assert.Equal(t, tt.titles[0], p.Title)
			assert.Equal(t, tt.seasons, p.Seasons)
			assert.Empty(t, p.Episodes)
			assert.Equal(t, tt.absolute, p.Absolute)
			assert.Equal(t, tt.year, p.Year)
			assert.Equal(t, tt.group, p.Group)
			assert.Equal(t, tt.kind, p.ReleaseType)
			assert.Equal(t, len(tt.seasons) > 0, p.FullSeason)
			assert.Equal(t, len(tt.seasons) > 1, p.MultiSeason)
		})
	}
}

// What the new shapes must not take: a fansub episode after a season
// marker stays an episode, a batch naming no season or episode stays
// unparsed, and a standard scene title still parses as before.
func TestParseSeriesAnimeNewShapesLeaveTheRestAlone(t *testing.T) {
	p, err := ParseKind("[SubsPlease] Frieren S2 - 05 (1080p) [ABCD1234].mkv", commonv1.MediaKindEpisode)
	require.NoError(t, err)
	assert.Equal(t, []int{5}, p.Absolute)
	assert.Empty(t, p.Seasons)

	_, err = ParseKind("[Polarwindz] Apocalypse Hotel (BD 1080p HEVC Opus)", commonv1.MediaKindEpisode)
	require.Error(t, err)

	p, err = ParseKind("Monster.2004.S01E01.Herr.Dr.Tenma.1080p.BluRay.DUAL.FLAC.2.0.x264-Kitsune", commonv1.MediaKindEpisode)
	require.NoError(t, err)
	assert.Equal(t, []int{1}, p.Seasons)
	assert.Equal(t, []int{1}, p.Episodes)
}
