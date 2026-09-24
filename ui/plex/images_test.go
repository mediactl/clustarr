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

// TestImagesMovie is GET .../{ratingKey}/images: the artwork gallery
// (research §7), poster and fanart only -- the fixture movie has no logo
// fetched.
func TestImagesMovie(t *testing.T) {
	h := newTestHandler(t, externalURLFixture, fixtureMovie())
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/images")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "images")
}

// TestImagesEpisodeEmpty proves an episode -- which has no artwork field
// (api/catalog/v1alpha1/episode_types.go) -- answers 200 with an empty
// gallery rather than 404: the ratingKey resolves to a real object, it
// simply has nothing to report yet.
func TestImagesEpisodeEmpty(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(episodes[0].UID)+"/images")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t,
		`{"MediaContainer":{"offset":0,"totalSize":0,"identifier":"tv.plex.agents.custom.clustarr.tv","size":0,"Image":[]}}`,
		rec.Body.String())
}
