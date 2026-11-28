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

package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirector answers every request with a redirect to target, after
// waiting delay, as tvload.win's panel sends a stream to a CDN edge.
func redirector(t *testing.T, delay time.Duration, target string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(delay)
		http.Redirect(w, req, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheTraceTimesEachHopOfARedirect(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("ts"))
	}))
	t.Cleanup(edge.Close)
	panel := redirector(t, 20*time.Millisecond, edge.URL+"/stream?token=secret")
	get := func() *upstreamTrace {
		tr := newUpstreamTrace(time.Now())
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), tr.hooks()), http.MethodGet, panel.URL+"/live/user/pass/1005.ts", nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return tr
	}

	tr := get()

	require.Len(t, tr.hops, 2)
	assert.Equal(t, strings.TrimPrefix(panel.URL, "http://"), tr.hops[0].host)
	assert.Equal(t, strings.TrimPrefix(edge.URL, "http://"), tr.hops[1].host)
	assert.GreaterOrEqual(t, tr.hops[0].answered, 20*time.Millisecond, "the panel's redirect")
	assert.GreaterOrEqual(t, tr.hops[1].answered-tr.hops[1].conn, 30*time.Millisecond, "the edge's first byte")
	assert.False(t, tr.hops[0].reused)
	s := tr.String()
	assert.Contains(t, s, " > ")
	assert.NotContains(t, s, "pass", "only hosts: the path carries the account")
	assert.NotContains(t, s, "secret", "only hosts: the edge's URL carries a token")

	again := get()

	require.Len(t, again.hops, 2)
	assert.True(t, again.hops[0].reused, "a kept-alive connection to the panel")
	assert.Contains(t, again.String(), "(reused)")
}

// TestAnAnsweredUpstreamLogsItsPath: the "upstream answered" line carries
// each hop's times, so a slow tune says whether the panel or the edge was
// slow.
func TestAnAnsweredUpstreamLogsItsPath(t *testing.T) {
	edge := tsUpstream(t, 0, nil, gop())
	panel := redirector(t, 0, edge)
	r := testRelay(t, Options{})
	body, logs := loggedViewer(t, r, panel.URL+"/live/user/pass/1005.ts")

	_, err := io.CopyN(io.Discard, body, 10_000)

	require.NoError(t, err)
	text := logs()
	assert.Contains(t, text, "livetv: upstream answered")
	assert.Regexp(t, `path="127\.0\.0\.1:\d+ conn \S+ answered \S+ > 127\.0\.0\.1:\d+ conn \S+ answered \S+"`, text)
	assert.NotContains(t, text, "pass")
}
