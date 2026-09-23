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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
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

		// The unevaluable case fails closed.
		{name: "a wholly non-Latin title, no ids, text query", title: "Дюна.2021.1080p.BluRay.x264-GRP", want: "UnknownItem", detail: "has nothing the title comparison can read"},
		{name: "a wholly non-Latin title with a matching id", title: "Дюна.2021.1080p.BluRay.x264-GRP", ids: map[string]string{common.IDKeyTMDB: "438631"}},
		{name: "a wholly non-Latin title from an id query", title: "Дюна.2021.1080p.BluRay.x264-GRP", indexer: idIndexer},
		{
			name: "an item known only by a non-Latin title", title: "The.Matrix.1999.1080p.BluRay.x264-GRP",
			identity: decision.Identity{Titles: []string{"Матрица"}, Year: 1999, IDs: map[string]string{common.IDKeyTMDB: "603"}},
			want:     "UnknownItem", detail: `none of the item's titles (primary "Матрица")`,
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

// TestIdentityHasNoRuleForOtherKinds: no caller evaluates a non-video kind
// today, and the first one to must bring an identity rule with it rather than
// inherit "approve anything".
func TestIdentityHasNoRuleForOtherKinds(t *testing.T) {
	tg := decision.Target{Kind: common.MediaKindAlbum, Available: true, Identity: decision.Identity{Titles: []string{"Kind of Blue"}}}
	rel := common.ReleaseInfo{Title: "Miles Davis - Kind of Blue (1959) [FLAC]", Protocol: common.ProtocolTorrent}
	ds := decision.Evaluate(context.Background(), tg, identityProfile(t), &catalogue.Catalogue{}, []common.ReleaseInfo{rel}, identityOptions())
	require.Len(t, ds, 1)
	require.NotNil(t, ds[0].Parsed)
	assertIdentity(t, ds[0], "UnknownItem", `no identity rule for kind "album"`)
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
