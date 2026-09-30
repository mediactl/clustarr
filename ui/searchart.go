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
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

const (
	// searchArtMaxBytes caps a proxied search poster.
	searchArtMaxBytes = 5 << 20
	// searchArtEntries, searchArtCacheBytes and searchArtTTL bound the
	// poster cache: by count, and by bytes so it fits the ui pod's 256Mi
	// limit however large the posters are.
	searchArtEntries    = 256
	searchArtCacheBytes = 32 << 20
	searchArtTTL        = time.Hour
)

// searchArtHosts are the only hosts /art/search fetches from: the image
// hosts of the providers Add New searches -- TMDB, TVDB, MusicBrainz's
// Cover Art Archive and Open Library.
var searchArtHosts = map[string]bool{
	"image.tmdb.org":         true,
	"artworks.thetvdb.com":   true,
	"coverartarchive.org":    true,
	"covers.openlibrary.org": true,
}

var errSearchArtRefused = errors.New("ui: search art: refused")

// archiveHost reports whether host is archive.org or one of its storage
// hosts, where Open Library author photos and every Cover Art Archive image
// redirect (verified live, 2026-09-29). A redirect may reach one; a signed
// URL never starts at one.
func archiveHost(host string) bool {
	return host == "archive.org" || strings.HasSuffix(host, ".archive.org")
}

type cachedArt struct {
	body        []byte
	contentType string
	at          time.Time
}

// searchArt serves an Add New search hit's poster (GET /art/search) so the
// browser never loads a provider URL (ADR-0011): a hit has a provider
// poster URL and no artwork object. It fetches only a URL it signed
// itself, only over https, only from searchArtHosts -- a redirect too --
// only an image, and at most searchArtMaxBytes; it keeps the last
// searchArtEntries for searchArtTTL. The signing key is Options.ArtSigningKey
// when one is configured, so a signed URL outlives the process -- Plex
// stores the photo URLs the provider hands it -- else per process, good
// until the ui restarts.
type searchArt struct {
	key    []byte
	hosts  map[string]bool
	client *http.Client
	// redirectHost is where a redirect may go beyond hosts: archiveHost.
	redirectHost func(host string) bool
	cache        *lru.Cache[string, cachedArt]

	// mu guards cachedBytes, the bytes the cache holds, which Add keeps
	// at or under maxCacheBytes by evicting the oldest.
	mu            sync.Mutex
	cachedBytes   int64
	maxCacheBytes int64
}

func newSearchArt(key []byte) *searchArt {
	if len(key) == 0 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	a := &searchArt{key: key, hosts: searchArtHosts, redirectHost: archiveHost, maxCacheBytes: searchArtCacheBytes}
	a.cache, _ = lru.NewWithEvict[string, cachedArt](searchArtEntries, func(_ string, v cachedArt) {
		a.cachedBytes -= int64(len(v.body))
	})
	a.client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: a.checkRedirect}
	return a
}

func (a *searchArt) allowed(u *url.URL) bool { return u.Scheme == "https" && a.hosts[u.Host] }

func (a *searchArt) checkRedirect(req *http.Request, via []*http.Request) error {
	u := req.URL
	reachable := a.allowed(u) || (u.Scheme == "https" && a.redirectHost(u.Host))
	if len(via) >= 3 || !reachable {
		return errSearchArtRefused
	}
	return nil
}

func (a *searchArt) sign(src string) string {
	m := hmac.New(sha256.New, a.key)
	_, _ = m.Write([]byte(src))
	return hex.EncodeToString(m.Sum(nil))
}

// URL is the ui path a template renders for a provider poster, or "" for
// one it will not fetch, which the template shows as a placeholder.
func (a *searchArt) URL(src string) string {
	u, err := url.Parse(src)
	if src == "" || err != nil || !a.allowed(u) {
		return ""
	}
	return "/art/search?src=" + url.QueryEscape(src) + "&sig=" + a.sign(src)
}

func (a *searchArt) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	src, sig := r.URL.Query().Get("src"), r.URL.Query().Get("sig")
	u, err := url.Parse(src)
	if err != nil || !a.allowed(u) || !hmac.Equal([]byte(sig), []byte(a.sign(src))) {
		http.Error(w, "not a signed search poster", http.StatusForbidden)
		return
	}
	art, ok := a.cache.Get(src)
	if !ok || time.Since(art.at) > searchArtTTL {
		art, err = a.fetch(r.Context(), src)
		if err != nil {
			logging.FromContext(r.Context()).Debug("search poster refused", "src", src, "error", err)
			http.Error(w, "poster unavailable", http.StatusBadGateway)
			return
		}
		a.store(src, art)
	}
	w.Header().Set("Content-Type", art.contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(art.body)
}

func (a *searchArt) fetch(ctx context.Context, src string) (cachedArt, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return cachedArt{}, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return cachedArt{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "image/") {
		return cachedArt{}, errSearchArtRefused
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, searchArtMaxBytes+1))
	if err != nil {
		return cachedArt{}, err
	}
	if len(body) > searchArtMaxBytes {
		return cachedArt{}, errSearchArtRefused
	}
	return cachedArt{body: body, contentType: ct, at: time.Now()}, nil
}

// store caches art, evicting the oldest entries until the cache holds at
// most maxCacheBytes. A poster larger than the whole budget is not kept.
func (a *searchArt) store(src string, art cachedArt) {
	size := int64(len(art.body))
	a.mu.Lock()
	defer a.mu.Unlock()
	if size > a.maxCacheBytes {
		return
	}
	a.cache.Remove(src)
	for a.cachedBytes+size > a.maxCacheBytes {
		if _, _, ok := a.cache.RemoveOldest(); !ok {
			break
		}
	}
	a.cachedBytes += size
	a.cache.Add(src, art)
}
