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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// The two indexers every identity case is found through: one answered an id
// query (and so vouches for the item), the other a text query (and so
// vouches for nothing).
const (
	idIndexer   = "id-idx"
	textIndexer = "text-idx"
)

// identityProfile allows every quality the cases below parse to, in any
// language, so the identity check is the only thing that can reject them.
func identityProfile(t *testing.T) quality.Profile {
	t.Helper()
	var tier []quality.Definition
	for _, name := range []string{"Bluray-1080p", "Bluray-720p", "WEBDL-1080p", "WEBDL-720p", "HDTV-720p", "HDTV-1080p"} {
		d, ok := quality.Lookup("video", name)
		require.True(t, ok, name)
		tier = append(tier, d)
	}
	return quality.Profile{
		Tiers:        [][]quality.Definition{tier},
		LanguageName: "any",
		Sizes:        quality.MovieSizeTable(),
		ProperPolicy: "preferAndUpgrade",
	}
}

func identityOptions() decision.Options {
	return decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true, "usenet": true}}
}

// evaluateOne runs the REAL decision.Evaluate -- real release.Parse, real
// checklist -- on one release. SizeBytes is 0 so the size model stays out of
// the way ("a release with unknown size is never rejected").
func evaluateOne(t *testing.T, tg decision.Target, title, indexer string, ids map[string]string) decision.Decision {
	t.Helper()
	rel := common.ReleaseInfo{
		GUID: indexer + ":" + title, IndexerRef: indexer, Title: title,
		Protocol: common.ProtocolTorrent, IDs: ids,
	}
	ds := decision.Evaluate(context.Background(), tg, identityProfile(t), &catalogue.Catalogue{}, []common.ReleaseInfo{rel}, identityOptions())
	require.Len(t, ds, 1)
	require.NotNil(t, ds[0].Parsed, "%q must parse; an UnableToParse rejection would make the identity verdict vacuous", title)
	return ds[0]
}

// identityVerdicts returns only the identity check's rejections.
func identityVerdicts(d decision.Decision) []string {
	var out []string
	for _, r := range d.Rejections {
		if strings.HasPrefix(r.Reason, decision.ReasonWrongItem.Code+":") ||
			strings.HasPrefix(r.Reason, decision.ReasonUnknownItem.Code+":") {
			out = append(out, r.Reason)
		}
	}
	return out
}

func duneIdentity() decision.Identity {
	return decision.Identity{
		Titles: []string{"Dune", "Dune: Part One"},
		Year:   2021,
		IDs:    map[string]string{common.IDKeyTMDB: "438631", common.IDKeyIMDB: "tt1160419"},
	}
}

func withSecondaryYear(id decision.Identity, year int) decision.Identity {
	id.SecondaryYear = year
	return id
}

func movieTarget(id decision.Identity) decision.Target {
	return decision.Target{Kind: common.MediaKindMovie, Available: true, Identity: id}
}

// TestIdentityRejectsTheWrongFilmAtTheRightQuality is the defect G1-6b
// closes, end to end: a text-fallback search for "Dune 2021" returns other
// films, at a quality the profile wants and a size it accepts, and until this
// check existed every one of them was approved. The right film must still be
// approved -- that is what proves the wrong ones are rejected for identity
// and nothing else.
func TestIdentityRejectsTheWrongFilmAtTheRightQuality(t *testing.T) {
	tg := movieTarget(duneIdentity())

	right := evaluateOne(t, tg, "Dune.2021.1080p.BluRay.x264-GRP", textIndexer, nil)
	require.True(t, right.Approved, "the right film at the right quality must be approved; rejected with %+v", right.Rejections)

	for _, wrong := range []struct{ title, detail string }{
		{"Dune.Part.Two.2024.1080p.BluRay.x264-GRP", `title "Dune Part Two" matches none`},
		{"Dune.1984.1080p.BluRay.x264-GRP", "release year 1984"},
		{"Arrival.2016.1080p.BluRay.x264-GRP", `title "Arrival" matches none`},
	} {
		t.Run(wrong.title, func(t *testing.T) {
			d := evaluateOne(t, tg, wrong.title, textIndexer, nil)
			require.False(t, d.Approved, "a different film at the right quality must not be approved")
			require.Len(t, d.Rejections, 1, "identity must be the ONLY reason -- quality and size pass: %+v", d.Rejections)
			require.True(t, strings.HasPrefix(d.Rejections[0].Reason, decision.ReasonWrongItem.Code+":"), d.Rejections[0].Reason)
			require.Contains(t, d.Rejections[0].Reason, wrong.detail)
			require.Equal(t, common.RejectionPermanent, d.Rejections[0].Type)
		})
	}
}

