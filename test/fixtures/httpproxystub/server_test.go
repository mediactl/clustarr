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

package httpproxystub

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestForwardsAndRecords drives this stub exactly the way
// indexarr/controller/indexer/proxy.go's resolveProxy configures a real
// client: an *http.Transport whose Proxy is http.ProxyURL(the fixture's own
// URL). It proves a plain-HTTP request reaches its real target THROUGH the
// proxy, with the response and body intact, and that GET /_proxied reports
// the target it forwarded.
func TestForwardsAndRecords(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/hello", r.URL.Path)
		w.Header().Set("X-Fixture", "upstream")
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(NewHandler(discardLogger()))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get(upstream.URL + "/hello") //nolint:noctx
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "upstream", resp.Header.Get("X-Fixture"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "hello from upstream", string(body))

	got, err := http.Get(proxy.URL + "/_proxied") //nolint:noctx,gosec // test-local fixture URL
	require.NoError(t, err)
	defer func() { _ = got.Body.Close() }()
	var proxied []string
	require.NoError(t, json.NewDecoder(got.Body).Decode(&proxied))
	require.Contains(t, proxied, upstream.URL+"/hello")
}

// TestNonProxyRequestIsRejected proves a plain request with a relative
// path (not what a forward-proxy client ever sends) is refused rather than
// silently answered by whatever "/" happens to route to.
func TestNonProxyRequestIsRejected(t *testing.T) {
	srv := httptest.NewServer(NewHandler(discardLogger()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/some/path") //nolint:noctx,gosec // test-local fixture URL
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
