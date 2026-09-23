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

package cardigann_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// fixtureServer answers every request with one fixture file.
func fixtureServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	body := readTestdataBytes(t, name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func searchErrorEngine(t *testing.T, srv *httptest.Server) (cardigann.Engine, *cardigann.Definition, cardigann.Config) {
	t.Helper()
	def, err := cardigann.Load(readTestdata(t, "search-error.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
	require.NoError(t, err)
	return cardigann.Engine{HTTP: srv.Client(), Now: func() time.Time { return testClock }}, def, cfg
}

// Ruling R6. A tracker's error page is a well-formed document with no result
// rows, so before search.error was evaluated it parsed as zero releases and
// the caller could not tell a failing tracker from one with nothing to offer.
// It must be an error, and a typed one.
func TestSearchErrorBlockSurfacesAsAnError(t *testing.T) {
	srv := fixtureServer(t, "search-error.html")
	eng, def, cfg := searchErrorEngine(t, srv)

	rels, err := eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: "some movie"})
	require.Error(t, err, "an error page read as a successful search")
	require.Empty(t, rels)
	require.ErrorIs(t, err, cardigann.ErrSearchFailed)

	var se *cardigann.SearchError
	require.ErrorAs(t, err, &se)
	// The matched element's own text, whitespace-collapsed.
	assert.Equal(t, "Too many requests: you are rate limited for 1 hour", se.Message)
}

func TestSearchErrorBlockRendersItsMessage(t *testing.T) {
	srv := fixtureServer(t, "search-error-banned.html")
	eng, def, cfg := searchErrorEngine(t, srv)

	_, err := eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: "x"})
	var se *cardigann.SearchError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "account banned on "+srv.URL+"/", se.Message)
}

// The mirror: a results page with no error element still parses, so the
// error check is not simply failing every search.
func TestSearchErrorBlockDoesNotFireOnAResultsPage(t *testing.T) {
	srv := fixtureServer(t, "search-error-results.html")
	eng, def, cfg := searchErrorEngine(t, srv)

	rels, err := eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: "movie"})
	require.NoError(t, err)
	require.Len(t, rels, 2)
	assert.Equal(t, srv.URL+"/dl/1.torrent", findRelease(t, rels, "Some.Movie.2024.1080p.WEB-DL-GRP").Link)
}

// countingLimiter records every key the engine waited on.
type countingLimiter struct {
	mu   sync.Mutex
	keys []string
	err  error
}

func (l *countingLimiter) Wait(_ context.Context, key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	return l.err
}

// The caller owns rate limiting: every outbound request waits on the
// injected limiter, under the caller's key.
func TestEngineWaitsOnTheInjectedLimiterForEveryRequest(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write(readTestdataBytes(t, "1337x-search.html"))
	}))
	defer srv.Close()

	def, err := cardigann.Load(readTestdata(t, "1337x.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
	require.NoError(t, err)

	lim := &countingLimiter{}
	eng := cardigann.Engine{
		HTTP: srv.Client(), Now: func() time.Time { return testClock },
		Limiter: lim, RateKey: "tracker.example:443",
	}
	_, err = eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: "x"})
	require.NoError(t, err)

	require.Equal(t, hits, len(lim.keys), "a request went out without waiting on the limiter")
	for _, k := range lim.keys {
		require.Equal(t, "tracker.example:443", k)
	}
}

// A limiter that refuses (a cancelled context, a deadline it cannot meet)
// stops the request before it is sent.
func TestEngineDoesNotSendWhenTheLimiterRefuses(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	def, err := cardigann.Load(readTestdata(t, "search-error.yml"))
	require.NoError(t, err)
	cfg, err := cardigann.NewConfig(def, srv.URL+"/", map[string]string{})
	require.NoError(t, err)

	eng := cardigann.Engine{HTTP: srv.Client(), Limiter: &countingLimiter{err: context.DeadlineExceeded}}
	_, err = eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: "x"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, hits)
}

// No limiter is "unpaced", never a default one switched on.
func TestEngineWithoutALimiterIsUnpaced(t *testing.T) {
	srv := fixtureServer(t, "search-error-results.html")
	eng, def, cfg := searchErrorEngine(t, srv)
	require.Nil(t, eng.Limiter)
	_, err := eng.Search(context.Background(), def, cfg, cardigann.Query{Type: "search", Q: "x"})
	require.NoError(t, err)
}

func TestQueryFromTorznabMapsModesAndFields(t *testing.T) {
	season := 3
	cases := map[torznab.SearchMode]string{
		torznab.ModeSearch:      "search",
		torznab.ModeTVSearch:    "tv-search",
		torznab.ModeMovieSearch: "movie-search",
		torznab.ModeMusicSearch: "music-search",
		torznab.ModeAudioSearch: "music-search",
		torznab.ModeBookSearch:  "book-search",
	}
	for in, want := range cases {
		got := cardigann.QueryFromTorznab(torznab.Query{Type: in, Q: "q", IMDBID: "tt1", Season: &season, Episode: "7"})
		assert.Equal(t, want, got.Type, "mode %s", in)
		assert.Equal(t, "3", got.Season)
		assert.Equal(t, "7", got.Ep)
		assert.Equal(t, "tt1", got.IMDBID)
		back, ok := cardigann.TorznabMode(want)
		require.True(t, ok)
		if in != torznab.ModeAudioSearch { // audio is torznab's alias of music
			assert.Equal(t, in, back)
		}
	}
}

func TestSessionRoundTripsThroughItsPersistedForm(t *testing.T) {
	in := &cardigann.Session{
		Cookies:   []*http.Cookie{{Name: "uid", Value: "42"}, {Name: "pass", Value: "s3cret"}},
		Headers:   http.Header{"Authorization": {"Bearer x"}},
		ExpiresAt: testClock.Add(time.Hour),
	}
	data, err := cardigann.MarshalSession(in)
	require.NoError(t, err)
	out, err := cardigann.UnmarshalSession(data)
	require.NoError(t, err)
	require.Len(t, out.Cookies, 2)
	assert.Equal(t, "uid", out.Cookies[0].Name)
	assert.Equal(t, "s3cret", out.Cookies[1].Value)
	assert.Equal(t, "Bearer x", out.Headers.Get("Authorization"))
	assert.True(t, in.ExpiresAt.Equal(out.ExpiresAt))
	assert.Equal(t, "uid=42; pass=s3cret", out.CookieHeader())

	assert.False(t, out.Expired(testClock))
	assert.True(t, out.Expired(testClock.Add(2*time.Hour)))
	assert.True(t, (*cardigann.Session)(nil).Expired(testClock))

	_, err = cardigann.UnmarshalSession([]byte(`{"v":99}`))
	require.True(t, errors.Is(err, cardigann.ErrSessionFormat))
	_, err = cardigann.UnmarshalSession([]byte(`not json`))
	require.ErrorIs(t, err, cardigann.ErrSessionFormat)
}
