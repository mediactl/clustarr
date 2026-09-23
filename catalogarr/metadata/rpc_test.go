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
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

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
	require.Contains(t, resp.Error, "does not support kind")
}

// TestServeRPCSearchSaysWhenNoProviderIsConfigured: a searchable kind with
// no provider of it is a configuration gap, not an unsupported kind.
func TestServeRPCSearchSaysWhenNoProviderIsConfigured(t *testing.T) {
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, &pkgmetadata.Registry{}))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindMovie, Text: "Inception"}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))

	require.Contains(t, resp.Error, "no movie metadata provider is configured")
	require.NotContains(t, resp.Error, "does not support kind")
}

type failingSearchArtistProvider struct {
	stubArtistProvider
	name string
	err  error
}

func (p failingSearchArtistProvider) Name() string { return p.name }
func (p failingSearchArtistProvider) SearchArtists(context.Context, string) ([]pkgmetadata.SearchHit, error) {
	return nil, p.err
}

// TestServeRPCSearchSurfacesEveryProviderFailure is the regression for a
// provider outage reading as "search does not support kind": when every
// configured provider fails, the response says so and carries each
// provider's own error, by name.
func TestServeRPCSearchSurfacesEveryProviderFailure(t *testing.T) {
	reg := &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{
		failingSearchArtistProvider{name: "musicbrainz", err: &pkgmetadata.RateLimitedError{Provider: "musicbrainz"}},
		failingSearchArtistProvider{name: "mirror", err: fmt.Errorf("dial tcp: connection refused")},
	}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindArtist, Text: "Radiohead"}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))

	require.NotContains(t, resp.Error, "does not support kind")
	require.Contains(t, resp.Error, "every artist search provider failed")
	require.Contains(t, resp.Error, "musicbrainz: metadata: musicbrainz: rate limited")
	require.Contains(t, resp.Error, "mirror: dial tcp: connection refused")
	require.NotContains(t, resp.Error, "\n", "one line: it travels into logs and conditions")
	require.Empty(t, resp.Results)
}

func TestServeRPCSearchFallsThroughAFailingProviderToTheNext(t *testing.T) {
	reg := &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{
		failingSearchArtistProvider{name: "down", err: pkgmetadata.ErrAuth},
		stubArtistProvider{hit: pkgmetadata.SearchHit{Title: "Radiohead"}},
	}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindArtist, Text: "Radiohead"}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))

	require.Empty(t, resp.Error)
	require.Equal(t, "musicbrainz", resp.Provider)
	require.Len(t, resp.Results, 1)
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

type stubComicProvider struct {
	issues    []pkgmetadata.ComicIssue
	err       error
	wantVolID string
	// name and lookupBy default to a ComicVine provider.
	name     string
	lookupBy []string
	calls    *[]string
}

func (p stubComicProvider) Name() string {
	if p.name == "" {
		return "comicvine"
	}
	return p.name
}

func (p stubComicProvider) Capabilities() pkgmetadata.Capabilities {
	if p.lookupBy == nil {
		return pkgmetadata.Capabilities{LookupBy: []string{pkgmetadata.KeyComicVine}}
	}
	return pkgmetadata.Capabilities{LookupBy: p.lookupBy}
}

func (p stubComicProvider) SearchVolumes(context.Context, string) ([]pkgmetadata.SearchHit, error) {
	return nil, nil
}

func (p stubComicProvider) Volume(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.ComicVolume, error) {
	return nil, pkgmetadata.ErrNotFound
}

func (p stubComicProvider) Issues(_ context.Context, volumeID string) ([]pkgmetadata.ComicIssue, error) {
	if p.calls != nil {
		*p.calls = append(*p.calls, p.Name()+":"+volumeID)
	}
	if p.wantVolID != "" && volumeID != p.wantVolID {
		return nil, fmt.Errorf("unexpected volume id %q", volumeID)
	}
	return p.issues, p.err
}

// TestServeRPCLookupListsIssuesForComicFanout is lookupEpisodes' test
// (TestServeRPCLookupListsEpisodesForTaskC6) mirrored for Comic->Issue:
// Registry.Lookup deliberately has no MediaKindIssue case (Issues(volumeID)
// returns a list, the wrong shape for "first entity from the first provider
// that succeeds"), so rpc.go's lookupIssues serves it directly, the way
// lookupEpisodes serves Episode.
func TestServeRPCLookupListsIssuesForComicFanout(t *testing.T) {
	reg := &pkgmetadata.Registry{Comics: []pkgmetadata.ComicProvider{stubComicProvider{
		wantVolID: "4050-12345",
		issues: []pkgmetadata.ComicIssue{
			{Number: "1", Title: "The Black Sword"},
			{Number: "2", Title: "The Golden Age"},
		},
	}}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{"comicvine": "4050-12345"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))

	require.Empty(t, resp.Error)
	require.Equal(t, "comicvine", resp.Provider)
	require.Len(t, resp.Results, 2)
	var issue pkgmetadata.ComicIssue
	require.NoError(t, json.Unmarshal(resp.Results[0], &issue))
	require.Equal(t, "The Black Sword", issue.Title)
}

