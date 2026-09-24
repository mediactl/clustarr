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
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist/plex"
)

func TestFetchPagesUntilAShortPage(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal(t, "/library/sections/watchlist/all", r.URL.Path)
		assert.Equal(t, "tok", r.URL.Query().Get("X-Plex-Token"))
		assert.Equal(t, "1", r.URL.Query().Get("type"))
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		fixture := "../../../test/data/importlist/plex/watchlist_page1.json"
		if start != 0 {
			fixture = "../../../test/data/importlist/plex/watchlist_page2.json"
		}
		b, err := os.ReadFile(fixture)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	w, err := plex.New("plex-watchlist", commonv1.MediaKindMovie, "tok", "client-1", plex.WithBaseURL(srv.URL), plex.WithPageSize(2))
	require.NoError(t, err)

	items, err := w.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 3)
	assert.Equal(t, "Dune", items[0].Title)
	assert.Equal(t, "tt1160419", items[0].ExternalIDs.IMDb)
	assert.Equal(t, "438631", items[0].ExternalIDs.TMDB)
	assert.Equal(t, 2, requests) // stops once a page comes back shorter than the page size
}

func TestFetchSeriesUsesTypeFilter2(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2", r.URL.Query().Get("type"))
		b, err := os.ReadFile("../../../test/data/importlist/plex/watchlist_series_page1.json")
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	w, err := plex.New("plex-watchlist-series", commonv1.MediaKindSeries, "tok", "client-1", plex.WithBaseURL(srv.URL))
	require.NoError(t, err)

	items, err := w.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Breaking Bad", items[0].Title)
	assert.Equal(t, "tt0903747", items[0].ExternalIDs.IMDb)
	assert.Equal(t, "81189", items[0].ExternalIDs.TVDB)
}

func TestFetchUnexpectedStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w, err := plex.New("plex-watchlist", commonv1.MediaKindMovie, "tok", "client-1", plex.WithBaseURL(srv.URL))
	require.NoError(t, err)

	items, err := w.Fetch(t.Context())
	require.Error(t, err)
	assert.Nil(t, items)
	assert.Contains(t, err.Error(), "500")
}
