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

package naming_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

var (
	// emptyBracketRe is a bracket pair with nothing but whitespace inside:
	// what a literal "({Release Year})" leaves when the year is unknown.
	emptyBracketRe = regexp.MustCompile(`\(\s*\)|\[\s*\]|\{\s*\}`)
	// doubleSeparatorRe is two dash separators with nothing between them:
	// what "{Book SeriesPosition} - {Release Year} - " leaves when both
	// tokens are empty.
	doubleSeparatorRe = regexp.MustCompile(`-\s+-`)
)

// assertCleanPath is the guard: a rendered path has no empty segment, no
// segment that begins or ends with a separator, no run of spaces where a
// token dropped out between two literal ones, no two separators with
// nothing between them, and no empty brackets.
func assertCleanPath(t *testing.T, label, got string) {
	t.Helper()
	if !assert.NotEmptyf(t, got, "%s: rendered nothing", label) {
		return
	}
	for i, seg := range strings.Split(got, "/") {
		assert.NotEmptyf(t, seg, "%s: segment %d of %q is empty", label, i, got)
		assert.Equalf(t, strings.Trim(seg, " -._"), seg, "%s: segment %q of %q has a dangling separator", label, seg, got)
		assert.NotContainsf(t, seg, "  ", "%s: segment %q of %q has a run of spaces", label, seg, got)
		assert.Falsef(t, doubleSeparatorRe.MatchString(seg), "%s: segment %q of %q has two separators with nothing between", label, seg, got)
		assert.Falsef(t, emptyBracketRe.MatchString(seg), "%s: segment %q of %q has empty brackets", label, seg, got)
	}
}

type presetCase struct {
	name   string
	render func(e naming.Engine, c naming.Context) (string, error)
	full   naming.Context
	// optional names every token the preset renders that the catalog does
	// not require, with how to empty it. Identity (titles, names, provider
	// ids, season/episode/track/issue numbers) is required by the CRDs and
	// is not here.
	optional map[string]func(*naming.Context)
}

func folder(kind commonv1.MediaKind) func(naming.Engine, naming.Context) (string, error) {
	return func(e naming.Engine, c naming.Context) (string, error) { return e.BuildFolder(kind, c) }
}

func file(kind commonv1.MediaKind) func(naming.Engine, naming.Context) (string, error) {
	return func(e naming.Engine, c naming.Context) (string, error) { return e.BuildFile(kind, c) }
}

// TestEveryPresetCollapsesEmptyOptionalTokens renders every preset, in all
// four dialects, with its full context, with each optional token emptied
// in turn, and with all of them emptied at once, and holds every result to
// assertCleanPath. docs/research/naming.md §A2 documents the *arr rule: "a
// token wrapped in extra characters is emitted only when non-empty" -- the
// decoration an optional token needs lives inside its braces, as in
// TRaSH's own "{Movie CleanTitle} {(Release Year)}".
func TestEveryPresetCollapsesEmptyOptionalTokens(t *testing.T) {
	airDate := time.Date(2013, time.October, 30, 0, 0, 0, 0, time.UTC)
	quality := commonv1.Quality{Source: commonv1.SourceWebDL, Resolution: 1080}
	noYear := func(c *naming.Context) { c.Year = 0 }
	noQuality := func(c *naming.Context) { c.Quality = commonv1.Quality{}; c.Revision = commonv1.Revision{} }
	noGroup := func(c *naming.Context) { c.ReleaseGroup = "" }

	movie := naming.Context{
		Kind: commonv1.MediaKindMovie, Title: "The Matrix", Year: 1999, TmdbID: "603",
		Quality: quality, Revision: commonv1.Revision{Version: 2}, ReleaseGroup: "RlsGrp",
	}
	episode := naming.Context{
		Kind: commonv1.MediaKindEpisode, SeriesTitle: "The Series Title!", SeriesYear: 2010, TvdbID: "153021",
		Season: 1, Episodes: []int{1}, EpisodeTitle: "Episode Title 1", Quality: quality, ReleaseGroup: "RlsGrp",
	}
	anime := episode
	anime.Absolute = []int{1}
	daily := episode
	daily.AirDate = &airDate
	episodeOptional := map[string]func(*naming.Context){
		"series year":   func(c *naming.Context) { c.SeriesYear = 0 },
		"episode title": func(c *naming.Context) { c.EpisodeTitle = "" },
		"quality":       noQuality,
		"release group": noGroup,
	}
	album := naming.Context{
		Kind: commonv1.MediaKindAlbum, ArtistName: "Radiohead", AlbumTitle: "OK Computer",
		Year: 1997, Track: 1, TrackTitle: "Airbag",
	}
	book := naming.Context{Kind: commonv1.MediaKindBook, AuthorName: "Frank Herbert", BookTitle: "Dune"}
	audiobook := naming.Context{
		Kind: commonv1.MediaKindAudiobook, AuthorName: "Terry Pratchett", BookSeries: "Discworld",
		BookSeriesPosition: "8", Year: 1989, BookTitle: "Guards! Guards!", Narrator: "Nigel Planer",
	}
	audiobookOptional := map[string]func(*naming.Context){
		"book series":          func(c *naming.Context) { c.BookSeries = "" },
		"book series position": func(c *naming.Context) { c.BookSeriesPosition = "" },
		"release year":         noYear,
		"narrator":             func(c *naming.Context) { c.Narrator = "" },
	}
	issue := naming.Context{Kind: commonv1.MediaKindIssue, ComicSeriesTitle: "Saga", IssueNumber: "001"}

	cases := []presetCase{
		{"movie folder", folder(commonv1.MediaKindMovie), movie, map[string]func(*naming.Context){"release year": noYear}},
		{"movie file", file(commonv1.MediaKindMovie), movie, map[string]func(*naming.Context){
			"release year": noYear, "quality": noQuality, "release group": noGroup,
		}},
		{"series folder", folder(commonv1.MediaKindSeries), episode, map[string]func(*naming.Context){
			"series year": func(c *naming.Context) { c.SeriesYear = 0 },
		}},
		{"episode folder", folder(commonv1.MediaKindEpisode), episode, map[string]func(*naming.Context){
			"series year": func(c *naming.Context) { c.SeriesYear = 0 },
		}},
		{"episode file (standard)", file(commonv1.MediaKindEpisode), episode, episodeOptional},
		{"episode file (anime)", file(commonv1.MediaKindEpisode), anime, episodeOptional},
		{"episode file (daily)", file(commonv1.MediaKindEpisode), daily, episodeOptional},
		{"artist folder", folder(commonv1.MediaKindArtist), album, nil},
		{"album folder", folder(commonv1.MediaKindAlbum), album, map[string]func(*naming.Context){"release year": noYear}},
		{"track file", file(commonv1.MediaKindAlbum), album, map[string]func(*naming.Context){
			"release year": noYear, "track title": func(c *naming.Context) { c.TrackTitle = "" },
		}},
		{"author folder", folder(commonv1.MediaKindAuthor), book, nil},
		{"book folder", folder(commonv1.MediaKindBook), book, nil},
		{"book file", file(commonv1.MediaKindBook), book, nil},
		{"audiobook folder", folder(commonv1.MediaKindAudiobook), audiobook, audiobookOptional},
		{"audiobook file", file(commonv1.MediaKindAudiobook), audiobook, audiobookOptional},
		{"comic folder", folder(commonv1.MediaKindComic), issue, nil},
		{"issue folder", folder(commonv1.MediaKindIssue), issue, nil},
		{"issue file", file(commonv1.MediaKindIssue), issue, nil},
	}

	for _, d := range []naming.Dialect{naming.DialectJellyfin, naming.DialectPlex, naming.DialectEmby, naming.DialectKodi} {
		e := naming.NewEngine(naming.Config{Dialect: d})
		for _, pc := range cases {
			names := make([]string, 0, len(pc.optional))
			for n := range pc.optional {
				names = append(names, n)
			}
			sort.Strings(names)

			render := func(label string, c naming.Context) {
				t.Helper()
				got, err := pc.render(e, c)
				require.NoErrorf(t, err, "%s", label)
				assertCleanPath(t, label, got)
			}
			render(fmt.Sprintf("%s/%s, every token set", d, pc.name), pc.full)
			all := pc.full
			for _, n := range names {
				c := pc.full
				pc.optional[n](&c)
				render(fmt.Sprintf("%s/%s, %s empty", d, pc.name, n), c)
				pc.optional[n](&all)
			}
			if len(names) > 1 {
				render(fmt.Sprintf("%s/%s, every optional token empty", d, pc.name), all)
			}
		}
	}
}

