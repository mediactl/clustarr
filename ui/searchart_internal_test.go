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
	a := newSearchArt(nil)
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
	// An off-allowlist host that is reachable, so only checkRedirect can
	// stop the fetch: a DNS failure would pass the test whether or not it
	// works (final review).
	offHits := 0
	off := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		offHits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG"))
	}))
	t.Cleanup(off.Close)
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, off.URL+"/x.png", http.StatusFound)
	})
	a, base := newTestSearchArt(t, mux)
	a.client.Transport = off.Client().Transport // trusts both test servers' certificates

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
	require.Zero(t, offHits, "the redirect's off-allowlist host is never fetched")
}

// TestSearchArtFollowsARedirectToTheArchive: Open Library author photos and
// every Cover Art Archive image redirect to archive.org's storage hosts
// (verified live, 2026-09-29), which a redirect may reach; a signed URL
// still never starts there.
func TestSearchArtFollowsARedirectToTheArchive(t *testing.T) {
	archive := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg"))
	}))
	t.Cleanup(archive.Close)
	archiveURL, err := url.Parse(archive.URL)
	require.NoError(t, err)
	a, base := newTestSearchArt(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, archive.URL+"/m_covers/1-M.jpg", http.StatusFound)
	}))
	a.client.Transport = archive.Client().Transport
	a.redirectHost = func(host string) bool { return host == archiveURL.Host }
	rec := serveSearchArt(a, a.URL(base+"/a/olid/OL1A-M.jpg"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "jpeg", rec.Body.String())
	require.Empty(t, a.URL(archive.URL+"/x.jpg"), "a redirect-only host is never signed")

	for host, want := range map[string]bool{
		"archive.org": true, "ia800100.us.archive.org": true,
		"archive.org.evil.example": false, "notarchive.org": false,
	} {
		require.Equal(t, want, archiveHost(host), host)
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

// TestSearchArtCacheIsBoundedInBytes: the cache fits the ui pod's memory
// limit however large the posters are, not merely by entry count.
func TestSearchArtCacheIsBoundedInBytes(t *testing.T) {
	fetched := map[string]int{}
	a, base := newTestSearchArt(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched[r.URL.Path]++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 40))
	}))
	a.maxCacheBytes = 100
	for _, p := range []string{"/a.jpg", "/b.jpg", "/c.jpg", "/a.jpg"} {
		require.Equal(t, http.StatusOK, serveSearchArt(a, a.URL(base+p)).Code)
	}
	require.Equal(t, 2, fetched["/a.jpg"], "three 40-byte posters exceed 100 bytes, so the oldest was evicted")
	require.LessOrEqual(t, a.cachedBytes, int64(100))
}

// Plex stores the photo URLs the provider hands it and loads them later, so
// a URL signed before a restart, or by another replica, must still verify:
// with ArtSigningKey set, every server signs alike. A per-process key made
// every stored cast photo, season poster and episode still a 403 after each
// deploy.
func TestSearchArtURLsSurviveARestartWithAConfiguredKey(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	src := "https://image.tmdb.org/t/p/w185/person.jpg"
	before := NewServer(t.Context(), Options{ArtSigningKey: key}).searchArt
	after := NewServer(t.Context(), Options{ArtSigningKey: key}).searchArt

	target := before.URL(src)
	require.NotEmpty(t, target)
	require.Equal(t, target, after.URL(src), "a restarted server signs the same URL alike")
	require.NotEqual(t, target, NewServer(t.Context(), Options{}).searchArt.URL(src),
		"without a configured key the server keeps its own per-process key")
}
