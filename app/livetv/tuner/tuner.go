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

// Package tuner is Live TV's tuner (spec 2026-10-07 §5): a leader-only
// runnable of the manager that, for every provider the IPTVProvider
// controller applies, fetches the playlist and the guides, renders the
// lineup and the XEPG guide, serves the HDHomeRun surface and relays
// streams, and reports a Snapshot both controllers write status from. It
// holds no Kubernetes client.
package tuner

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
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
)

// Options configures a Tuner.
type Options struct {
	// BindAddress is where Start listens; empty serves nothing (tests use
	// Handler).
	BindAddress string
	// Store is the clustarr-livetv object store.
	Store events.ObjectStore
	// HTTP fetches playlists, guides and streams. It must carry no overall
	// Timeout: streams are long.
	HTTP *http.Client
	// Now is the clock for fetch times and the guide's window.
	Now func() time.Time
	// Debounce is how long a render waits for more changes. Default 2s.
	Debounce time.Duration
	// Notify is told a provider's snapshot changed.
	Notify func(types.NamespacedName)
}

// Tuner serves every provider.
type Tuner struct {
	o          Options
	base       context.Context
	cancelBase context.CancelFunc
	wake       chan struct{}

	mu        sync.Mutex
	providers map[types.NamespacedName]*provider
	// ports maps each block's port to its provider and block, for the
	// root routes (spec §5.0). Rebuilt on every Apply and Remove.
	ports map[int32]blockRef
}

// blockRef names one block of one provider.
type blockRef struct {
	key   types.NamespacedName
	start string
}

// surface is one block's rendered HDHomeRun surface and guide.
type surface struct {
	// entries' URLs are relative ("stream/<number>"); a request's base is
	// put in front, so the lineup follows the Host Plex used.
	entries    []hdhr.LineupEntry
	lineupHash string
	guideXML   []byte
	guideHash  string
	// storedGuide marks guideXML as the object store's copy, served until
	// every guide has been fetched in this process.
	storedGuide     bool
	storedGuideHash string
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
	fetching        map[string]bool

	guides map[string]*guide

	streams map[string]string // number -> upstream URL
	// guideXML is the whole provider's guide, for the UI and curl.
	guideXML  []byte
	guideHash string
	// blocks are each block's surface, by start.
	blocks map[string]*surface

	snap        Snapshot
	notified    *Snapshot
	renderTimer *time.Timer
	renderSince time.Time
	tunersTimer *time.Timer

	storedPlaylistHash string
	storedGuideHash    string
}

type guide struct {
	cfg    GuideConfig
	g      xmltv.Guide
	loaded bool
	wanted map[string]bool // the channel ids g was parsed for
	state  GuideState
	due    bool
	next   time.Time
}

// New returns a Tuner.
func New(o Options) *Tuner {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Transport: http.DefaultTransport}
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
	registerMetrics()
	base, cancel := context.WithCancel(context.Background())
	return &Tuner{o: o, base: base, cancelBase: cancel, wake: make(chan struct{}, 1), providers: map[types.NamespacedName]*provider{}, ports: map[int32]blockRef{}}
}

// NeedLeaderElection makes the tuner a cluster singleton: only the lease
// holder holds a provider's upstream connections.
func (t *Tuner) NeedLeaderElection() bool { return true }

// Apply sets a provider's configuration: a new provider, or a change to one.
// It never blocks on a fetch.
func (t *Tuner) Apply(c Config) {
	t.mu.Lock()
	p, ok := t.providers[c.Key]
	if !ok {
		p = &provider{key: c.Key, fetching: map[string]bool{}, guides: map[string]*guide{}, blocks: map[string]*surface{}, playlistDue: true}
		t.providers[c.Key] = p
	}
	t.mu.Unlock()

	// A block the tuner has not served before starts from its stored guide.
	p.mu.Lock()
	var fresh []string
	for _, b := range c.Blocks {
		if p.blocks[b.Start] == nil {
			p.blocks[b.Start] = &surface{}
			fresh = append(fresh, b.Start)
		}
	}
	p.mu.Unlock()
	for _, start := range fresh {
		t.loadStoredGuide(p, start)
	}

	p.mu.Lock()
	first := p.relay == nil
	if first {
		key := c.Key
		p.relay = relay.New(relay.Options{
			Context: t.base, HTTP: t.o.HTTP, Tuners: c.Tuners, Buffer: c.Buffer, Linger: c.Linger, UserAgent: c.UserAgent,
			OnChange: func(inUse int) { t.tunersChanged(p, inUse) },
			OnError:  func(reason string) { upstreamErrors.WithLabelValues(label(key), reason).Inc() },
			OnBytes:  func(n int) { streamBytes.WithLabelValues(label(key)).Add(float64(n)) },
		})
	} else {
		p.relay.SetTuners(c.Tuners)
	}
	refreshed := !first && c.Refresh != p.cfg.Refresh
	if c.PlaylistURL != p.cfg.PlaylistURL || refreshed {
		p.playlistDue = true
	}
	guides := make(map[string]*guide, len(c.Guides))
	for _, gc := range c.Guides {
		g := p.guides[gc.Name]
		if g == nil {
			g = &guide{due: true, state: GuideState{Name: gc.Name}}
		} else if g.cfg.URL != gc.URL || refreshed {
			g.due = true
		}
		g.cfg = gc
		guides[gc.Name] = g
	}
	p.guides = guides
	p.cfg = c
	t.markWantedGuidesDue(p)
	p.mu.Unlock()
	t.reindex()
	t.scheduleRender(p)
	t.poke()
}

