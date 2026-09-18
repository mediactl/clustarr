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

package imdbcsv_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/imdbcsv"
)

const csvFixture = `Const,Your Rating,Date Rated,Title,URL,Title Type,IMDb Rating,Runtime (mins),Year,Genres,Num Votes,Release Date,Directors
tt0133093,,,The Matrix,https://www.imdb.com/title/tt0133093/,movie,8.7,136,1999,"Action, Sci-Fi",2000000,1999-03-31,"Lana Wachowski, Lilly Wachowski"
tt0903747,,,Breaking Bad,https://www.imdb.com/title/tt0903747/,tvSeries,9.5,49,2008,"Crime, Drama",2100000,2008-01-20,
tt9999999,,,Some Video Game,https://www.imdb.com/title/tt9999999/,videoGame,7.0,,2020,Action,500,,
`

func TestParseMapsConstTitleYearAndSkipsUnknownTitleTypes(t *testing.T) {
	items, err := imdbcsv.Parse(strings.NewReader(csvFixture), commonv1.MediaKindMovie)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "The Matrix", items[0].Title)
	assert.Equal(t, int32(1999), items[0].Year)
	assert.Equal(t, "tt0133093", items[0].ExternalIDs.IMDb)

	series, err := imdbcsv.Parse(strings.NewReader(csvFixture), commonv1.MediaKindSeries)
	require.NoError(t, err)
	require.Len(t, series, 1)
	assert.Equal(t, "Breaking Bad", series[0].Title)
}

func TestParseMalformedInputNeverPanics(t *testing.T) {
	tests := map[string]struct {
		input string

		// wantErrContains, when non-empty, asserts Parse returned an error
		// whose message contains this substring. When empty, Parse must
		// return no error at all.
		wantErrContains string

		// wantItems is the expected item count when wantErrContains is
		// empty (Parse succeeded).
		wantItems int
	}{
		"empty": {
			input:           "",
			wantErrContains: "read header",
		},
		"header only": {
			input:     "Const,Title,Year,Title Type\n",
			wantItems: 0,
		},
		"missing columns": {
			// the header has no "Year" or "Title Type" column: Parse must
			// report the specific missing column rather than silently
			// returning nothing or guessing a layout.
			input:           "Const,Title\ntt1,The Matrix\n",
			wantErrContains: `missing column "Year"`,
		},
		"truncated quote": {
			input:           "Const,Title,Year,Title Type\ntt1,\"unterminated,1999,movie\n",
			wantErrContains: "read row",
		},
		"ragged row": {
			// the data row has fewer fields than the header; encoding/csv
			// (FieldsPerRecord=-1) lets it through without erroring, and
			// Parse must skip it -- not panic indexing past the row's
			// length, and not error either, since a short row is not by
			// itself invalid CSV.
			input:     "Const,Title,Year,Title Type\ntt1\n",
			wantItems: 0,
		},
		"garbage": {
			// binary noise with no comma becomes a single-field "header"
			// that matches none of the required column names.
			input:           "\x00\x01\xff not csv at all",
			wantErrContains: "missing column",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var items []importlist.Item
			var err error
			assert.NotPanics(t, func() {
				items, err = imdbcsv.Parse(strings.NewReader(tt.input), commonv1.MediaKindMovie)
			})

			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				assert.Nil(t, items)
				return
			}
			require.NoError(t, err)
			assert.Len(t, items, tt.wantItems)
		})
	}
}
