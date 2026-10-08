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
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// seasonTarget is a season search as the search agent builds it
// (app/catalog/worker/search's season snapshot): the Series, the season's
// episodes, SeasonSearch set at the call site.
func seasonTarget(titles []string, season int, episodes, absolute []int, scene []decision.SceneMapping) decision.Target {
	return decision.Target{Kind: common.MediaKindSeries, Available: true, Identity: decision.Identity{
		Titles: titles, Season: season, Episodes: episodes, Absolute: absolute,
		SceneMappings: scene, SeasonSearch: true,
	}}
}

func seq(from, to int) []int {
	out := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

// TestSeasonSearchAcceptsAnySingleOfTheSeason is series search design §6.1:
// a season search takes anything of the season -- a single, a
// multi-episode release, a pack, a multi-season pack naming it -- and still
// refuses another season.
func TestSeasonSearchAcceptsAnySingleOfTheSeason(t *testing.T) {
	tg := seasonTarget([]string{"Breaking Bad"}, 1, seq(1, 7), nil, nil)
	cases := []struct {
		title, want, detail string
	}{
		{title: "Breaking.Bad.S01E05.720p.HDTV.x264-GRP"},
		{title: "Breaking.Bad.S01E05E06.720p.HDTV.x264-GRP"},
		{title: "Breaking.Bad.S01.720p.BluRay.x264-GRP"},
		{title: "Breaking.Bad.S01-S03.720p.BluRay.x264-GRP"},
		{title: "Breaking.Bad.S02E01.720p.HDTV.x264-GRP", want: "WrongItem", detail: "which covers no episode of the season 1"},
		{title: "Breaking.Bad.S02.720p.BluRay.x264-GRP", want: "WrongItem", detail: "which covers no episode of the season 1"},
		{title: "Breaking.Bad.S01E09.720p.HDTV.x264-GRP", want: "WrongItem", detail: "which covers no episode of the season 1"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			d := evaluateOne(t, tg, tc.title, textIndexer, nil)
			assertIdentity(t, d, tc.want, tc.detail)
			if tc.want == "" {
				require.True(t, d.Approved, "%+v", d.Rejections)
				require.Empty(t, d.Rejections, "a season search never refuses a pack as FullSeason")
			}
		})
	}
}

// TestSeasonSearchReadsSceneNumbers is design §6.1 over a TheXEM table:
// kaijuSceneMap files scene season 2 as TVDB S01E14-26, so a scene single
// of scene season 2 is an episode of TVDB season 1, and an absolute batch
// overlapping season 1's absolute numbers is of the season too.
func TestSeasonSearchReadsSceneNumbers(t *testing.T) {
	scene := kaijuSceneMap()
	season1 := seasonTarget([]string{"Kaiju Show"}, 1, seq(1, 26), seq(1, 26), scene)
	season2 := seasonTarget([]string{"Kaiju Show"}, 2, seq(1, 11), seq(27, 37), scene)

	cases := []struct {
		name   string
		target decision.Target
		title  string
		want   string
		detail string
	}{
		{name: "a scene S02 single is TVDB season 1", target: season1, title: "Kaiju.Show.S02E05.1080p.WEB.x264-GRP"},
		{name: "a scene S02 pack is TVDB season 1", target: season1, title: "Kaiju.Show.S02.1080p.BluRay.x264-GRP"},
		{name: "a scene S03 single is TVDB season 2", target: season2, title: "Kaiju.Show.S03E05.1080p.WEB.x264-GRP"},
		{
			name: "a scene S02 single is not of TVDB season 2", target: season2, title: "Kaiju.Show.S02E05.1080p.WEB.x264-GRP",
			want: "WrongItem", detail: "(scene numbering; TVDB S01E18), which covers no episode of the season 2",
		},
		{name: "an absolute batch overlapping the season", target: season1, title: "[SubsPlease] Kaiju Show - 20-30 (1080p) [ABCDEF12].mkv"},
		{name: "a scene absolute single of the season", target: season2, title: "[SubsPlease] Kaiju Show - 30 (1080p) [ABCDEF12].mkv"},
		{
			name: "an absolute single of the other season", target: season2, title: "[SubsPlease] Kaiju Show - 12 (1080p) [ABCDEF12].mkv",
			want: "WrongItem", detail: "which covers no episode of the season 2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertIdentity(t, evaluateOne(t, tc.target, tc.title, textIndexer, nil), tc.want, tc.detail)
		})
	}
}