// reindex rebuilds the port index from every provider's blocks. A port two
// blocks claim keeps the first in key order; the controller never hands one
// out twice (spec §5.0).
func (t *Tuner) reindex() {
	t.mu.Lock()
	defer t.mu.Unlock()
	keys := make([]types.NamespacedName, 0, len(t.providers))
	for k := range t.providers {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b types.NamespacedName) int { return strings.Compare(a.String(), b.String()) })
	ports := make(map[int32]blockRef)
	for _, k := range keys {
		p := t.providers[k]
		p.mu.Lock()
		for _, b := range p.cfg.Blocks {
			if _, taken := ports[b.Port]; !taken && b.Port > 0 {
				ports[b.Port] = blockRef{key: k, start: b.Start}
			}
		}
		p.mu.Unlock()
	}
	t.ports = ports
}

// byPort is the provider and block a port belongs to.
func (t *Tuner) byPort(port int32) (*provider, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ref, ok := t.ports[port]
	if !ok {
		return nil, ""
	}
	return t.providers[ref.key], ref.start
}

// Remove ends a provider: its streams stop and Plex sees the device gone.
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
	t.reindex()
	forgetMetrics(key)
	t.o.Notify(key)
}

// Snapshot returns what the tuner knows of a provider; false for one it
// does not serve.
func (t *Tuner) Snapshot(key types.NamespacedName) (Snapshot, bool) {
	t.mu.Lock()
	p := t.providers[key]
	t.mu.Unlock()
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
			t.mu.Lock()
			ps := make([]*provider, 0, len(t.providers))
			for _, p := range t.providers {
				ps = append(ps, p)
			}
			t.mu.Unlock()
			for _, p := range ps {
				p.relay.Close()
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

// dispatch starts every fetch that is due and not already running. Guides
// wait for the first playlist: their wanted channels come from it.
func (t *Tuner) dispatch(ctx context.Context) {
	now := t.o.Now()
	for _, p := range t.list() {
		p.mu.Lock()
		if p.removed {
			p.mu.Unlock()
			continue
		}
		tunersInUse.WithLabelValues(label(p.key)).Set(float64(p.relay.InUse()))
		viewers.WithLabelValues(label(p.key)).Set(float64(p.relay.Viewers()))
		if (p.playlistDue || (!p.playlistNext.IsZero() && !now.Before(p.playlistNext))) && !p.fetching["playlist"] {
			p.playlistDue = false
			p.playlistNext = time.Time{}
			p.fetching["playlist"] = true
			go t.fetchPlaylist(ctx, p, p.cfg.PlaylistURL, p.cfg.UserAgent)
		}
		if p.playlistFetched && p.cfg.EPGSource != "PMS" {
			for name, g := range p.guides {
				job := "guide/" + name
				if (g.due || (!g.next.IsZero() && !now.Before(g.next))) && !p.fetching[job] {
					g.due = false
					g.next = time.Time{}
					p.fetching[job] = true
					go t.fetchGuide(ctx, p, name)
				}
			}
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
	p.tunersTimer = time.AfterFunc(tunersDebounce, func() {
		p.mu.Lock()
		p.tunersTimer = nil
		removed := p.removed
		p.mu.Unlock()
		if !removed {
			t.o.Notify(p.key)
		}
	})
}

func label(key types.NamespacedName) string { return key.Namespace + "/" + key.Name }

func cloneSnapshot(s Snapshot) Snapshot {
	out := s
	out.Guides = append([]GuideState(nil), s.Guides...)
	out.Blocks = append([]BlockState(nil), s.Blocks...)
	out.Channels = make(map[string]ChannelState, len(s.Channels))
	for k, v := range s.Channels {
		out.Channels[k] = v
	}
	return out
}
