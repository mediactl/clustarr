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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestQueryValuesBuildsTheWireParameters(t *testing.T) {
	season := 3
	q := torznab.Query{
		Type: torznab.ModeMovieSearch, Q: "some movie",
		Categories: []newznab.CategoryID{newznab.CatMovies},
		IMDBID:     "tt0133093", Limit: 50,
	}
	v := q.Values("secret")
	assert.Equal(t, "movie", v.Get("t"))
	assert.Equal(t, "secret", v.Get("apikey"))
	assert.Equal(t, "some movie", v.Get("q"))
	assert.Equal(t, "2000", v.Get("cat"))
	assert.Equal(t, "0133093", v.Get("imdbid"), "wire imdbid drops the tt prefix")
	assert.Equal(t, "50", v.Get("limit"))
	assert.Equal(t, "1", v.Get("extended"))
	_ = season
}

func TestQueryValuesSeasonAndEpisode(t *testing.T) {
	season := 3
	q := torznab.Query{Type: torznab.ModeTVSearch, Season: &season, Episode: "05"}
	v := q.Values("")
	assert.Equal(t, "3", v.Get("season"))
	assert.Equal(t, "05", v.Get("ep"))
	assert.Empty(t, v.Get("apikey"), "an empty apikey is omitted")
}

func TestQueryValuesMultipleCategoriesAreCommaJoined(t *testing.T) {
	q := torznab.Query{Type: torznab.ModeSearch, Categories: []newznab.CategoryID{newznab.CatMovies, newznab.CatMoviesHD}}
	v := q.Values("")
	assert.Equal(t, "2000,2040", v.Get("cat"))
}

func TestQueryValidateRejectsAnUnavailableMode(t *testing.T) {
	f, err := os.Open("../../test/data/torznab/caps.xml")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	caps, err := torznab.ParseCaps(f)
	require.NoError(t, err)

	require.NoError(t, torznab.Query{Type: torznab.ModeMovieSearch}.Validate(caps))
	require.Error(t, torznab.Query{Type: torznab.ModeMusicSearch}.Validate(caps))
}
