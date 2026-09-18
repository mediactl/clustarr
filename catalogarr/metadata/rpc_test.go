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

package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

func newTestBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus
}

func TestServeRPCLookupReturnsTheProviderDocument(t *testing.T) {
	body, err := os.ReadFile("../../testdata/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	reg := &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "27205"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))

	require.Empty(t, resp.Error)
	var got pkgmetadata.Movie
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	require.Equal(t, "Inception", got.Title)
}

func TestServeRPCLookupReturnsAnErrorStringOnFailure(t *testing.T) {
	reg := &pkgmetadata.Registry{} // no providers configured
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "1"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.NotEmpty(t, resp.Error)
}
