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
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mediactl/clustarr/pkg/iptv"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxErrorLen is the CRD's bound on status's error strings.
const maxErrorLen = 1024

// maxAccountBytes caps player_api.php's answer.
const maxAccountBytes = 1 << 20

// get opens url with the provider's user agent. A status of 300 or more is
// an error naming the redacted URL.
func (t *Tuner) get(ctx context.Context, url, ua string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", iptv.Redact(url), err)
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := t.o.Fetch.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %d from %s", resp.StatusCode, iptv.Redact(url))
	}
	return resp.Body, nil
}

func (t *Tuner) fetchPlaylist(ctx context.Context, p *provider, cfg Config) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var (
		pl  m3u.Playlist
		sum string
	)
	body, err := t.get(ctx, cfg.PlaylistURL(), cfg.UserAgent)
	if err == nil {
		h := sha256.New()
		pl, err = m3u.Parse(io.TeeReader(body, h))
		_ = body.Close()
		sum = hex.EncodeToString(h.Sum(nil))
	}
	fetchDuration.WithLabelValues(label(p.key), "playlist").Observe(time.Since(start).Seconds())

	p.mu.Lock()
	delete(p.fetching, "playlist")
	if err == nil && len(pl.Entries) == 0 && len(p.entries) > 0 {
		// A panel in maintenance or an expired line answers #EXTM3U alone:
		// that is a failed fetch, not a provider with no channels.
		err = errNoLiveEntries
	}
	if err != nil {
		p.playlist.Error = clamp(iptv.RedactError(fmt.Errorf("fetch playlist: %w", err)))
		logging.FromContext(ctx).Warn("livetv: playlist fetch failed", "provider", label(p.key), "err", p.playlist.Error)
		p.playlistBackoff = nextBackoff(p.playlistBackoff, cfg.Every)
		p.playlistNext = t.o.Now().Add(p.playlistBackoff)
	} else {
		p.entries = pl.Entries
		p.playlistFetched = true
		p.playlist.FetchedAt = t.o.Now()
		p.playlist.Hash = sum
		p.playlist.Error = ""
		t.markWantedGuideDue(p)
		p.playlistBackoff = 0
		p.playlistNext = t.o.Now().Add(cfg.Every)
	}
	p.mu.Unlock()
	t.scheduleRender(p)
	t.poke()
}

// errNoLiveEntries is a fetch that parsed but held no live entry, after one
// that did.
var errNoLiveEntries = errors.New("the playlist holds no live channel; serving the last one")

// nextBackoff is the wait after another failed fetch: retryStart, doubling
// to retryMax, and never past the refresh interval.
func nextBackoff(prev, refresh time.Duration) time.Duration {
	next := retryStart
	if prev > 0 {
		next = 2 * prev
	}
	next = min(next, retryMax)
	if refresh > 0 {
		next = min(next, refresh)
	}
	return next
}

// accountReport is player_api.php's answer. Panels send the numbers as
// strings or numbers, and exp_date as null for an account without end.
type accountReport struct {
	UserInfo struct {
		Auth           json.RawMessage `json:"auth"`
		Status         string          `json:"status"`
		ExpDate        json.RawMessage `json:"exp_date"`
		MaxConnections json.RawMessage `json:"max_connections"`
		ActiveCons     json.RawMessage `json:"active_cons"`
	} `json:"user_info"`
}

// flexInt reads a JSON number, a numeric string, or null (0).
func flexInt(raw json.RawMessage) int64 {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// fetchAccount asks the panel for the account's status, expiry and
// connection limit; the limit is the relay's when spec.tuners is unset.
func (t *Tuner) fetchAccount(ctx context.Context, p *provider, cfg Config) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var rep accountReport
	body, err := t.get(ctx, cfg.AccountURL(), cfg.UserAgent)
	if err == nil {
		err = json.NewDecoder(io.LimitReader(body, maxAccountBytes)).Decode(&rep)
		_ = body.Close()
		if err == nil && flexInt(rep.UserInfo.Auth) != 1 {
			err = errors.New("the panel refused the account's credentials")
		}
	}

	p.mu.Lock()
	delete(p.fetching, "account")
	if p.removed {
		p.mu.Unlock()
		return
	}
	now := t.o.Now()
	if err != nil {
		p.account.Error = clamp(iptv.RedactError(fmt.Errorf("fetch account: %w", err)))
		logging.FromContext(ctx).Warn("livetv: account fetch failed", "provider", label(p.key), "err", p.account.Error)
		p.accountBackoff = nextBackoff(p.accountBackoff, accountEvery)
		p.accountNext = now.Add(p.accountBackoff)
	} else {
		a := AccountState{
			Status:         rep.UserInfo.Status,
			MaxConnections: int(flexInt(rep.UserInfo.MaxConnections)),
			Active:         int(flexInt(rep.UserInfo.ActiveCons)),
			FetchedAt:      now,
		}
		if exp := flexInt(rep.UserInfo.ExpDate); exp > 0 {
			a.ExpiresAt = time.Unix(exp, 0).UTC()
		}
		p.account = a
		p.accountBackoff = 0
		p.accountNext = now.Add(min(cfg.Every, accountEvery))
		p.relay.SetTuners(effectiveTuners(p.cfg, p.account))
	}
	p.mu.Unlock()
	t.scheduleRender(p)
}

// wantSource is what choosing the guide's wanted channels needs of one
// active channel, copied out from under the provider's lock.
type wantSource struct {
	m           *xmltv.Mapping
	tvgID, name string
	auto        bool
}