// TestIdentityMovie is the movie rule table: ids first, then the indexer's id
// query, then titles and the year, then the unevaluable case.
func TestIdentityMovie(t *testing.T) {
	cases := []struct {
		name     string
		identity decision.Identity // zero means duneIdentity()
		title    string
		indexer  string
		ids      map[string]string
		want     string // "" = no identity rejection, else the Reason code
		detail   string // substring the rejection must carry
	}{
		// Titles and the year.
		{name: "same film", title: "Dune.2021.1080p.BluRay.x264-GRP"},
		{name: "one year early is the same film", title: "Dune.2020.1080p.BluRay.x264-GRP"},
		{name: "one year late is the same film", title: "Dune.2022.1080p.BluRay.x264-GRP"},
		{name: "two years early is not", title: "Dune.2019.1080p.BluRay.x264-GRP", want: "WrongItem", detail: "release year 2019 is more than 1 year from the item's 2021"},
		{name: "two years late is not", title: "Dune.2023.1080p.BluRay.x264-GRP", want: "WrongItem", detail: "release year 2023"},
		// Ruling R-7: SecondaryYear exactly, however far from Year; the ±1
		// around Year still stands, but not around SecondaryYear.
		{
			name: "the secondary year, beyond the tolerance around Year", title: "Dune.2019.1080p.BluRay.x264-GRP",
			identity: withSecondaryYear(duneIdentity(), 2019),
		},
		{
			name: "one year off the secondary year is not", title: "Dune.2018.1080p.BluRay.x264-GRP",
			identity: withSecondaryYear(duneIdentity(), 2019), want: "WrongItem",
			detail: "release year 2018 is more than 1 year from the item's 2021, and is not its secondary year 2019",
		},
		{
			name: "the tolerance around Year still holds with a secondary year", title: "Dune.2022.1080p.BluRay.x264-GRP",
			identity: withSecondaryYear(duneIdentity(), 2019),
		},
		{
			name: "an id-query release named by the secondary year", title: "Dune.2019.1080p.BluRay.x264-GRP", indexer: idIndexer,
			identity: withSecondaryYear(duneIdentity(), 2019),
		},
		{name: "an alternate title of the item", title: "Dune.Part.One.2021.1080p.BluRay.x264-GRP"},
		{name: "an AKA second title on the release", title: "Duna.AKA.Dune.2021.1080p.BluRay.x264-GRP"},
		{
			name: "roman and arabic numerals are one sequel", title: "Rocky.2.1979.1080p.BluRay.x264-GRP",
			identity: decision.Identity{Titles: []string{"Rocky II"}, Year: 1979},
		},
		{
			name: "an ampersand and 'and' are one title", title: "Fast.and.Furious.2009.1080p.BluRay.x264-GRP",
			identity: decision.Identity{Titles: []string{"Fast & Furious"}, Year: 2009},
		},
		{name: "a region title with no ids from a text query", title: "Der.Wuestenplanet.2021.German.1080p.BluRay.x264-GRP", want: "WrongItem", detail: `title "Der Wuestenplanet"`},

		// Ids decide when both sides have them.
		{name: "a region title with a matching tmdb id", title: "Der.Wuestenplanet.2021.German.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTMDB: "438631"}},
		{name: "a matching imdb id in another zero-padding", title: "Der.Wuestenplanet.2021.German.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyIMDB: "tt01160419"}},
		{name: "a conflicting tmdb id even though the title matches", title: "Dune.2021.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTMDB: "841"}, want: "WrongItem", detail: "release tmdb id 841 conflicts with the item's 438631"},
		{name: "one matching and one conflicting id", title: "Dune.2021.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTMDB: "438631", common.IDKeyIMDB: "tt0087182"}, want: "WrongItem", detail: "release imdb id tt0087182 conflicts"},
		{name: "an id embedded in the release title counts", title: "Dune.2021.1080p.BluRay.x264-GRP [tmdbid-841]", want: "WrongItem", detail: "release tmdb id 841"},
		{name: "an indexer's 0 is unknown, not a conflict", title: "Dune.2021.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTMDB: "0", common.IDKeyIMDB: "0"}},
		{name: "a key the item lacks is not compared", title: "Dune.2021.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTVDB: "12345"}},

		// The indexer's id query.
		{name: "a region title with no ids of its own, from an id query", title: "Der.Wuestenplanet.2021.German.1080p.BluRay.x264-GRP", indexer: idIndexer},
		{name: "an id-query release decades off is still the wrong film", title: "Dune.1984.1080p.BluRay.x264-GRP", indexer: idIndexer, want: "WrongItem", detail: "release year 1984"},
		{name: "an id-query release's own conflicting id still rejects", title: "Dune.2021.1080p.BluRay.x264-GRP", indexer: idIndexer, ids: map[string]string{common.IDKeyTMDB: "841"}, want: "WrongItem", detail: "release tmdb id 841"},

		// The unevaluable case fails closed. A title of symbols alone keys to
		// nothing under any title normaliser (TestIdentityNonLatinTitles
		// covers the scripts pkg/release's normaliser may or may not keep).
		{name: "a title with nothing readable, no ids, text query", title: "★★★.2021.1080p.BluRay.x264-GRP", want: "UnknownItem", detail: `release title "★★★" has nothing the title comparison can read`},
		{name: "a title with nothing readable, with a matching id", title: "★★★.2021.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTMDB: "438631"}},
		{name: "a title with nothing readable, from an id query", title: "★★★.2021.1080p.BluRay.x264-GRP", indexer: idIndexer},
		{name: "a wholly non-Latin title with a matching id", title: "Дюна.2021.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTMDB: "438631"}},
		{name: "a wholly non-Latin title from an id query", title: "Дюна.2021.1080p.BluRay.x264-GRP", indexer: idIndexer},
		{
			name: "an item known only by a title with nothing readable", title: "The.Matrix.1999.1080p.BluRay.x264-GRP",
			identity: decision.Identity{Titles: []string{"★"}, Year: 1999, IDs: map[string]string{common.IDKeyTMDB: "603"}},
			want:     "UnknownItem", detail: `none of the item's titles (primary "★")`,
		},
		{
			name: "an empty Identity identifies nothing", title: "Dune.2021.1080p.BluRay.x264-GRP",
			identity: decision.Identity{IDQueryIndexers: map[string]bool{}}, // non-zero only so the table default does not replace it
			want:     "UnknownItem", detail: "the item has no title yet",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.identity
			if id.Titles == nil && id.IDQueryIndexers == nil {
				id = duneIdentity()
			}
			if id.IDQueryIndexers == nil {
				id.IDQueryIndexers = map[string]bool{idIndexer: true}
			}
			indexer := tc.indexer
			if indexer == "" {
				indexer = textIndexer
			}
			assertIdentity(t, evaluateOne(t, movieTarget(id), tc.title, indexer, tc.ids), tc.want, tc.detail)
		})
	}
}

// TestIdentityEpisode is the series-and-numbering rule table. The item half
// is the movie rule with the series tvdb id and no year bound; the numbering
// half has to pass as well.
func TestIdentityEpisode(t *testing.T) {
	breakingBad := func() decision.Identity {
		return decision.Identity{
			Titles: []string{"Breaking Bad"}, Year: 2008,
			IDs:    map[string]string{common.IDKeyTVDB: "81189"},
			Season: 1, Episodes: []int{5},
			IDQueryIndexers: map[string]bool{idIndexer: true},
		}
	}
	airDate := time.Date(2024, 1, 15, 23, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		kind     common.MediaKind // zero means episode
		identity func() decision.Identity
		title    string
		indexer  string
		ids      map[string]string
		want     string
		detail   string
	}{
		{name: "the right episode", title: "Breaking.Bad.S01E05.720p.HDTV.x264-GRP"},
		{name: "the wrong episode number", title: "Breaking.Bad.S01E06.720p.HDTV.x264-GRP", want: "WrongItem", detail: "release is S01E06, the item is S01E05"},
		{name: "the wrong season", title: "Breaking.Bad.S02E05.720p.HDTV.x264-GRP", want: "WrongItem", detail: "release is S02E05"},
		{name: "a multi-episode release covering it", title: "Breaking.Bad.S01E05E06.720p.HDTV.x264-GRP"},
		{name: "a season pack of its season", title: "Breaking.Bad.S01.720p.BluRay.x264-GRP"},
		{name: "a different series", title: "Better.Call.Saul.S01E05.720p.HDTV.x264-GRP", want: "WrongItem", detail: `title "Better Call Saul"`},
		{name: "a conflicting tvdb id even though the title matches", title: "Breaking.Bad.S01E05.720p.HDTV.x264-GRP", ids: map[string]string{common.IDKeyTVDB: "273181"}, want: "WrongItem", detail: "release tvdb id 273181"},
		{name: "an id-query release for the wrong episode", title: "Breaking.Bad.S01E06.720p.HDTV.x264-GRP", indexer: idIndexer, want: "WrongItem", detail: "release is S01E06"},
		{
			name: "an episode's own imdb id is not compared against the series'", title: "Breaking.Bad.S01E05.720p.HDTV.x264-GRP",
			identity: func() decision.Identity {
				id := breakingBad()
				id.IDs[common.IDKeyIMDB] = "tt0903747"
				return id
			},
			ids: map[string]string{common.IDKeyIMDB: "tt0959621"},
		},
		{
			name: "a year-disambiguated series title", title: "Doctor.Who.2005.S01E01.720p.HDTV.x264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"Doctor Who"}, Year: 2005, Season: 1, Episodes: []int{1}}
			},
		},
		{
			name: "the other series of the same name", title: "Doctor.Who.1963.S01E01.720p.HDTV.x264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"Doctor Who"}, Year: 2005, Season: 1, Episodes: []int{1}}
			},
			want: "WrongItem", detail: `title "Doctor Who 1963"`,
		},
		{
			name: "anime by absolute number", title: "[SubsPlease] Frieren - 12 (1080p) [ABCDEF12].mkv",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"Frieren: Beyond Journey's End", "Frieren"}, Season: 1, Episodes: []int{12}, Absolute: []int{12}}
			},
		},
		{
			name: "anime, the wrong absolute number", title: "[SubsPlease] Frieren - 13 (1080p) [ABCDEF12].mkv",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"Frieren"}, Season: 1, Episodes: []int{12}, Absolute: []int{12}}
			},
			want: "WrongItem", detail: "release is absolute [13], the item is absolute [12]",
		},
		{
			name: "a daily episode by air date", title: "The.Daily.Show.2024.01.15.720p.WEB.h264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"The Daily Show"}, Season: 2024, Episodes: []int{7}, AirDate: &airDate}
			},
		},
		{
			name: "a daily episode, the wrong day", title: "The.Daily.Show.2024.01.16.720p.WEB.h264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"The Daily Show"}, Season: 2024, Episodes: []int{7}, AirDate: &airDate}
			},
			want: "WrongItem", detail: "release aired 2024-01-16, the item aired 2024-01-15",
		},
		{
			name: "no numbering in common fails closed", title: "Breaking.Bad.S01E05.720p.HDTV.x264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"Breaking Bad"}, IDs: map[string]string{common.IDKeyTVDB: "81189"}}
			},
			want: "UnknownItem", detail: "names no season/episode, absolute number or air date the item also has",
		},

		// A pack target, as the RSS matcher builds one: the Series, with the
		// episodes the release was matched to.
		{
			name: "a season pack covering the pack target", kind: common.MediaKindSeries, title: "The.Wire.S01.1080p.BluRay.x264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"The Wire"}, Year: 2002, IDs: map[string]string{common.IDKeyTVDB: "79126"}, Season: 1, Episodes: []int{1, 2, 3}}
			},
		},
		{
			name: "a multi-episode release short of the pack target", kind: common.MediaKindSeries, title: "The.Wire.S01E01E02.1080p.BluRay.x264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"The Wire"}, Year: 2002, Season: 1, Episodes: []int{1, 2, 3}}
			},
			want: "WrongItem", detail: "release is S01E01E02, the item is S01E01E02E03",
		},
		{
			name: "a pack of another season", kind: common.MediaKindSeries, title: "The.Wire.S02.1080p.BluRay.x264-GRP",
			identity: func() decision.Identity {
				return decision.Identity{Titles: []string{"The Wire"}, Year: 2002, Season: 1, Episodes: []int{1, 2, 3}}
			},
			want: "WrongItem", detail: "release is S02 full season",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idFn := tc.identity
			if idFn == nil {
				idFn = breakingBad
			}
			kind := tc.kind
			if kind == "" {
				kind = common.MediaKindEpisode
			}
			indexer := tc.indexer
			if indexer == "" {
				indexer = textIndexer
			}
			tg := decision.Target{Kind: kind, Available: true, Identity: idFn()}
			assertIdentity(t, evaluateOne(t, tg, tc.title, indexer, tc.ids), tc.want, tc.detail)
		})
	}
}

