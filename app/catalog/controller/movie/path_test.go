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

package movie_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/pkg/naming"
)

// TestPath's expected folder literal is the real Jellyfin preset output,
// confirmed against test/data/naming/dialects.json's golden
// ("The Matrix (1999) [tmdbid-603]") rather than guessed: [tmdbid-<id>], not
// Plex's {tmdb-<id>}.
func TestPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	ctx := naming.Context{Kind: commonv1.MediaKindMovie, Title: "Inception", Year: 2010, TmdbID: "27205"}

	got, err := movie.Path("/data/media/movies", nil, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (2010) [tmdbid-27205]", got)

	override := "Inception (Director's Cut)"
	got, err = movie.Path("/data/media/movies", &override, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (Director's Cut)", got)

	empty := ""
	got, err = movie.Path("/data/media/movies", &empty, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (2010) [tmdbid-27205]", got, "an empty override falls back to the engine")
}
