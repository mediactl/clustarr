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

package clientcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxclients "github.com/mediactl/clustarr/app/indexer/clients"
	"github.com/mediactl/clustarr/app/indexer/download"
	"github.com/mediactl/clustarr/app/indexer/proxy"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// DefaultClientCacheTTL bounds how long a cached client may outlive a change
// this cache cannot observe.
//
// The cache key is the Indexer's UID plus its resourceVersion, so any edit to
// the object is picked up immediately. What it CANNOT see is the referenced
// Secret changing: rotating an apikey or a passkey does not touch the Indexer,
// and indexarr reads Secrets without an informer (see indexarr.Options'
// ManagerOptions), so there is no watch to invalidate on. The TTL is that
// bound. Five minutes is well inside the reprobe tick that would notice a
// rejected credential anyway, and still collapses a fan-out across a hundred
// indexers from a hundred apiserver GETs per search to a hundred per five
// minutes.
const DefaultClientCacheTTL = 5 * time.Minute

// ClientCache builds the wire [idxclients.Client] for one Indexer -- a
// *torznab.Client for spec.generic, the Cardigann engine adapter for
// spec.definition and spec.definitionRef -- and is the [search.ClientFor]
// and rss SearcherFor the
// wiring hands to the search fan-out and the RSS poll. That one factory
// serving both source kinds IS ruling R5: a Cardigann indexer reaches the
// fan-out through the same seam as a Torznab one, so the fan-out's dedupe,
// query-limit window and health/backoff apply to it unchanged.
//
// # Why it exists in THIS package
//
// The client must be built by exactly one function, [buildWireClient],
// because spec.proxyRef is applied there: if the fan-out had its own copy,
// the caps probe would honour the proxy while every search and every RSS
// poll bypassed it, leaking the operator's real IP to a private tracker
// while status reported the proxy Ready. Sharing one builder makes that
// impossible rather than merely unlikely.
//
// It writes limiter config only through [ClientCache.ApplyRateLimit], gated
// on the Indexer's generation. In the index agent nothing else would write
// it, because the Indexer reconciler runs in the manager and paces that
// process's own limiter (§5.12). Without the gate, a search holding a
// pre-edit *Indexer would miss the resourceVersion lookup, rebuild, and
// re-apply the old spec.requestDelay over an operator's edit. Every client
// the cache builds reads the one limiter, so every caller still draws on one
// bucket per host.
//
// # Why it caches
//
// A fan-out calls this once per candidate indexer per search, and the RSS
// poll once per poll. Each call would otherwise be a live apiserver GET for
// the Secret, because indexarr disables the Secret informer. A wanted-cron
// sweep of 200 items across 20 indexers is 4,000 GETs against
// controller-runtime's default 20 QPS client.
//
// That is not only slow, it is a CORRECTNESS problem, and this is the part
// worth being precise about: the GET happens inside the per-indexer search
// timeout, and a ClientFor error is a named failure outcome, which runs
// RecordFailure -- escalationLevel, then disabledUntil. So a throttled REST
// client or a brief apiserver blip could escalate a perfectly healthy indexer
// toward disabled. Caching makes the GET once per change rather than once per
// query, which removes the amplification that turns a blip into a backoff.
//
// The zero value is not usable; call [NewClientCache].
type ClientCache struct {
	client   client.Client
	limiters *ratelimit.Limiter

	// Sessions is where a definition-backed Indexer's login session is read
	// from when its client is built, and where a relogin Saves or Drops it.
	// nil means "the owned Secret only", written as indexarr-worker
	// (NewSessionStore(c, nil, k8s.ManagerIndexarrWorker)), which is always
	// correct because the
	// reconciler writes the Secret on every login; wiring the bus here adds
	// the clustarr-indexer-sessions KV read in front of it.
	Sessions *idxclients.SessionStore

	// TTL bounds a cached entry's age. Zero means [DefaultClientCacheTTL];
	// negative disables caching entirely, which is what a test that wants to
	// count GETs sets.
	TTL time.Duration

	// Now is the clock seam. nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	entries map[types.UID]clientEntry
	// applied is the newest generation of each Indexer whose requestDelay
	// this cache has written to its limiter. It outlives an entry's Forget
	// (a session change) and is dropped only when Prune sees the Indexer
	// gone, so an object read before an edit can never put the old delay
	// back.
	applied map[types.UID]int64
}