// TestSingleEpisodeSearchIsUnchanged is the R-3 corpus with SeasonSearch
// false: a single-episode search still covers-all, still refuses a pack as
// FullSeason, and still refuses a sibling episode the season mode accepts.
func TestSingleEpisodeSearchIsUnchanged(t *testing.T) {
	episode := func(seasonSearch bool) decision.Target {
		tg := decision.Target{Kind: common.MediaKindEpisode, Available: true, Identity: decision.Identity{
			Titles: []string{"Breaking Bad"}, IDs: map[string]string{common.IDKeyTVDB: "81189"},
			Season: 1, Episodes: []int{5}, SingleEpisodeSearch: true,
		}}
		if seasonSearch {
			// The same series searched as its season (S01E01-07).
			tg.Kind = common.MediaKindSeries
			tg.Identity.Episodes = seq(1, 7)
			tg.Identity.SingleEpisodeSearch, tg.Identity.SeasonSearch = false, true
		}
		return tg
	}
	codes := func(d decision.Decision) []string {
		var out []string
		for _, r := range d.Rejections {
			out = append(out, r.Reason[:strings.Index(r.Reason, ":")])
		}
		return out
	}
	cases := []struct {
		title  string
		single []string // the single-episode search's rejection codes
		season []string // the same release in a season search
	}{
		{title: "Breaking.Bad.S01E05.720p.HDTV.x264-GRP"},
		{title: "Breaking.Bad.S01E05E06.720p.HDTV.x264-GRP"},
		{title: "Breaking.Bad.S01E06.720p.HDTV.x264-GRP", single: []string{"WrongItem"}},
		{title: "Breaking.Bad.S01.720p.BluRay.x264-GRP", single: []string{"FullSeason"}},
		{title: "Breaking.Bad.S01-S03.720p.BluRay.x264-GRP", single: []string{"FullSeason"}},
		{title: "Breaking.Bad.S02.720p.BluRay.x264-GRP", single: []string{"WrongItem"}, season: []string{"WrongItem"}},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			require.Equal(t, tc.single, codes(evaluateOne(t, episode(false), tc.title, textIndexer, nil)), "single-episode search")
			require.Equal(t, tc.season, codes(evaluateOne(t, episode(true), tc.title, textIndexer, nil)), "season search")
		})
	}
}

// TestCoverageOfMatchesTheNumberingHalf holds CoverageOf to the numbering
// half: over the identity corpus, with and without a scene table, a
// single-episode target is accepted exactly when the release's exported
// coverage names its episode or a pack of its season. CoverageOf is what
// the search agent records and the grab planner judges packs by; if it
// drifted from numberingRejection the planner would grab what the agent
// would have refused.
func TestCoverageOfMatchesTheNumberingHalf(t *testing.T) {
	corpus := []struct {
		titles []string
		title  string
	}{
		{[]string{"Breaking Bad"}, "Breaking.Bad.S01E05.720p.HDTV.x264-GRP"},
		{[]string{"Breaking Bad"}, "Breaking.Bad.S01E05E06.720p.HDTV.x264-GRP"},
		{[]string{"Breaking Bad"}, "Breaking.Bad.S01.720p.BluRay.x264-GRP"},
		{[]string{"Breaking Bad"}, "Breaking.Bad.S01-S03.720p.BluRay.x264-GRP"},
		{[]string{"Breaking Bad"}, "Breaking.Bad.S02E05.720p.HDTV.x264-GRP"},
		{[]string{"The Wire"}, "The.Wire.S01E01E02.1080p.BluRay.x264-GRP"},
		{[]string{"Kaiju Show"}, "Kaiju.Show.S02E05.1080p.WEB.x264-GRP"},
		{[]string{"Kaiju Show"}, "Kaiju.Show.S03E05.1080p.WEB.x264-GRP"},
		{[]string{"Kaiju Show"}, "Kaiju.Show.S02.1080p.BluRay.x264-GRP"},
		{[]string{"Kaiju Show"}, "[SubsPlease] Kaiju Show - 30 (1080p) [ABCDEF12].mkv"},
		{[]string{"Kaiju Show"}, "[SubsPlease] Kaiju Show - 37 (1080p) [ABCDEF12].mkv"},
	}
	for _, scene := range [][]decision.SceneMapping{nil, kaijuSceneMap()} {
		for _, c := range corpus {
			parsed, err := release.Parse(c.title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err, c.title)
			cov := decision.CoverageOf(parsed, scene)
			named := func(season, ep int) bool {
				return slices.Contains(cov.FullSeasons, season) ||
					slices.Contains(cov.Episodes, decision.SeasonEpisode{Season: season, Episode: ep})
			}
			for season := 1; season <= 3; season++ {
				for ep := 1; ep <= 26; ep++ {
					tg := decision.Target{Kind: common.MediaKindEpisode, Available: true, Identity: decision.Identity{
						Titles: c.titles, Season: season, Episodes: []int{ep}, SceneMappings: scene,
					}}
					d := evaluateOne(t, tg, c.title, textIndexer, nil)
					accepted := len(identityVerdicts(d)) == 0
					require.Equal(t, named(season, ep), accepted,
						"%s (scene table %t) for S%02dE%02d: coverage %+v, identity %v",
						c.title, scene != nil, season, ep, cov, identityVerdicts(d))
					if accepted {
						require.NotNil(t, d.Coverage)
						require.Equal(t, cov, *d.Coverage, "Evaluate's Decision.Coverage is CoverageOf through the same table")
					}
				}
			}
		}
	}
}