// TestEmptyOptionalTokensCollapseToTheseExactPaths pins the two renders
// the defect report named, and the full forms beside them, so the collapse
// is not satisfied by some other shape that merely passes the guard.
func TestEmptyOptionalTokensCollapseToTheseExactPaths(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	for _, tc := range []struct {
		name string
		kind commonv1.MediaKind
		c    naming.Context
		want string
	}{
		{
			"audiobook, no series, position or year", commonv1.MediaKindAudiobook,
			naming.Context{AuthorName: "Terry Pratchett", BookTitle: "Guards! Guards!"},
			"Terry Pratchett/Guards! Guards!",
		},
		{
			"audiobook, series without a position", commonv1.MediaKindAudiobook,
			naming.Context{AuthorName: "Terry Pratchett", BookSeries: "Discworld", Year: 1989, BookTitle: "Guards! Guards!"},
			"Terry Pratchett/Discworld/1989 - Guards! Guards!",
		},
		{
			"audiobook, every token", commonv1.MediaKindAudiobook,
			naming.Context{AuthorName: "Terry Pratchett", BookSeries: "Discworld", BookSeriesPosition: "8", Year: 1989, BookTitle: "Guards! Guards!", Narrator: "Nigel Planer"},
			"Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer",
		},
		{
			"album, no year", commonv1.MediaKindAlbum,
			naming.Context{ArtistName: "Radiohead", AlbumTitle: "Kid A"},
			"Radiohead/Kid A",
		},
		{
			"album, with year", commonv1.MediaKindAlbum,
			naming.Context{ArtistName: "Radiohead", AlbumTitle: "Kid A", Year: 2000},
			"Radiohead/Kid A (2000)",
		},
		{
			"movie, no year", commonv1.MediaKindMovie,
			naming.Context{Title: "The Matrix", TmdbID: "603"},
			"The Matrix [tmdbid-603]",
		},
	} {
		got, err := e.BuildFolder(tc.kind, tc.c)
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}

	got, err := e.MovieFile(naming.Context{Title: "The Matrix", Year: 1999, ReleaseGroup: "RlsGrp"})
	require.NoError(t, err)
	assert.Equal(t, "The Matrix (1999)-RlsGrp", got, "no quality: no dangling \" - \" before the group")

	got, err = e.TrackFile(naming.Context{ArtistName: "Radiohead", AlbumTitle: "Kid A", Track: 1})
	require.NoError(t, err)
	assert.Equal(t, "Kid A/Radiohead - Kid A - 01", got)
}
