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

// Package tuner is Live TV's tuner (spec 2026-10-07 §5, amended by
// 2026-10-20-iptv-xtream-ranges-design.md): a leader-only runnable of the
// manager that, for every Xtream provider the IPTVProvider controller
// applies, fetches the playlist, the guide and the account, caches the
// first two in the clustarr-livetv object store, renders the lineup and the
// XEPG guide, serves the HDHomeRun surface for any range of channel
// numbers, relays streams, and reports a Snapshot both controllers write
// status from. It holds no Kubernetes client.
package tuner

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/livetv/relay"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

const (
	// defaultDebounce is how long a render waits for more changes.
	defaultDebounce = 2 * time.Second
	// tunersDebounce bounds how often a change in tuners in use notifies.
	tunersDebounce = 5 * time.Second
	// fetchTimeout bounds one playlist or guide fetch.
	fetchTimeout = 10 * time.Minute
	// guideDays is how far ahead the XEPG guide reaches.
	guideDays = 7
	// retryStart and retryMax bound the wait after a failed fetch: a fetch
	// that failed once is not left for a whole refresh interval.
	retryStart = 30 * time.Second
	retryMax   = 15 * time.Minute
	// accountEvery is the most the account's report may age: its expiry
	// and connections are worth more than a daily look, and it is cheap.
	accountEvery = time.Hour
	// defaultEvery is Config.Every when unset.
	defaultEvery = 24 * time.Hour
)

// Options configures a Tuner.
type Options struct {
	// BindAddress is where Start listens; empty serves nothing (tests use
	// Handler).
	BindAddress string
	// Store is the clustarr-livetv object store.
	Store events.ObjectStore
	// HTTP fetches streams. It must carry no overall Timeout: streams are
	// long.
	HTTP *http.Client
	// Fetch fetches playlists, guides and the account; default HTTP. An
	// Xtream panel builds its whole m3u_plus before it answers (tvload.win
	// took 13 s for 588 MB on 2026-10-20), so its transport may wait longer
	// for headers than a stream's; fetchTimeout bounds each fetch whole.
	Fetch *http.Client
	// Now is the clock for fetch times and the guide's window.
	Now func() time.Time
	// Debounce is how long a render waits for more changes. Default 2s.
	Debounce time.Duration
	// Notify is told a provider's snapshot changed.
	Notify func(types.NamespacedName)
	// NotifyTuners is told a provider's tuners in use changed, at most every
	// TunersDebounce (default 5s). Only the provider's status shows it, so
	// it is not a snapshot change.
	NotifyTuners   func(types.NamespacedName)
	TunersDebounce time.Duration
	// Search asks the metadata gateway for a programme title's movies and
	// series, for the guide's programme artwork; nil adds none.
	Search SearchFunc
}

// Tuner serves every provider.
type Tuner struct {
	o          Options
	base       context.Context
	cancelBase context.CancelFunc
	wake       chan struct{}

	// listening closes once Start has bound BindAddress (Listening).
	listening     chan struct{}
	listeningOnce sync.Once

	mu        sync.Mutex
	providers map[types.NamespacedName]*provider

	// logoFlight fetches each channel logo once however many requests ask.
	logoFlight flight
}

type provider struct {
	key types.NamespacedName

	mu      sync.Mutex
	cfg     Config
	relay   *relay.Relay
	removed bool

	entries         []m3u.Entry
	playlist        PlaylistState
	playlistFetched bool
	playlistDue     bool
	playlistNext    time.Time
	// playlistBackoff is the wait after a failed fetch, doubling to
	// retryMax; zero after a success.
	playlistBackoff time.Duration
	fetching        map[string]bool

	guide guide
	// panel is the panel's own guide as last fetched beside a guideURL's,
	// cut to the channels that guide lacks (fallbackGuide); kept for a
	// fetch whose panel half fails.
	panel *xmltv.Guide

	account        AccountState
	accountDue     bool
	accountNext    time.Time
	accountBackoff time.Duration

	streams   map[string]string // number -> upstream URL
	languages map[string]string // number -> the ISO 639-2 code the channel forces, "" for the provider's fill
	// logoSources is each channel's logo as the playlist or spec gave it;
	// the guide and playlist point at the tuner's processed copy.
	logoSources map[string]string
	// art is the guide's programme artwork, loaded from the store on first
	// use.
	art *programmeArt
	// lineup is the rendered lineup in number order, its URLs relative
	// ("stream/<number>"); lineupChans is its channels for the guide, in the
	// same order. loaded and guideFrom are the guide's other inputs.
	lineup      []hdhr.LineupEntry
	lineupChans []xmltv.LineupChannel
	loaded      map[string]xmltv.Guide
	guideFrom   time.Time
	// blocks are each block's rendered surface, by device ID.
	blocks map[string]*surface
	// ranges caches each range's guide body until the next render.
	ranges map[string][]byte
	// renderGen counts renders; one that finds a newer started drops its
	// result.
	renderGen uint64

	snap        Snapshot
	notified    *Snapshot
	renderTimer *time.Timer
	renderSince time.Time
	tunersTimer *time.Timer

	storedPlaylistHash string
	storedGuideHash    string
}

