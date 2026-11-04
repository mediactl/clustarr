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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxPlexLineupBytes caps a Plex lineup answer: DIRECTV Mobile's 1,000
// stations are 296 KB.
const maxPlexLineupBytes = 8 << 20

// plexState is the Plex lineup a provider holds (the Plex native guide
// design §5.2).
type plexState struct {
	lineup  plexguide.Lineup
	loaded  bool
	due     bool
	next    time.Time
	backoff time.Duration
	err     string
}

// PlexMapping is one channel of a Plex block's channel map, as plexmap.json
// serves it: the device's number and the lineup channel it is paired with,
// the fields PMS's channel-map save takes (plexapi.ChannelMapping).
type PlexMapping struct {
	DeviceIdentifier string `json:"deviceIdentifier"`
	ChannelKey       string `json:"channelKey"`
	LineupIdentifier string `json:"lineupIdentifier"`
}

// fetchPlexLineup fetches cfg.Plex's lineup from Plex's cloud guide, keeps
// it and stores it; a failure keeps the last good one and backs off.
func (t *Tuner) fetchPlexLineup(ctx context.Context, p *provider, cfg Config) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	l, err := t.plexLineup(ctx, cfg)

	p.mu.Lock()
	delete(p.fetching, "plex")
	if p.removed {
		p.mu.Unlock()
		return
	}
	now := t.o.Now()
	if err != nil {
		p.plex.err = clamp(cfg.scrub(fmt.Sprintf("fetch the Plex lineup: %v", err)))
		logging.FromContext(ctx).Warn("livetv: Plex lineup fetch failed", "provider", label(p.key), "err", p.plex.err)
		p.plex.backoff = nextBackoff(p.plex.backoff, cfg.Every)
		p.plex.next = now.Add(p.plex.backoff)
	} else {
		p.plex.lineup, p.plex.loaded, p.plex.err = l, true, ""
		p.plex.backoff = 0
		p.plex.next = now.Add(cfg.Every)
	}
	p.mu.Unlock()
	if err == nil {
		t.putJSON(t.base, livetvstore.PlexLineupObject(p.key), l)
	}
	t.scheduleRender(p)
}

// plexLineup reads the lineup's title and stations.
func (t *Tuner) plexLineup(ctx context.Context, cfg Config) (plexguide.Lineup, error) {
	id := cfg.Plex.LineupID
	h := http.Header{"X-Plex-Token": {cfg.Plex.Token}, "Accept": {"application/json"}}
	var l plexguide.Lineup
	body, err := t.getWith(ctx, cfg, plexguide.LineupURL(id), h)
	if err != nil {
		return l, err
	}
	l.Title, err = plexguide.DecodeTitle(io.LimitReader(body, maxPlexLineupBytes))
	_ = body.Close()
	if err != nil {
		return l, err
	}
	if body, err = t.getWith(ctx, cfg, plexguide.ChannelsURL(id), h); err != nil {
		return l, err
	}
	l.Stations, err = plexguide.DecodeChannels(io.LimitReader(body, maxPlexLineupBytes))
	_ = body.Close()
	if err != nil {
		return l, err
	}
	if len(l.Stations) == 0 {
		return l, errors.New("the lineup lists no station")
	}
	l.ID, l.FetchedAt = id, t.o.Now()
	return l, nil
}

// loadPlexLineup reads the cached lineup when it is cfg's; p.mu is not
// held.
func (t *Tuner) loadPlexLineup(p *provider, c Config) {
	if c.Plex == nil || t.o.Store == nil {
		return
	}
	var l plexguide.Lineup
	if !t.readJSON(livetvstore.PlexLineupObject(p.key), &l) || l.ID != c.Plex.LineupID || len(l.Stations) == 0 {
		return
	}
	next := l.FetchedAt.Add(c.Every)
	p.mu.Lock()
	p.plex = plexState{lineup: l, loaded: true, next: next, due: !next.After(t.o.Now())}
	p.mu.Unlock()
}

// plexSnapshot is p's lineup for the snapshot; p.mu is held.
func (p *provider) plexSnapshot() PlexState {
	l := p.plex.lineup
	return PlexState{LineupID: l.ID, Title: l.Title, Stations: len(l.Stations), FetchedAt: l.FetchedAt, Error: p.plex.err, Loaded: p.plex.loaded}
}

// plexMap is a Plex block's channel map: each of entries' numbers paired
// on Plex (byNumber), in the entries' order, and its JSON.
func plexMap(entries []hdhr.LineupEntry, byNumber map[string]plexguide.Station) ([]PlexMapping, []byte) {
	m := make([]PlexMapping, 0, len(entries))
	for _, e := range entries {
		if s, ok := byNumber[e.GuideNumber]; ok {
			m = append(m, PlexMapping{DeviceIdentifier: e.GuideNumber, ChannelKey: s.ID, LineupIdentifier: s.VCN})
		}
	}
	b, _ := json.Marshal(m) // strings only: cannot fail
	return m, b
}
