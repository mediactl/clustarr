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

package gestdown_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
)

// Ruling R3: Gestdown had no rate limiter at all before this change. These
// two tests mirror pkg/torznab/client_test.go:220
// (TestWithRateLimitUsesTheCallersLimiter) and its sibling
// TestOneLimiterIsSharedAcrossSeveralClients -- the same pair
// opensubtitlescom's limiter_test.go adds for its own fix, adapted to
// Download, which (unlike Search) has no internal cache that could mask
// repeated calls.

func TestNoLimiterMeansNoClientSideWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHi.\n"))
	}))
	defer srv.Close()

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	for range 20 {
		_, _, err := p.Download(ctx, subtitles.Candidate{FetchID: "/subtitles/download/x"})
		require.NoError(t, err, "a call blocked despite no Limiter being configured")
	}
}

// TestOneLimiterIsSharedAcrossSeveralProviders is torznab's
// TestOneLimiterIsSharedAcrossSeveralClients, adapted: several Provider
// instances sharing ONE *rate.Limiter must draw from ONE budget, not one
// each -- otherwise an option that accepted the caller's limiter and quietly
// rebuilt a private bucket with the same settings would pass a single-client
// test and still let N fetch workers collectively out-pace Gestdown.
func TestOneLimiterIsSharedAcrossSeveralProviders(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:02,000\nHi.\n"))
	}))
	defer srv.Close()

	lim := rate.NewLimiter(rate.Limit(0.001), 1) // one token, then effectively never refills again

	var providers []*gestdown.Provider
	for range 3 {
		providers = append(providers, gestdown.New(gestdown.Config{Endpoint: srv.URL, Limiter: lim}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, _, err := providers[0].Download(ctx, subtitles.Candidate{FetchID: "/subtitles/download/x"})
	require.NoError(t, err, "the first provider should spend the single shared token")

	for i, p := range providers[1:] {
		// golang.org/x/time/rate.Limiter.Wait returns its own "would exceed
		// context deadline" error rather than wrapping context.DeadlineExceeded
		// when it can tell analytically the wait is hopeless -- so this checks
		// for an error at all, not a specific one; the invariant that matters
		// is that the call never reaches the server, asserted below via calls.
		_, _, err := p.Download(ctx, subtitles.Candidate{FetchID: "/subtitles/download/x"})
		require.Error(t, err, "provider %d did not wait on the shared limiter; it has its own private allowance", i+1)
	}
	require.EqualValues(t, 1, calls, "only the first provider's download should have reached the server")
}
