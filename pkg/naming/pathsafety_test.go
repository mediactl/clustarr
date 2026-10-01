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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

var allDialects = []naming.Dialect{naming.DialectJellyfin, naming.DialectPlex, naming.DialectEmby, naming.DialectKodi}

// TestTokenValuesNeverSplitAPathComponent renders provider-shaped titles
// through every real preset: a "/" or "\" in a title is one character of
// one component, never a separator, so "AC/DC" is one artist folder and a
// crowd-edited "x/../../../../etc" cannot climb out of the library. The
// *arrs' FileNameBuilder.CleanFileName turns both into "+".
func TestTokenValuesNeverSplitAPathComponent(t *testing.T) {
	cases := []struct {
		name string
		kind commonv1.MediaKind
		c    naming.Context
		file bool
		want string
	}{
		{"artist folder", commonv1.MediaKindArtist, naming.Context{ArtistName: "AC/DC"}, false, "AC+DC"},
		{"album folder", commonv1.MediaKindAlbum, naming.Context{ArtistName: "AC/DC", AlbumTitle: "Back in Black", Year: 1980}, false, "AC+DC/Back in Black (1980)"},
		{"track file", commonv1.MediaKindAlbum, naming.Context{ArtistName: "AC/DC", AlbumTitle: "Back in Black", Year: 1980, Track: 1, TrackTitle: "Hells Bells"}, true, "Back in Black (1980)/AC+DC - Back in Black - 01 - Hells Bells"},
		{"author folder, windows separator", commonv1.MediaKindAuthor, naming.Context{AuthorName: `a\b`}, false, "a+b"},
		{"book file", commonv1.MediaKindBook, naming.Context{AuthorName: "x/../../../../etc", BookTitle: "Either/Or"}, true, "Either+Or/x+..+..+..+..+etc"},
		{"audiobook folder", commonv1.MediaKindAudiobook, naming.Context{AuthorName: "x/../../../../etc", BookSeries: "../..", BookTitle: "Either/Or"}, false, "x+..+..+..+..+etc/..+../Either+Or"},
		{"comic folder", commonv1.MediaKindComic, naming.Context{ComicSeriesTitle: "x/../../../../etc"}, false, "x+..+..+..+..+etc"},
		{"issue file", commonv1.MediaKindIssue, naming.Context{ComicSeriesTitle: "Fables/Jack", IssueNumber: "001"}, true, "Fables+Jack/Fables+Jack c001"},
		{"series folder", commonv1.MediaKindSeries, naming.Context{SeriesTitle: "x/../../../../etc", SeriesYear: 2010, TvdbID: "1"}, false, "x+..+..+..+..+etc (2010)"},
	}
	for _, d := range allDialects {
		e := naming.NewEngine(naming.Config{Dialect: d})
		for _, tc := range cases {
			t.Run(string(d)+"/"+tc.name, func(t *testing.T) {
				var got string
				var err error
				if tc.file {
					got, err = e.BuildFile(tc.kind, tc.c)
				} else {
					got, err = e.BuildFolder(tc.kind, tc.c)
				}
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(got, tc.want), "got %q, want prefix %q", got, tc.want)
				require.NotContains(t, got, `\`)
				for _, comp := range strings.Split(got, "/") {
					require.NotEqual(t, "..", comp)
					require.NotEqual(t, ".", comp)
				}
			})
		}
	}
}

// TestEpisodeFileKeepsASlashedSeriesTitleInOneComponent is the series
// case through Sonarr-shaped metadata: TVDB titles the Irish drama
// "Love/Hate".
func TestEpisodeFileKeepsASlashedSeriesTitleInOneComponent(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{Kind: commonv1.MediaKindEpisode, SeriesTitle: "Love/Hate", SeriesYear: 2010, Season: 1, Episodes: []int{1}, EpisodeTitle: "Episode 1/6"}
	got, err := e.EpisodeFile(c)
	require.NoError(t, err)
	require.NotContains(t, got, "/")
	require.True(t, strings.HasPrefix(got, "Love+Hate (2010) - S01E01 - Episode 1 6"), got, "{Episode CleanTitle} spaces a slash, as Radarr's CleanTitle does")
}

// TestRenderRefusesADotOnlyComponent: a title that is nothing but dots
// renders a component the filesystem reads as the folder itself or its
// parent, so Render refuses it rather than guess where the item belongs.
// Dots inside a longer component are ordinary text.
func TestRenderRefusesADotOnlyComponent(t *testing.T) {
	for _, d := range allDialects {
		e := naming.NewEngine(naming.Config{Dialect: d})
		for _, title := range []string{".", "..", "...", ". ."} {
			_, err := e.BuildFolder(commonv1.MediaKindArtist, naming.Context{ArtistName: title})
			require.ErrorIs(t, err, naming.ErrUnsafeComponent, "%s artist %q", d, title)
			_, err = e.BuildFolder(commonv1.MediaKindAlbum, naming.Context{ArtistName: "Sigur Rós", AlbumTitle: title})
			require.ErrorIs(t, err, naming.ErrUnsafeComponent, "%s album %q", d, title)
			_, err = e.BuildFile(commonv1.MediaKindIssue, naming.Context{ComicSeriesTitle: title, IssueNumber: "001"})
			require.ErrorIs(t, err, naming.ErrUnsafeComponent, "%s comic %q", d, title)
		}
		got, err := e.SeriesFolder(naming.Context{SeriesTitle: "..", SeriesYear: 2020, TvdbID: "1"})
		require.NoError(t, err, "a dotted title beside the year is a name, not a parent")
		require.True(t, strings.HasPrefix(got, ".. (2020)"), got)
	}
	_, err := naming.NewEngine(naming.Config{Dialect: naming.DialectKodi}).
		Render("{Series TitleWithoutYear}", naming.Context{SeriesTitle: ".."})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent)
	_, err = naming.NewEngine(naming.Config{}).Render("../{Movie Title}", naming.Context{Title: "Heat"})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a template's own parent reference is refused too")
}
