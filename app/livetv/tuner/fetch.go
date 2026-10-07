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
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxErrorLen is the CRD's bound on status.playlist.error and guides[].error.
const maxErrorLen = 1024

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
	resp, err := t.o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %d from %s", resp.StatusCode, iptv.Redact(url))
	}
	return resp.Body, nil
}

func (t *Tuner) fetchPlaylist(ctx context.Context, p *provider, url, ua string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var (
		pl  m3u.Playlist
		sum string
	)
	body, err := t.get(ctx, url, ua)
	if err == nil {
		h := sha256.New()
		pl, err = m3u.Parse(io.TeeReader(body, h))
		_ = body.Close()
		sum = hex.EncodeToString(h.Sum(nil))
	}
	fetchDuration.WithLabelValues(label(p.key), "playlist").Observe(time.Since(start).Seconds())

	p.mu.Lock()
	delete(p.fetching, "playlist")
	if err != nil {
		p.playlist.Error = clamp(iptv.RedactError(fmt.Errorf("fetch playlist: %w", err)))
		logging.FromContext(ctx).Warn("livetv: playlist fetch failed", "provider", label(p.key), "err", p.playlist.Error)
	} else {
		p.entries = pl.Entries
		p.playlistFetched = true
		p.playlist.FetchedAt = t.o.Now()
		p.playlist.Hash = sum
		p.playlist.Error = ""
		t.markWantedGuidesDue(p)
	}
	p.playlistNext = t.o.Now().Add(p.cfg.PlaylistRefresh)
	p.mu.Unlock()
	t.scheduleRender(p)
	t.poke()
}

// wantSource is what choosing a guide's wanted channels needs of one active
// channel, copied out from under the provider's lock.
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

// wanted is the channel ids of guide name that src map to, given the
// guide's channels.
func wanted(name string, chans []xmltv.Channel, src []wantSource) map[string]bool {
	out := map[string]bool{}
	ix := xmltv.NewIndex(map[string]xmltv.Guide{name: {Channels: chans}})
	for _, s := range src {
		if s.m != nil {
			if s.m.Guide == name {
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

// markWantedGuidesDue marks due every loaded guide whose wanted channels
// now include one it was not parsed for; p.mu is held.
func (t *Tuner) markWantedGuidesDue(p *provider) {
	if !p.playlistFetched {
		return
	}
	src := wantSources(p)
	for name, g := range p.guides {
		if !g.loaded {
			continue
		}
		for id := range wanted(name, g.g.Channels, src) {
			if !g.wanted[id] {
				g.due = true
				break
			}
		}
	}
}

func (t *Tuner) fetchGuide(ctx context.Context, p *provider, name string) {
	start := time.Now()
	p.mu.Lock()
	g := p.guides[name]
	if g == nil {
		delete(p.fetching, "guide/"+name)
		p.mu.Unlock()
		return
	}
	url, ua, src := g.cfg.URL, p.cfg.UserAgent, wantSources(p)
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	now := t.o.Now()
	var (
		parsed xmltv.Guide
		want   map[string]bool
	)
	body, err := t.get(ctx, url, ua)
	if err == nil {
		parsed, err = xmltv.ParseFunc(body, func(chans []xmltv.Channel) map[string]bool {
			want = wanted(name, chans, src)
			return want
		}, now, now.AddDate(0, 0, guideDays))
		_ = body.Close()
		if want == nil {
			want = wanted(name, parsed.Channels, src) // a guide with no programmes
		}
	}
	fetchDuration.WithLabelValues(label(p.key), name).Observe(time.Since(start).Seconds())

	p.mu.Lock()
	delete(p.fetching, "guide/"+name)
	g = p.guides[name]
	if g == nil || p.removed {
		p.mu.Unlock()
		return
	}
	if err != nil {
		g.state.Error = clamp(iptv.RedactError(fmt.Errorf("fetch guide %s: %w", name, err)))
		logging.FromContext(ctx).Warn("livetv: guide fetch failed", "provider", label(p.key), "guide", name, "err", g.state.Error)
	} else {
		g.g, g.loaded, g.wanted = parsed, true, want
		n := 0
		for _, ps := range parsed.Programmes {
			n += len(ps)
		}
		g.state = GuideState{Name: name, Channels: len(parsed.Channels), Programmes: n, FetchedAt: t.o.Now()}
	}
	g.next = t.o.Now().Add(g.cfg.Refresh)
	p.mu.Unlock()
	if err == nil {
		t.putGuideChannels(ctx, p, name, parsed.Channels)
	}
	t.scheduleRender(p)
}

// putGuideChannels stores a guide's channel list for the ui's mapping
// dropdown.
func (t *Tuner) putGuideChannels(ctx context.Context, p *provider, name string, chans []xmltv.Channel) {
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
	t.putJSON(ctx, GuideChannelsObject(p.key, name), rows)
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
	meta := events.ObjectMeta{Headers: map[string]string{"Content-Type": contentType, "Content-Encoding": "gzip"}}
	if _, err := t.o.Store.Put(ctx, name, &zb, meta); err != nil {
		logging.FromContext(ctx).Warn("livetv: store object", "object", name, "err", err)
	}
}

// loadStoredGuide serves a block's stored XEPG guide until the guides have been
// fetched in this process, so a restart never blanks Plex's guide.
func (t *Tuner) loadStoredGuide(p *provider, start string) {
	if t.o.Store == nil {
		return
	}
	_, rc, err := t.o.Store.Get(t.base, BlockGuideObject(p.key, start))
	if err != nil {
		return
	}
	defer func() { _ = rc.Close() }()
	zr, err := gzip.NewReader(io.LimitReader(rc, xmltv.MaxBytes))
	if err != nil {
		return
	}
	b, err := io.ReadAll(io.LimitReader(zr, xmltv.MaxBytes))
	if err != nil {
		return
	}
	sum := sha256.Sum256(b)
	p.mu.Lock()
	if s := p.blocks[start]; s != nil && s.guideXML == nil {
		s.guideXML, s.storedGuide = b, true
		s.guideHash = hex.EncodeToString(sum[:])
		s.storedGuideHash = s.guideHash
	}
	p.mu.Unlock()
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