// wantSources lists the active channels' mappings; p.mu is held.
func wantSources(p *provider) []wantSource {
	byKey := make(map[string]m3u.Entry, len(p.entries))
	for _, e := range p.entries {
		byKey[e.Key] = e
	}
	var out []wantSource
	for _, ch := range p.cfg.Channels {
		if !ch.Active {
			continue
		}
		switch {
		case ch.Map != nil:
			out = append(out, wantSource{m: ch.Map})
		case ch.DummyMinutes == 0:
			if e, ok := byKey[ch.Key]; ok {
				out = append(out, wantSource{tvgID: e.TvgID, name: e.Name, auto: true})
			}
		}
	}
	return out
}

// wanted is the guide channel ids src map to, given the guide's channels.
func wanted(chans []xmltv.Channel, src []wantSource) map[string]bool {
	out := map[string]bool{}
	ix := xmltv.NewIndex(map[string]xmltv.Guide{GuideName: {Channels: chans}})
	for _, s := range src {
		if s.m != nil {
			if s.m.Guide == GuideName {
				out[s.m.ChannelID] = true
			}
			continue
		}
		if m, ok := ix.AutoMap(s.tvgID, s.name); ok {
			out[m.ChannelID] = true
		}
	}
	return out
}

// markWantedGuideDue marks the guide due when its wanted channels now
// include one it was not parsed for; p.mu is held.
func (t *Tuner) markWantedGuideDue(p *provider) {
	if !p.playlistFetched || !p.guide.loaded {
		return
	}
	for id := range wanted(p.guide.g.Channels, wantSources(p)) {
		if !p.guide.wanted[id] {
			p.guide.due = true
			return
		}
	}
}

func (t *Tuner) fetchGuide(ctx context.Context, p *provider, cfg Config) {
	start := time.Now()
	p.mu.Lock()
	src := wantSources(p)
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	// From the day's start, as the guides are built (guideFrom), and a day
	// past their end, so the last day stays covered until the next fetch.
	from := guideFrom(t.o.Now())
	var (
		parsed xmltv.Guide
		want   map[string]bool
	)
	body, err := t.get(ctx, cfg.GuideURL(), cfg.UserAgent)
	if err == nil {
		parsed, err = xmltv.ParseFunc(body, func(chans []xmltv.Channel) map[string]bool {
			want = wanted(chans, src)
			return want
		}, from, from.AddDate(0, 0, guideDays+1))
		_ = body.Close()
		if want == nil {
			want = wanted(parsed.Channels, src) // a guide with no programmes
		}
	}
	fetchDuration.WithLabelValues(label(p.key), "guide").Observe(time.Since(start).Seconds())

	p.mu.Lock()
	delete(p.fetching, "guide")
	if p.removed {
		p.mu.Unlock()
		return
	}
	g := &p.guide
	if err != nil {
		g.state.Error = clamp(iptv.RedactError(fmt.Errorf("fetch guide: %w", err)))
		logging.FromContext(ctx).Warn("livetv: guide fetch failed", "provider", label(p.key), "err", g.state.Error)
		g.backoff = nextBackoff(g.backoff, cfg.Every)
		g.next = t.o.Now().Add(g.backoff)
	} else {
		g.g, g.loaded, g.wanted = parsed, true, want
		g.state = guideState(parsed, t.o.Now())
		g.backoff = 0
		g.next = t.o.Now().Add(cfg.Every)
	}
	p.mu.Unlock()
	if err == nil {
		t.putGuideCache(ctx, p, parsed, want, t.o.Now())
		t.putGuideChannels(ctx, p, parsed.Channels)
	}
	t.scheduleRender(p)
}

// guideState counts g's channels and programmes.
func guideState(g xmltv.Guide, fetchedAt time.Time) GuideState {
	n := 0
	for _, ps := range g.Programmes {
		n += len(ps)
	}
	return GuideState{Channels: len(g.Channels), Programmes: n, FetchedAt: fetchedAt}
}

// putGuideChannels stores the guide's channel list for the ui's mapping
// dropdown.
func (t *Tuner) putGuideChannels(ctx context.Context, p *provider, chans []xmltv.Channel) {
	type row struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName,omitempty"`
		Icon        string `json:"icon,omitempty"`
	}
	rows := make([]row, 0, len(chans))
	for _, c := range chans {
		r := row{ID: c.ID, Icon: c.Icon}
		if len(c.DisplayNames) > 0 {
			r.DisplayName = c.DisplayNames[0]
		}
		rows = append(rows, r)
	}
	t.putJSON(ctx, GuideChannelsObject(p.key), rows)
}

// putJSON stores v as gzip JSON.
func (t *Tuner) putJSON(ctx context.Context, name string, v any) {
	var raw bytes.Buffer
	if err := json.NewEncoder(&raw).Encode(v); err != nil {
		logging.FromContext(ctx).Error("livetv: encode object", "object", name, "err", err)
		return
	}
	t.putGzip(ctx, name, raw.Bytes(), "application/json")
}

// putGzip stores b gzipped.
func (t *Tuner) putGzip(ctx context.Context, name string, b []byte, contentType string) {
	if t.o.Store == nil {
		return
	}
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	_, _ = zw.Write(b)
	_ = zw.Close()
	if _, err := t.o.Store.Put(ctx, name, &zb, objectMeta(name, contentType)); err != nil {
		logging.FromContext(ctx).Warn("livetv: store object", "object", name, "err", err)
	}
}

// clamp cuts s to the CRD's error bound on a rune boundary.
func clamp(s string) string {
	if len(s) <= maxErrorLen {
		return s
	}
	cut := maxErrorLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