// surface is one block's rendered HDHomeRun surface and guide.
type surface struct {
	block BlockConfig
	// entries' URLs are relative ("stream/<number>"); a request's base is
	// put in front, so the lineup follows the way Plex came.
	entries               []hdhr.LineupEntry
	guideXML              []byte
	lineupHash, guideHash string
}

type guide struct {
	g      xmltv.Guide
	loaded bool
	// wanted is the channel ids the guide was parsed for.
	wanted  map[string]bool
	due     bool
	next    time.Time
	backoff time.Duration
	state   GuideState
}

// New returns a Tuner.
func New(o Options) *Tuner {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Transport: http.DefaultTransport}
	}
	if o.Fetch == nil {
		o.Fetch = o.HTTP
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Debounce <= 0 {
		o.Debounce = defaultDebounce
	}
	if o.Notify == nil {
		o.Notify = func(types.NamespacedName) {}
	}
	if o.NotifyTuners == nil {
		o.NotifyTuners = func(types.NamespacedName) {}
	}
	if o.TunersDebounce <= 0 {
		o.TunersDebounce = tunersDebounce
	}
	registerMetrics()
	base, cancel := context.WithCancel(context.Background())
	return &Tuner{
		o: o, base: base, cancelBase: cancel, wake: make(chan struct{}, 1), listening: make(chan struct{}),
		providers: map[types.NamespacedName]*provider{},
	}
}

// Listening closes once Start has bound BindAddress, so the leader publishes
// itself as the livetv endpoint only when the tuner can answer (control-plane
// placement design §5.4). It never closes without a bind address, or when
// the bind failed.
func (t *Tuner) Listening() <-chan struct{} { return t.listening }

// NeedLeaderElection keeps the tuner on the leader: one process holds a
// provider's upstream connections, and the leader publishes itself as the
// livetv endpoint (control-plane placement design §5.4).
func (t *Tuner) NeedLeaderElection() bool { return true }

// Apply sets a provider's configuration. A provider the tuner has not
// served before starts from its cached playlist and guide, each fetched
// again only once it is older than Config.Every.
func (t *Tuner) Apply(c Config) {
	c = cloneConfig(c)
	if c.Every <= 0 {
		c.Every = defaultEvery
	}
	t.mu.Lock()
	p, ok := t.providers[c.Key]
	if !ok {
		p = &provider{key: c.Key, fetching: map[string]bool{}, playlistDue: true, accountDue: true, guide: guide{due: true}}
		t.providers[c.Key] = p
	}
	t.mu.Unlock()

	// The controller applies on every reconcile: an unchanged configuration
	// renders nothing.
	p.mu.Lock()
	same := p.relay != nil && reflect.DeepEqual(p.cfg, c)
	first := p.relay == nil
	p.mu.Unlock()
	if same {
		return
	}
	if first {
		t.loadCache(p, c)
	}

	p.mu.Lock()
	if first {
		key := c.Key
		p.relay = relay.New(relay.Options{
			Context: t.base, HTTP: t.o.HTTP, Tuners: effectiveTuners(c, p.account), Buffer: c.Buffer, Linger: c.Linger, UserAgent: c.UserAgent,
			Language: languageCode(c.Language),
			OnChange: func(inUse int) { t.tunersChanged(p, inUse) },
			OnError:  func(reason string) { upstreamErrors.WithLabelValues(label(key), reason).Inc() },
			OnBytes:  func(n int) { streamBytes.WithLabelValues(label(key)).Add(float64(n)) },
			OnFirstByte: func(d time.Duration) {
				upstreamFirstByte.WithLabelValues(label(key)).Observe(d.Seconds())
			},
		})
	} else {
		p.relay.Configure(effectiveTuners(c, p.account), c.Buffer, c.Linger, c.UserAgent, languageCode(c.Language))
	}
	refreshed := !first && c.Refresh != p.cfg.Refresh
	moved := !first && (c.Server != p.cfg.Server || c.Username != p.cfg.Username || c.Password != p.cfg.Password)
	if moved || refreshed {
		p.playlistDue, p.accountDue, p.guide.due = true, true, true
	}
	// Another guide source is fetched now; the old source's guide is
	// served until it lands.
	if !first && guideFingerprint(c) != guideFingerprint(p.cfg) {
		p.guide.due = true
	}
	p.cfg = c
	t.markWantedGuideDue(p)
	p.mu.Unlock()
	t.scheduleRender(p)
	t.poke()
}