// TestServeRPCLookupIssuesHandsEachIDOnlyToProvidersOfItsKey is the X6b
// fix: a MangaDex Comic's UUID travels under "mangadex" and reaches only a
// provider that looks issues up by that key, and a ComicVine id never
// reaches a MangaDex-only provider.
func TestServeRPCLookupIssuesHandsEachIDOnlyToProvidersOfItsKey(t *testing.T) {
	var calls []string
	reg := &pkgmetadata.Registry{Comics: []pkgmetadata.ComicProvider{
		stubComicProvider{calls: &calls, issues: []pkgmetadata.ComicIssue{{Number: "1"}}},
		stubComicProvider{calls: &calls, name: "mangadex", lookupBy: []string{"mangadex"}, issues: []pkgmetadata.ComicIssue{{Number: "41"}, {Number: "42"}}},
	}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{"mangadex": "801513ba-a712-498c-8f57-cae55b38cc92"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Empty(t, resp.Error)
	require.Equal(t, "mangadex", resp.Provider)
	require.Len(t, resp.Results, 2)

	resp = schema.MetadataResponse{}
	req = schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{"comicvine": "4050-12345"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Equal(t, "comicvine", resp.Provider)

	require.Equal(t, []string{"mangadex:801513ba-a712-498c-8f57-cae55b38cc92", "comicvine:4050-12345"}, calls,
		"the ComicVine provider never saw the UUID and the MangaDex one never saw the ComicVine id")
}

func TestServeRPCLookupIssuesNamesEveryProvidersFailure(t *testing.T) {
	reg := &pkgmetadata.Registry{Comics: []pkgmetadata.ComicProvider{
		stubComicProvider{err: fmt.Errorf("upstream 502")},
		stubComicProvider{name: "metron", lookupBy: []string{"metron", "comicvine"}, err: pkgmetadata.ErrNotFound},
	}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{"comicvine": "4050-12345"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Contains(t, resp.Error, "comicvine: upstream 502")
	require.Contains(t, resp.Error, "metron: metadata: not found")
}

func TestServeRPCLookupIssuesRequiresAComicVineVolumeID(t *testing.T) {
	reg := &pkgmetadata.Registry{}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindIssue, IDs: map[string]string{}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.NotEmpty(t, resp.Error)
}

type stubAlbumListProvider struct {
	albums       []pkgmetadata.Album
	err          error
	wantArtistID string
}

func (p stubAlbumListProvider) Name() string { return "musicbrainz" }
func (p stubAlbumListProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p stubAlbumListProvider) SearchArtists(context.Context, string) ([]pkgmetadata.SearchHit, error) {
	return nil, nil
}

func (p stubAlbumListProvider) Artist(context.Context, string) (*pkgmetadata.Artist, error) {
	return nil, pkgmetadata.ErrNotFound
}

func (p stubAlbumListProvider) Albums(_ context.Context, mbArtistID string) ([]pkgmetadata.Album, error) {
	if p.wantArtistID != "" && mbArtistID != p.wantArtistID {
		return nil, fmt.Errorf("unexpected artist id %q", mbArtistID)
	}
	return p.albums, p.err
}

func (p stubAlbumListProvider) Album(context.Context, string) (*pkgmetadata.Album, error) {
	return nil, pkgmetadata.ErrNotFound
}

// TestServeRPCLookupListsAlbumsForArtistFanout is lookupEpisodes' own test
// (TestServeRPCLookupListsEpisodesForTaskC6) mirrored for Artist->Album (task
// G2-2): Registry.Lookup(kind=album) only covers the single-release-group
// fetch (keyed by KeyMBReleaseGroup, the gateway's own status.metadata
// path), so rpc.go's lookupAlbums serves the artist's whole release-group
// list instead, when the caller's ids carry KeyMBArtist.
func TestServeRPCLookupListsAlbumsForArtistFanout(t *testing.T) {
	reg := &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{stubAlbumListProvider{
		wantArtistID: "5b11f4ce-a62d-471e-81fc-a69a8278c7da",
		albums: []pkgmetadata.Album{
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "rg-1"}, Title: "OK Computer", PrimaryType: "Album"},
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "rg-2"}, Title: "Kid A", PrimaryType: "Album"},
		},
	}}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindAlbum,
		IDs:  map[string]string{pkgmetadata.KeyMBArtist: "5b11f4ce-a62d-471e-81fc-a69a8278c7da"},
	}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))

	require.Empty(t, resp.Error)
	require.Equal(t, "musicbrainz", resp.Provider)
	require.Len(t, resp.Results, 2)
	var alb pkgmetadata.Album
	require.NoError(t, json.Unmarshal(resp.Results[0], &alb))
	require.Equal(t, "OK Computer", alb.Title)
}

