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

package tvdb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// TestSearchSeriesMapsTheRecordedResponse runs the search against a
// response recorded from TheTVDB's live /search (2026-09-29).
func TestSearchSeriesMapsTheRecordedResponse(t *testing.T) {
	login, _ := os.ReadFile("../../../../test/data/metadata/tvdb/login.json")
	body, err := os.ReadFile("../../../../test/data/metadata/tvdb/search_breaking_bad.json")
	require.NoError(t, err)
	var gotQuery, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/search":
			gotQuery, gotType = r.URL.Query().Get("query"), r.URL.Query().Get("type")
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := tvdb.New("key", "", srv.Client(), srv.URL, metadata.NewLimiter(1000, 1))

	hits, err := c.SearchSeries(context.Background(), "breaking bad")
	require.NoError(t, err)
	require.Equal(t, "breaking bad", gotQuery)
	require.Equal(t, "series", gotType)
	require.Len(t, hits, 3)
	require.Equal(t, "81189", hits[0].IDs[metadata.KeyTVDB])
	require.Equal(t, "Breaking Bad", hits[0].Title)
	require.Equal(t, int32(2008), hits[0].Year)
	require.Equal(t, "https://artworks.thetvdb.com/banners/posters/81189-10.jpg", hits[0].Poster)
}

// TestSearchSeriesDropsAHitWithNoID: a hit the add form could not key an
// item by is not a result at all.
func TestSearchSeriesDropsAHitWithNoID(t *testing.T) {
	login, _ := os.ReadFile("../../../../test/data/metadata/tvdb/login.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			_, _ = w.Write(login)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":[{"name":"No Id"},{"tvdb_id":"0","name":"Zero"},{"tvdb_id":"42","name":"Kept","year":""}]}`))
	}))
	t.Cleanup(srv.Close)
	c := tvdb.New("key", "", srv.Client(), srv.URL, metadata.NewLimiter(1000, 1))

	hits, err := c.SearchSeries(context.Background(), "x")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "42", hits[0].IDs[metadata.KeyTVDB])
	require.Zero(t, hits[0].Year, "a blank year is no year, not an error")
}
