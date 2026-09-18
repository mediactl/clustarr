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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist/imdbcsv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	tests := map[string]string{
		"empty":           "",
		"header only":     "Const,Title,Year,Title Type\n",
		"missing columns": "Const,Title\ntt1,The Matrix\n",
		"truncated quote": "Const,Title,Year,Title Type\ntt1,\"unterminated,1999,movie\n",
		"ragged row":      "Const,Title,Year,Title Type\ntt1\n",
		"garbage":         "\x00\x01\xff not csv at all",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				_, _ = imdbcsv.Parse(strings.NewReader(input), commonv1.MediaKindMovie)
			})
		})
	}
}
