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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/ui/plex"
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

// TestMetadataMalformedRatingKeyNotFound is TestMetadataMovieNotFound's
// twin for a ratingKey that is not even shaped like one:
// [ratingKeyPattern] (ui/plex/ratingkey.go) never matches a Kubernetes
// name's periods, so ParseRatingKey fails before any lookup runs, and the
// handler answers 404 the same as any other unresolvable ratingKey --
// never a 500 or a panic on a malformed path segment straight off the URL.
func TestMetadataMalformedRatingKeyNotFound(t *testing.T) {
	h := newTestHandler(t, externalURLFixture, fixtureMovie())
	rec := getJSON(t, h, "/plex/movies/library/metadata/not..a..key")
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

// TestRootsResolveOnlyTheirOwnTypes: each root answers only the types spec
// §D.1 gives it -- /plex/movies type 1, /plex/tv types 2, 3 and 4 -- on
// every route that resolves a ratingKey or a match. A show fetched through
// the movies root came back as a show under the movies identifier, which
// Plex would then attach to a movie library.
func TestRootsResolveOnlyTheirOwnTypes(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	objs := []client.Object{fixtureMovie(), series}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	h := newTestHandler(t, externalURLFixture, objs...)
	season := plex.SeasonKey(seriesUID, 1)
	episode := string(episodes[0].UID)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/plex/movies/library/metadata/" + string(movieUID), http.StatusOK},
		{"/plex/movies/library/metadata/" + string(seriesUID), http.StatusNotFound},
		{"/plex/movies/library/metadata/" + season, http.StatusNotFound},
		{"/plex/movies/library/metadata/" + episode, http.StatusNotFound},
		{"/plex/movies/library/metadata/" + string(seriesUID) + "/images", http.StatusNotFound},
		{"/plex/movies/library/metadata/" + season + "/images", http.StatusNotFound},
		{"/plex/tv/library/metadata/" + string(seriesUID), http.StatusOK},
		{"/plex/tv/library/metadata/" + season, http.StatusOK},
		{"/plex/tv/library/metadata/" + episode, http.StatusOK},
		{"/plex/tv/library/metadata/" + string(movieUID), http.StatusNotFound},
		{"/plex/tv/library/metadata/" + string(movieUID) + "/images", http.StatusNotFound},
	} {
		require.Equal(t, tc.want, getJSON(t, h, tc.path).Code, "GET %s", tc.path)
	}

	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{"/plex/movies/library/metadata/matches", map[string]any{"type": 2, "title": "Harborview"}},
		{"/plex/tv/library/metadata/matches", map[string]any{"type": 1, "title": "Skyfall Protocol"}},
	} {
		rec := postJSON(t, h, tc.path, tc.body)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Empty(t, matchedYears(t, rec.Body.Bytes()), "POST %s type %v", tc.path, tc.body["type"])
	}
}
