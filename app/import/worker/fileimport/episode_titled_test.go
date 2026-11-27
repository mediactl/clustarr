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

package fileimport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/release"
)

// A pack's file named "<the series' title> <number>" is that episode of the
// pack's season. The release and its files are the ones talos downloaded
// whole and held for a person on 2026-11-27, eleven episodes of Wandering
// Witch: The Journey of Elaina missing meanwhile: no release pattern reads
// a number that only follows a title, and the title here is known.
func TestAPacksFileNamedByTheSeriesTitleAndANumberIsThatEpisode(t *testing.T) {
	const pack = "[anime4life.] Wandering Witch The Journey of Elaina Season 1 BD 1080p Dolby TrueHD Dual Audio"
	p, err := release.Parse(pack, release.Options{Kind: commonv1.MediaKindEpisode})
	require.NoError(t, err)
	require.True(t, p.FullSeason, "the release is a season pack")
	require.Equal(t, []int{1}, p.Seasons)
	packSeason := p.Seasons[0]

	series := &catalogv1alpha1.Series{}
	series.Status.Metadata = &catalogv1alpha1.SeriesMetadata{
		Title: "Wandering Witch: The Journey of Elaina",
		AlternateTitles: []catalogv1alpha1.AltTitle{
			{Title: "魔女の旅々"}, {Title: "Majo no Tabitabi"}, {Title: "The Witch's Journey"},
		},
	}
	titles := seriesTitles(series)
	cands := twoSeasons()
	dir := "/data/usenet/complete/tv/show-0000000000/" + pack + "/"

	for file, want := range map[string]string{
		"Wandering Witch The Journey of Elaina 01.mkv":         "show-s01e01",
		"Wandering Witch The Journey of Elaina 12.mkv":         "show-s01e12",
		"[anime4life] Majo no Tabitabi 07v2 [BD 1080p].mkv":    "show-s01e07",
		"Wandering.Witch.The.Journey.of.Elaina.03.mkv":         "show-s01e03",
		"Some Other Show 01.mkv":                               "",
		"Wandering Witch The Journey of Elaina Creditless.mkv": "",
		"Wandering Witch The Journey of Elaina 2 01.mkv":       "", // another title: a sequel's
	} {
		t.Run(file, func(t *testing.T) {
			_, perr := parseMediaFile(dir+file, "", commonv1.MediaKindEpisode)
			if want != "" && file == "Wandering Witch The Journey of Elaina 01.mkv" {
				require.Error(t, perr, "no release pattern reads the name: the case this is for")
			}
			got := titledEpisode(dir+file, titles, packSeason)
			if want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			eps, reason := MatchDownloaded(got, catalogv1alpha1.SeriesTypeAnime, cands, decision.Identity{})
			require.Empty(t, reason)
			assert.Equal(t, []string{want}, names(eps))
		})
	}

	assert.Nil(t, titledEpisode(dir+"Wandering Witch The Journey of Elaina 01.mkv", titles, 0),
		"a download that is not one season's pack names no season for it")
	assert.Nil(t, titledEpisode(dir+"Wandering Witch The Journey of Elaina 01.mkv", nil, packSeason))
}

// A pack's file named "<the series' title> <number> <the episode's title>"
// is that episode too. The release and its file names are the pack of Gosick
// talos downloaded whole from usenet and held for a person on 2026-11-27,
// nineteen episodes missing meanwhile.
func TestAPacksFileNamedByTheSeriesTitleANumberAndTheEpisodesTitleIsThatEpisode(t *testing.T) {
	const pack = "Gosick  [Dual Audio] [HEVC] [10-bit] [BDRip] [HD 1080p] [bluury]"
	series := &catalogv1alpha1.Series{}
	series.Status.Metadata = &catalogv1alpha1.SeriesMetadata{
		Title:           "Gosick",
		AlternateTitles: []catalogv1alpha1.AltTitle{{Title: "GOSICK -ゴシック-"}, {Title: "Mob Psycho 100"}},
	}
	titles := seriesTitles(series)
	cands := twoSeasons()
	dir := "/data/usenet/complete/tv/show-0000000000/" + pack + "/"

	for file, want := range map[string]string{
		"Gosick 01 The Dark Reaper Finds the Golden Fairy.mkv":           "show-s01e01",
		"Gosick 02 The Souls of the Dead Raise a Shipwreck.mkv":          "show-s01e02",
		"Gosick 03 The Hares Break Promises Beneath the Morning Sun.mkv": "show-s01e03",
		"Gosick.12.A.Summer.Afternoon.mkv":                               "show-s01e12",
		"[bluury] Gosick 07 A Divine Revelation [1080p].mkv":             "show-s01e07",
		// A title that ends in a number: the episode is the number after it.
		"Mob Psycho 100 05 Ochimusha.mkv": "show-s01e05",
		"Mob Psycho 100.mkv":              "",
		// Another title: a sequel's, and its own episode.
		"Gosick 2 01.mkv":                 "",
		"Gosick 2 01 A New Beginning.mkv": "",
		"Gosick Movie 01 Something.mkv":   "",
		"The Dark Reaper 01 Gosick.mkv":   "",
		"Gosick Creditless Opening.mkv":   "",
	} {
		t.Run(file, func(t *testing.T) {
			if want == "show-s01e01" {
				_, perr := parseMediaFile(dir+file, "", commonv1.MediaKindEpisode)
				require.Error(t, perr, "no release pattern reads the name: the case this is for")
			}
			got := titledEpisode(dir+file, titles, 1)
			if want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			eps, reason := MatchDownloaded(got, catalogv1alpha1.SeriesTypeAnime, cands, decision.Identity{})
			require.Empty(t, reason)
			assert.Equal(t, []string{want}, names(eps))
		})
	}
}
