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
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestParseCaps(t *testing.T) {
	f, err := os.Open("../../test/data/torznab/caps.xml")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	caps, err := torznab.ParseCaps(f)
	require.NoError(t, err)

	require.Equal(t, "Clustarr Test Indexer", caps.ServerTitle)
	require.Equal(t, 50, caps.LimitsDefault)
	require.Equal(t, 100, caps.LimitsMax)

	search := caps.Modes[torznab.ModeSearch]
	require.True(t, search.Available)
	require.Equal(t, []string{"q"}, search.SupportedParams)
	require.Equal(t, "raw", search.SearchEngine)

	movie := caps.Modes[torznab.ModeMovieSearch]
	require.True(t, movie.Available)
	require.Equal(t, []string{"q", "imdbid", "tmdbid"}, movie.SupportedParams)

	music := caps.Modes[torznab.ModeMusicSearch]
	require.False(t, music.Available)

	require.True(t, caps.Supports(torznab.ModeMovieSearch, "imdbid"))
	require.False(t, caps.Supports(torznab.ModeMusicSearch, "q"))

	require.Contains(t, caps.Categories, newznab.Category{
		ID: newznab.CatMovies, Name: "Movies",
		Sub: []newznab.SubCategory{{ID: newznab.CatMoviesHD, Name: "Movies/HD"}, {ID: newznab.CatMoviesUHD, Name: "Movies/UHD"}},
	})
	require.Equal(t, "Download doesn't count toward ratio", caps.Tags["freeleech"])
}

func TestParseCapsUsenetVariant(t *testing.T) {
	f, err := os.Open("../../test/data/newznab/caps.xml")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	caps, err := torznab.ParseCaps(f)
	require.NoError(t, err)

	music := caps.Modes[torznab.ModeMusicSearch]
	require.True(t, music.Available)

	search := caps.Modes[torznab.ModeSearch]
	require.Empty(t, search.SearchEngine, "no searchEngine attribute present -> raw search unsupported here")
}

func TestParseCapsMalformedInputNeverPanics(t *testing.T) {
	cases := map[string]string{
		"garbage":   "not xml at all {{{",
		"truncated": `<?xml version="1.0"?><caps><server title="x"`,
		"empty":     "",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				_, _ = torznab.ParseCaps(strings.NewReader(body))
			})
		})
	}
}

// A malformed caps document is a TYPED error: the <limits> attributes stay
// ints (a guessed page size is worse than a refusal), and every failure --
// a non-integer limit included -- matches ErrMalformedCaps, so the Indexer
// reconciler can name it rather than report a generic probe failure.
func TestParseCapsMalformedDocumentIsATypedError(t *testing.T) {
	cases := map[string]string{
		"non-integer max":     `<caps><limits default="50" max="lots"/></caps>`,
		"non-integer default": `<caps><limits default="fifty" max="100"/></caps>`,
		"garbage":             "not xml at all {{{",
		"truncated":           `<?xml version="1.0"?><caps><server title="x"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := torznab.ParseCaps(strings.NewReader(body))
			require.ErrorIs(t, err, torznab.ErrMalformedCaps)
		})
	}

	// The underlying cause survives the wrap.
	_, err := torznab.ParseCaps(strings.NewReader(`<caps><limits max="lots"/></caps>`))
	var numErr *strconv.NumError
	require.True(t, errors.As(err, &numErr), "want the strconv cause through the wrap, got %v", err)

	// A well-formed document with no <limits> at all is not malformed.
	caps, err := torznab.ParseCaps(strings.NewReader(`<caps><server title="x"/></caps>`))
	require.NoError(t, err)
	require.Zero(t, caps.LimitsMax)
}
