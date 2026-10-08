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

// Package looptest runs the remediation loop's item path with its stages
// against an envtest apiserver and an in-memory bus, for the item stages'
// envtests (ADR-0019 A3.3: grab, downloads, search, metadata, artwork,
// overlay). The manager's own wiring builds the stages
// (app/remediation/manager.Stages), the item kinds are the eight the
// manager runs, and passes run on demand (Loop.Pass) under a fake clock,
// so a test steps the loop the way a requeue would. The loop's sources --
// each item's watches and every stage's sources -- feed a key recorder
// (Loop.Woken) instead of the passes, so a test sees what a change wakes
// without racing a pass of its own.
//
// It is test support: only _test.go files import it, so no binary links
// it. It imports app/remediation/manager, so a package the manager imports
// may use it only from an external test package.
package looptest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/app/remediation"
	rmanager "github.com/mediactl/clustarr/app/remediation/manager"
	searchstage "github.com/mediactl/clustarr/app/remediation/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/objindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
)

// Clock is the loop's fake clock: the pass clock, the Pacer's and the
// ledger's.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now is the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock on by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Published is one message the loop published.
type Published struct {
	Subject string
	Env     *events.Envelope
}

// Bus is the in-memory bus, recording every publish; a KV bucket can be
// made to fail its reads (FailKV).
type Bus struct {
	*membus.Bus
	mu    sync.Mutex
	pubs  []Published
	fails map[string]error
}

// KV is the bucket, its reads failing while FailKV holds an error for it.
func (b *Bus) KV(name string) events.KV {
	return &failingKV{KV: b.Bus.KV(name), bus: b, name: name}
}

// FailKV makes every Get of bucket name fail with err; nil heals it.
func (b *Bus) FailKV(name string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fails == nil {
		b.fails = map[string]error{}
	}
	b.fails[name] = err
}

func (b *Bus) failure(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fails[name]
}

type failingKV struct {
	events.KV
	bus  *Bus
	name string
}

func (k *failingKV) Get(ctx context.Context, key string) (events.Entry, error) {
	if err := k.bus.failure(k.name); err != nil {
		return events.Entry{}, err
	}
	return k.KV.Get(ctx, key)
}

// Publish records the message and publishes it.
func (b *Bus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	r, err := b.Bus.Publish(ctx, subject, e, opts...)
	if err == nil {
		b.mu.Lock()
		b.pubs = append(b.pubs, Published{Subject: subject, Env: e})
		b.mu.Unlock()
	}
	return r, err
}

// Published is every message published so far whose subject has prefix.
func (b *Bus) Published(prefix string) []Published {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Published
	for _, p := range b.pubs {
		if strings.HasPrefix(p.Subject, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// Woken records the keys the loop's sources enqueue.
type Woken struct {
	mu   sync.Mutex
	keys []remediation.Key
}

// Reconcile implements reconcile.TypedReconciler: it records k.
func (w *Woken) Reconcile(_ context.Context, k remediation.Key) (reconcile.Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keys = append(w.keys, k)
	return reconcile.Result{}, nil
}

// Count is how many times k was woken.
func (w *Woken) Count(k remediation.Key) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, x := range w.keys {
		if x == k {
			n++
		}
	}
	return n
}

// Options configures a Loop.
type Options struct {
	// Start is the clock's start; zero is 2026-10-07T12:00:00Z.
	Start time.Time
	// SearchLivePerMinute is the live-search pace (0: the default 20).
	SearchLivePerMinute int
	// Artwork runs the artwork and overlay stages over an objindex of
	// clustarr-artwork.
	Artwork bool
	// Intake runs the candidate intake consumer into the loop's inbox.
	Intake bool
}

// Loop is the item path under test.
type Loop struct {
	Ctx context.Context
	// C is the manager's cache-backed client; API reads uncached.
	C, API client.Client
	Mgr    ctrl.Manager
	Bus    *Bus
	IR     *remediation.ItemReconciler
	Ledger *dispatch.Ledger
	Pacer  *records.Pacer
	Inbox  *intake.Inbox
	Budget *searchplan.DonorBudget
	Index  *objindex.Index
	Clock  *Clock
	Woken  *Woken
}

// Start builds and starts the loop's item path on cfg: the manager's
// indexes and stages, the eight item kinds, the dispatch ledger (rebuilt
// from the cache, as after a leader change) and the sources feeding Woken.
// It stops with the test.
func Start(t *testing.T, cfg *rest.Config, o Options) *Loop {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	start := o.Start
	if start.IsZero() {
		start = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	}
	clock := &Clock{now: start}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: new(true)},
	})
	require.NoError(t, err)
	require.NoError(t, remediation.RegisterIndexes(ctx, mgr.GetFieldIndexer()))

	mb := membus.New(nil)
	require.NoError(t, mb.Ensure(ctx, events.Default()))
	bus := &Bus{Bus: mb}

	rates := records.DefaultRates()
	if o.SearchLivePerMinute > 0 {
		rates[records.PaceSearch] = o.SearchLivePerMinute
	}
	l := &Loop{
		Ctx: ctx, C: mgr.GetClient(), Mgr: mgr, Bus: bus, Clock: clock, Woken: &Woken{},
		Pacer:  records.NewPacer(rates, clock.Now),
		Inbox:  intake.NewInbox(1000),
		Budget: searchplan.NewDonorBudget(),
		Ledger: dispatch.New(dispatch.Options{Topology: events.Default(), Reader: mgr.GetClient(), Now: clock.Now}),
	}
	l.API, err = client.New(cfg, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	l.Ledger.Register(searchstage.LedgerSource{})
	l.Ledger.Register(searchstage.SeasonLedgerSource{})
	require.NoError(t, mgr.Add(l.Ledger))
	if o.Intake {
		require.NoError(t, mgr.Add(&intake.CandidateConsumer{Bus: bus, Inbox: l.Inbox, Topology: events.Default(), Now: clock.Now}))
	}

	env := &remediation.Env{Reader: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bus: bus, IO: remediation.NewIOExecutor(1), Pacer: l.Pacer}
	opts := rmanager.Options{Dispatch: l.Ledger, Inbox: l.Inbox, Book: dispatch.NewDeliveryBook(), DonorBudget: l.Budget}
	if o.Artwork {
		l.Index = objindex.New(bus.ObjectStore(events.BucketArtwork), objindex.WithMetadataChanges())
		require.NoError(t, mgr.Add(k8s.LeaderOnly(l.Index.Run)))
		opts.Artwork = l.Index
	}
	recorder := func(name string) k8sevents.EventRecorder { return mgr.GetEventRecorder(name) }
	l.IR = &remediation.ItemReconciler{
		Items:  Items(mgr.GetClient(), recorder, bus),
		Stages: rmanager.Stages(rmanager.Deps{Env: env, Options: opts, Manager: mgr}),
		Env:    env, Dispatch: l.Ledger, Book: opts.Book, Inbox: l.Inbox,
		Client: mgr.GetClient(), Recorder: recorder, Clock: clock.Now,
	}
	b, err := l.IR.Watch(mgr, builder.TypedControllerManagedBy[remediation.Key](mgr).Named("looptest"), map[string]bool{})
	require.NoError(t, err)
	require.NoError(t, b.WithOptions(controller.TypedOptions[remediation.Key]{SkipNameValidation: new(true)}).Complete(l.Woken))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx), "the cache never synced")
	require.Eventually(t, func() bool {
		d := l.Ledger.Admit(searchplan.Durable, dispatch.Key{Kind: "Probe", Name: "ledger-rebuilt"}, 0)
		return d.Reason != "Rebuilding"
	}, 10*time.Second, 20*time.Millisecond, "the dispatch ledger never rebuilt")
	return l
}

