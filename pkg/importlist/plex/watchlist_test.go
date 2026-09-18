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
		fixture := "../../../testdata/importlist/plex/watchlist_page1.json"
		if start != 0 {
			fixture = "../../../testdata/importlist/plex/watchlist_page2.json"
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
