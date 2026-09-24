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

package importliststub

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/mdblist"
	"github.com/mediactl/clustarr/pkg/importlist/plex"
	"github.com/mediactl/clustarr/pkg/importlist/trakt"
)

const recordedDir = "../../data/importlist"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestTraktDeviceFlowPollsThenAuthorizes drives the fixture with the REAL
// pkg/importlist/trakt.DeviceFlow -- the same code importarr's ImportList
// controller runs -- proving Start, a pending Poll and an authorized Poll
// all parse exactly as that client expects.
func TestTraktDeviceFlowPollsThenAuthorizes(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	flow := trakt.NewDeviceFlow(trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, trakt.WithBaseURL(srv.URL))

	dc, err := flow.Start(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, dc.DeviceCode)
	require.NotEmpty(t, dc.UserCode)

	for i := 0; i < pendingPollsBeforeAuthorized; i++ {
		status, _, err := flow.Poll(t.Context(), dc)
		require.NoError(t, err)
		require.Equal(t, trakt.PollStatusPending, status, "poll %d", i+1)
	}

	status, tok, err := flow.Poll(t.Context(), dc)
	require.NoError(t, err)
	require.Equal(t, trakt.PollStatusAuthorized, status)
	require.NotEmpty(t, tok.AccessToken)
}

// TestTraktWatchlistRequiresTheAuthorizedToken drives the fixture end to
// end: the device flow to a real token, then trakt.List.Fetch against the
// watchlist route with that token, and a second Fetch with a wrong token
// that must 401.
func TestTraktWatchlistRequiresTheAuthorizedToken(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	flow := trakt.NewDeviceFlow(trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, trakt.WithBaseURL(srv.URL))
	dc, err := flow.Start(t.Context())
	require.NoError(t, err)
	for i := 0; i <= pendingPollsBeforeAuthorized; i++ {
		_, _, err := flow.Poll(t.Context(), dc)
		require.NoError(t, err)
	}
	_, tok, err := flow.Poll(t.Context(), dc)
	require.NoError(t, err)

	store := importlist.NewMemoryTokenStore()
	require.NoError(t, store.Save(t.Context(), tok))
	cfg := importlist.TraktConfig{ListType: importlist.TraktListTypeWatchlist, Username: "e2e-fixture-user"}
	l, err := trakt.New("trakt-watchlist", commonv1.MediaKindMovie, cfg,
		trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, store, nil, trakt.WithBaseURL(srv.URL))
	require.NoError(t, err)

	items, err := l.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Deadpool", items[0].Title)

	// A store holding the wrong token must see a 401 from the fixture --
	// proving handleTraktWatchlist's gate is real, not a pass-through.
	badStore := importlist.NewMemoryTokenStore()
	require.NoError(t, badStore.Save(t.Context(), importlist.Token{AccessToken: "not-the-authorized-token"}))
	l2, err := trakt.New("trakt-watchlist", commonv1.MediaKindMovie, cfg,
		trakt.Credentials{ClientID: "cid", ClientSecret: "secret"}, badStore, nil, trakt.WithBaseURL(srv.URL))
	require.NoError(t, err)
	_, err = l2.Fetch(t.Context())
	require.Error(t, err)
}

// TestPlexWatchlistMoviesAndShows drives the fixture with the real
// pkg/importlist/plex.Watchlist for both kinds.
func TestPlexWatchlistMoviesAndShows(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	movies, err := plex.New("plex-movies", commonv1.MediaKindMovie, "tok", "client-id", plex.WithBaseURL(srv.URL))
	require.NoError(t, err)
	items, err := movies.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 2)

	shows, err := plex.New("plex-shows", commonv1.MediaKindSeries, "tok", "client-id", plex.WithBaseURL(srv.URL))
	require.NoError(t, err)
	items, err = shows.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
}

// TestMdblist drives the fixture with the real pkg/importlist/mdblist.List,
// including its mediatype filter -- proving a movie-kind List sees only
// "The Matrix" and a series-kind List sees only "Breaking Bad" from the
// SAME recorded response.
func TestMdblist(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	movies, err := mdblist.New("mdblist-movies", commonv1.MediaKindMovie,
		importlist.MdblistConfig{URL: srv.URL + "/mdblist/list.json"}, "fixture-key")
	require.NoError(t, err)
	items, err := movies.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "The Matrix", items[0].Title)

	shows, err := mdblist.New("mdblist-shows", commonv1.MediaKindSeries,
		importlist.MdblistConfig{URL: srv.URL + "/mdblist/list.json"}, "fixture-key")
	require.NoError(t, err)
	items, err = shows.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "Breaking Bad", items[0].Title)
}

// TestUnrecognisedRouteLogsAndFourOhFours proves the stub's catch-all
// behaves like every other stub in this tree.
func TestUnrecognisedRouteLogsAndFourOhFours(t *testing.T) {
	srv := httptest.NewServer(NewHandler(recordedDir, discardLogger()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope") //nolint:noctx,gosec // test-local fixture URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
