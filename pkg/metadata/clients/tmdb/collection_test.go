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

package tmdb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

func serveFixture(t *testing.T, path, name string) *tmdb.Client {
	t.Helper()
	body, err := os.ReadFile("../../../../test/data/metadata/tmdb/" + name)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	return c
}

// A movie's details name its collection's poster and backdrop, which the
// Plex provider shows on the collection (recorded: Back to the Future).
func TestMovieCarriesItsCollectionsArtwork(t *testing.T) {
	m, err := serveFixture(t, "/movie/105", "movie-105.json").Movie(context.Background(), "105", "US")
	require.NoError(t, err)
	require.NotNil(t, m.Collection)
	require.Equal(t, "264", m.Collection.IDs[metadata.KeyTMDB])
	require.Equal(t, "Back to the Future Collection", m.Collection.Title)
	require.ElementsMatch(t, []metadata.Image{
		{Type: metadata.ImageTypePoster, URL: "https://image.tmdb.org/t/p/w500/5Xsu2o5IsZRuuxCEVZ9nVve21FP.jpg"},
		{Type: metadata.ImageTypeFanart, URL: "https://image.tmdb.org/t/p/original/c9C9Pg2QctyjZHRmS0P8rZg1OTA.jpg"},
	}, m.Collection.Images)
}

// A collection's summary is only in its own record, /collection/{id}.
func TestCollectionCarriesItsSummaryAndArtwork(t *testing.T) {
	col, err := serveFixture(t, "/collection/264", "collection_264.json").Collection(context.Background(), "264")
	require.NoError(t, err)
	require.Equal(t, "264", col.IDs[metadata.KeyTMDB])
	require.Equal(t, "Back to the Future Collection", col.Title)
	require.Contains(t, col.Overview, "Marty McFly")
	require.Len(t, col.Images, 2)
}

func TestCollectionMapsA404ToErrNotFound(t *testing.T) {
	_, err := serveFixture(t, "/collection/264", "collection_264.json").Collection(context.Background(), "999")
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestCollectionRefusesANonNumericID(t *testing.T) {
	_, err := serveFixture(t, "/collection/264", "collection_264.json").Collection(context.Background(), "x")
	require.Error(t, err)
}

var _ metadata.CollectionProvider = (*tmdb.Client)(nil)
