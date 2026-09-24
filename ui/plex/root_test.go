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

// TestRootMovies is spec §D.1/§D.2's GET /plex/movies: the MediaProvider
// declaring type 1 only, and the match/metadata Feature keys.
func TestRootMovies(t *testing.T) {
	h := newTestHandler(t, externalURLFixture)
	rec := getJSON(t, h, "/plex/movies")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	requireGolden(t, rec, "root_movies")
}

// TestRootTV is GET /plex/tv: types 2 (show), 3 (season) and 4 (episode),
// per spec §D.1's "one-parent-type-per-provider" split.
func TestRootTV(t *testing.T) {
	h := newTestHandler(t, externalURLFixture)
	rec := getJSON(t, h, "/plex/tv")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "root_tv")
}

// TestRootWithoutExternalURL is spec §D.1's 503: "the roots return 503
// with a body naming the flag" when --plex-provider is on but
// --external-url was never set.
func TestRootWithoutExternalURL(t *testing.T) {
	h := newTestHandler(t, "")

	for _, path := range []string{"/plex/movies", "/plex/tv"} {
		rec := getJSON(t, h, path)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, "path %s", path)
		require.JSONEq(t, `{"error":"--external-url is required for the Plex provider"}`, rec.Body.String())
	}
}
