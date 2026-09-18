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

package torznab_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestParseItemWithAttrs(t *testing.T) {
	rels := parseResultsFile(t, "../../testdata/torznab/search_with_attrs.xml")
	require.Len(t, rels, 1)
	r := rels[0]

	require.Equal(t, "Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP", r.Title)
	require.Equal(t, "https://tracker.example.invalid/details/123", r.GUID)
	require.Equal(t, "https://tracker.example.invalid/details/123#comments", r.CommentURL)
	require.Equal(t, int64(1234567890), r.Size)
	require.Equal(t, time.Date(2026, 9, 17, 10, 11, 12, 0, time.UTC), r.PubDate.UTC())
	require.ElementsMatch(t, []newznab.CategoryID{newznab.CatMovies, newznab.CatMoviesHD}, r.Categories)

	require.NotNil(t, r.Seeders)
	require.Equal(t, int32(12), *r.Seeders)
	require.NotNil(t, r.Peers)
	require.Equal(t, int32(15), *r.Peers)
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", r.InfoHash)
	require.Contains(t, r.MagnetURL, "urn:btih:")
	require.NotNil(t, r.DownloadVolumeFactor)
	require.Equal(t, 0.0, *r.DownloadVolumeFactor)
	require.NotNil(t, r.MinimumSeedTime)
	require.Equal(t, int64(604800), *r.MinimumSeedTime)
	require.NotNil(t, r.Grabs)
	require.Equal(t, int32(7), *r.Grabs)

	require.Equal(t, "tt0133093", r.IDs["imdb"])
	require.Equal(t, "603", r.IDs["tmdb"])
	require.Equal(t, "139", r.IDs["tvmazeid"])

	require.Equal(t, []string{"Action, Sci-Fi"}, r.Attrs["genre"])
}

func TestParseItemWithoutAttrsHasNoPanicAndNilOptionalFields(t *testing.T) {
	rels := parseResultsFile(t, "../../testdata/torznab/search_without_attrs.xml")
	require.Len(t, rels, 1)
	r := rels[0]

	require.Equal(t, "Some.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP", r.Title)
	require.Nil(t, r.Seeders)
	require.Nil(t, r.Peers)
	require.Empty(t, r.InfoHash)
	require.Empty(t, r.IDs)
}

func TestParseItemToleratesTheNonCanonicalTorznabNamespace(t *testing.T) {
	rels := parseResultsFile(t, "../../testdata/torznab/namespace_variant.xml")
	require.Len(t, rels, 1)
	require.NotNil(t, rels[0].Seeders)
	require.Equal(t, int32(4), *rels[0].Seeders)
	require.Equal(t, "fedcba9876543210fedcba9876543210fedcba9", rels[0].InfoHash)
}

func TestParseItemUsenetNzbAttrs(t *testing.T) {
	rels := parseResultsFile(t, "../../testdata/newznab/usenet_search.xml")
	require.Len(t, rels, 1)
	r := rels[0]
	require.Equal(t, "alt.binaries.sounds.flac", r.Group)
	require.Equal(t, "uploader@example.invalid", r.Poster)
	require.NotNil(t, r.UsenetDate)
	require.NotNil(t, r.Password)
	require.Equal(t, int32(0), *r.Password)
	require.NotNil(t, r.NFO)
	require.Equal(t, int32(1), *r.NFO)
}

func TestParseResultsMalformedInputNeverPanics(t *testing.T) {
	cases := map[string]string{
		"garbage":   "not xml at all {{{",
		"truncated": `<?xml version="1.0"?><rss><channel><item><title>x`,
		"empty":     "",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				_, _ = torznab.ParseResults(strings.NewReader(body))
			})
		})
	}
}

func TestParseItemBadPubDateReturnsErrorNotPanic(t *testing.T) {
	const body = `<item><title>x</title><guid>g</guid><pubDate>not-a-date</pubDate></item>`
	require.NotPanics(t, func() {
		_, err := torznab.ParseItem(strings.NewReader(body))
		require.Error(t, err)
	})
}

func parseResultsFile(t *testing.T, path string) []torznab.Release {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	rels, err := torznab.ParseResults(f)
	require.NoError(t, err)
	return rels
}