// kaijuSceneMap is a TheXEM-shaped table for a series TVDB files as one
// 26-episode season 1 and a season 2, which the scene releases as three
// seasons: scene S01 is TVDB S01E01-13, scene S02 is TVDB S01E14-26, scene S03
// is TVDB season 2. From scene S03 on, the scene absolute is TVDB's minus one
// (the scene skips a recap TVDB counts). One scene absolute, 37, is claimed
// by two rows (S03E10 and S03E11), which Sonarr refuses to pick between.
func kaijuSceneMap() []decision.SceneMapping {
	var rows []decision.SceneMapping
	for e := 1; e <= 13; e++ {
		rows = append(rows,
			decision.SceneMapping{Scene: decision.EpisodeNumbering{Season: 1, Episode: e, Absolute: e}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: e, Absolute: e}},
			decision.SceneMapping{Scene: decision.EpisodeNumbering{Season: 2, Episode: e, Absolute: 13 + e}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 13 + e, Absolute: 13 + e}},
		)
	}
	for e := 1; e <= 10; e++ {
		rows = append(rows, decision.SceneMapping{
			Scene: decision.EpisodeNumbering{Season: 3, Episode: e, Absolute: 25 + e},
			TVDB:  decision.EpisodeNumbering{Season: 2, Episode: e, Absolute: 26 + e},
		})
	}
	rows = append(rows, decision.SceneMapping{
		Scene: decision.EpisodeNumbering{Season: 3, Episode: 11, Absolute: 37},
		TVDB:  decision.EpisodeNumbering{Season: 2, Episode: 11, Absolute: 37},
	})
	rows[len(rows)-2].Scene.Absolute = 37 // S03E10 and S03E11 both claim scene absolute 37
	return rows
}

