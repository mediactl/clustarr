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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

const (
	sessionCookieValue = "SECRETUIDVALUE"
	sessionHeaderValue = "SECRETHEADERVALUE"
)

// seen records what one test server was sent.
type seen struct {
	mu      sync.Mutex
	hits    int
	cookies []string
	headers []string
}

func (s *seen) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	s.cookies = append(s.cookies, r.Header.Get("Cookie"))
	s.headers = append(s.headers, r.Header.Get("X-Session"))
}

func (s *seen) snapshot() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits, strings.Join(s.cookies, "|") + strings.Join(s.headers, "|")
}

// offSite rewrites an httptest URL (always 127.0.0.1) onto "localhost": the
// same listener, but another HOST as far as the site rule is concerned --
// which is what an attacker's server is.
func offSite(u string) string { return strings.Replace(u, "127.0.0.1", "localhost", 1) }

// evilServer is a host that is not the tracker: it records anything it is
// sent and serves a torrent-shaped body, so a fetch that should never have
// happened succeeds rather than failing for an unrelated reason.
func evilServer(t *testing.T) (*seen, string) {
	t.Helper()
	var s seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		_, _ = w.Write([]byte(fakeTorrent))
	}))
	t.Cleanup(srv.Close)
	return &s, offSite(srv.URL)
}

func loggedIn(t *testing.T, def *cardigann.Definition, site string) cardigann.Config {
	t.Helper()
	cfg, err := cardigann.NewConfig(def, site+"/", nil)
	require.NoError(t, err)
	cfg.Session = &cardigann.Session{
		Cookies: []*http.Cookie{{Name: "uid", Value: sessionCookieValue}},
		Headers: http.Header{"X-Session": {sessionHeaderValue}},
	}
	return cfg
}

// A link naming another host than the definition's site is never fetched:
// it is the caller's URL, not the tracker's, and fetching it with the
// tracker's session is how a private tracker's cookies reach an attacker --
// and fetching it at all is how an in-cluster or metadata address answers
// through indexarr. Download refuses it with ErrOffSite before any request.
func TestEngineDownloadRefusesAnOffSiteLink(t *testing.T) {
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeTorrent))
	}))
	t.Cleanup(tracker.Close)
	evil, evilURL := evilServer(t)

	def := &cardigann.Definition{Links: []string{tracker.URL + "/"}}
	cfg := loggedIn(t, def, tracker.URL)

	_, err := cardigann.Engine{HTTP: tracker.Client()}.Download(context.Background(), def, cfg, evilURL+"/steal?x=1")
	require.ErrorIs(t, err, cardigann.ErrOffSite)
	assert.NotContains(t, err.Error(), "x=1", "the refusal names the URL redacted")
	hits, _ := evil.snapshot()
	assert.Zero(t, hits, "an off-site link must never be requested")

	// The site's own links, absolute or relative, are still fetched.
	for _, link := range []string{tracker.URL + "/dl/1", "/dl/1"} {
		rc, err := cardigann.Engine{HTTP: tracker.Client()}.Download(context.Background(), def, cfg, link)
		require.NoError(t, err, link)
		_ = rc.Close()
	}
}

// The session is the tracker's: it is attached to a request on the site and
// to nothing else -- not to a download selector's link on another host (the
// 1337x shape: the details page points at a torrent cache), and not to the
// next hop of a redirect that leaves the site. Go's client drops a Cookie
// header on a cross-host redirect by itself, but copies every other header,
// so a session header would otherwise follow the redirect off-site.
func TestEngineSessionIsNeverSentOffSite(t *testing.T) {
	evil, evilURL := evilServer(t)
	var site seen
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site.record(r)
		switch r.URL.Path {
		case "/details/1":
			_, _ = w.Write([]byte(`<a class="dl" href="` + evilURL + `/cache/1.torrent">download</a>`))
		case "/hop":
			http.Redirect(w, r, evilURL+"/landing", http.StatusFound)
		}
	}))
	t.Cleanup(tracker.Close)

	t.Run("a selector's off-site link", func(t *testing.T) {
		def := &cardigann.Definition{
			Links: []string{tracker.URL + "/"},
			Download: &cardigann.DownloadBlock{
				Selectors: []cardigann.SelectorField{{Selector: "a.dl", Attribute: "href"}},
			},
		}
		cfg := loggedIn(t, def, tracker.URL)
		rc, err := cardigann.Engine{HTTP: tracker.Client()}.Download(context.Background(), def, cfg, "/details/1")
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		_ = rc.Close()
		assert.Equal(t, fakeTorrent, string(body), "the off-site file is still fetched, just without the session")
	})

	t.Run("a redirect off the site", func(t *testing.T) {
		def := &cardigann.Definition{Links: []string{tracker.URL + "/"}}
		cfg := loggedIn(t, def, tracker.URL)
		rc, err := cardigann.Engine{HTTP: tracker.Client()}.Download(context.Background(), def, cfg, "/hop")
		require.NoError(t, err)
		_ = rc.Close()
	})

	hits, sent := evil.snapshot()
	require.Equal(t, 2, hits)
	assert.NotContains(t, sent, sessionCookieValue)
	assert.NotContains(t, sent, sessionHeaderValue)

	_, onSite := site.snapshot()
	assert.Contains(t, onSite, sessionCookieValue, "the site itself still gets the session cookie")
	assert.Contains(t, onSite, sessionHeaderValue, "and the session header")
}

// A span is exported to the trace backend, which is not where an indexer
// passkey belongs: every error recorded on one is redacted exactly like the
// error returned. net/http's *url.Error carries the whole request URL.
func TestEngineSpanErrorsCarryNoSecret(t *testing.T) {
	const secret = "SPANSECRETPASSKEY"
	recorder := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing listening: Do fails with a *url.Error naming the URL

	def := &cardigann.Definition{Links: []string{base + "/"}}
	cfg, err := cardigann.NewConfig(def, base+"/", nil)
	require.NoError(t, err)
	_, err = cardigann.Engine{}.Download(context.Background(), def, cfg, "/dl.php?id=42&passkey="+secret)
	require.Error(t, err)

	spans := recorder.Ended()
	require.NotEmpty(t, spans)
	var recorded int
	for _, s := range spans {
		assert.NotContains(t, s.Status().Description, secret, s.Name())
		for _, ev := range s.Events() {
			for _, kv := range ev.Attributes {
				recorded++
				assert.NotContains(t, kv.Value.String(), secret, s.Name()+" "+ev.Name)
			}
		}
	}
	require.NotZero(t, recorded, "the failure must still be recorded on the span")
}
