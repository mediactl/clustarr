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

package indexer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/download"
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

// ClientCache builds the wire [Client] for one Indexer -- a *torznab.Client
// for spec.generic, the Cardigann engine adapter for spec.definition and
// spec.definitionRef -- and is the [search.ClientFor] and rss SearcherFor the
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
// It notably does NOT write limiter config. That is [applyRateLimit], called
// only from Reconcile: this reconciler is the only reader of
// spec.requestDelay, and a fan-out re-applying it from a cached Indexer would
// silently revert an operator's edit. The cache only ever READS the limiter
// onto the client it builds, so every caller still draws on one bucket per
// host.
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
	// from when its client is built. nil means "the owned Secret only"
	// (NewSessionStore(c, nil)), which is always correct because the
	// reconciler writes the Secret on every login; wiring the bus here adds
	// the clustarr-indexer-sessions KV read in front of it.
	Sessions *SessionStore

	// TTL bounds a cached entry's age. Zero means [DefaultClientCacheTTL];
	// negative disables caching entirely, which is what a test that wants to
	// count GETs sets.
	TTL time.Duration

	// Now is the clock seam. nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	entries map[types.UID]clientEntry
}

// clientEntry is one Indexer's built client and what it was built from.
type clientEntry struct {
	resourceVersion string
	client          Client
	builtAt         time.Time
}

// NewClientCache returns a cache building clients for c's Indexers, paced by
// limiters. limiters is the one process-wide instance; a nil one disables
// pacing rather than panicking, matching [Reconciler.Limiters].
func NewClientCache(c client.Client, limiters *ratelimit.Limiter) *ClientCache {
	return &ClientCache{client: c, limiters: limiters, entries: map[types.UID]clientEntry{}}
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
// what indexarr/run.go hands to both.
//
// The cache key cannot see a new login session either -- a session lives in
// a Secret and a KV entry, not on the Indexer -- so the reconciler calls
// [ClientCache.Forget] after every successful login, and the next call
// rebuilds with the fresh session.
func (cc *ClientCache) For(ctx context.Context, idx *indexv1alpha1.Indexer) (Client, error) {
	if idx == nil {
		return nil, errors.New("indexer: no Indexer to build a client for")
	}
	if cached, ok := cc.lookup(idx); ok {
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
		sessions = NewSessionStore(cc.client, nil)
	}
	built, err := buildWireClient(ctx, cc.client, idx, cc.limiters, sessions)
	if err != nil {
		return nil, err
	}
	cc.store(idx, built)
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
	cg, ok := cli.(*cardigannClient)
	if !ok {
		return nil, fmt.Errorf("indexer: %s/%s is not definition-backed", idx.Namespace, idx.Name)
	}
	return download.EngineFetcher(cg), nil
}

// buildWireClient is the ONE construction of an Indexer's wire client,
// shared by the cache (search, RSS, download). The Indexer reconciler builds
// the same pieces -- buildClient, buildCardigann, resolveProxy -- from the
// spec and Secret it already holds, so the proxy and the limiter reach
// every path through the same functions.
func buildWireClient(
	ctx context.Context,
	c client.Client,
	idx *indexv1alpha1.Indexer,
	lim *ratelimit.Limiter,
	sessions *SessionStore,
) (Client, error) {
	kind, err := resolveSource(idx.Spec)
	if err != nil {
		return nil, err
	}
	secret, err := readSecret(ctx, c, idx.Namespace, idx.Spec.SecretRef)
	if err != nil {
		return nil, err
	}
	transport, err := resolveProxy(ctx, c, idx)
	if err != nil {
		return nil, err
	}
	if kind == sourceGeneric {
		tc, _, err := buildClient(idx.Spec, secret, lim, transport)
		if err != nil {
			// Not `return tc, err`: a nil *torznab.Client in a non-nil
			// interface is a nil dereference one call later.
			return nil, err
		}
		return tc, nil
	}
	def, err := resolveDefinition(ctx, c, idx.Spec)
	if err != nil {
		return nil, err
	}
	sess, err := sessions.Load(ctx, idx)
	if err != nil {
		return nil, err
	}
	cg, err := buildCardigann(idx.Spec, def, secret, sess, lim, transport)
	if err != nil {
		return nil, err
	}
	return cg, nil
}

// lookup returns the cached client for idx when it was built from the same
// resourceVersion and is still inside the TTL.
func (cc *ClientCache) lookup(idx *indexv1alpha1.Indexer) (Client, bool) {
	if cc.ttl() < 0 {
		return nil, false
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	e, ok := cc.entries[idx.UID]
	if !ok || e.resourceVersion != idx.ResourceVersion {
		return nil, false
	}
	if cc.now().Sub(e.builtAt) >= cc.ttl() {
		return nil, false
	}
	return e.client, true
}

func (cc *ClientCache) store(idx *indexv1alpha1.Indexer, built Client) {
	if cc.ttl() < 0 {
		return
	}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.entries == nil {
		cc.entries = map[types.UID]clientEntry{}
	}
	cc.entries[idx.UID] = clientEntry{
		resourceVersion: idx.ResourceVersion,
		client:          built,
		builtAt:         cc.now(),
	}
}

// Limiters is the one process-wide limiter this cache paces its clients with.
// It is exposed so the wiring hands the SAME instance to the reconciler (which
// writes each host's Config) and to the download verb's fetcher (which only
// Waits), rather than threading a second variable alongside the cache and
// leaving room for the two to diverge.
func (cc *ClientCache) Limiters() *ratelimit.Limiter { return cc.limiters }

// Forget drops idx's entry. The Indexer reconciler calls it on delete, which
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
