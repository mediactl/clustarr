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

package plex_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMetadataMovie is GET /plex/movies/library/metadata/{ratingKey}: the
// full movie object (spec §D.5), including Rating[] filtered to the four
// sources ruling R4 sends Plex and rounded to one decimal (tmdb's
// 831/100.0 = 8.31 -> 8.3).
func TestMetadataMovie(t *testing.T) {
	movie := fixtureMovie()
	h := newTestHandler(t, externalURLFixture, movie)

	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID))
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_movie")
}

// TestMetadataMovieNotFound is the 404 half of research §3's return-code
// table: an unknown ratingKey.
func TestMetadataMovieNotFound(t *testing.T) {
	h := newTestHandler(t, externalURLFixture, fixtureMovie())
	rec := getJSON(t, h, "/plex/movies/library/metadata/99999999-9999-9999-9999-999999999999")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestMetadataShowIncludeChildren is GET .../{ratingKey}?includeChildren=1
// for the show: Children holds one Metadata per season (research §5.2, "a
// show returns its seasons"), not the seasons' own episodes.
func TestMetadataShowIncludeChildren(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"?includeChildren=1")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_show")
}

// TestMetadataSeason is GET .../{seasonRatingKey}: spec §D.3's
// "<seriesUID>-s<NN>" ratingKey.
func TestMetadataSeason(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"-s01")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_season")
}

// TestMetadataEpisode is GET .../{episodeRatingKey}: parent/grandparent
// fields resolved through the owning season and series.
func TestMetadataEpisode(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(episodes[0].UID))
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "metadata_episode")
}
