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

// guideFrom is where every guide starts: the day's start, so a guide's ETag
// moves when the guide does, not as its programmes end. cluster-plex reloads
// a DVR's guide on every change of ETag, and each reload rebuilds PMS's
// guide database.
func guideFrom(now time.Time) time.Time { return now.UTC().Truncate(24 * time.Hour) }

// render rebuilds p's lineup, guide and snapshot from its configuration,
// playlist and guide, stores what changed, and notifies a changed snapshot.
// The whole guide is built outside p.mu, which lineups and tunes take: a few
// thousand channels' guide takes seconds. A render that finds a newer one
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

	cands := filter.Candidates(p.entries, cfg.Filters)
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

	loaded := map[string]xmltv.Guide{}
	if p.guide.loaded {
		loaded[GuideName] = p.guide.g
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
	// Before the first playlist no channel is placed: every one would read
	// Missing, and its status flip there and back after each restart.
	for _, ch := range chans {
		if !fetched {
			break
		}
		var st ChannelState
		e, ok := candidate[ch.Key]
		st.Missing = !ok
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
			case ch.Map != nil:
				m := *ch.Map
				lc.Map = &m
			case ch.DummyMinutes == 0:
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
	from := guideFrom(t.o.Now())
	p.mu.Unlock()

	// Split the lineup by block: a channel belongs to the block listing its
	// number, else to the last block starting at or below it (spec §3.1).
	blockEntries, blockChans := splitBlocks(cfg.Blocks, entries, lineupChans)
	build := func(lc []xmltv.LineupChannel) []byte {
		var b bytes.Buffer
		_ = buildGuide(&b, lc, loaded, from, guideDays)
		return b.Bytes()
	}
	// Each block's guide is built for its device; the whole provider's for
	// the store (the ui reads it); a range's when it is first asked for.
	var whole []byte
	blockGuides := make([][]byte, len(cfg.Blocks))
	if !pms && fetched {
		whole = build(lineupChans)
		for i := range cfg.Blocks {
			blockGuides[i] = build(blockChans[i])
		}
	}

	p.mu.Lock()
	if p.removed || p.renderGen != gen {
		p.mu.Unlock()
		return
	}
	var putGuide bool
	if whole != nil {
		if h := sha(whole); h != p.storedGuideHash {
			p.storedGuideHash, putGuide = h, true
		}
	}
	p.lineup, p.lineupChans, p.loaded, p.guideFrom = entries, lineupChans, loaded, from
	p.ranges = nil
	p.streams = streams
	blocks := make(map[string]*surface, len(cfg.Blocks))
	blockStates := make([]BlockState, len(cfg.Blocks))
	for i, b := range cfg.Blocks {
		sf := &surface{block: b, entries: blockEntries[i], guideXML: blockGuides[i]}
		if fetched {
			sf.lineupHash = sha(hdhr.Lineup(sf.entries))
		}
		if sf.guideXML != nil {
			sf.guideHash = sha(sf.guideXML)
		}
		blocks[b.DeviceID] = sf
		blockStates[i] = BlockState{Start: b.Start, Active: len(sf.entries), LineupHash: sf.lineupHash, GuideHash: sf.guideHash}
	}
	p.blocks = blocks

	pl := p.playlist
	pl.Entries, pl.Groups, pl.Candidates = len(p.entries), len(groups), len(cands)
	lineup := LineupState{Active: active, Unmapped: unmapped, Missing: missing}
	if len(entries) > 0 {
		lineup.First, lineup.Last = entries[0].GuideNumber, entries[len(entries)-1].GuideNumber
	}
	p.snap = Snapshot{
		PlaylistFetched: fetched,
		Playlist:        pl,
		Guide:           p.guide.state,
		Account:         p.account,
		Lineup:          lineup,
		Blocks:          blockStates,
		Tuners:          effectiveTuners(cfg, p.account),
		Channels:        states,
	}

	putPlaylist := fetched && p.playlist.Hash != "" && p.playlist.Hash != p.storedPlaylistHash
	var playlistEntries []m3u.Entry
	if putPlaylist {
		p.storedPlaylistHash = p.playlist.Hash
		playlistEntries = p.entries // replaced, never mutated
	}
	plHash, plFetched := p.playlist.Hash, p.playlist.FetchedAt
	notify := p.notified == nil || !reflect.DeepEqual(*p.notified, p.snap)
	if notify {
		n := cloneSnapshot(p.snap)
		p.notified = &n
	}
	p.mu.Unlock()

	if putPlaylist {
		t.putPlaylistCache(t.base, p, cfg, plHash, plFetched, playlistEntries)
	}
	if putGuide {
		t.putGzip(t.base, GuideObject(p.key), whole, "application/xml")
	}
	if notify {
		t.o.Notify(p.key)
	}
}

// splitBlocks is the lineup's entries and channels by block: a channel
// belongs to the block listing its number, else to the last block starting
// at or below it.
func splitBlocks(bs []BlockConfig, entries []hdhr.LineupEntry, chans []xmltv.LineupChannel) ([][]hdhr.LineupEntry, [][]xmltv.LineupChannel) {
	member := map[string]int{}
	for i, b := range bs {
		for _, n := range b.Numbers {
			member[n] = i
		}
	}
	blockOf := func(number string) int {
		if i, ok := member[number]; ok {
			return i
		}
		i := 0
		for j, b := range bs {
			if xmltv.CompareNumbers(b.Start, number) <= 0 {
				i = j
			}
		}
		return i
	}
	outE := make([][]hdhr.LineupEntry, len(bs))
	outC := make([][]xmltv.LineupChannel, len(bs))
	if len(bs) == 0 {
		return outE, outC
	}
	for i, e := range entries {
		bi := blockOf(e.GuideNumber)
		outE[bi] = append(outE[bi], e)
		outC[bi] = append(outC[bi], chans[i])
	}
	return outE, outC
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
