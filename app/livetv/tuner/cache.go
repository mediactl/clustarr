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

package tuner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The placeholders a cached stream URL carries for the account's
// credentials.
const (
	placeholderUsername = "{username}"
	placeholderPassword = "{password}"
)

// cachedGuide is the guide object: every channel, the programmes of the
// channels the lineup wanted, and which those were.
type cachedGuide struct {
	FetchedAt time.Time `json:"fetchedAt"`
	// Source is the guide's guideFingerprint; a cache from another source
	// is served but fetched again at once. Empty (before 2026-10-27) is the
	// panel's.
	Source string      `json:"source,omitempty"`
	Wanted []string    `json:"wanted"`
	Guide  xmltv.Guide `json:"guide"`
}

// accountFingerprint names the account a cache belongs to without
// carrying its credentials.
func accountFingerprint(c Config) string {
	sum := sha256.Sum256([]byte(c.Server + "\x00" + c.Username))
	return hex.EncodeToString(sum[:8])
}

// templateURL is raw with each path segment and query value equal to the
// account's username or password replaced by its placeholder; false when
// raw cannot be parsed or names a credential any other way (userinfo).
func templateURL(raw, user, pass string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return "", false
	}
	path, query, _ := strings.Cut(strings.TrimPrefix(raw, u.Scheme+"://"+u.Host), "?")
	segs := strings.Split(path, "/")
	for i, s := range segs {
		switch {
		case s == "":
		case s == user || s == url.PathEscape(user):
			segs[i] = placeholderUsername
		case s == pass || s == url.PathEscape(pass):
			segs[i] = placeholderPassword
		}
	}
	out := u.Scheme + "://" + u.Host + strings.Join(segs, "/")
	if query != "" {
		pairs := strings.Split(query, "&")
		for i, kv := range pairs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch {
			case v == "":
			case v == url.QueryEscape(user):
				pairs[i] = k + "=" + placeholderUsername
			case v == url.QueryEscape(pass):
				pairs[i] = k + "=" + placeholderPassword
			}
		}
		out += "?" + strings.Join(pairs, "&")
	}
	return out, true
}

// expandURL is templateURL undone for user and pass.
func expandURL(tmpl, user, pass string) string {
	path, query, hasQuery := strings.Cut(tmpl, "?")
	path = strings.NewReplacer(placeholderUsername, url.PathEscape(user), placeholderPassword, url.PathEscape(pass)).Replace(path)
	if !hasQuery {
		return path
	}
	query = strings.NewReplacer(placeholderUsername, url.QueryEscape(user), placeholderPassword, url.QueryEscape(pass)).Replace(query)
	return path + "?" + query
}

// cachePlaylist is entries as the playlist object holds them; false when an
// entry's stream URL cannot be templated, which caches nothing rather than
// a credential.
func cachePlaylist(c Config, hash string, fetchedAt time.Time, entries []m3u.Entry) (livetvstore.Playlist, bool) {
	out := livetvstore.Playlist{Account: accountFingerprint(c), Hash: hash, FetchedAt: fetchedAt, Entries: make([]livetvstore.PlaylistEntry, 0, len(entries))}
	for _, e := range entries {
		tmpl, ok := templateURL(e.URL, c.Username, c.Password)
		if !ok {
			return livetvstore.Playlist{}, false
		}
		out.Entries = append(out.Entries, livetvstore.PlaylistEntry{
			Key: e.Key, Name: e.Name, Group: e.Group, TvgID: e.TvgID, TvgName: e.TvgName, Logo: e.Logo,
			URL: tmpl, UserAgent: e.UserAgent,
		})
	}
	return out, true
}

// loadCache starts a provider the tuner has not served before from its
// cached playlist and guide: each is served at once, and fetched again
// only once it is older than c.Every. A cache that is missing, unreadable
// or another account's is passed over, and the source is fetched now.
func (t *Tuner) loadCache(p *provider, c Config) {
	if t.o.Store == nil {
		return
	}
	now := t.o.Now()
	var pl livetvstore.Playlist
	if t.readJSON(livetvstore.PlaylistObject(p.key), &pl) && pl.Account == accountFingerprint(c) && len(pl.Entries) > 0 {
		entries := make([]m3u.Entry, 0, len(pl.Entries))
		for _, e := range pl.Entries {
			entries = append(entries, m3u.Entry{
				Key: e.Key, Name: e.Name, Group: e.Group, TvgID: e.TvgID, TvgName: e.TvgName, Logo: e.Logo,
				URL: expandURL(e.URL, c.Username, c.Password), UserAgent: e.UserAgent,
			})
		}
		p.mu.Lock()
		p.entries, p.playlistFetched = entries, true
		p.playlist.Hash, p.playlist.FetchedAt = pl.Hash, pl.FetchedAt
		p.storedPlaylistHash = pl.Hash
		p.playlistDue, p.playlistNext = false, pl.FetchedAt.Add(c.Every)
		if !p.playlistNext.After(now) {
			p.playlistDue = true
		}
		p.mu.Unlock()
	}
	var g cachedGuide
	if t.readJSON(livetvstore.GuideCacheObject(p.key), &g) {
		wanted := make(map[string]bool, len(g.Wanted))
		for _, id := range g.Wanted {
			wanted[id] = true
		}
		next := g.FetchedAt.Add(c.Every)
		p.mu.Lock()
		source := g.Source
		if source == "" {
			source = guideFingerprint(Config{Server: c.Server, Username: c.Username})
		}
		due := !next.After(now) || source != guideFingerprint(c)
		p.guide = guide{g: g.Guide, loaded: true, wanted: wanted, next: next, due: due, state: guideState(g.Guide, g.FetchedAt)}
		p.mu.Unlock()
	}
}

// readJSON decodes the gzip JSON object name into v.
func (t *Tuner) readJSON(name string, v any) bool {
	_, rc, err := t.o.Store.Get(t.base, name)
	if err != nil {
		return false
	}
	defer func() { _ = rc.Close() }()
	return livetvstore.Decode(rc, v) == nil
}

// putPlaylistCache stores the playlist object for a fetched playlist.
func (t *Tuner) putPlaylistCache(ctx context.Context, p *provider, c Config, hash string, fetchedAt time.Time, entries []m3u.Entry) {
	pl, ok := cachePlaylist(c, hash, fetchedAt, entries)
	if !ok {
		logging.FromContext(ctx).Warn("livetv: a stream URL names the account another way; the playlist is not cached", "provider", label(p.key))
		return
	}
	t.putJSON(ctx, livetvstore.PlaylistObject(p.key), pl)
}

// putGuideCache stores the guide object for a fetched guide.
func (t *Tuner) putGuideCache(ctx context.Context, p *provider, cfg Config, g xmltv.Guide, want map[string]bool, fetchedAt time.Time) {
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	t.putJSON(ctx, livetvstore.GuideCacheObject(p.key), cachedGuide{FetchedAt: fetchedAt, Source: guideFingerprint(cfg), Wanted: ids, Guide: g})
}
