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
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

// hostRecorder is an http.RoundTripper that sends every request, whatever
// host it names, to one httptest server, and records the host and path it
// was addressed to -- so a Provider on the default (real) endpoint can be
// driven without a network, and a test can see which host it chose.
type hostRecorder struct {
	target *url.URL
	mu     sync.Mutex
	calls  []string // "host/path"
}

func (h *hostRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.calls = append(h.calls, r.URL.Host+r.URL.Path)
	h.mu.Unlock()
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host = h.target.Scheme, h.target.Host
	out.Host = ""
	return http.DefaultTransport.RoundTrip(out)
}

func (h *hostRecorder) recorded() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// vipServer answers /login with base_url and /subtitles with the search
// fixture, on any host.
func vipServer(t *testing.T, baseURL string) *hostRecorder {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/login"):
			_, _ = w.Write([]byte(`{"user":{"vip":true},"base_url":"` + baseURL + `","token":"vip-token","status":200}`))
		case strings.HasSuffix(r.URL.Path, "/subtitles"):
			_, _ = w.Write(readFixture(t, "search.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return &hostRecorder{target: u}
}

// A VIP account's login names vip-api.opensubtitles.com in base_url, and
// Bazarr's server_url() sends every later request there. The login itself
// goes to the default host.
func TestAVIPAccountsRequestsGoToTheLoginsBaseURL(t *testing.T) {
	rec := vipServer(t, "vip-api.opensubtitles.com")
	p := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", HTTPClient: &http.Client{Transport: rec},
	})
	_, err := p.Search(context.Background(), movieQuery())
	require.NoError(t, err)
	assert.Equal(t, []string{
		"api.opensubtitles.com/api/v1/login",
		"vip-api.opensubtitles.com/api/v1/subtitles",
	}, rec.recorded())
}

// Every request carries the API key and the bearer token, so a base_url
// that is not a bare opensubtitles.com host is not followed.
func TestABaseURLOffOpenSubtitlesIsNotFollowed(t *testing.T) {
	for _, bad := range []string{
		"evil.example", "vip-api.opensubtitles.com.evil.example", "vip-api.opensubtitles.com/steal",
		"user@vip-api.opensubtitles.com", "vip-api.opensubtitles.com:8443", "https://vip-api.opensubtitles.com",
	} {
		rec := vipServer(t, bad)
		p := opensubtitlescom.New(opensubtitlescom.Config{
			APIKey: "k", Username: "u", Password: "p", HTTPClient: &http.Client{Transport: rec},
		})
		_, err := p.Search(context.Background(), movieQuery())
		require.NoError(t, err, bad)
		calls := rec.recorded()
		require.Len(t, calls, 2, bad)
		assert.Equal(t, "api.opensubtitles.com/api/v1/subtitles", calls[1], bad)
	}
}

// An explicit Endpoint -- a proxy, a test server -- is used as is: the
// login's base_url does not move requests off it.
func TestAnExplicitEndpointIgnoresBaseURL(t *testing.T) {
	rec := vipServer(t, "vip-api.opensubtitles.com")
	p := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", Endpoint: "https://proxy.internal/os/api/v1",
		HTTPClient: &http.Client{Transport: rec},
	})
	_, err := p.Search(context.Background(), movieQuery())
	require.NoError(t, err)
	assert.Equal(t, []string{"proxy.internal/os/api/v1/login", "proxy.internal/os/api/v1/subtitles"}, rec.recorded())
}

// The host travels with the token through the shared cache, as Bazarr
// caches oscom_server beside oscom_token: a replica that adopts a VIP
// account's token sends it to the VIP host without logging in.
func TestTheBaseURLIsSharedWithTheToken(t *testing.T) {
	rec := vipServer(t, "vip-api.opensubtitles.com")
	cache := &memCache{}
	first := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", HTTPClient: &http.Client{Transport: rec}, TokenCache: cache,
	})
	_, err := first.Search(context.Background(), movieQuery())
	require.NoError(t, err)
	assert.Equal(t, []string{"vip-api.opensubtitles.com"}, cache.servers)

	// A fresh token on the second replica's cache: no login, VIP host.
	cache.mu.Lock()
	cache.expiresAt = time.Now().Add(24 * time.Hour)
	cache.mu.Unlock()
	rec2 := vipServer(t, "api.opensubtitles.com")
	second := opensubtitlescom.New(opensubtitlescom.Config{
		APIKey: "k", Username: "u", Password: "p", HTTPClient: &http.Client{Transport: rec2}, TokenCache: cache,
	})
	_, err = second.Search(context.Background(), movieQuery())
	require.NoError(t, err)
	assert.Equal(t, []string{"vip-api.opensubtitles.com/api/v1/subtitles"}, rec2.recorded())
}