// TestIdentitySceneNumbering is the anime scene-numbering carried defect: a
// scene-numbered release named a TVDB episode the release's own numbers do
// not spell, and with no scene-to-TVDB map it was rejected as WrongItem. The
// map is read the way Sonarr reads it for an indexer release: the scene
// reading replaces the literal one wherever a row exists, and the literal one
// stands where none does.
func TestIdentitySceneNumbering(t *testing.T) {
	target := func(season, episode, absolute int, scene []decision.SceneMapping) decision.Target {
		return decision.Target{Kind: common.MediaKindEpisode, Available: true, Identity: decision.Identity{
			Titles: []string{"Kaiju Show"}, Season: season, Episodes: []int{episode}, Absolute: []int{absolute},
			SceneMappings: scene,
		}}
	}
	s01e18 := func(scene []decision.SceneMapping) decision.Target { return target(1, 18, 18, scene) }
	s02e05 := func(scene []decision.SceneMapping) decision.Target { return target(2, 5, 31, scene) }

	cases := []struct {
		name   string
		target decision.Target
		title  string
		want   string
		detail string
	}{
		// The defect, and its fix.
		{name: "with no map, scene S02E05 is the wrong item", target: s01e18(nil), title: "Kaiju.Show.S02E05.1080p.WEB.x264-GRP", want: "WrongItem", detail: "release is S02E05, the item is S01E18"},
		{name: "with the map, scene S02E05 is TVDB S01E18", target: s01e18(kaijuSceneMap()), title: "Kaiju.Show.S02E05.1080p.WEB.x264-GRP"},
		{name: "a number with no scene row is read literally", target: s01e18(kaijuSceneMap()), title: "Kaiju.Show.S01E18.1080p.WEB.x264-GRP"},

		// The scene reading replaces the literal one: scene S02E05 is not
		// TVDB S02E05 on this series, although the digits say so.
		{name: "without the map the digits alone would pass", target: s02e05(nil), title: "Kaiju.Show.S02E05.1080p.WEB.x264-GRP"},
		{name: "with the map, scene S02E05 is not TVDB S02E05", target: s02e05(kaijuSceneMap()), title: "Kaiju.Show.S02E05.1080p.WEB.x264-GRP", want: "WrongItem", detail: "release is S02E05 (scene numbering; TVDB S01E18), the item is S02E05"},
		{name: "scene S03E05 is TVDB S02E05", target: s02e05(kaijuSceneMap()), title: "Kaiju.Show.S03E05.1080p.WEB.x264-GRP"},

		// A pack is the TVDB episodes of its SCENE season.
		{name: "a scene S02 pack covers TVDB S01E18", target: s01e18(kaijuSceneMap()), title: "Kaiju.Show.S02.1080p.BluRay.x264-GRP"},
		{name: "a scene S02 pack does not cover TVDB S02E05", target: s02e05(kaijuSceneMap()), title: "Kaiju.Show.S02.1080p.BluRay.x264-GRP", want: "WrongItem", detail: "(scene numbering; TVDB S01E14, S01E15, S01E16, S01E17 and 9 more)"},

		// Absolute numbers: scene absolute 30 is TVDB absolute 31 (S02E05).
		{name: "with no map, scene absolute 30 is the wrong item", target: s02e05(nil), title: "[SubsPlease] Kaiju Show - 30 (1080p) [ABCDEF12].mkv", want: "WrongItem", detail: "release is absolute [30], the item is absolute [31]"},
		{name: "with the map, scene absolute 30 is TVDB S02E05", target: s02e05(kaijuSceneMap()), title: "[SubsPlease] Kaiju Show - 30 (1080p) [ABCDEF12].mkv"},
		{
			name: "a scene absolute two rows claim is read literally", target: target(2, 11, 37, kaijuSceneMap()),
			title: "[SubsPlease] Kaiju Show - 37 (1080p) [ABCDEF12].mkv",
		},
		{
			name: "so it does not reach the rows' other episode", target: target(2, 10, 36, kaijuSceneMap()),
			title: "[SubsPlease] Kaiju Show - 37 (1080p) [ABCDEF12].mkv", want: "WrongItem", detail: "release is absolute [37], the item is absolute [36]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertIdentity(t, evaluateOne(t, tc.target, tc.title, textIndexer, nil), tc.want, tc.detail)
		})
	}
}

