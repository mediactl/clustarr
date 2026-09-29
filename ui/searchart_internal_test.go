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

package ui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestSearchArt points a searchArt at a local TLS server, which stands
// in for the one allowlisted provider host.
func newTestSearchArt(t *testing.T, h http.Handler) (*searchArt, string) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	a := newSearchArt()
	a.client = srv.Client()
	a.client.CheckRedirect = a.checkRedirect
	a.hosts = map[string]bool{u.Host: true}
	return a, srv.URL
}

func serveSearchArt(a *searchArt, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestSearchArtServesASignedAllowedImage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	hits := 0
	a, base := newTestSearchArt(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	target := a.URL(base + "/poster.png")
	require.True(t, strings.HasPrefix(target, "/art/search?src="), target)
	for range 2 {
		rec := serveSearchArt(a, target)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, png, rec.Body.Bytes())
		require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
		require.Equal(t, "private, max-age=3600", rec.Header().Get("Cache-Control"))
	}
	require.Equal(t, 1, hits, "the second request is served from the cache")
}

func TestSearchArtRefusesWhatItMustNotFetch(t *testing.T) {
	big := bytes.Repeat([]byte("x"), searchArtMaxBytes+1)
	mux := http.NewServeMux()
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(big)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/x.jpg", http.StatusFound)
	})
	a, base := newTestSearchArt(t, mux)

	require.Empty(t, a.URL("https://example.invalid/x.jpg"), "a host off the allowlist is never signed")
	require.Empty(t, a.URL("http://"+strings.TrimPrefix(base, "https://")+"/p.jpg"), "plain http is never signed")
	require.Empty(t, a.URL(""), "no poster is no URL")

	signed := a.URL(base + "/html")
	for name, target := range map[string]string{
		"no signature":              "/art/search?src=" + url.QueryEscape(base+"/html"),
		"a tampered signature":      strings.Replace(signed, "sig=", "sig=00", 1),
		"a non-image body":          signed,
		"an oversized body":         a.URL(base + "/big"),
		"an off-allowlist redirect": a.URL(base + "/away"),
	} {
		rec := serveSearchArt(a, target)
		require.NotEqual(t, http.StatusOK, rec.Code, name)
		require.NotContains(t, rec.Body.String(), "<html>", name)
	}
}

// TestSearchArtIsRouted: the server serves /art/search, and refuses an
// unsigned request there rather than falling through to another route.
func TestSearchArtIsRouted(t *testing.T) {
	srv := NewServer(t.Context(), Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/search?src=https://image.tmdb.org/x.jpg", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
}
