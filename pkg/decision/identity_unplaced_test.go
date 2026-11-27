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

package decision

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// jojo is JoJo's Bizarre Adventure (TVDB 262954) as talos held it on
// 2026-11-27: the series' own titles from its metadata, TheXEM's names for
// its seasons, and TheXEM's table, in which the scene counts each of the
// six seasons from 1 (26, 48, 39, 39, 38 and 12 episodes), as TVDB does.
func jojo() Identity {
	id := Identity{
		Year: 2012,
		Titles: []string{
			"JoJo's Bizarre Adventure (2012)", "JoJo no Kimyou na Bouken", "JoJo's Bizarre Adventure",
			"JoJo's Bizarre Adventure: Stardust Crusaders", "JoJo's Bizarre Adventure: Diamond is Unbreakable",
			"JoJo's Bizarre Adventure: Golden Wind", "JoJo no Kimyou na Bouken: Phantom Blood",
			"JoJo no Kimyou na Bouken: Sentou Chouryuu", "JoJo no Kimyou na Bouken: Stardust Crusaders",
			"JoJo no Kimyou na Bouken: Diamond wa Kudakenai", "JoJo no Kimyou na Bouken: Ougon no Kaze",
			"JoJo no Kimyou na Bouken - Steel Ball Run", "JoJo no Kimyou na Bouken (2012)",
			"JoJo's Bizarre Adventure: Phantom Blood", "JoJo's Bizarre Adventure: Battle Tendency",
			"JoJo's Bizarre Adventure: Stone Ocean", "JoJo's Bizarre Adventure: Steel Ball Run", "JJBA",
		},
		SceneSeasonNames: []SceneSeasonName{
			{Title: "JoJo's Bizarre Adventure - Stardust Crusaders", Season: 2},
			{Title: "JoJo's Bizarre Adventure", Season: 1},
			{Title: "JoJo's Bizarre Adventure - Golden Wind", Season: 4},
			{Title: "JoJo Bizarre Adventure - Stardust Crusaders", Season: 2},
			{Title: "JoJo Bizarre Adventure", Season: 1},
			{Title: "JoJo's Bizarre Adventure - Stone Ocean", Season: 5},
			{Title: "JoJo no Kimyou na Bouken: Stone Ocean", Season: 5},
			{Title: "JoJo no Kimyou na Bouken", Season: 1},
			{Title: "JoJo no Kimyou na Bouken: Stardust Crusaders", Season: 2},
			{Title: "JoJo no Kimyou na Bouken: Diamond wa Kudakenai", Season: 3},
			{Title: "JoJo no Kimyou na Bouken: Ougon no Kaze", Season: 4},
			{Title: "JoJo's Bizarre Adventure - Diamond is Unbreakable", Season: 3},
			{Title: "JoJo's Bizarre Adventure Part 6: Stone Ocean", Season: 5},
			{Title: "Le Bizzarre Avventure di JoJo", Season: 0},
			{Title: "JoJo no Kimyou na Bouken: Steel Ball Run", Season: 6},
		},
	}
	for s, n := range []int{26, 48, 39, 39, 38, 12} {
		for e := 1; e <= n; e++ {
			at := EpisodeNumbering{Season: s + 1, Episode: e, Absolute: e}
			id.SceneMappings = append(id.SceneMappings, SceneMapping{Scene: at, TVDB: at})
		}
	}
	return id
}