// TestSingleEpisodeSearchRejectsASeasonPack is ruling R-3, after Sonarr's
// SingleEpisodeSearchMatchSpecification: a search for ONE episode does not
// take a full-season pack of its season, while the RSS path (no search) and a
// pack target keep accepting one. The pack's numbering is right in every
// case, so the only thing that can reject it is what was asked for.
func TestSingleEpisodeSearchRejectsASeasonPack(t *testing.T) {
	episode := func(single bool) decision.Target {
		return decision.Target{Kind: common.MediaKindEpisode, Available: true, Identity: decision.Identity{
			Titles: []string{"Breaking Bad"}, IDs: map[string]string{common.IDKeyTVDB: "81189"},
			Season: 1, Episodes: []int{5}, SingleEpisodeSearch: single,
		}}
	}
	fullSeason := func(d decision.Decision) []string {
		var out []string
		for _, r := range d.Rejections {
			if strings.HasPrefix(r.Reason, decision.ReasonFullSeason.Code+":") {
				out = append(out, r.Reason)
			}
		}
		return out
	}

	for _, title := range []string{
		"Breaking.Bad.S01.720p.BluRay.x264-GRP",
		"Breaking.Bad.S01-S03.720p.BluRay.x264-GRP", // a multi-season pack is a FullSeason too
	} {
		t.Run("a single-episode search refuses "+title, func(t *testing.T) {
			d := evaluateOne(t, episode(true), title, textIndexer, nil)
			require.False(t, d.Approved)
			require.Len(t, d.Rejections, 1, "the pack is the right season at the right quality; only R-3 may reject it: %+v", d.Rejections)
			got := fullSeason(d)
			require.Len(t, got, 1, "%+v", d.Rejections)
			require.Contains(t, got[0], "this is a search for the single episode S01E05")
			require.Equal(t, common.RejectionPermanent, d.Rejections[0].Type, "Permanent, so Search.spec.override can still take it")
		})
	}

	t.Run("the same pack without a single-episode search (RSS) is accepted", func(t *testing.T) {
		d := evaluateOne(t, episode(false), "Breaking.Bad.S01.720p.BluRay.x264-GRP", textIndexer, nil)
		require.True(t, d.Approved, "%+v", d.Rejections)
	})
	t.Run("a single-episode search still takes the single episode", func(t *testing.T) {
		d := evaluateOne(t, episode(true), "Breaking.Bad.S01E05.720p.HDTV.x264-GRP", textIndexer, nil)
		require.True(t, d.Approved, "%+v", d.Rejections)
	})
	t.Run("and a multi-episode release covering it, as Sonarr does", func(t *testing.T) {
		d := evaluateOne(t, episode(true), "Breaking.Bad.S01E05E06.720p.HDTV.x264-GRP", textIndexer, nil)
		require.True(t, d.Approved, "%+v", d.Rejections)
	})
	t.Run("a pack of another season is the wrong item, not a pack rejection", func(t *testing.T) {
		d := evaluateOne(t, episode(true), "Breaking.Bad.S02.720p.BluRay.x264-GRP", textIndexer, nil)
		assertIdentity(t, d, "WrongItem", "release is S02 full season")
		require.Empty(t, fullSeason(d))
	})
}

