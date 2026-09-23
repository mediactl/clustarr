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

// The music-search and book-search answers name the work through Newznab's
// artist/album/author/publisher attrs. They land on typed fields (first value
// wins) and stay verbatim in Attrs, alongside the attrs with no typed field.
func TestParseItemNonVideoAttrs(t *testing.T) {
	rels := parseResultsFile(t, "../../testdata/torznab/nonvideo_search.xml")
	require.Len(t, rels, 2)

	album := rels[0]
	require.Equal(t, "Radiohead", album.Artist)
	require.Equal(t, "Kid A", album.Album)
	require.Equal(t, "Parlophone", album.Publisher)
	require.Empty(t, album.Author)
	require.Equal(t, []string{"10"}, album.Attrs["tracks"])

	book := rels[1]
	require.Equal(t, "Frank Herbert", book.Author, "a repeated attr keeps its first value")
	require.Equal(t, []string{"Frank Herbert", "Brian Herbert"}, book.Attrs["author"])
	require.Equal(t, "Chilton Books", book.Publisher)
	require.Empty(t, book.Artist)
	require.Empty(t, book.Album)
	require.Equal(t, []string{"Dune"}, book.Attrs["booktitle"])
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

// TestParseItemMalformedNumericValuesDegradeGracefully covers the "one bad
// value doesn't ruin the whole item" contract for numeric fields that
// aren't wrapped in a *T and so can't simply be left nil: an out-of-band
// string in a numeric slot must leave that field at its zero value (or, for
// a pointer-typed attr field, nil) and must not turn into a decode error or
// a panic, exactly like every other malformed torznab:/newznab: attr this
// package already tolerates (see applyAttr's use of parseInt32/parseFloat64).
func TestParseItemMalformedNumericValuesDegradeGracefully(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		check func(t *testing.T, r torznab.Release)
	}{
		{
			name: "seeders attr is not a number",
			body: `<item><title>x</title><guid>g</guid><attr name="seeders" value="not-a-number"/></item>`,
			check: func(t *testing.T, r torznab.Release) {
				require.Nil(t, r.Seeders)
			},
		},
		{
			name: "size element is a humanized string, not a number",
			body: `<item><title>x</title><guid>g</guid><size>12.5GB</size></item>`,
			check: func(t *testing.T, r torznab.Release) {
				require.Equal(t, int64(0), r.Size)
			},
		},
		{
			name: "downloadvolumefactor attr is not a number",
			body: `<item><title>x</title><guid>g</guid><attr name="downloadvolumefactor" value="free"/></item>`,
			check: func(t *testing.T, r torznab.Release) {
				require.Nil(t, r.DownloadVolumeFactor)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r torznab.Release
			require.NotPanics(t, func() {
				var err error
				r, err = torznab.ParseItem(strings.NewReader(tc.body))
				require.NoError(t, err, "a malformed numeric value must degrade, not fail the whole parse")
			})
			tc.check(t, r)
		})
	}
}

// TestParseResultsMalformedNumericValueInOneItemDoesNotFailTheWholeFeed is
// the ParseResults-level counterpart: a bad value inside one <item> among
// several must not prevent the other items in the same feed from parsing.
func TestParseResultsMalformedNumericValueInOneItemDoesNotFailTheWholeFeed(t *testing.T) {
	const body = `<rss><channel>
		<item><title>bad</title><guid>g1</guid><size>12.5GB</size><attr name="seeders" value="not-a-number"/></item>
		<item><title>good</title><guid>g2</guid><size>100</size></item>
	</channel></rss>`

	var rels []torznab.Release
	require.NotPanics(t, func() {
		var err error
		rels, err = torznab.ParseResults(strings.NewReader(body))
		require.NoError(t, err)
	})
	require.Len(t, rels, 2)
	require.Equal(t, int64(0), rels[0].Size)
	require.Nil(t, rels[0].Seeders)
	require.Equal(t, int64(100), rels[1].Size)
}

// TestParseItemSkipsNonNumericCategoriesButKeepsValidOnes covers the same
// encoding/xml hard-abort risk as Size, but for the base <category>
// elements: a non-numeric category id (some indexers, notably certain
// Jackett/Cardigann definitions, emit a slug like "tv/hd" instead of the
// numeric Newznab id) must be skipped, not abort the whole item's parse,
// while a well-formed sibling <category> is still kept.
func TestParseItemSkipsNonNumericCategoriesButKeepsValidOnes(t *testing.T) {
	const body = `<item><title>x</title><guid>g</guid><category>tv/hd</category><category>5040</category></item>`

	var r torznab.Release
	require.NotPanics(t, func() {
		var err error
		r, err = torznab.ParseItem(strings.NewReader(body))
		require.NoError(t, err, "a non-numeric <category> must degrade, not fail the whole parse")
	})
	require.Equal(t, []newznab.CategoryID{newznab.CatTVHD}, r.Categories)
}

// TestParseResultsMalformedCategoryInOneItemDoesNotFailTheWholeFeed is the
// ParseResults-level counterpart of the test above: a bad <category> value
// inside one item among several must not prevent the other items in the
// same feed from parsing, and must not corrupt that item's own valid
// categories.
func TestParseResultsMalformedCategoryInOneItemDoesNotFailTheWholeFeed(t *testing.T) {
	const body = `<rss><channel>
		<item><title>bad</title><guid>g1</guid><category>tv/hd</category><category>5040</category></item>
		<item><title>good</title><guid>g2</guid><category>2000</category></item>
	</channel></rss>`

	var rels []torznab.Release
	require.NotPanics(t, func() {
		var err error
		rels, err = torznab.ParseResults(strings.NewReader(body))
		require.NoError(t, err)
	})
	require.Len(t, rels, 2)
	require.Equal(t, []newznab.CategoryID{newznab.CatTVHD}, rels[0].Categories)
	require.Equal(t, []newznab.CategoryID{newznab.CatMovies}, rels[1].Categories)
}

// TestParseItemDuplicateCategoriesAreDeduped proves the base <category>
// loop dedups against itself, not only against the separate
// torznab:attr-category loop (see TestParseItemWithAttrs, whose fixture
// carries the same ids in both places already).
func TestParseItemDuplicateCategoriesAreDeduped(t *testing.T) {
	const body = `<item><title>x</title><guid>g</guid><category>5040</category><category>5040</category></item>`

	r, err := torznab.ParseItem(strings.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, []newznab.CategoryID{newznab.CatTVHD}, r.Categories)
}

// TestParseItemMalformedEnclosureLengthDoesNotAbortParse covers the same
// hard-abort risk in the nested wireEnclosure.Length attribute: it is
// never surfaced on Release (there is no typed field for it), but a
// malformed value must not prevent the rest of the item -- including
// Link, populated from the same <enclosure> element -- from parsing.
func TestParseItemMalformedEnclosureLengthDoesNotAbortParse(t *testing.T) {
	const body = `<item><title>x</title><guid>g</guid>` +
		`<enclosure url="https://tracker.example.invalid/dl/1" length="not-a-number" type="application/x-bittorrent"/>` +
		`</item>`

	var r torznab.Release
	require.NotPanics(t, func() {
		var err error
		r, err = torznab.ParseItem(strings.NewReader(body))
		require.NoError(t, err, "a malformed enclosure length must degrade, not fail the whole parse")
	})
	require.Equal(t, "https://tracker.example.invalid/dl/1", r.Link)
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