// TestAYearlessNamesakeIsUnknownItem is design §9: a release matched to the
// item only through a title another library item also carries, naming no
// year, cannot say which item it is. Monster (2004) shares its title with a
// newer Monster in the library; Pocket Monsters is not a namesake at all.
func TestAYearlessNamesakeIsUnknownItem(t *testing.T) {
	monster := func() decision.Target {
		return decision.Target{Kind: common.MediaKindEpisode, Available: true, Identity: decision.Identity{
			Titles: []string{"Monster"}, Year: 2004, Season: 1, Episodes: []int{1},
			Namesakes:       []string{"Monster"},
			IDQueryIndexers: map[string]bool{idIndexer: true},
		}}
	}
	t.Run("a yearless title-only match is UnknownItem", func(t *testing.T) {
		assertIdentity(t, evaluateOne(t, monster(), "Monster.S01E01.1080p.WEB.x264-GRP", textIndexer, nil),
			"UnknownItem", `the library holds another series titled "Monster"`)
	})
	t.Run("the title with the series' year passes", func(t *testing.T) {
		assertIdentity(t, evaluateOne(t, monster(), "Monster.2004.S01E01.1080p.WEB.x264-GRP", textIndexer, nil), "", "")
	})
	t.Run("an id query still identifies the yearless release", func(t *testing.T) {
		assertIdentity(t, evaluateOne(t, monster(), "Monster.S01E01.1080p.WEB.x264-GRP", idIndexer, nil), "", "")
	})
	t.Run("the release's own id still identifies it", func(t *testing.T) {
		tg := monster()
		tg.Identity.IDs = map[string]string{common.IDKeyTVDB: "79352"}
		assertIdentity(t, evaluateOne(t, tg, "Monster.S01E01.1080p.WEB.x264-GRP", textIndexer,
			map[string]string{common.IDKeyTVDB: "79352"}), "", "")
	})
	t.Run("without the namesake the yearless release passes", func(t *testing.T) {
		tg := monster()
		tg.Identity.Namesakes = nil
		assertIdentity(t, evaluateOne(t, tg, "Monster.S01E01.1080p.WEB.x264-GRP", textIndexer, nil), "", "")
	})
	t.Run("Pocket Monsters is another title, not a namesake", func(t *testing.T) {
		assertIdentity(t, evaluateOne(t, monster(), "Pocket.Monsters.S01E01.1080p.WEB.x264-GRP", textIndexer, nil),
			"WrongItem", `title "Pocket Monsters"`)
	})

	dune := decision.Target{Kind: common.MediaKindMovie, Available: true, Identity: decision.Identity{
		Titles: []string{"Dune"}, Year: 2021, Namesakes: []string{"Dune"},
	}}
	t.Run("a yearless movie never reaches the namesake rule", func(t *testing.T) {
		// The plan's case ("Dune" with no year is UnknownItem) cannot be
		// built through the real parser: release.Parse refuses a movie
		// title naming no year, so the release is UnableToParse -- other
		// titles, never approved -- before identity runs. The movie half of
		// the namesake rule is reachable only from a hand-built
		// ParsedRelease (test batch D2 finding).
		rel := common.ReleaseInfo{GUID: "t:dune", IndexerRef: textIndexer, Title: "Dune.1080p.BluRay.x264-GRP", Protocol: common.ProtocolTorrent}
		ds := decision.Evaluate(context.Background(), dune, identityProfile(t), &catalogue.Catalogue{}, []common.ReleaseInfo{rel}, identityOptions())
		require.Len(t, ds, 1)
		require.False(t, ds[0].Approved)
		require.Nil(t, ds[0].Parsed)
		require.Equal(t, decision.BandOtherTitle, decision.BandOf(ds[0].Approved, ds[0].Rejections))
		require.True(t, decision.ReasonUnableToParse.Of(ds[0].Rejections[0]), ds[0].Rejections[0].Reason)
	})
	t.Run("the movie naming its year passes", func(t *testing.T) {
		assertIdentity(t, evaluateOne(t, dune, "Dune.2021.1080p.BluRay.x264-GRP", textIndexer, nil), "", "")
	})
	t.Run("the namesake's own year is the other film", func(t *testing.T) {
		assertIdentity(t, evaluateOne(t, dune, "Dune.1984.1080p.BluRay.x264-GRP", textIndexer, nil), "WrongItem", "1984")
	})
}
