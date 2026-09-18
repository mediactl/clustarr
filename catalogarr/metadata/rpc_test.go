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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

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

type stubArtistProvider struct{ hit pkgmetadata.SearchHit }

func (p stubArtistProvider) Name() string { return "musicbrainz" }
func (p stubArtistProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p stubArtistProvider) SearchArtists(context.Context, string) ([]pkgmetadata.SearchHit, error) {
	return []pkgmetadata.SearchHit{p.hit}, nil
}

func (p stubArtistProvider) Artist(context.Context, string) (*pkgmetadata.Artist, error) {
	return nil, pkgmetadata.ErrNotFound
}

func (p stubArtistProvider) Albums(context.Context, string) ([]pkgmetadata.Album, error) {
	return nil, nil
}

func (p stubArtistProvider) Album(context.Context, string) (*pkgmetadata.Album, error) {
	return nil, pkgmetadata.ErrNotFound
}

func TestServeRPCSearchDispatchesByKind(t *testing.T) {
	reg := &pkgmetadata.Registry{
		Artists: []pkgmetadata.ArtistProvider{stubArtistProvider{hit: pkgmetadata.SearchHit{Title: "Radiohead"}}},
	}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindArtist, Text: "Radiohead"}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))
	require.Len(t, resp.Results, 1)
	var hit pkgmetadata.SearchHit
	require.NoError(t, json.Unmarshal(resp.Results[0], &hit))
	require.Equal(t, "Radiohead", hit.Title)
}

func TestServeRPCSearchReportsUnsupportedKinds(t *testing.T) {
	reg := &pkgmetadata.Registry{}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindSeries, Text: "anything"}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))
	require.NotEmpty(t, resp.Error, "SeriesProvider has no search method (spec-pinned; see pkg/metadata go doc)")
}

// TestServeRPCSearchReportsAlbumAsUnsupported: unlike the brief's original
// draft, ArtistProvider (go doc ./pkg/metadata) has no SearchAlbums method --
// only SearchArtists (by text) and Albums(mbArtistID) (list, not search, of
// a known artist's albums). There is no provider surface a MediaKindAlbum
// search could call, so it is unsupported exactly like series, not routed
// to a nonexistent method.
func TestServeRPCSearchReportsAlbumAsUnsupported(t *testing.T) {
	reg := &pkgmetadata.Registry{
		Artists: []pkgmetadata.ArtistProvider{stubArtistProvider{hit: pkgmetadata.SearchHit{Title: "Radiohead"}}},
	}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindAlbum, Text: "OK Computer"}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))
	require.NotEmpty(t, resp.Error)
}

type stubResolver struct{ add pkgmetadata.ExternalIDs }

func (p stubResolver) Name() string                           { return "wikidata" }
func (p stubResolver) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p stubResolver) Resolve(context.Context, commonv1.MediaKind, pkgmetadata.ExternalIDs) (pkgmetadata.ExternalIDs, error) {
	return p.add, nil
}

func TestServeRPCResolveMergesEveryResolver(t *testing.T) {
	reg := &pkgmetadata.Registry{Resolvers: []pkgmetadata.IDResolver{
		stubResolver{add: pkgmetadata.ExternalIDs{"imdb": "tt1375666"}},
	}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "27205"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataResolve, req, &resp))
	require.Equal(t, "27205", resp.IDs["tmdb"])
	require.Equal(t, "tt1375666", resp.IDs["imdb"])
}

type stubSeriesProvider struct {
	episodes  []pkgmetadata.Episode
	err       error
	wantOrder string
}

func (p stubSeriesProvider) Name() string { return "tvdb" }
func (p stubSeriesProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p stubSeriesProvider) Series(context.Context, string) (*pkgmetadata.Series, error) {
	return nil, pkgmetadata.ErrNotFound
}

func (p stubSeriesProvider) Episodes(_ context.Context, tvdbID, order string) ([]pkgmetadata.Episode, error) {
	if p.wantOrder != "" && order != p.wantOrder {
		return nil, fmt.Errorf("unexpected order %q", order)
	}
	return p.episodes, p.err
}

func (p stubSeriesProvider) Updates(context.Context, time.Time) ([]string, error) { return nil, nil }

func TestServeRPCLookupListsEpisodesForTaskC6(t *testing.T) {
	reg := &pkgmetadata.Registry{Series: []pkgmetadata.SeriesProvider{stubSeriesProvider{
		wantOrder: "absolute",
		episodes: []pkgmetadata.Episode{
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Two"},
		},
	}}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindEpisode,
		IDs:  map[string]string{"tvdb": "121361", "order": "absolute"},
	}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))

	require.Empty(t, resp.Error)
	require.Equal(t, "tvdb", resp.Provider)
	require.Len(t, resp.Results, 2)
	var ep pkgmetadata.Episode
	require.NoError(t, json.Unmarshal(resp.Results[0], &ep))
	require.Equal(t, "Pilot", ep.Title)
}

func TestServeRPCLookupEpisodesRequiresATVDBID(t *testing.T) {
	reg := &pkgmetadata.Registry{}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"order": "official"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.NotEmpty(t, resp.Error)
}
