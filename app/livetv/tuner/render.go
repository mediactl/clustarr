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

// guideFrom is where every guide starts: the day's start, so a guide's hash
// moves when the guide does, not as its programmes end. cluster-plex reloads
// a DVR's guide on every change of hash, and each reload rebuilds PMS's guide
// database.
func guideFrom(now time.Time) time.Time { return now.UTC().Truncate(24 * time.Hour) }

// render rebuilds p's lineup, guides and snapshot from its configuration,
// playlist and guides, stores what changed, and notifies a changed snapshot.
// The guides are built outside p.mu, which lineups and tunes take: a few
// thousand channels' guides take seconds. A render that finds a newer one
// started meanwhile drops its result.
func (t *Tuner) render(p *provider) {
	p.mu.Lock()
	p.renderTimer = nil
	if p.removed {
		p.mu.Unlock()
		return
	}
	p.renderGen++
	gen := p.renderGen
	cfg := p.cfg
	pms := cfg.EPGSource == "PMS"
	fetched := p.playlistFetched

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
	// Before the first fetch no channel is placed: every one would read
	// Missing, and its status flip there and back after each restart.
	for _, ch := range chans {
		if !fetched {
			break
		}
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
		entries = append(entries, hdhr.LineupEntry{GuideNumber: ch.Number, GuideName: name, URL: "stream/" + ch.Number})
		streams[ch.Number] = e.URL
		lineupChans = append(lineupChans, lc)
		states[ch.Object] = st
	}

	// Split the lineup by block: a channel belongs to the block listing its
	// number, else to the last block starting at or below it (spec §3.1).
	starts := make([]string, len(cfg.Blocks))
	member := map[string]int{}
	for i, b := range cfg.Blocks {
		starts[i] = b.Start
		for _, n := range b.Numbers {
			member[n] = i
		}
	}
	blockOf := func(number string) int {
		if i, ok := member[number]; ok {
			return i
		}
		i := 0
		for j, st := range starts {
			if xmltv.CompareNumbers(st, number) <= 0 {
				i = j
			}
		}
		return i
	}
	blockEntries := make([][]hdhr.LineupEntry, len(cfg.Blocks))
	blockChans := make([][]xmltv.LineupChannel, len(cfg.Blocks))
	if len(cfg.Blocks) > 0 {
		for i, e := range entries {
			bi := blockOf(e.GuideNumber)
			blockEntries[bi] = append(blockEntries[bi], e)
			blockChans[bi] = append(blockChans[bi], lineupChans[i])
		}
	}
	// A block serves its stored guide until every guide is loaded.
	build := make([]bool, len(cfg.Blocks))
	for i, b := range cfg.Blocks {
		sf := p.blocks[b.Start]
		build[i] = !pms && fetched && (allLoaded || sf == nil || !sf.storedGuide)
	}
	from := guideFrom(t.o.Now())
	p.mu.Unlock()

	type builtGuide struct {
		xml  []byte
		hash string
	}
	buildOne := func(lc []xmltv.LineupChannel) builtGuide {
		var b bytes.Buffer
		_ = buildGuide(&b, lc, loaded, from, guideDays)
		sum := sha256.Sum256(b.Bytes())
		return builtGuide{b.Bytes(), hex.EncodeToString(sum[:])}
	}
	built := make([]builtGuide, len(cfg.Blocks))
	for i := range cfg.Blocks {
		if build[i] {
			built[i] = buildOne(blockChans[i])
		}
	}
	// The whole provider's guide is built for the store (the ui reads it)
	// and not kept: /livetv/<ns>/<name>/xmltv.xml builds it on request.
	var whole builtGuide
	if !pms && fetched {
		whole = buildOne(lineupChans)
	}

	p.mu.Lock()
	if p.removed || p.renderGen != gen {
		p.mu.Unlock()
		return
	}
	type put struct {
		name string
		b    []byte
	}
	var puts []put
	if whole.hash != "" && whole.hash != p.storedGuideHash {
		puts = append(puts, put{GuideObject(p.key), whole.xml})
		p.storedGuideHash = whole.hash
	}
	blockStates := make([]BlockState, len(cfg.Blocks))
	for i, b := range cfg.Blocks {
		sf := p.blocks[b.Start]
		if sf == nil {
			sf = &surface{}
			p.blocks[b.Start] = sf
		}
		sf.entries = blockEntries[i]
		sf.lineupHash = ""
		if fetched {
			sum := sha256.Sum256(hdhr.Lineup(sf.entries))
			sf.lineupHash = hex.EncodeToString(sum[:])
		}
		switch {
		case pms:
			sf.guideXML, sf.guideHash, sf.storedGuide = nil, "", false
		case build[i]:
			sf.guideXML, sf.guideHash, sf.storedGuide = built[i].xml, built[i].hash, false
			if sf.guideHash != sf.storedGuideHash {
				puts = append(puts, put{BlockGuideObject(p.key, b.Start), sf.guideXML})
				sf.storedGuideHash = sf.guideHash
			}
		}
		blockStates[i] = BlockState{Start: b.Start, Active: len(sf.entries), LineupHash: sf.lineupHash, GuideHash: sf.guideHash}
	}
	// Drop the surfaces of blocks the current configuration no longer has.
	current := map[string]bool{}
	for _, b := range p.cfg.Blocks {
		current[b.Start] = true
	}
	for start := range p.blocks {
		if !current[start] {
			delete(p.blocks, start)
		}
	}
	p.lineupChans, p.loaded, p.guideFrom = lineupChans, loaded, from

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
		PlaylistFetched: fetched,
		Playlist:        pl,
		Guides:          guideStates,
		Lineup:          LineupState{Active: active, Unmapped: unmapped, Missing: missing},
		Blocks:          blockStates,
		Channels:        states,
	}
	p.streams = streams

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
	for _, pt := range puts {
		t.putGzip(t.base, pt.name, pt.b, "application/xml")
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

// buildGuide is xmltv.BuildGuide; a test holds it to see the render's lock.
var buildGuide = xmltv.BuildGuide