// TestServeRPCLookupAlbumStillUsesRegistryLookupForAReleaseGroupID proves the
// new dispatch in lookup() does not swallow the pre-existing single-album
// fetch: a request keyed by KeyMBReleaseGroup (no KeyMBArtist) still reaches
// Registry.Lookup(kind=album) -> ArtistProvider.Album, never lookupAlbums.
// stubArtistProvider.Album always returns ErrNotFound (unlike .Albums, which
// returns an empty, non-error slice) -- a distinguishable failure mode that
// pins which path actually ran.
func TestServeRPCLookupAlbumStillUsesRegistryLookupForAReleaseGroupID(t *testing.T) {
	reg := &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{stubArtistProvider{}}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindAlbum, IDs: map[string]string{pkgmetadata.KeyMBReleaseGroup: "rg-1"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.NotEmpty(t, resp.Error)
}

// TestServeRPCLookupAlbumWithNoIDsFallsThroughToRegistryLookup: neither key
// present falls through to Registry.Lookup, which reports its own "requires
// mb-release-group" error rather than lookupAlbums silently accepting an
// empty artist id.
func TestServeRPCLookupAlbumWithNoIDsFallsThroughToRegistryLookup(t *testing.T) {
	reg := &pkgmetadata.Registry{}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindAlbum, IDs: map[string]string{}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.NotEmpty(t, resp.Error)
}

type stubBookListProvider struct {
	books        []pkgmetadata.Book
	err          error
	wantAuthorID string
}

func (p stubBookListProvider) Name() string { return "openlibrary" }
func (p stubBookListProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p stubBookListProvider) SearchBooks(context.Context, string) ([]pkgmetadata.SearchHit, error) {
	return nil, nil
}

func (p stubBookListProvider) Author(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Author, error) {
	return nil, pkgmetadata.ErrNotFound
}

func (p stubBookListProvider) Books(_ context.Context, authorID string) ([]pkgmetadata.Book, error) {
	if p.wantAuthorID != "" && authorID != p.wantAuthorID {
		return nil, fmt.Errorf("unexpected author id %q", authorID)
	}
	return p.books, p.err
}

func (p stubBookListProvider) Book(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Book, error) {
	return nil, pkgmetadata.ErrNotFound
}

func (p stubBookListProvider) Edition(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Edition, error) {
	return nil, pkgmetadata.ErrNotFound
}

// TestServeRPCLookupListsBooksForAuthorFanout is
// TestServeRPCLookupListsAlbumsForArtistFanout mirrored for Author->Book
// (task G2-2): Registry.Lookup(kind=book) only covers the single-work fetch
// (keyed by KeyOpenLibraryWork, Book's own status.metadata path), so
// rpc.go's lookupBooks serves the author's whole works list instead, when
// the caller's ids carry KeyOpenLibraryAuthor.
func TestServeRPCLookupListsBooksForAuthorFanout(t *testing.T) {
	reg := &pkgmetadata.Registry{Books: []pkgmetadata.BookProvider{stubBookListProvider{
		wantAuthorID: "OL23919A",
		books: []pkgmetadata.Book{
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryWork: "OL45883W"}, Title: "The Hobbit"},
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryWork: "OL27482W"}, Title: "The Fellowship of the Ring"},
		},
	}}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindBook,
		IDs:  map[string]string{pkgmetadata.KeyOpenLibraryAuthor: "OL23919A"},
	}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))

	require.Empty(t, resp.Error)
	require.Equal(t, "openlibrary", resp.Provider)
	require.Len(t, resp.Results, 2)
	var bk pkgmetadata.Book
	require.NoError(t, json.Unmarshal(resp.Results[0], &bk))
	require.Equal(t, "The Hobbit", bk.Title)
}

