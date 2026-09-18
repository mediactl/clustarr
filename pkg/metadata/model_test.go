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

package metadata_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestRatingsRoundTripThroughJSONWithoutFloatDrift(t *testing.T) {
	m := metadata.Movie{
		Title: "Inception",
		Ratings: metadata.Ratings{
			"tmdb": {Source: "tmdb", ValueCentis: 837, Votes: 36892, Kind: "user"},
			"imdb": {Source: "imdb", ValueCentis: 880, Votes: 2400000, Kind: "user"},
		},
	}

	data, err := json.Marshal(m)
	require.NoError(t, err)

	var got metadata.Movie
	require.NoError(t, json.Unmarshal(data, &got))

	require.Equal(t, int32(837), got.Ratings["tmdb"].ValueCentis)
	require.Equal(t, int32(880), got.Ratings["imdb"].ValueCentis)
}
