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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestClientSearchAgainstAnHTTPTestServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "movie", r.URL.Query().Get("t"))
		require.Equal(t, "secret", r.URL.Query().Get("apikey"))
		w.Header().Set("Content-Type", "application/rss+xml")
		f, err := os.Open("../../testdata/torznab/search_with_attrs.xml")
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

	c, err := torznab.NewClient(srv.URL, "", torznab.WithRateLimit(rate.Every(50*time.Millisecond), 1))
	require.NoError(t, err)

	_, err = c.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch})
	require.NoError(t, err)
	_, err = c.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch})
	require.NoError(t, err)

	require.Len(t, hits, 2)
	require.GreaterOrEqual(t, hits[1].Sub(hits[0]), 40*time.Millisecond, "the second request must wait for the limiter")
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
		f, err := os.Open("../../testdata/torznab/caps.xml")
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
		_, _ = io.Copy(w, mustOpen(t, "../../testdata/torznab/error.xml"))
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

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}
