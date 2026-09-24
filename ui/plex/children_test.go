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

// TestChildrenSeasonPage2Of3 is the brief's own "children page 2 of 3":
// season 1 of the fixture series has three episodes; with
// X-Plex-Container-Size=1 and X-Plex-Container-Start=1 the response is
// page 2 -- the second episode alone -- with the true totalSize (3) still
// reported (spec §D.2).
func TestChildrenSeasonPage2Of3(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"-s01/children",
		"X-Plex-Container-Size", "1", "X-Plex-Container-Start", "1")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "1", rec.Header().Get("X-Plex-Container-Start"))
	require.Equal(t, "3", rec.Header().Get("X-Plex-Container-Total-Size"))
	requireGolden(t, rec, "children_page2")
}

// TestChildrenShowIsSeasons is research §8: a show's children are its
// seasons, default paging (0/20) fitting both in one page.
func TestChildrenShowIsSeasons(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"/children")
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		MediaContainer struct {
			Size      int `json:"size"`
			TotalSize int `json:"totalSize"`
			Metadata  []struct {
				Type string `json:"type"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &body))
	require.Equal(t, 2, body.MediaContainer.TotalSize)
	require.Len(t, body.MediaContainer.Metadata, 2)
	for _, m := range body.MediaContainer.Metadata {
		require.Equal(t, "season", m.Type)
	}
}

// TestGrandchildrenShowIsEveryEpisode is research §8: a show's
// grandchildren are its episodes, flattened across every season (six here,
// three per season).
func TestGrandchildrenShowIsEveryEpisode(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"/grandchildren")
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		MediaContainer struct {
			TotalSize int `json:"totalSize"`
			Metadata  []struct {
				Title string `json:"title"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &body))
	require.Equal(t, len(episodes), body.MediaContainer.TotalSize)
	require.Equal(t, "The Tide Comes In", body.MediaContainer.Metadata[0].Title)
	require.Equal(t, "Harbor Lights", body.MediaContainer.Metadata[len(episodes)-1].Title)
}

// TestGrandchildrenOnSeasonIsNotFound: spec §D.2 names grandchildren "a
// show's episodes"; a season ratingKey has no grandchildren of its own.
func TestGrandchildrenOnSeasonIsNotFound(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"-s01/grandchildren")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestPagingStartAtOrPastTotalIsEmpty is Review Focus 3 (spec §D.2): a
// start at or past the total answers an empty Metadata array with the true
// totalSize, not a 400 or a truncated total.
func TestPagingStartAtOrPastTotalIsEmpty(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"/children",
		"X-Plex-Container-Start", "5")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t,
		`{"MediaContainer":{"offset":5,"totalSize":2,"identifier":"tv.plex.agents.custom.clustarr.tv","size":0,"Metadata":[]}}`,
		rec.Body.String())

	// Exactly at the boundary (start == total) is the same case.
	recAtBoundary := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"/children",
		"X-Plex-Container-Start", "2")
	require.Equal(t, http.StatusOK, recAtBoundary.Code)
	require.JSONEq(t,
		`{"MediaContainer":{"offset":2,"totalSize":2,"identifier":"tv.plex.agents.custom.clustarr.tv","size":0,"Metadata":[]}}`,
		recAtBoundary.Body.String())
}
