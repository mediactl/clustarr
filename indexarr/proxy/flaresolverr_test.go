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

package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const challengePage = `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>checking</body></html>`

// cfTracker is a tracker behind Cloudflare: it serves the challenge page to
// any request without the clearance cookie AND the user agent that cookie was
// issued to, the way Cloudflare binds cf_clearance.
func cfTracker(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "cloudflare")
		ck, err := r.Cookie("cf_clearance")
		if err != nil || ck.Value != "cleared" || r.UserAgent() != "FS-Agent/1.0" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, challengePage)
			return
		}
		served.Add(1)
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "results for "+r.Method+" "+string(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &served
}

// fakeFlareSolverr answers /v1 like FlareSolverr and records every command.
type fakeFlareSolverr struct {
	srv   *httptest.Server
	mu    sync.Mutex
	cmds  []map[string]any
	reply string
}

func newFakeFlareSolverr(t *testing.T) *fakeFlareSolverr {
	t.Helper()
	f := &fakeFlareSolverr{reply: `{"status":"ok","message":"Challenge solved!","solution":{"status":200,` +
		`"userAgent":"FS-Agent/1.0","cookies":[{"name":"cf_clearance","value":"cleared","domain":"x","path":"/"}],` +
		`"response":"<html>solved copy</html>"}}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var cmd map[string]any
		_ = json.NewDecoder(r.Body).Decode(&cmd)
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		reply := f.reply
		f.mu.Unlock()
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFlareSolverr) commands() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.cmds...)
}

// A challenged request is solved through FlareSolverr and then sent again,
// with the clearance, through the caller's own route -- FlareSolverr's copy
// of the page is never the answer -- and the clearance is reused, so the next
// request costs no solve at all.
func TestFlareSolverrSolvesAChallengeAndReusesTheClearance(t *testing.T) {
	site, served := cfTracker(t)
	fs := newFakeFlareSolverr(t)
	rt := &FlareSolverr{Endpoint: fs.srv.URL + "/v1", Clearances: &ClearanceCache{}, Proxy: "socks5://egress:1080"}

	body, err := get(t, rt, site.URL+"/browse?q=x")
	require.NoError(t, err)
	require.Equal(t, "results for GET ", body, "the answer came through the route, not FlareSolverr's copy")
	cmds := fs.commands()
	require.Len(t, cmds, 1)
	require.Equal(t, "request.get", cmds[0]["cmd"])
	require.Equal(t, site.URL+"/browse?q=x", cmds[0]["url"])
	require.EqualValues(t, 60000, cmds[0]["maxTimeout"])
	require.Equal(t, map[string]any{"url": "socks5://egress:1080"}, cmds[0]["proxy"],
		"the solve must fetch through the Indexer's own route")

	body, err = get(t, rt, site.URL+"/browse?q=y")
	require.NoError(t, err)
	require.Equal(t, "results for GET ", body)
	require.Len(t, fs.commands(), 1, "a host with a clearance must not be solved again")
	require.Equal(t, int32(2), served.Load())
}

// A form POST is solved with request.post and its url-encoded body, and the
// body is replayed on the retry; any other POST cannot go through FlareSolverr.
func TestFlareSolverrReplaysAFormPost(t *testing.T) {
	site, _ := cfTracker(t)
	fs := newFakeFlareSolverr(t)
	rt := &FlareSolverr{Endpoint: fs.srv.URL + "/v1", Clearances: &ClearanceCache{}}

	form := url.Values{"username": {"alice"}}.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, site.URL+"/login", strings.NewReader(form))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, "results for POST "+form, string(b))
	require.Equal(t, "request.post", fs.commands()[0]["cmd"])
	require.Equal(t, form, fs.commands()[0]["postData"])

	other := &FlareSolverr{Endpoint: fs.srv.URL + "/v1", Clearances: &ClearanceCache{}}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, site.URL+"/api", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	_, err = other.RoundTrip(req)
	require.ErrorIs(t, err, ErrFlareSolverr)
}

// A failed solve is an error, not the challenge page read as a result.
func TestFlareSolverrFailuresAreErrors(t *testing.T) {
	site, _ := cfTracker(t)
	for name, reply := range map[string]string{
		"status error": `{"status":"error","message":"Error solving the challenge. Timeout after 60.0 seconds."}`,
		"no cookies":   `{"status":"ok","solution":{"userAgent":"x","cookies":[]}}`,
		"not json":     `<html>502</html>`,
	} {
		t.Run(name, func(t *testing.T) {
			fs := newFakeFlareSolverr(t)
			fs.reply = reply
			rt := &FlareSolverr{Endpoint: fs.srv.URL + "/v1", Clearances: &ClearanceCache{}}
			_, err := get(t, rt, site.URL+"/browse")
			require.ErrorIs(t, err, ErrFlareSolverr)
		})
	}

	// FlareSolverr unreachable.
	rt := &FlareSolverr{Endpoint: "http://127.0.0.1:1/v1", Clearances: &ClearanceCache{}}
	_, err := get(t, rt, site.URL+"/browse")
	require.ErrorIs(t, err, ErrFlareSolverr)
}

// Prowlarr's CloudFlareDetectionService, case by case. Only a response whose
// Server header names Cloudflare or DDoS-Guard is looked into, and the body
// it peeks at is handed back whole.
func TestIsChallenge(t *testing.T) {
	resp := func(status int, server, body string, hdr ...string) *http.Response {
		h := http.Header{}
		if server != "" {
			h.Set("Server", server)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			h.Set(hdr[i], hdr[i+1])
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
	}
	cases := []struct {
		name string
		r    *http.Response
		want bool
	}{
		{"cloudflare just-a-moment 503", resp(503, "cloudflare", challengePage), true},
		{"cloudflare access denied 403", resp(403, "cloudflare-nginx", "<title>Access denied</title>"), true},
		{"attention required", resp(403, "cloudflare", "<title>Attention Required! | Cloudflare</title>"), true},
		{"error 1020", resp(403, "cloudflare", "  error code: 1020\n"), true},
		{"ddos-guard", resp(403, "ddos-guard", "<TITLE>DDOS-GUARD</TITLE>"), true},
		{"custom ddos page", resp(200, "cloudflare", "ddos protection", "Vary", "Accept-Encoding,User-Agent"), true},
		{"a cloudflare-served result page", resp(200, "cloudflare", "<rss></rss>"), false},
		{"challenge title from another server", resp(503, "nginx", challengePage), false},
		{"cloudflare 503 without a challenge page", resp(503, "cloudflare", "maintenance"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := func() string { b, _ := io.ReadAll(tc.r.Body); return string(b) }
			got, err := IsChallenge(tc.r)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.NotEmpty(t, body(), "the peeked body must be handed back")
		})
	}
}

// errBody is a response body whose first read fails.
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errBody) Close() error             { return nil }

type fixedTripper struct{ resp *http.Response }

func (f fixedTripper) RoundTrip(*http.Request) (*http.Response, error) { return f.resp, nil }

// A body that cannot be peeked for a challenge is an error, and only an
// error: a RoundTripper that returns a response AND an error breaks
// net/http's contract, and the caller would parse a half-read body.
func TestFlareSolverrReturnsAnErrorAloneWhenThePeekFails(t *testing.T) {
	h := http.Header{}
	h.Set("Server", "cloudflare")
	rt := &FlareSolverr{
		Endpoint:   "http://127.0.0.1:1/v1",
		Clearances: &ClearanceCache{},
		Next:       fixedTripper{resp: &http.Response{StatusCode: http.StatusOK, Header: h, Body: errBody{}}},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://tracker.example/browse", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.Error(t, err)
	require.Nil(t, resp, "a RoundTripper returns a response or an error, never both")
}