// TestIdentityNonLatinTitles pins the non-Latin case against whatever
// pkg/release's title normaliser does with the script, rather than against
// one normaliser: while release.CleanTitle drops Cyrillic, a Cyrillic title
// is unreadable and fails closed as UnknownItem; once it keeps it, the title
// is compared like any other -- a different film's Cyrillic title is
// WrongItem and the film's own Cyrillic title is a match.
func TestIdentityNonLatinTitles(t *testing.T) {
	readable := release.CleanTitle("Дюна") != ""
	id := duneIdentity()
	id.IDQueryIndexers = map[string]bool{idIndexer: true}

	d := evaluateOne(t, movieTarget(id), "Дюна.2021.1080p.BluRay.x264-GRP", textIndexer, nil)
	if readable {
		assertIdentity(t, d, "WrongItem", `title "Дюна" matches none of the item's`)
	} else {
		assertIdentity(t, d, "UnknownItem", `release title "Дюна" has nothing the title comparison can read`)
	}

	id.Titles = append(id.Titles, "Дюна")
	d = evaluateOne(t, movieTarget(id), "Дюна.2021.1080p.BluRay.x264-GRP", textIndexer, nil)
	if readable {
		assertIdentity(t, d, "", "")
	} else {
		assertIdentity(t, d, "UnknownItem", "has nothing the title comparison can read")
	}
}