// effectiveTuners is the relay's limit: the override, else the account's
// max_connections, else 1.
func effectiveTuners(c Config, a AccountState) int {
	switch {
	case c.Tuners > 0:
		return c.Tuners
	case a.MaxConnections > 0:
		return a.MaxConnections
	default:
		return 1
	}
}

// Remove ends a provider: its streams stop and Plex sees its devices gone.
func (t *Tuner) Remove(key types.NamespacedName) {
	t.mu.Lock()
	p := t.providers[key]
	delete(t.providers, key)
	t.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	p.removed = true
	for _, tm := range []*time.Timer{p.renderTimer, p.tunersTimer} {
		if tm != nil {
			tm.Stop()
		}
	}
	rl := p.relay
	p.mu.Unlock()
	if rl != nil {
		rl.Close()
	}
	forgetMetrics(key)
	t.o.Notify(key)
}

func (t *Tuner) lookup(key types.NamespacedName) *provider {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.providers[key]
}

// lookupDevice is the provider one of whose blocks is deviceID; nil when
// none is.
func (t *Tuner) lookupDevice(deviceID string) *provider {
	t.mu.Lock()
	providers := make([]*provider, 0, len(t.providers))
	for _, p := range t.providers {
		providers = append(providers, p)
	}
	t.mu.Unlock()
	for _, p := range providers {
		p.mu.Lock()
		_, ok := p.blocks[deviceID]
		p.mu.Unlock()
		if ok {
			return p
		}
	}
	return nil
}