// clientEntry is one Indexer's built client and what it was built from.
//
// proxies is the fingerprint of the IndexerProxies that applied at build
// time. The Indexer's resourceVersion cannot see a proxy change -- a proxy
// edited, or a new one whose spec.selector starts matching -- and a client
// kept past one would route a private tracker's searches around the proxy the
// operator just added, for up to the TTL. The selection is re-read from the
// informer on every For, which is a cached List, and a changed one rebuilds.
type clientEntry struct {
	resourceVersion string
	generation      int64
	proxies         string
	client          idxclients.Client
	builtAt         time.Time
}

// NewClientCache returns a cache building clients for c's Indexers, paced by
// limiters. limiters is the one process-wide instance; a nil one disables
// pacing rather than panicking, matching the Indexer reconciler's Limiters.
func NewClientCache(c client.Client, limiters *ratelimit.Limiter) *ClientCache {
	return &ClientCache{
		client: c, limiters: limiters,
		entries: map[types.UID]clientEntry{}, applied: map[types.UID]int64{},
	}
}

// ApplyRateLimit writes idx's spec.requestDelay, raised to floor (a
// definition's own requestDelay), onto this cache's limiter. It skips the
// write when it has already applied a NEWER generation of the same Indexer,
// and reports whether it wrote. An equal generation applies again. A
// status-only change, a proxy change or a definition edit leaves generation
// alone, and re-applying the same spec is harmless.
func (cc *ClientCache) ApplyRateLimit(idx *indexv1alpha1.Indexer, def *cardigann.Definition, floor time.Duration) bool {
	if idx == nil || cc.limiters == nil {
		return false
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.applied == nil {
		cc.applied = map[types.UID]int64{}
	}
	if last, ok := cc.applied[idx.UID]; ok && idx.Generation < last {
		return false
	}
	idxclients.ApplyRateLimit(idx.Spec, def, cc.limiters, floor)
	cc.applied[idx.UID] = idx.Generation
	return true
}

func (cc *ClientCache) now() time.Time {
	if cc.Now != nil {
		return cc.Now()
	}
	return time.Now()
}

func (cc *ClientCache) ttl() time.Duration {
	if cc.TTL == 0 {
		return DefaultClientCacheTTL
	}
	return cc.TTL
}

// For returns the wire client for idx, building it if the cached one is for a
// different resourceVersion or has aged past the TTL.
//
// It is shaped exactly like search.ClientFor and rss.Deps.SearcherFor, and is
// what app/indexer/run.go hands to both.
//
// The cache key cannot see a new login session either -- a session lives in
// a Secret and a KV entry, not on the Indexer -- so the reconciler calls
// [ClientCache.Forget] after every successful login, and the next call
// rebuilds with the fresh session.
func (cc *ClientCache) For(ctx context.Context, idx *indexv1alpha1.Indexer) (idxclients.Client, error) {
	if idx == nil {
		return nil, errors.New("indexer: no Indexer to build a client for")
	}
	sel, err := proxy.Selected(ctx, cc.client, idx)
	if err != nil {
		return nil, err
	}
	if cached, ok := cc.lookup(idx, sel.Fingerprint()); ok {
		return cached, nil
	}

	// Read the Secret and build OUTSIDE the lock: this is a network call to
	// the apiserver, and holding the mutex across it would serialise a
	// fan-out that is meant to be concurrent across indexers. The cost is
	// that two goroutines racing on one indexer may both build; the loser's
	// client is simply dropped, and a torznab.Client is an http.Client and a
	// URL, not a connection.
	sessions := cc.Sessions
	if sessions == nil {
		sessions = idxclients.NewSessionStore(cc.client, nil, k8s.ManagerIndexarrWorker)
	}
	built, err := buildWireClientFor(ctx, cc.client, idx, sel, cc.limiters, sessions,
		func(def *cardigann.Definition, floor time.Duration) { cc.ApplyRateLimit(idx, def, floor) })
	if err != nil {
		return nil, err
	}
	cc.store(idx, sel.Fingerprint(), built)
	return built, nil
}

// DefinitionFetcherFor is the download.FetcherFor for a definition-backed
// Indexer: rpc.indexarr.download dispatches a Cardigann release to
// Engine.Download (its download block, before-request and selectors) rather
// than to a plain GET that would skip all three. It reads through the same
// cache as [ClientCache.For], so a download and a search on one indexer use
// one engine, one session and one proxy.
func (cc *ClientCache) DefinitionFetcherFor(ctx context.Context, idx *indexv1alpha1.Indexer) (download.Fetcher, error) {
	cli, err := cc.For(ctx, idx)
	if err != nil {
		return nil, err
	}
	cg, ok := cli.(*idxclients.CardigannClient)
	if !ok {
		return nil, fmt.Errorf("indexer: %s/%s is not definition-backed", idx.Namespace, idx.Name)
	}
	return download.EngineFetcher(cg), nil
}

// buildWireClient is the ONE construction of an Indexer's wire client,
// shared by the cache (search, RSS, download). The Indexer reconciler builds
// the same pieces -- BuildClient, BuildCardigann, ResolveProxy -- from the
// spec and Secret it already holds, so the proxy and the limiter reach
// every path through the same functions.
func buildWireClient(
	ctx context.Context,
	c client.Client,
	idx *indexv1alpha1.Indexer,
	lim *ratelimit.Limiter,
	sessions *idxclients.SessionStore,
) (idxclients.Client, error) {
	sel, err := proxy.Selected(ctx, c, idx)
	if err != nil {
		return nil, err
	}
	return buildWireClientFor(ctx, c, idx, sel, lim, sessions, nil)
}

// buildWireClientFor is buildWireClient for a proxy selection already made,
// so ClientCache.For selects once and both caches and builds from it.
//
// pace, when non-nil, writes the Indexer's limiter config: ClientCache.For
// passes its generation-gated [ClientCache.ApplyRateLimit]. It runs before
// the Secret read for a generic Indexer, so a failing build still paces its
// host, and once the definition is resolved for a Cardigann one, whose own
// requestDelay is the floor. buildWireClient passes nil, so a direct build
// writes no limiter config.
func buildWireClientFor(
	ctx context.Context,
	c client.Client,
	idx *indexv1alpha1.Indexer,
	sel proxy.Selection,
	lim *ratelimit.Limiter,
	sessions *idxclients.SessionStore,
	pace func(def *cardigann.Definition, floor time.Duration),
) (idxclients.Client, error) {
	kind, err := idxclients.ResolveSource(idx.Spec)
	if err != nil {
		return nil, err
	}
	if kind == idxclients.SourceGeneric && pace != nil {
		pace(nil, 0) // before the Secret read, so a failing build still paces its host
	}
	secret, err := idxclients.ReadSecret(ctx, c, idx.Namespace, idx.Spec.SecretRef)
	if err != nil {
		return nil, err
	}
	transport, err := proxy.Build(ctx, c, sel)
	if err != nil {
		return nil, err
	}
	if kind == idxclients.SourceGeneric {
		tc, _, err := idxclients.BuildClient(idx.Spec, secret, lim, transport)
		if err != nil {
			// Not `return tc, err`: a nil *torznab.Client in a non-nil
			// interface is a nil dereference one call later.
			return nil, err
		}
		return tc, nil
	}
	def, err := idxclients.ResolveDefinition(ctx, c, idx.Spec)
	if err != nil {
		return nil, err
	}
	if pace != nil {
		pace(def, idxclients.DefinitionDelay(def.RequestDelay))
	}
	sess, err := sessions.Load(ctx, idx)
	if err != nil {
		return nil, err
	}
	cg, err := idxclients.BuildCardigann(idx.Spec, def, secret, sess, lim, transport)
	if err != nil {
		return nil, err
	}
	if def.Login != nil {
		cg.SetRelogin(reloginFunc(cg, idx.DeepCopy(), sessions))
	}
	return cg, nil
}

// reloginFunc is how a cached Cardigann client recovers from a session the
// tracker killed: log in again with the same engine (so the same proxy and
// limiter) and persist the new session through the store the reconciler
// writes, so every other path -- the reconciler, the generic fetcher reading
// the Secret's cookie key -- sees it too.
//
// A failed login drops the session it failed with, and only that one: a
// session the manager saved meanwhile stays (SessionStore.Drop's
// compare-and-swap). The reconciler only logs in when the session is missing
// or near expiry, so a killed but unexpired session left in place would be
// reused by every search until it aged out. Dropped, the next reconcile logs
// in and reports a credential problem as the Authenticated condition, where
// an operator looks.
func reloginFunc(cg *idxclients.CardigannClient, owner *indexv1alpha1.Indexer, sessions *idxclients.SessionStore) idxclients.ReloginFunc {
	return func(ctx context.Context, stale *cardigann.Session) (*cardigann.Session, error) {
		log := logging.FromContext(ctx).With("indexer", client.ObjectKeyFromObject(owner))
		cfg := cg.Config()
		cfg.Session = nil
		sess, err := cg.Engine().Login(ctx, cg.Definition(), cfg)
		if err != nil {
			if derr := sessions.Drop(ctx, owner, stale); derr != nil {
				log.Warn("indexer: dropping the expired session failed", "error", derr)
			}
			return nil, fmt.Errorf("indexer: logging in again after the tracker expired the session: %w",
				cardigann.RedactErr(err))
		}
		if sess != nil && cg.Definition().RequiresSession() {
			if serr := sessions.Save(ctx, owner, sess); serr != nil {
				// The new session works in this client either way; the
				// reconciler's next pass persists one of its own.
				log.Warn("indexer: persisting the renewed session failed", "error", serr)
			}
		}
		log.Info("indexer: the tracker expired the session; logged in again")
		return sess, nil
	}
}

// lookup returns the cached client for idx when it was built from the same
// resourceVersion and the same proxy selection, and is still inside the TTL.
func (cc *ClientCache) lookup(idx *indexv1alpha1.Indexer, proxies string) (idxclients.Client, bool) {
	if cc.ttl() < 0 {
		return nil, false
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	e, ok := cc.entries[idx.UID]
	if !ok || e.resourceVersion != idx.ResourceVersion || e.proxies != proxies {
		return nil, false
	}
	if cc.now().Sub(e.builtAt) >= cc.ttl() {
		return nil, false
	}
	return e.client, true
}

func (cc *ClientCache) store(idx *indexv1alpha1.Indexer, proxies string, built idxclients.Client) {
	if cc.ttl() < 0 {
		return
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.entries == nil {
		cc.entries = map[types.UID]clientEntry{}
	}
	if e, ok := cc.entries[idx.UID]; ok && idx.Generation < e.generation {
		return // a stale object's client never replaces a newer one's
	}
	cc.entries[idx.UID] = clientEntry{
		resourceVersion: idx.ResourceVersion,
		generation:      idx.Generation,
		proxies:         proxies,
		client:          built,
		builtAt:         cc.now(),
	}
}

// Limiters is the one process-wide limiter this cache paces its clients with.
// It is exposed so the wiring hands the same instance to the download verb's
// generic fetcher, which only Waits, rather than threading a second variable
// alongside the cache and leaving room for the two to diverge.
func (cc *ClientCache) Limiters() *ratelimit.Limiter { return cc.limiters }

// Forget drops idx's entry. The Indexer reconciler calls it on delete
// (through its ForgetClient hook, which the wiring points here), which
// is what keeps the map bounded by the cluster's live Indexer set rather than
// by every Indexer this process has ever seen. It is keyed by UID, so an
// Indexer deleted and recreated under the same name is a different object and
// correctly gets a fresh client.
func (cc *ClientCache) Forget(uid types.UID) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	delete(cc.entries, uid)
}

// Len reports how many entries are cached. It exists for tests, which is why
// it is the only accessor: nothing in production asks.
func (cc *ClientCache) Len() int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return len(cc.entries)
}
