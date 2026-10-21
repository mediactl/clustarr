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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

// rateLimitedLogin is OpenSubtitles.com's login allowance in miniature: a
// login sooner than window after the last one, from anyone, answers 429.
type rateLimitedLogin struct {
	window time.Duration

	mu     sync.Mutex
	last   time.Time
	logins []time.Time
	status []int
}

func (s *rateLimitedLogin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/login" {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	now := time.Now()
	s.logins = append(s.logins, now)
	limited := !s.last.IsZero() && now.Sub(s.last) < s.window
	s.last = now
	if limited {
		s.status = append(s.status, http.StatusTooManyRequests)
		s.mu.Unlock()
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Login rate limit exceeded: 1 req/sec per IP"}`))
		return
	}
	s.status = append(s.status, http.StatusOK)
	s.mu.Unlock()
	_, _ = w.Write([]byte(`{"token":"` + jwt(now, now.Add(24*time.Hour)) + `","user":{"vip":false}}`))
}

// TestABurstOfCallersSpendsOnePacedLogin: five workers asking a cold
// Provider for a token at once, just after another login from the same IP,
// all get one. The first login is refused for its rate and tried again
// after the interval, and the others reuse its token. Before, each queued
// worker logged in the moment the one before it failed: on talos on
// 2026-10-20 that was four 429s in a second, five errors, and caption's
// strike floor throttled the provider for an hour.
func TestABurstOfCallersSpendsOnePacedLogin(t *testing.T) {
	const window = 80 * time.Millisecond
	srv := &rateLimitedLogin{window: window, last: time.Now()}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	p := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", Endpoint: ts.URL,
		LoginInterval: window + 20*time.Millisecond,
	})

	errs := make(chan error, 5)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { errs <- p.EnsureLoggedIn(context.Background()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Equal(t, []int{http.StatusTooManyRequests, http.StatusOK}, srv.status, "one refused login, one retried after the interval, and no more")
	assert.GreaterOrEqual(t, srv.logins[1].Sub(srv.logins[0]), window)
}

// TestALoginRefusedForItsRateStopsAfterItsAttempts: a login refused every
// time is returned as the 429 it is, after a bounded number of tries.
func TestALoginRefusedForItsRateStopsAfterItsAttempts(t *testing.T) {
	srv := &rateLimitedLogin{window: time.Hour, last: time.Now()}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	p := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", Endpoint: ts.URL, LoginInterval: 10 * time.Millisecond,
	})
	err := p.EnsureLoggedIn(context.Background())
	require.Error(t, err)
	assert.True(t, subtitles.IsRateLimited(err))
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Len(t, srv.logins, 3)
}