// Items are the eight item kinds the manager runs (the manager's own
// items(), restated: it is unexported).
func Items(c client.Client, recorder func(string) k8sevents.EventRecorder, bus events.Bus) map[remediation.KeyKind]rollup.Item {
	s := c.Scheme()
	return map[remediation.KeyKind]rollup.Item{
		remediation.KindMovie:   &movie.Reconciler{Client: c, Scheme: s, Recorder: recorder("movie"), Bus: bus},
		remediation.KindEpisode: &episode.Reconciler{Client: c, Scheme: s, Recorder: recorder("episode"), Bus: bus},
		remediation.KindAlbum: &album.Reconciler{
			Client: c, Scheme: s, Recorder: recorder("album"), Bus: bus,
			Releases: album.NewReleaseCache(album.DefaultReleaseCacheSize),
		},
		remediation.KindBook:      &book.Reconciler{Client: c, Scheme: s, Recorder: recorder("book"), Bus: bus},
		remediation.KindAudiobook: &audiobook.Reconciler{Client: c, Scheme: s, Recorder: recorder("audiobook"), Bus: bus},
		remediation.KindIssue:     &issue.Reconciler{Client: c, Scheme: s, Recorder: recorder("issue"), Bus: bus},
		remediation.KindSeries:    &series.Reconciler{Client: c, Scheme: s, Recorder: recorder("series"), Bus: bus},
		remediation.KindComic:     &comic.Reconciler{Client: c, Scheme: s, Recorder: recorder("comic"), Bus: bus},
	}
}

// Pass runs one item pass of kind/ns/name, as the loop's worker would.
func (l *Loop) Pass(t *testing.T, kind remediation.KeyKind, ns, name string) reconcile.Result {
	t.Helper()
	res, err := l.IR.Reconcile(l.Ctx, remediation.Key{Kind: kind, Namespace: ns, Name: name})
	require.NoError(t, err)
	return res
}

// Synced waits until the cache holds obj at least as new as the apiserver
// does: a pass reads the cache.
func (l *Loop) Synced(t *testing.T, obj client.Object) {
	t.Helper()
	fresh := obj.DeepCopyObject().(client.Object)
	require.NoError(t, l.API.Get(l.Ctx, client.ObjectKeyFromObject(obj), fresh))
	require.Eventually(t, func() bool {
		cached := obj.DeepCopyObject().(client.Object)
		if err := l.C.Get(l.Ctx, client.ObjectKeyFromObject(obj), cached); err != nil {
			return false
		}
		return cached.GetResourceVersion() == fresh.GetResourceVersion()
	}, 10*time.Second, 10*time.Millisecond, "the cache never saw %s", obj.GetName())
}
