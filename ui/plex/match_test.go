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

// TestMatchByGuid is D.4 rule 1: a match request's guid ("tmdb://<id>")
// resolves straight to the movie with that spec.tmdbID, no title involved.
func TestMatchByGuid(t *testing.T) {
	h := newTestHandler(t, externalURLFixture, fixtureMovie())

	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
		"type": 1,
		"guid": "tmdb://424680",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "match_guid")
}

// TestMatchByTitleYear is D.4 rule 2: two movies share a normalised title
// ([fixtureMovie], [fixtureMovieRemake]); with manual=1 both come back,
// ordered exact-year first (2015, the request's own year) and the other
// -- sixteen years off -- after it.
func TestMatchByTitleYear(t *testing.T) {
	h := newTestHandler(t, externalURLFixture, fixtureMovie(), fixtureMovieRemake())

	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
		"type":   1,
		"title":  "Skyfall Protocol",
		"year":   2015,
		"manual": 1,
	})
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "match_title_year")
}

// TestMatchNoResult proves D.4's "no match returns an empty container" --
// not 404 -- so Plex falls through to the next provider in the agent.
func TestMatchNoResult(t *testing.T) {
	h := newTestHandler(t, externalURLFixture, fixtureMovie())

	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
		"type":  1,
		"title": "Nothing Like This Exists",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t,
		`{"MediaContainer":{"offset":0,"totalSize":0,"identifier":"tv.plex.agents.custom.clustarr.movies","size":0,"Metadata":[]}}`,
		rec.Body.String())
}

// TestMatchEpisodeByIndex is D.4 rule 3: an episode match resolves its show
// by grandparentTitle, then the episode by parentIndex/index.
func TestMatchEpisodeByIndex(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type":             4,
		"grandparentTitle": "Harborview",
		"parentIndex":      1,
		"index":            2,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
				Title     string `json:"title"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &body))
	require.Len(t, body.MediaContainer.Metadata, 1)
	require.Equal(t, "Low Water", body.MediaContainer.Metadata[0].Title)
	require.Equal(t, string(episodes[1].UID), body.MediaContainer.Metadata[0].RatingKey)
}