// Snapshot returns what the tuner knows of a provider; false for one it
// does not serve.
func (t *Tuner) Snapshot(key types.NamespacedName) (Snapshot, bool) {
	p := t.lookup(key)
	if p == nil {
		return Snapshot{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := cloneSnapshot(p.snap)
	if p.relay != nil {
		s.TunersInUse = p.relay.InUse()
	}
	return s, true
}

// ChannelView is one channel as the tuner last rendered it.
type ChannelView struct {
	State ChannelState
	// Listed is the channel being in the render; PlaylistFetched is the
	// render having had a playlist. Before the first one nothing is listed.
	Listed, PlaylistFetched bool
}

// Channel is one channel's view, without copying the provider's snapshot:
// the channel controller asks once per channel, and a provider holds
// thousands. false for a provider the tuner does not serve.
func (t *Tuner) Channel(key types.NamespacedName, object string) (ChannelView, bool) {
	p := t.lookup(key)
	if p == nil {
		return ChannelView{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st, listed := p.snap.Channels[object]
	return ChannelView{State: st, Listed: listed, PlaylistFetched: p.snap.PlaylistFetched}, true
}

// Start listens on BindAddress and runs the fetch scheduler until ctx ends,
// then ends every stream.
func (t *Tuner) Start(ctx context.Context) error {
	log := logging.FromContext(ctx)
	var srv *http.Server
	if t.o.BindAddress != "" {
		ln, err := net.Listen("tcp", t.o.BindAddress)
		if err != nil {
			return err
		}
		t.listeningOnce.Do(func() { close(t.listening) })
		srv = &http.Server{
			Handler: t.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
			BaseContext: func(net.Listener) context.Context { return ctx },
		}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("livetv: tuner server", "err", err)
			}
		}()
		log.Info("livetv: tuner listening", "address", ln.Addr().String())
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	t.dispatch(ctx)
	for {
		select {
		case <-ctx.Done():
			t.cancelBase()
			for _, p := range t.list() {
				p.mu.Lock()
				rl := p.relay
				p.mu.Unlock()
				if rl != nil {
					rl.Close()
				}
			}
			if srv != nil {
				sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = srv.Shutdown(sctx)
				cancel()
				_ = srv.Close()
			}
			return nil
		case <-tick.C:
			t.dispatch(ctx)
		case <-t.wake:
			t.dispatch(ctx)
		}
	}
}

func (t *Tuner) poke() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *Tuner) list() []*provider {
	t.mu.Lock()
	defer t.mu.Unlock()
	ps := make([]*provider, 0, len(t.providers))
	for _, p := range t.providers {
		ps = append(ps, p)
	}
	return ps
}

// isDue reports whether a source flagged due, or whose next fetch time has
// come, may start: one not already running.
func isDue(flag bool, next, now time.Time, running bool) bool {
	return !running && (flag || (!next.IsZero() && !now.Before(next)))
}

// dispatch starts every fetch that is due and not already running. The
// guide waits for a playlist: its wanted channels come from it.
func (t *Tuner) dispatch(ctx context.Context) {
	now := t.o.Now()
	for _, p := range t.list() {
		p.mu.Lock()
		// A provider Apply has published but not finished -- it builds the
		// relay after loading the cache -- waits for the next tick: Start's
		// first dispatch races the controller's first Apply, and the nil
		// relay crash-looped the lease holder (talos, 2026-10-15).
		if p.removed || p.relay == nil {
			p.mu.Unlock()
			continue
		}
		tunersInUse.WithLabelValues(label(p.key)).Set(float64(p.relay.InUse()))
		viewers.WithLabelValues(label(p.key)).Set(float64(p.relay.Viewers()))
		if isDue(p.accountDue, p.accountNext, now, p.fetching["account"]) {
			p.accountDue, p.accountNext = false, time.Time{}
			p.fetching["account"] = true
			go t.fetchAccount(ctx, p, p.cfg)
		}
		if isDue(p.playlistDue, p.playlistNext, now, p.fetching["playlist"]) {
			p.playlistDue, p.playlistNext = false, time.Time{}
			p.fetching["playlist"] = true
			go t.fetchPlaylist(ctx, p, p.cfg)
		}
		if p.playlistFetched && p.cfg.EPGSource != "PMS" && isDue(p.guide.due, p.guide.next, now, p.fetching["guide"]) {
			p.guide.due, p.guide.next = false, time.Time{}
			p.fetching["guide"] = true
			go t.fetchGuide(ctx, p, p.cfg)
		}
		p.mu.Unlock()
	}
}

// tunersChanged notifies, at most every tunersDebounce, that the tuners in
// use changed.
func (t *Tuner) tunersChanged(p *provider, inUse int) {
	tunersInUse.WithLabelValues(label(p.key)).Set(float64(inUse))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tunersTimer != nil || p.removed {
		return
	}
	p.tunersTimer = time.AfterFunc(t.o.TunersDebounce, func() {
		p.mu.Lock()
		p.tunersTimer = nil
		removed := p.removed
		p.mu.Unlock()
		if !removed {
			t.o.NotifyTuners(p.key)
		}
	})
}

func label(key types.NamespacedName) string { return key.Namespace + "/" + key.Name }

func cloneSnapshot(s Snapshot) Snapshot {
	out := s
	out.Blocks = slices.Clone(s.Blocks)
	out.Channels = make(map[string]ChannelState, len(s.Channels))
	for k, v := range s.Channels {
		out.Channels[k] = v
	}
	return out
}

// cloneConfig copies c's slices and pointers: the tuner keeps c and compares
// the next Apply with it, so it must not share them with the caller.
func cloneConfig(c Config) Config {
	c.Blocks = slices.Clone(c.Blocks)
	for i := range c.Blocks {
		c.Blocks[i].Numbers = slices.Clone(c.Blocks[i].Numbers)
	}
	c.Filters = slices.Clone(c.Filters)
	for i := range c.Filters {
		c.Filters[i].Include, c.Filters[i].Exclude = slices.Clone(c.Filters[i].Include), slices.Clone(c.Filters[i].Exclude)
	}
	c.Channels = slices.Clone(c.Channels)
	for i := range c.Channels {
		if m := c.Channels[i].Map; m != nil {
			mm := *m
			c.Channels[i].Map = &mm
		}
	}
	return c
}
