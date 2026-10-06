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
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/plex"
)

const (
	token   = "test-token-0123456789"
	fireID  = "5d9c086c7d06d9001ffd27aa"
	arrival = "5d776b83fb0d55001f56a04b"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../test/data/metadata/plex/" + name)
	require.NoError(t, err)
	return b
}

// fakePlex serves the recorded responses by path and query, refusing a
// request without the token header as the live service does (401).
type fakePlex struct {
	t        *testing.T
	mu       sync.Mutex
	requests []*http.Request
	// matches maps a match request's guid to the fixture answering it;
	// any other guid answers matches_empty.json.
	matches map[string]string
}

func (f *fakePlex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()
	if r.Header.Get("X-Plex-Token") != token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	require.Equal(f.t, "application/json", r.Header.Get("Accept"))
	q := r.URL.Query()
	switch r.URL.Path {
	case "/library/metadata/matches":
		name, ok := f.matches[q.Get("guid")]
		if !ok {
			name = "matches_empty.json"
		}
		_, _ = w.Write(fixture(f.t, name))
	case "/library/metadata/" + fireID + "/children":
		_, _ = w.Write(fixture(f.t, "children_"+fireID+".json"))
	case "/library/metadata/" + fireID + "/grandchildren":
		require.Equal(f.t, "1", q.Get("includeGuids"))
		_, _ = w.Write(fixture(f.t, "grandchildren_"+fireID+"_"+q.Get("X-Plex-Container-Start")+".json"))
	default:
		f.t.Errorf("unexpected request %s", r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func newClient(t *testing.T, matches map[string]string) (*plex.Client, *fakePlex) {
	t.Helper()
	f := &fakePlex{t: t, matches: matches}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token, PageSize: 10})
	require.NoError(t, err)
	return c, f
}

var recorded = map[string]string{
	"tmdb://329865":    "matches_movie_tmdb_329865.json",
	"imdb://tt2543164": "matches_movie_tmdb_329865.json", // Arrival's record carries its imdb id too
	"tvdb://78874":     "matches_show_tvdb_78874.json",
	"tmdb://1":         "matches_movie_tmdb_329865.json", // a result that is not the item asked for
}

func TestNewRefusesNoToken(t *testing.T) {
	_, err := plex.New(plex.Config{})
	require.ErrorIs(t, err, plex.ErrNoToken)
}

func TestResolveFindsAMovieByItsTMDBID(t *testing.T) {
	c, _ := newClient(t, recorded)
	got, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "329865"})
	require.NoError(t, err)
	require.Equal(t, metadata.ExternalIDs{metadata.KeyPlex: arrival}, got)
}

func TestResolveFallsBackToIMDb(t *testing.T) {
	c, _ := newClient(t, recorded)
	got, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt2543164"})
	require.NoError(t, err)
	require.Equal(t, arrival, got[metadata.KeyPlex])
}

func TestResolveFindsAShowByItsTVDBID(t *testing.T) {
	c, _ := newClient(t, recorded)
	got, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "78874"})
	require.NoError(t, err)
	require.Equal(t, fireID, got[metadata.KeyPlex])
}

func TestResolveIsNotFoundWhenPlexHasNoMatch(t *testing.T) {
	c, _ := newClient(t, recorded)
	_, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "999999999"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

// A result whose Guid[] does not carry the id asked for is another item:
// clustarr never guesses a Plex id.
func TestResolveRefusesAResultWithoutTheAskedID(t *testing.T) {
	c, _ := newClient(t, recorded)
	_, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "1"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestResolveIsUnsupportedForOtherKinds(t *testing.T) {
	c, _ := newClient(t, recorded)
	_, err := c.Resolve(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyTMDB: "329865"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

func TestShowChildrenPagesEveryEpisode(t *testing.T) {
	c, f := newClient(t, recorded)
	got, err := c.ShowChildren(context.Background(), metadata.ExternalIDs{metadata.KeyTVDB: "78874"})
	require.NoError(t, err)
	require.Equal(t, fireID, got.ShowID)
	require.Equal(t, []metadata.PlexSeason{
		{Number: 0, ID: "5d9c09de08fddd001f2afb57"},
		{Number: 1, ID: "5d9c09de08fddd001f2afb4c"},
	}, got.Seasons)
	require.Len(t, got.Episodes, 23, "every page of totalSize 23 at page size 10")
	require.Contains(t, got.Episodes, metadata.PlexEpisode{Season: 1, Episode: 1, TVDB: "297989", ID: "5d9c127e4eefaa001f6449c2"})
	require.Contains(t, got.Episodes, metadata.PlexEpisode{Season: 0, Episode: 7, ID: "5ea14257f3d60a003f39ea44"}, "an episode Plex has no TVDB id for")
	require.Len(t, f.requests, 5, "one match, one seasons page, three episode pages")
}

func TestShowChildrenIsCached(t *testing.T) {
	c, f := newClient(t, recorded)
	ids := metadata.ExternalIDs{metadata.KeyTVDB: "78874"}
	_, err := c.ShowChildren(context.Background(), ids)
	require.NoError(t, err)
	n := len(f.requests)
	_, err = c.ShowChildren(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, f.requests, n, "the second call is served from the client's cache")
}

func TestAWrongTokenIsAnAuthError(t *testing.T) {
	f := &fakePlex{t: t, matches: recorded}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "wrong"})
	require.NoError(t, err)
	require.ErrorIs(t, c.Ping(context.Background()), metadata.ErrAuth)
}

// The token travels in the X-Plex-Token header only: no request URL and no
// error carries it.
func TestTheTokenNeverLeavesTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, r.URL.String(), token)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	c, err := plex.New(plex.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: token})
	require.NoError(t, err)
	_, err = c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "329865"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
	require.False(t, strings.Contains(err.Error(), "X-Plex-Token"))
}
