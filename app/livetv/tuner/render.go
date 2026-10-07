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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// defaultDummyMinutes is an unmapped channel's dummy block length.
const defaultDummyMinutes = 60

// scheduleRender renders p after Debounce of quiet, but never later than
// five Debounces after the first change, so a stream of edits still renders.
func (t *Tuner) scheduleRender(p *provider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.removed {
		return
	}
	now := time.Now()
	if p.renderTimer != nil {
		if now.Sub(p.renderSince) >= 5*t.o.Debounce {
			return
		}
		p.renderTimer.Stop()
	} else {
		p.renderSince = now
	}
	p.renderTimer = time.AfterFunc(t.o.Debounce, func() { t.render(p) })
}

// baseURL is every URL the tuner hands out for p.
func baseURL(c Config) string {
	return "http://" + c.Address + "/livetv/" + c.Key.Namespace + "/" + c.Key.Name
}

// render rebuilds p's lineup, guide and snapshot from its configuration,
// playlist and guides, stores what changed, and notifies a changed snapshot.
func (t *Tuner) render(p *provider) {
	p.mu.Lock()
	p.renderTimer = nil
	if p.removed {
		p.mu.Unlock()
		return
	}
	cfg := p.cfg
	pms := cfg.EPGSource == "PMS"

	rules := cfg.Filters
	cands := filter.Candidates(p.entries, rules)
	candidate := make(map[string]m3u.Entry, len(cands))
	for _, e := range cands {
		candidate[e.Key] = e
	}
	groups := map[string]bool{}
	for _, e := range p.entries {
		if e.Group != "" {
			groups[e.Group] = true
		}
	}

	configured := map[string]bool{}
	loaded := map[string]xmltv.Guide{}
	allLoaded := true
	for _, gc := range cfg.Guides {
		configured[gc.Name] = true
		if g := p.guides[gc.Name]; g != nil && g.loaded {
			loaded[gc.Name] = g.g
		} else {
			allLoaded = false
		}
	}
	ix := xmltv.NewIndex(loaded)

	chans := slices.Clone(cfg.Channels)
	slices.SortStableFunc(chans, func(a, b ChannelConfig) int {
		if c := xmltv.CompareNumbers(a.Number, b.Number); c != 0 {
			return c
		}
		return strings.Compare(a.Object, b.Object)
	})
	var (
		entries                   []hdhr.LineupEntry
		lineupChans               []xmltv.LineupChannel
		active, unmapped, missing int
	)
	streams := map[string]string{}
	states := make(map[string]ChannelState, len(chans))
	taken := map[string]bool{}
	base := baseURL(cfg)
	for _, ch := range chans {
		var st ChannelState
		e, ok := candidate[ch.Key]
		st.Missing = !ok
		st.GuideNotFound = ch.Map != nil && !configured[ch.Map.Guide]
		if !ch.Active {
			states[ch.Object] = st
			continue
		}
		if st.Missing {
			missing++
			states[ch.Object] = st
			continue
		}
		if taken[ch.Number] {
			st.Duplicate = true
			states[ch.Object] = st
			continue
		}
		taken[ch.Number] = true
		name := firstNonEmpty(ch.Name, e.Name)
		lc := xmltv.LineupChannel{Number: ch.Number, Name: name, Logo: firstNonEmpty(ch.Logo, e.Logo), TimeshiftMinutes: ch.TimeshiftMinutes}
		if !pms {
			switch {
			case ch.Map != nil && !st.GuideNotFound:
				m := *ch.Map
				lc.Map = &m
			case ch.Map == nil && ch.DummyMinutes == 0:
				if m, ok := ix.AutoMap(e.TvgID, e.Name); ok {
					lc.Map = &m
				}
			}
			if lc.Map == nil {
				lc.DummyMinutes = ch.DummyMinutes
				if lc.DummyMinutes <= 0 {
					lc.DummyMinutes = defaultDummyMinutes
				}
				unmapped++
				st.Guide = fmt.Sprintf("dummy/%d", lc.DummyMinutes)
			} else {
				st.Guide = lc.Map.Guide + "/" + lc.Map.ChannelID
			}
		}
		active++
		entries = append(entries, hdhr.LineupEntry{GuideNumber: ch.Number, GuideName: name, URL: base + "/stream/" + ch.Number})
		streams[ch.Number] = e.URL
		lineupChans = append(lineupChans, lc)
		states[ch.Object] = st
	}

	lineup := hdhr.Lineup(entries)
	lineupHash := ""
	if p.playlistFetched {
		sum := sha256.Sum256(lineup)
		lineupHash = hex.EncodeToString(sum[:])
	}

	var putGuide []byte
	switch {
	case pms:
		p.guideXML, p.guideHash, p.storedGuide = nil, "", false
	case p.playlistFetched && (allLoaded || !p.storedGuide):
		var b bytes.Buffer
		_ = xmltv.BuildGuide(&b, lineupChans, loaded, t.o.Now(), guideDays)
		sum := sha256.Sum256(b.Bytes())
		p.guideXML, p.guideHash, p.storedGuide = b.Bytes(), hex.EncodeToString(sum[:]), false
		if p.guideHash != p.storedGuideHash {
			putGuide = p.guideXML
			p.storedGuideHash = p.guideHash
		}
	}

	guideStates := make([]GuideState, 0, len(cfg.Guides))
	for _, gc := range cfg.Guides {
		gs := GuideState{Name: gc.Name}
		if g := p.guides[gc.Name]; g != nil {
			gs = g.state
			gs.Name = gc.Name
		}
		guideStates = append(guideStates, gs)
	}
	pl := p.playlist
	pl.Entries, pl.Groups, pl.Candidates = len(p.entries), len(groups), len(cands)
	p.snap = Snapshot{
		PlaylistFetched: p.playlistFetched,
		Playlist:        pl,
		Guides:          guideStates,
		Lineup:          LineupState{Active: active, Unmapped: unmapped, Missing: missing, Hash: lineupHash},
		GuideHash:       p.guideHash,
		Channels:        states,
	}
	p.lineup, p.streams = lineup, streams

	var putPlaylist []storedEntry
	if p.playlistFetched && p.playlist.Hash != p.storedPlaylistHash {
		p.storedPlaylistHash = p.playlist.Hash
		putPlaylist = make([]storedEntry, 0, len(p.entries))
		for _, e := range p.entries {
			putPlaylist = append(putPlaylist, storedEntry{Key: e.Key, Name: e.Name, Group: e.Group, TvgID: e.TvgID, TvgName: e.TvgName, Logo: e.Logo})
		}
	}
	plHash, plFetched := p.playlist.Hash, p.playlist.FetchedAt
	notify := p.notified == nil || !reflect.DeepEqual(*p.notified, p.snap)
	if notify {
		n := cloneSnapshot(p.snap)
		p.notified = &n
	}
	p.mu.Unlock()

	if putPlaylist != nil {
		t.putJSON(t.base, PlaylistObject(p.key), storedPlaylist{Hash: plHash, FetchedAt: plFetched, Entries: putPlaylist})
	}
	if putGuide != nil {
		t.putGzip(t.base, GuideObject(p.key), putGuide, "application/xml")
	}
	if notify {
		t.o.Notify(p.key)
	}
}

// storedPlaylist is the playlist object: no stream URL or user agent, since
// the ui never holds a credential (spec §5.4).
type storedPlaylist struct {
	Hash      string        `json:"hash"`
	FetchedAt time.Time     `json:"fetchedAt"`
	Entries   []storedEntry `json:"entries"`
}

type storedEntry struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Group   string `json:"group,omitempty"`
	TvgID   string `json:"tvgID,omitempty"`
	TvgName string `json:"tvgName,omitempty"`
	Logo    string `json:"logo,omitempty"`
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
