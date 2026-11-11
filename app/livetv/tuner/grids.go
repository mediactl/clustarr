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
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/plexgrid"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The guide data of the custom-guides design (§5.3): each channel's station
// is paired over the provider's Plex lineups in order, each paired
// station's 14-day grid is fetched from Plex's guide service under a rate
// and a hold and stored, and a channel whose stored grid has programmes in
// the guide's window takes them before every XMLTV source. A fetch never
// holds a render: a render takes what the store has.
const (
	// plexGuideName is the guide the grids' programmes go by in the
	// render's guides, mapped by channel number (xmltv.Mapping.Guide).
	plexGuideName = "plex"
	// gridDays is how many days one grid request covers, today included.
	gridDays = 14
	// defaultGridRefresh is GuideSourcesConfig.Refresh when unset.
	defaultGridRefresh = 24 * time.Hour
	// defaultGridRate is GuideSourcesConfig.Rate when unset: calls a second.
	defaultGridRate = 2
	// minGridHold is the shortest hold a 429 sets, whatever its Retry-After.
	minGridHold = time.Minute
	// gridTimeout bounds one grid request.
	gridTimeout = time.Minute
	// gridReadTimeout bounds one grid object's read: a read of an object
	// the fetch pass overwrites meanwhile stalls until its deadline.
	gridReadTimeout = 10 * time.Second
)

// gridState is the grid fetch loop's state of one provider.
type gridState struct {
	// want is the grid keys the last render's pairing named, sorted.
	want []string
	// fetched is each grid object's write time and digest, as the fetch
	// pass stored it or a read found it; a key absent has none known.
	fetched map[string]gridObject
	// retry is when a grid whose fetch failed or came back empty is asked
	// for again.
	retry map[string]time.Time
	// holdUntil is the end of the hold the last 429 set.
	holdUntil time.Time
	// force makes every wanted grid due (a new refresh value, a new token).
	force bool
	// fetchedAt is when the last pass ran; err is its last failure, "" for
	// a clean pass.
	fetchedAt time.Time
	err       string
	// byNumber is the grid key whose programmes each channel took in the
	// last render, by channel number (the ranges' guides read it). The
	// programmes themselves are never kept between renders (2026-11-03:
	// the parsed guide was 84 MB of the manager's heap); each render and
	// range reads them back from the store.
	byNumber map[string]string
}

type gridObject struct {
	at     time.Time
	digest string
}

// lineupSchedule schedules the fetch of spec.guideSources' lineups, as one.
type lineupSchedule struct {
	due     bool
	force   bool
	next    time.Time
	backoff time.Duration
	err     string
}

