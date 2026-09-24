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

package torznab_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestClientSearchAgainstAnHTTPTestServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "movie", r.URL.Query().Get("t"))
		require.Equal(t, "secret", r.URL.Query().Get("apikey"))
		w.Header().Set("Content-Type", "application/rss+xml")
		f, err := os.Open("../../test/data/torznab/search_with_attrs.xml")
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		_, _ = io.Copy(w, f)
	}))
	defer srv.Close()

	c, err := torznab.NewClient(srv.URL, "secret")
	require.NoError(t, err)

	rels, err := c.Search(context.Background(), torznab.Query{Type: torznab.ModeMovieSearch, IMDBID: "tt0133093"})
	require.NoError(t, err)
	require.Len(t, rels, 1)
	require.Equal(t, "tt0133093", rels[0].IDs["imdb"])
}

func TestClientRateLimitsPerHost(t *testing.T) {
	var hits []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, time.Now())
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	}))
	defer srv.Close()

	lim := ratelimit.New(ratelimit.Config{RPS: 20, Burst: 1}) // one token every 50ms
	c, err := torznab.NewClient(srv.URL, "", torznab.WithRateLimit(lim))
	require.NoError(t, err)

	_, err = c.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch})
	require.NoError(t, err)
	_, err = c.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch})
	require.NoError(t, err)

	require.Len(t, hits, 2)
	require.GreaterOrEqual(t, hits[1].Sub(hits[0]), 40*time.Millisecond, "the second request must wait for the limiter")
}

// TestClientDoesNotRateLimitByDefault covers ruling F4: pacing is the
// caller's job. A client built with no WithRateLimit option must not hold
// requests back at all -- the indexarr controller owns one limiter per
// indexer host, and a library default would stack a second limiter in
// series underneath it.
func TestClientDoesNotRateLimitByDefault(t *testing.T) {
	var hits []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, time.Now())
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<rss><channel></channel></rss>`))
	}))
	defer srv.Close()

	c, err := torznab.NewClient(srv.URL, "")
	require.NoError(t, err)

	start := time.Now()
	_, err = c.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch})
	require.NoError(t, err)
	_, err = c.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch})
	require.NoError(t, err)

	require.Len(t, hits, 2)
	require.Less(t, hits[1].Sub(hits[0]), 500*time.Millisecond,
		"back-to-back requests must not be paced when no limiter was supplied")
	require.Less(t, time.Since(start), time.Second)
}

func TestClientMapsHTTPStatusesToError(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		retryAfter string
		wantStatus int
	}{
		{"disabled", http.StatusGone, "", 410},
		{"rate limited", http.StatusTooManyRequests, "120", 429},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			c, err := torznab.NewClient(srv.URL, "")
			require.NoError(t, err)

			_, err = c.Caps(context.Background())
			require.Error(t, err)
			var terr *torznab.Error
			require.True(t, errors.As(err, &terr))
			require.Equal(t, tc.wantStatus, terr.HTTPStatus)
			if tc.retryAfter != "" {
				require.Equal(t, 120*time.Second, terr.RetryAfter)
			}
		})
	}
}

func TestClientSearchHonoursContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := torznab.NewClient(srv.URL, "")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.Search(ctx, torznab.Query{Type: torznab.ModeSearch})
	require.ErrorIs(t, err, context.Canceled)
}

func TestClientCapsAgainstAnHTTPTestServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "caps", r.URL.Query().Get("t"))
		w.Header().Set("Content-Type", "application/xml")
		f, err := os.Open("../../test/data/torznab/caps.xml")
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		_, _ = io.Copy(w, f)
	}))
	defer srv.Close()

	c, err := torznab.NewClient(srv.URL, "")
	require.NoError(t, err)

	caps, err := c.Caps(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Clustarr Test Indexer", caps.ServerTitle)
}

func TestClientCapsXMLErrorBodyBecomesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.Copy(w, mustOpen(t, "../../test/data/torznab/error.xml"))
	}))
	defer srv.Close()

	c, err := torznab.NewClient(srv.URL, "")
	require.NoError(t, err)

	_, err = c.Caps(context.Background())
	require.Error(t, err)
	var terr *torznab.Error
	require.True(t, errors.As(err, &terr))
	require.Equal(t, torznab.ErrRequestLimitReached, terr.Code)
	require.Equal(t, 200, terr.HTTPStatus)
}

func TestClientRejectsAnOversizedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		// One byte over the client's body size limit -- an indexer
		// misbehaving (or serving something malicious) must not let the
		// client buffer an unbounded amount of memory reading it.
		buf := make([]byte, 8<<20+1)
		_, _ = w.Write(buf)
	}))
	defer srv.Close()

	c, err := torznab.NewClient(srv.URL, "")
	require.NoError(t, err)

	_, err = c.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch})
	require.Error(t, err)
	require.ErrorIs(t, err, torznab.ErrResponseTooLarge)
}

// The caller owns rate limiting: indexarr shares one limiter per host across
// the caps probe, the search fan-out and the RSS poll, so all three draw on
// a single budget. An option that constructs its own limiter internally
// makes that impossible -- each client would get its own allowance and the
// host would see three times the intended rate.
func TestWithRateLimitUsesTheCallersLimiter(t *testing.T) {
	var calls int
	lim := ratelimit.New(ratelimit.Config{RPS: 0.001, Burst: 1}) // one token, then effectively never refills again
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.Copy(w, mustOpen(t, "../../test/data/torznab/caps.xml"))
	}))
	defer srv.Close()

	c, err := torznab.NewClient(srv.URL, "apikey", torznab.WithRateLimit(lim))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = c.Caps(ctx) // consumes the single token
	require.NoError(t, err)

	_, err = c.Caps(ctx) // must block on the CALLER's limiter, then time out
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the second call did not wait on the caller's limiter")
	require.Equal(t, 1, calls, "the second request reached the server despite an exhausted limiter")
}

// The test above uses ONE client, which is not quite the scenario this option
// exists for: an implementation that accepted the caller's limiter and then
// rebuilt a private bucket with the same settings would pass it. indexarr's
// actual shape is several clients against one host -- the caps probe, the
// search fan-out and the RSS poll -- and the guarantee is that they share one
// budget rather than getting one each.
func TestOneLimiterIsSharedAcrossSeveralClients(t *testing.T) {
	var calls int32
	lim := ratelimit.New(ratelimit.Config{RPS: 0.001, Burst: 1}) // one token for the host
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.Copy(w, mustOpen(t, "../../test/data/torznab/caps.xml"))
	}))
	defer srv.Close()

	// Three separate clients, one shared limiter -- indexarr's real shape.
	var clients []*torznab.Client
	for range 3 {
		c, err := torznab.NewClient(srv.URL, "apikey", torznab.WithRateLimit(lim))
		require.NoError(t, err)
		clients = append(clients, c)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := clients[0].Caps(ctx)
	require.NoError(t, err, "the first client should spend the single token")

	for i, c := range clients[1:] {
		_, err := c.Caps(ctx)
		require.ErrorIs(t, err, context.DeadlineExceeded,
			"client %d did not wait on the shared limiter; it has its own allowance", i+1)
	}
	require.EqualValues(t, 1, atomic.LoadInt32(&calls),
		"more than one request reached the host, so the three clients did not share a budget")
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}