// TestIdentityHasNoRuleForContainerKinds: an Artist, Author or Comic is what
// albums, books and issues belong to, not something a release is for. No
// search targets one, and the first that does must bring an identity rule
// with it rather than inherit "approve anything".
func TestIdentityHasNoRuleForContainerKinds(t *testing.T) {
	for _, tc := range []struct {
		kind  common.MediaKind
		title string
	}{
		{common.MediaKindArtist, "Miles Davis - Kind of Blue (1959) [FLAC]"},
		{common.MediaKindAuthor, "Frank Herbert - Dune (1965) [EPUB]"},
		{common.MediaKindComic, "Batman 050 (2018).cbz"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			tg := decision.Target{Kind: tc.kind, Available: true, Identity: decision.Identity{Titles: []string{"anything"}, Creators: []string{"anyone"}}}
			assertIdentity(t, evaluateOne(t, tg, tc.title, textIndexer, nil), "UnknownItem", fmt.Sprintf("no identity rule for kind %q", tc.kind))
		})
	}
}

func assertIdentity(t *testing.T, d decision.Decision, want, detail string) {
	t.Helper()
	got := identityVerdicts(d)
	if want == "" {
		require.Empty(t, got, "no identity rejection expected for %q", d.Release.Title)
		return
	}
	require.Len(t, got, 1, "exactly one identity rejection expected for %q; all rejections: %+v", d.Release.Title, d.Rejections)
	require.True(t, strings.HasPrefix(got[0], want+":"), "want %s, got %q", want, got[0])
	require.Contains(t, got[0], detail)
	require.False(t, d.Approved)
}