// TestServeRPCLookupBookStillUsesRegistryLookupForAWorkID proves the new
// dispatch in lookup() does not swallow the pre-existing single-book fetch:
// a request keyed by KeyOpenLibraryWork (no KeyOpenLibraryAuthor) still
// reaches Registry.Lookup(kind=book) -> BookProvider.Book, never
// lookupBooks. stubBookListProvider.Book always returns ErrNotFound (unlike
// .Books, which returns an empty, non-error slice) -- a distinguishable
// failure mode that pins which path actually ran.
func TestServeRPCLookupBookStillUsesRegistryLookupForAWorkID(t *testing.T) {
	reg := &pkgmetadata.Registry{Books: []pkgmetadata.BookProvider{stubBookListProvider{}}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindBook, IDs: map[string]string{pkgmetadata.KeyOpenLibraryWork: "OL45883W"}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.NotEmpty(t, resp.Error)
}

// TestServeRPCLookupBookWithNoIDsFallsThroughToRegistryLookup: neither key
// present falls through to Registry.Lookup, which -- unlike album/episode/
// issue -- has no "requires X" guard for kind=book (Registry.Lookup's own
// case commonv1.MediaKindBook calls BookProvider.Book(ctx, ids) directly
// with whatever ids it was given), so this pins that the empty-id request
// still resolves through the ordinary single-book path (ErrNotFound from
// the stub) rather than silently being swallowed by lookupBooks.
func TestServeRPCLookupBookWithNoIDsFallsThroughToRegistryLookup(t *testing.T) {
	reg := &pkgmetadata.Registry{Books: []pkgmetadata.BookProvider{stubBookListProvider{}}}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindBook, IDs: map[string]string{}}
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.NotEmpty(t, resp.Error)
}

// TestIdsOfCoversAlbumAndBook pins the two idsOf cases task G2-1 added
// alongside Registry.Lookup's new album/book support -- without them,
// schema.MetadataResponse.IDs would come back nil for these two kinds even
// though Lookup succeeded, exactly the gap movie/series/artist/author/
// audiobook/comic already avoid.
func TestIdsOfCoversAlbumAndBook(t *testing.T) {
	album := &pkgmetadata.Album{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "rg-1"}}
	require.Equal(t, map[string]string{pkgmetadata.KeyMBReleaseGroup: "rg-1"}, idsOf(album))

	book := &pkgmetadata.Book{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryWork: "OL45883W"}}
	require.Equal(t, map[string]string{pkgmetadata.KeyOpenLibraryWork: "OL45883W"}, idsOf(book))
}

// TestServeRPCHandlersCreateASpanPerVerb is review round 1's Important fix:
// the RPC surface previously never called tracing.Start, unlike worker.go
// (which spans both the handler and the nested registry call). Concretely,
// the bus's Serve callback hands each inbound RPC request a bare
// context.Background() -- pkg/events' Requester.Serve does not yet extract
// Clustarr-Trace from the request, a gap tracked and fixed separately by
// the task that owns pkg/events -- so every RPC-triggered provider call ran
// with no parent context at all, and the provider client's own span became
// an orphaned root. This is likely the majority of interactive gateway
// traffic: import-list searches, resolver calls and episode listings.
//
// This test only proves a span is created per RPC verb (name and count),
// per the reviewer's own scoping: proving the eventual parent link is the
// other task's job once it lands, not this test's.
func TestServeRPCHandlersCreateASpanPerVerb(t *testing.T) {
	prevTP := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prevTP) })

	body, err := os.ReadFile("../../testdata/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	movieClient, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	reg := &pkgmetadata.Registry{
		Movies:    []pkgmetadata.MovieProvider{movieClient},
		Series:    []pkgmetadata.SeriesProvider{stubSeriesProvider{episodes: []pkgmetadata.Episode{{Title: "Pilot"}}}},
		Resolvers: []pkgmetadata.IDResolver{stubResolver{add: pkgmetadata.ExternalIDs{"imdb": "tt1375666"}}},
	}
	bus := newTestBus(t)
	require.NoError(t, ServeRPC(bus, reg))

	var resp schema.MetadataResponse
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup,
		schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "27205"}}, &resp))
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup,
		schema.MetadataRequest{Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"tvdb": "1", "order": "official"}}, &resp))
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch,
		schema.MetadataRequest{Kind: commonv1.MediaKindMovie, Text: "Inception"}, &resp))
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataResolve,
		schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "27205"}}, &resp))

	seen := map[string]int{}
	for _, s := range recorder.Ended() {
		seen[s.Name()]++
	}
	for _, name := range []string{
		"metadata.rpc.serve.lookup", "metadata.rpc.serve.search", "metadata.rpc.serve.resolve",
		"metadata.rpc.lookup", "metadata.rpc.lookupEpisodes", "metadata.rpc.search", "metadata.rpc.resolve",
	} {
		require.Positive(t, seen[name], "expected at least one ended span named %q, saw spans: %v", name, seen)
	}
}
