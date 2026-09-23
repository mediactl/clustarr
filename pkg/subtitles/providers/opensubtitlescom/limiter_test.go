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

package opensubtitlescom_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

// Ruling R3: Config.Limiter must never be defaulted on. A library that
// creates its own limiter when the caller passes none gives every Provider
// instance a private allowance, so N fetch workers sharing one
// OpenSubtitles.com account would collectively exceed the account's real
// rate limit by a factor of N -- CLAUDE.md's "the caller owns rate limiting"
// rule, which names this exact client. These two tests mirror
// pkg/torznab/client_test.go:220 (TestWithRateLimitUsesTheCallersLimiter)
// and its sibling TestOneLimiterIsSharedAcrossSeveralClients.

func readLoginFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../testdata/subtitles/opensubtitles/login.json")
	require.NoError(t, err)
	return b
}

// TestNoLimiterMeansNoClientSideWaiting is the regression the removed
// default guarded against silently reintroducing: with cfg.Limiter left
// nil, Provider must never wait on ANYTHING before a request, however many
// are made in quick succession. Before ruling R3, New silently installed
// rate.NewLimiter(5, 5); this loop of well over five rapid Search calls,
// inside a context whose deadline is far shorter than a 5 req/s burst
// limiter would have permitted, would time out under the old default and
// must not under the fix.
func TestNoLimiterMeansNoClientSideWaiting(t *testing.T) {
	var logins int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login" {
			atomic.AddInt32(&logins, 1)
			_, _ = w.Write(readLoginFixture(t))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	p := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// The first call also logs in; every one of these twenty must return
	// well inside the deadline with no Limiter configured.
	for range 20 {
		_, err := p.Search(ctx, subtitles.Query{Kind: "movie"})
		require.NoError(t, err, "a call blocked despite no Limiter being configured")
	}
	require.EqualValues(t, 1, logins, "the cached token must still avoid re-logging in on every call")
}

// TestOneLimiterIsSharedAcrossSeveralProviders is torznab's
// TestOneLimiterIsSharedAcrossSeveralClients, adapted: several Provider
// instances (indexarr's shape is several clients against one host; here it
// is several fetch worker goroutines against one OpenSubtitles.com account)
// sharing ONE *rate.Limiter must draw from ONE budget, not one each. Three
// separate Providers so each makes exactly one request (EnsureLoggedIn
// caches after the first success, so re-using a single Provider cannot
// distinguish "no default" from "a shared limiter honoured").
func TestOneLimiterIsSharedAcrossSeveralProviders(t *testing.T) {
	var calls int32
	fixture := readLoginFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	lim := rate.NewLimiter(rate.Limit(0.001), 1) // one token, then effectively never refills again

	var providers []*opensubtitlescom.Provider
	for range 3 {
		providers = append(providers, opensubtitlescom.New(opensubtitlescom.Config{
			APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL, Limiter: lim,
		}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	require.NoError(t, providers[0].EnsureLoggedIn(ctx), "the first provider should spend the single shared token")
	for i, p := range providers[1:] {
		// golang.org/x/time/rate.Limiter.Wait returns its own "would exceed
		// context deadline" error rather than wrapping context.DeadlineExceeded
		// when it can tell analytically the wait is hopeless -- so this checks
		// for an error at all, not a specific one; the invariant that matters
		// is that the call never reaches the server, asserted below via calls.
		err := p.EnsureLoggedIn(ctx)
		require.Error(t, err, "provider %d did not wait on the shared limiter; it has its own private allowance", i+1)
	}
	require.EqualValues(t, 1, calls, "only the first provider's login should have reached the server")
}