// A bare number counts in the season its release's title names, and under a
// title that is neither the series' own nor one of its seasons' names it is
// placed nowhere: two releases of later seasons, each under a title TheXEM
// does not list, were approved for S01E02 and one was taken for it (owner,
// 2026-11-27: "that's season 5 --- definitely should not be an approved
// S01E02 release!", and "Ensure that only the correct season is selected").
func TestABareNumberUnderATitleTheSeriesDoesNotKnowIsPlacedNowhere(t *testing.T) {
	id := jojo()
	for _, tc := range []struct {
		title    string
		want     []SeasonEpisode
		unplaced bool
	}{
		// The two the owner found among S01E02's approved results.
		{title: "[Erai-raws] JoJo no Kimyou na Bouken - Stone Ocean Part 2 - 01 ~ 12 [1080p][Multiple Subtitle] [ENG][PT-BR][SPA-LA][SPA][ARA][FRE][GER][ITA][RUS][JPN][POR][POL][DUT][NOB][FIN][TUR][SWE][GRE][HEB][RUM]", unplaced: true},
		{title: "[KaiDubs] JoJo's Bizarre Adventure: Golden Wind (Part 5) - 02 [1080p] [English Dub] [CC] [AS-DL] [401D6E42]", unplaced: true},
		// The series' own titles are its first season.
		{title: "[Judas] JoJo no Kimyou na Bouken - 02 [BD 1080p][HEVC x265 10bit][Dual-Audio]", want: []SeasonEpisode{{1, 2}}},
		{title: "[Group] JoJo's Bizarre Adventure - 02 [1080p]", want: []SeasonEpisode{{1, 2}}},
		{title: "[Group] JoJo no Kimyou na Bouken - Phantom Blood - 02 [1080p]", want: []SeasonEpisode{{1, 2}}},
		{title: "[Group] JoJo's Bizarre Adventure (2012) - 02 [1080p]", want: []SeasonEpisode{{1, 2}}},
		// A name the scene uses for every season is the series' own too.
		{title: "[Group] Le Bizzarre Avventure di JoJo - 02 [1080p]", want: []SeasonEpisode{{1, 2}}},
		// A season's scene name is that season.
		{title: "[KaiDubs] JoJo's Bizarre Adventure - Golden Wind - 24 [1080p] [English Dub]", want: []SeasonEpisode{{4, 24}}},
		{title: "[Erai-raws] JoJo no Kimyou na Bouken - Stone Ocean - 02 [1080p]", want: []SeasonEpisode{{5, 2}}},
		{title: "[Moozzi2] JoJo no Kimyou na Bouken Stone Ocean-34 [BD 1920x1080 x265-10Bit Flac]", want: []SeasonEpisode{{5, 34}}},
		// A season marker says its own season, whatever the title before it.
		{title: "JoJos.Bizarre.Adventure.S01E02.A.Letter.from.the.Past.1080p.BluRay.10bit.x265-GRP", want: []SeasonEpisode{{1, 2}}},
	} {
		t.Run(tc.title, func(t *testing.T) {
			p, err := release.Parse(tc.title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err)
			cov := CoverageFor(p, id)
			assert.Equal(t, tc.unplaced, cov.Unplaced, "title read as %q", p.Title)
			assert.Equal(t, tc.want, cov.Episodes, "title read as %q", p.Title)
			assert.Empty(t, cov.Absolute, "no number is left to be read as the series' own count")
		})
	}
}

// The check itself: S01E02 refuses both releases, from an indexer asked by
// the series' id (no title check stands in their way), and still takes its
// own.
func TestAnEpisodeRefusesAReleaseOfAnotherPartOfItsSeries(t *testing.T) {
	id := jojo()
	id.IDs = map[string]string{common.IDKeyTVDB: "262954"}
	id.Season, id.Episodes, id.Absolute = 1, []int{2}, []int{2}
	id.IDQueryIndexers = map[string]bool{"amenzb": true}
	target := Target{Kind: common.MediaKindEpisode, Identity: id, Anime: true}
	idx := newIdentityIndex(common.MediaKindEpisode, id)
	check := func(title string) *common.Rejection {
		p, err := release.Parse(title, release.Options{Kind: common.MediaKindEpisode})
		require.NoError(t, err)
		return identityRejection(target, idx, p, common.ReleaseInfo{Title: title, IndexerRef: "amenzb"})
	}
	for _, title := range []string{
		"[Erai-raws] JoJo no Kimyou na Bouken - Stone Ocean Part 2 - 01 ~ 12 [1080p][Multiple Subtitle] [ENG][PT-BR]",
		"[KaiDubs] JoJo's Bizarre Adventure: Golden Wind (Part 5) - 02 [1080p] [English Dub] [CC] [AS-DL] [401D6E42]",
	} {
		r := check(title)
		require.NotNil(t, r, title)
		assert.True(t, strings.HasPrefix(r.Reason, ReasonUnknownItem.Code+":"), r.Reason)
		assert.Contains(t, r.Reason, "neither the series' own nor one of its seasons' scene names", title)
	}
	// A later season's release under that season's name is the wrong item.
	r := check("[KaiDubs] JoJo's Bizarre Adventure - Golden Wind - 02 [1080p] [English Dub]")
	require.NotNil(t, r)
	assert.True(t, strings.HasPrefix(r.Reason, ReasonWrongItem.Code+":"), r.Reason)
	// Its own.
	assert.Nil(t, check("[Judas] JoJo no Kimyou na Bouken - 02 [BD 1080p][HEVC x265 10bit][Dual-Audio]"))
	assert.Nil(t, check("JoJos.Bizarre.Adventure.S01E02.A.Letter.from.the.Past.1080p.BluRay.10bit.x265-GRP"))
}

// A series with no scene names for its seasons, and an identity with no
// titles, read a bare number as before.
func TestABareNumberIsReadAsBeforeWithoutSeasonNames(t *testing.T) {
	p, err := release.Parse("[Group] Some Show Part 2 - 03 [1080p]", release.Options{Kind: common.MediaKindEpisode})
	require.NoError(t, err)

	plain := Identity{Titles: []string{"Some Show"}}
	cov := CoverageFor(p, plain)
	assert.False(t, cov.Unplaced)
	assert.Equal(t, []int{3}, cov.Absolute)

	untitled := jojo()
	untitled.Titles = nil
	q, err := release.Parse("[Erai-raws] JoJo no Kimyou na Bouken - Stone Ocean Part 2 - 02 [1080p]", release.Options{Kind: common.MediaKindEpisode})
	require.NoError(t, err)
	cov = CoverageFor(q, untitled)
	assert.False(t, cov.Unplaced, "an identity that carries no titles cannot say a title is not the series'")
}