// stationLineups is the lineups a channel's station is paired over, in
// order: the cloud lineup first when the Plex guide is enabled (or when it
// is the only one, so a dry run still shows its pairing), then
// spec.guideSources' lineups, each once.
func stationLineups(c Config) []string {
	var out []string
	add := func(id string) {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if c.Plex != nil && (c.Plex.Enabled || c.GuideSources == nil) {
		add(c.Plex.LineupID)
	}
	if c.GuideSources != nil {
		for _, id := range c.GuideSources.Lineups {
			add(id)
		}
	}
	return out
}

// cloudFetched is whether the cloud lineup is fetched under its own token.
func cloudFetched(c Config) bool {
	return c.Plex != nil && c.Plex.LineupID != "" && c.Plex.Token != ""
}

// sourceLineups is spec.guideSources' lineups the tuner fetches with its
// token: each once, the cloud lineup's left to its own fetch.
func sourceLineups(c Config) []string {
	gs := c.GuideSources
	if gs == nil || gs.Token == "" {
		return nil
	}
	var out []string
	for _, id := range gs.Lineups {
		if id == "" || slices.Contains(out, id) || (cloudFetched(c) && id == c.Plex.LineupID) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// lineupFor is lineup id as p holds it; p.mu is held.
func (p *provider) lineupFor(c Config, id string) (plexguide.Lineup, bool) {
	if c.Plex != nil && id == c.Plex.LineupID && p.plex.loaded && p.plex.lineup.ID == id {
		return p.plex.lineup, true
	}
	l, ok := p.lineups[id]
	return l, ok && len(l.Stations) > 0
}

// pairStations pairs chans; p.mu is held. recorded is each channel's
// pairing as its status records it: the first of the station lineups
// (stationLineups) to pair it Manual or Automatic, else the first held
// lineup's answer, each with its Lineup. A lineup not yet held answers with
// the pairings the channels' statuses record from it, so a restart before
// the cache or the first fetch moves nobody. cloud is the pairing with the
// cloud lineup alone, which a Plex block's map is made of; nil without one.
func (p *provider) pairStations(c Config, chans []plexguide.Channel, now time.Time) (recorded, cloud map[string]plexguide.Pairing) {
	order := stationLineups(c)
	// With several lineups, a channel's last pairing is shown only to the
	// lineup it came from or one listing its station: Pair keeps a station
	// absent from its lineup for MissingGrace, which would keep a later
	// lineup's station as missing from an earlier one.
	multi := len(order) > 1
	pairWith := func(id string) map[string]plexguide.Pairing {
		var pairs map[string]plexguide.Pairing
		if l, ok := p.lineupFor(c, id); ok {
			in := chans
			if multi {
				in = prevFor(l, chans)
			}
			pairs = plexguide.Pair(l, in, now)
			for obj, pr := range pairs {
				pr.Lineup = id
				pairs[obj] = pr
			}
			return pairs
		}
		return keptPairings(chans, id, multi)
	}
	if c.Plex != nil {
		cloud = pairWith(c.Plex.LineupID)
	}
	if len(order) == 0 {
		return nil, cloud
	}
	recorded = make(map[string]plexguide.Pairing, len(chans))
	for _, id := range order {
		pairs := cloud
		if c.Plex == nil || id != c.Plex.LineupID {
			pairs = pairWith(id)
		}
		for obj, pr := range pairs {
			cur, seen := recorded[obj]
			if !seen || (!cur.Match.OnPlex() && pr.Match.OnPlex()) || (cur.Match == plexguide.NoLineup && pr.Match != plexguide.NoLineup) {
				recorded[obj] = pr
			}
		}
	}
	return recorded, cloud
}

// prevFor is chans with each last pairing cleared that is neither l's (its
// station's id is "<lineup>-<gridKey>") nor a station l lists.
func prevFor(l plexguide.Lineup, chans []plexguide.Channel) []plexguide.Channel {
	listed := make(map[string]bool, len(l.Stations))
	for _, s := range l.Stations {
		listed[s.GridKey] = true
	}
	out := slices.Clone(chans)
	for i, ch := range out {
		st := ch.Prev.Station
		if st.ID == "" || listed[st.GridKey] || strings.HasPrefix(st.ID, l.ID+"-") {
			continue
		}
		out[i].Prev = plexguide.Prev{}
	}
	return out
}

// keptPairings is lineup id's answer before it is held: each channel keeps
// the pairing on Plex its status records (only one from id, with several
// lineups), else reads NoLineup.
func keptPairings(chans []plexguide.Channel, id string, filter bool) map[string]plexguide.Pairing {
	out := make(map[string]plexguide.Pairing, len(chans))
	for _, ch := range chans {
		out[ch.Object] = plexguide.Pairing{Match: plexguide.NoLineup}
		prev := ch.Prev
		if !prev.Match.OnPlex() || prev.Station.ID == "" || (filter && !strings.HasPrefix(prev.Station.ID, id+"-")) {
			continue
		}
		out[ch.Object] = plexguide.Pairing{Match: prev.Match, Station: prev.Station, MissingSince: prev.MissingSince, Lineup: id}
	}
	return out
}

// fetchSourceLineups fetches spec.guideSources' lineups that are missing,
// stale or forced, keeps each one fetched and stores it; a failure keeps
// the last good copy and backs the set off.
func (t *Tuner) fetchSourceLineups(ctx context.Context, p *provider, cfg Config) {
	ids := sourceLineups(cfg)
	p.mu.Lock()
	force := p.sources.force
	p.sources.force = false
	fetchedAt := make(map[string]time.Time, len(ids))
	for _, id := range ids {
		if l, ok := p.lineups[id]; ok {
			fetchedAt[id] = l.FetchedAt
		}
	}
	p.mu.Unlock()

	now := t.o.Now()
	got := map[string]plexguide.Lineup{}
	var failed error
	for _, id := range ids {
		if at, ok := fetchedAt[id]; ok && !force && now.Sub(at) < cfg.Every {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, time.Minute)
		l, err := t.plexLineup(lctx, cfg, id, cfg.GuideSources.Token)
		cancel()
		if err != nil {
			failed = fmt.Errorf("fetch the Plex lineup %s: %w", id, err)
			continue
		}
		got[id] = l
	}

	p.mu.Lock()
	if p.removed {
		p.mu.Unlock()
		return
	}
	configured := sourceLineups(p.cfg)
	if p.lineups == nil {
		p.lineups = map[string]plexguide.Lineup{}
	}
	for id, l := range got {
		if slices.Contains(configured, id) {
			p.lineups[id] = l
		} else {
			delete(got, id) // the configuration moved on meanwhile: never held or stored
		}
	}
	s := &p.sources
	if failed != nil {
		s.err = clamp(cfg.scrubGrid(failed.Error()))
		logging.FromContext(ctx).Warn("livetv: a guide-source lineup fetch failed", "provider", label(p.key), "err", s.err)
		s.force = s.force || force
		s.backoff = nextBackoff(s.backoff, cfg.Every)
		s.next = now.Add(s.backoff)
	} else {
		s.err, s.backoff = "", 0
		s.next = now.Add(cfg.Every)
		for _, id := range configured {
			if l, ok := p.lineups[id]; ok && l.FetchedAt.Add(cfg.Every).Before(s.next) {
				s.next = l.FetchedAt.Add(cfg.Every)
			}
		}
	}
	p.mu.Unlock()
	for id, l := range got {
		t.putJSON(t.base, livetvstore.PlexLineupObjectFor(p.key, id), l)
	}
}

// loadSourceLineups reads spec.guideSources' lineups from the store at a
// provider's start; p.mu is not held.
func (t *Tuner) loadSourceLineups(p *provider, c Config) {
	ids := sourceLineups(c)
	if len(ids) == 0 || t.o.Store == nil {
		return
	}
	held := map[string]plexguide.Lineup{}
	due := false
	var next time.Time
	for _, id := range ids {
		var l plexguide.Lineup
		if !t.readJSON(livetvstore.PlexLineupObjectFor(p.key, id), &l) || l.ID != id || len(l.Stations) == 0 {
			due = true
			continue
		}
		held[id] = l
		if at := l.FetchedAt.Add(c.Every); next.IsZero() || at.Before(next) {
			next = at
		}
	}
	now := t.o.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lineups == nil {
		p.lineups = map[string]plexguide.Lineup{}
	}
	maps.Copy(p.lineups, held)
	p.sources = lineupSchedule{due: due || !next.After(now), next: next}
}

// applyGuideSources marks what a new configuration c makes due; p.mu is
// held and p.cfg is still the configuration c replaces. Another set of
// lineups to fetch is fetched now (those held and fresh are kept); a new
// token or refresh value fetches every lineup and grid again, and a new
// token lifts the last token's hold and retries.
func (t *Tuner) applyGuideSources(p *provider, c Config, first, refreshed bool) {
	gs := c.GuideSources
	if gs == nil {
		p.lineups, p.sources, p.grids = nil, lineupSchedule{due: true}, gridState{}
		gridHold.DeleteLabelValues(label(p.key))
		return
	}
	old := p.cfg.GuideSources
	tokenMoved := !first && (old == nil || old.Token != gs.Token)
	if !first && !slices.Equal(sourceLineups(c), sourceLineups(p.cfg)) {
		p.sources.due = true
	}
	if tokenMoved || refreshed {
		p.sources.due, p.sources.force = true, true
		p.grids.force = true
	}
	if tokenMoved {
		p.grids.retry, p.grids.holdUntil = nil, time.Time{}
	}
	maps.DeleteFunc(p.lineups, func(id string, _ plexguide.Lineup) bool { return !slices.Contains(gs.Lineups, id) })
}

// gridRefresh is how old a stored grid may grow before it is fetched again.
func gridRefresh(gs *GuideSourcesConfig) time.Duration {
	if gs == nil || gs.Refresh <= 0 {
		return defaultGridRefresh
	}
	return gs.Refresh
}

// gridRate is the grid requests a second.
func gridRate(gs *GuideSourcesConfig) int {
	if gs == nil || gs.Rate <= 0 {
		return defaultGridRate
	}
	return gs.Rate
}

// gridsDue is whether a grid pass may start: a wanted grid has no object
// known, or one older than the refresh, and no hold is in force; p.mu is
// held.
func (p *provider) gridsDue(now time.Time) bool {
	gs := p.cfg.GuideSources
	if gs == nil || gs.Token == "" || p.fetching["grids"] || len(p.grids.want) == 0 || now.Before(p.grids.holdUntil) {
		return false
	}
	if p.grids.force {
		return true
	}
	refresh := gridRefresh(gs)
	for _, k := range p.grids.want {
		if now.Before(p.grids.retry[k]) {
			continue
		}
		if o, ok := p.grids.fetched[k]; !ok || now.Sub(o.at) >= refresh {
			return true
		}
	}
	return false
}

// fetchGrids is one grid pass: each wanted grid that is due is asked for
// today through today+13 at the configured rate and stored; a 429 holds
// every grid for max(Retry-After, 1 minute) and ends the pass. The render
// it asks for takes the grids stored.
func (t *Tuner) fetchGrids(ctx context.Context, p *provider, cfg Config) {
	gs := cfg.GuideSources
	p.mu.Lock()
	want := slices.Clone(p.grids.want)
	fetched := maps.Clone(p.grids.fetched)
	retry := maps.Clone(p.grids.retry)
	force := p.grids.force
	p.grids.force = false
	p.mu.Unlock()

	now := t.o.Now()
	refresh := gridRefresh(gs)
	from := now.UTC().Truncate(24 * time.Hour)
	to := from.AddDate(0, 0, gridDays-1)
	client := plexgrid.Client{Token: gs.Token, HTTP: t.o.Fetch, Limiter: rate.NewLimiter(rate.Limit(gridRate(gs)), 1)}
	prov := label(p.key)
	var (
		known   = map[string]gridObject{}
		stored  = map[string]gridObject{}
		failed  = map[string]time.Time{}
		hold    time.Time
		lastErr string
	)
	result := func(r string) { gridCalls.WithLabelValues(prov, r).Inc() }
	for _, k := range want {
		if ctx.Err() != nil {
			break
		}
		if !force {
			if now.Before(retry[k]) {
				continue
			}
			o, ok := fetched[k]
			if !ok && t.o.Store != nil {
				// Not seen since the start: the store says how old it is.
				if info, err := t.o.Store.Info(ctx, livetvstore.GridObject(p.key, k)); err == nil {
					o, ok = gridObject{at: info.ModTime, digest: info.Digest}, true
					known[k] = o
				}
			}
			if ok && now.Sub(o.at) < refresh {
				continue
			}
		}
		gctx, cancel := context.WithTimeout(ctx, gridTimeout)
		progs, err := client.Grid(gctx, k, from, to)
		cancel()
		var limited *plexgrid.RateLimitedError
		switch {
		case errors.As(err, &limited):
			hold = now.Add(max(limited.RetryAfter, minGridHold))
			result("held")
		case errors.Is(err, plexgrid.ErrNotFound), err == nil && len(progs) == 0:
			// The service has nothing for the station: its channel keeps
			// its next source until the next refresh asks again.
			result("empty")
			failed[k] = now.Add(refresh)
		case err != nil:
			result("error")
			lastErr = cfg.scrubGrid(fmt.Sprintf("fetch the grid of station %s: %v", k, err))
			failed[k] = now.Add(min(retryMax, refresh))
		default:
			digest, err := t.putGrid(ctx, p.key, k, progs)
			if err != nil {
				result("error")
				lastErr = fmt.Sprintf("store the grid of station %s: %v", k, err)
				failed[k] = now.Add(min(retryMax, refresh))
				break
			}
			result("ok")
			stored[k] = gridObject{at: now, digest: digest}
		}
		if !hold.IsZero() {
			gridHold.WithLabelValues(prov).Set(hold.Sub(now).Seconds())
			break
		}
	}
	if lastErr != "" {
		logging.FromContext(ctx).Warn("livetv: a Plex grid fetch failed", "provider", prov, "err", lastErr)
	}

	p.mu.Lock()
	delete(p.fetching, "grids")
	if p.removed {
		p.mu.Unlock()
		return
	}
	g := &p.grids
	if g.fetched == nil {
		g.fetched = map[string]gridObject{}
	}
	if g.retry == nil {
		g.retry = map[string]time.Time{}
	}
	for k, o := range known {
		if _, ok := g.fetched[k]; !ok {
			g.fetched[k] = o
		}
	}
	for k, o := range stored {
		g.fetched[k] = o
		delete(g.retry, k)
	}
	maps.Copy(g.retry, failed)
	if !hold.IsZero() {
		g.holdUntil = hold
	}
	g.fetchedAt, g.err = now, clamp(lastErr)
	p.mu.Unlock()
	t.scheduleRender(p)
}

// putGrid stores one grid's programmes as gzip JSON and returns the stored
// object's digest.
func (t *Tuner) putGrid(ctx context.Context, key types.NamespacedName, gridKey string, progs []plexgrid.Programme) (string, error) {
	if t.o.Store == nil {
		return "", errors.New("no store")
	}
	var b bytes.Buffer
	if err := livetvstore.Encode(&b, progs); err != nil {
		return "", err
	}
	name := livetvstore.GridObject(key, gridKey)
	info, err := t.o.Store.Put(ctx, name, &b, objectMeta(name, "application/json"))
	if err != nil {
		return "", err
	}
	return info.Digest, nil
}

// loadGrids reads the grids of keys back from the store, each once
// however many channels share its station, cut to [from, from+guideDays)
// with its programmes' inner XML rendered. Nothing of them is kept: the
// caller's render or range drops them when it is done. A grid with no
// object, or one that cannot be read, is left out: its channel keeps its
// next source.
func (t *Tuner) loadGrids(p *provider, keys []string, from time.Time) map[string][]xmltv.Programme {
	out := make(map[string][]xmltv.Programme, len(keys))
	if t.o.Store == nil || len(keys) == 0 {
		return out
	}
	to := from.AddDate(0, 0, guideDays)
	p.mu.Lock()
	absent := map[string]bool{}
	for _, k := range keys {
		// The pass found no object and nothing to store: no read to try.
		_, stored := p.grids.fetched[k]
		_, settled := p.grids.retry[k]
		absent[k] = !stored && settled
	}
	p.mu.Unlock()

	seen := map[string]gridObject{}
	for _, k := range keys {
		if _, done := out[k]; done || absent[k] {
			continue
		}
		name := livetvstore.GridObject(p.key, k)
		ctx, cancel := context.WithTimeout(t.base, gridReadTimeout)
		info, rc, err := t.o.Store.Get(ctx, name)
		if err != nil {
			cancel()
			if !errors.Is(err, events.ErrObjectNotFound) {
				logging.FromContext(t.base).Warn("livetv: read a Plex grid", "provider", label(p.key), "object", name, "err", err)
			}
			continue
		}
		var progs []plexgrid.Programme
		err = livetvstore.Decode(rc, &progs)
		_ = rc.Close()
		cancel()
		if err != nil {
			logging.FromContext(t.base).Warn("livetv: decode a Plex grid", "provider", label(p.key), "object", name, "err", err)
			continue
		}
		out[k] = windowed(progs, from, to)
		seen[k] = gridObject{at: info.ModTime, digest: info.Digest}
	}

	p.mu.Lock()
	if p.grids.fetched == nil {
		p.grids.fetched = map[string]gridObject{}
	}
	for k, o := range seen {
		// A read found an object the pass has not seen yet (a restart): its
		// age decides the pass's next ask.
		if f, ok := p.grids.fetched[k]; !ok || f.digest == "" {
			p.grids.fetched[k] = o
		}
	}
	p.mu.Unlock()
	return out
}

// windowed is progs in [from, to), as the guide's programmes.
func windowed(progs []plexgrid.Programme, from, to time.Time) []xmltv.Programme {
	var out []xmltv.Programme
	for _, pr := range progs {
		if !pr.Stop.After(from) || !pr.Start.Before(to) {
			continue
		}
		out = append(out, xmltv.Programme{Start: pr.Start, Stop: pr.Stop, Inner: pr.Listing.Inner()})
	}
	return out
}

// mergeGrids moves each channel whose station's grid has programmes in the
// window onto them, as the "plex" guide by its number: its status reads
// plex/<gridKey>. Every other channel keeps its next source (its XMLTV
// mapping, else its dummy guide). objOf names each number's IPTVChannel,
// gridOf its station's grid key. It returns the guide, each merged
// channel's grid key by number, and how many dummy channels it mapped.
func mergeGrids(chans []xmltv.LineupChannel, objOf, gridOf map[string]string, grids map[string][]xmltv.Programme, states map[string]ChannelState) (xmltv.Guide, map[string]string, int) {
	g := xmltv.Guide{Programmes: map[string][]xmltv.Programme{}}
	byNumber := map[string]string{}
	mapped := 0
	for i := range chans {
		lc := &chans[i]
		k := gridOf[lc.Number]
		progs := grids[k]
		if k == "" || len(progs) == 0 || lc.TimeshiftMinutes != 0 {
			continue
		}
		if lc.Map == nil {
			mapped++
		}
		lc.Map, lc.DummyMinutes = &xmltv.Mapping{Guide: plexGuideName, ChannelID: lc.Number}, 0
		own := make([]xmltv.Programme, len(progs))
		for j, pr := range progs {
			pr.Channel = lc.Number
			own[j] = pr
		}
		g.Programmes[lc.Number] = own
		byNumber[lc.Number] = k
		if obj, ok := objOf[lc.Number]; ok {
			st := states[obj]
			st.Guide = plexGuideName + "/" + k
			states[obj] = st
		}
	}
	return g, byNumber, mapped
}

// plexGuide is the "plex" guide of chans as the last render merged them,
// read back from the store for a range's guide from from; false when none
// of chans takes a grid.
func (t *Tuner) plexGuide(p *provider, chans []xmltv.LineupChannel, from time.Time) (xmltv.Guide, bool) {
	p.mu.Lock()
	keyOf := map[string]string{}
	var keys []string
	for _, lc := range chans {
		if lc.Map == nil || lc.Map.Guide != plexGuideName {
			continue
		}
		if k := p.grids.byNumber[lc.Number]; k != "" {
			keyOf[lc.Number] = k
			keys = append(keys, k)
		}
	}
	p.mu.Unlock()
	if len(keys) == 0 {
		return xmltv.Guide{}, false
	}
	grids := t.loadGrids(p, keys, from)
	g := xmltv.Guide{Programmes: map[string][]xmltv.Programme{}}
	for number, k := range keyOf {
		progs := grids[k]
		if len(progs) == 0 {
			continue
		}
		own := make([]xmltv.Programme, len(progs))
		for j, pr := range progs {
			pr.Channel = number
			own[j] = pr
		}
		g.Programmes[number] = own
	}
	return g, len(g.Programmes) > 0
}

// guideSourcesSnapshot is the grid loop's state for the snapshot; p.mu is
// held.
func (p *provider) guideSourcesSnapshot(c Config) GuideSourcesState {
	gs := c.GuideSources
	if gs == nil {
		return GuideSourcesState{}
	}
	s := GuideSourcesState{
		Lineups: len(stationLineups(c)), Stations: len(p.grids.want),
		FetchedAt: p.grids.fetchedAt, HoldUntil: p.grids.holdUntil,
		Error: firstNonEmpty(p.grids.err, p.sources.err),
	}
	for _, k := range p.grids.want {
		if _, ok := p.grids.fetched[k]; ok {
			s.Grids++
		}
	}
	if gs.Token == "" {
		s.Error = cmp.Or(gs.TokenError, "no Plex token")
	}
	return s
}

// scrubGrid is scrub with the guide sources' token replaced too: the grid
// and lineup requests send it in a header only, and no error may carry it.
func (c Config) scrubGrid(s string) string {
	s = c.scrub(s)
	if c.GuideSources == nil || c.GuideSources.Token == "" {
		return s
	}
	tok := c.GuideSources.Token
	for _, form := range []string{tok, url.PathEscape(tok), url.QueryEscape(tok)} {
		s = strings.ReplaceAll(s, form, "{plexToken}")
	}
	return s
}
