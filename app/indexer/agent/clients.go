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

package agent

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/clientcache"
	idxclients "github.com/mediactl/clustarr/app/indexer/clients"
	"github.com/mediactl/clustarr/app/indexer/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// newClientCache builds the index agent's own wire-client cache, limiter and
// session store (R8, §5.12), and adds the runnable that keeps it current:
// ClientCache.WatchSessions over clustarr-indexer-sessions, as a
// k8s.EveryReplica. Both happen in one call, so an agent cache can never
// exist without the watch that replaced the reconciler's in-process Forget.
// The limiter's fallback is the CRD default. Each host's own requestDelay
// arrives from ClientCache.ApplyRateLimit, gated on generation.
// Session writes go under indexarr-worker (§5.3.2).
func newClientCache(c client.Client, bus events.Bus, add func(manager.Runnable) error) (*clientcache.ClientCache, error) {
	cc := clientcache.NewClientCache(c, ratelimit.New(idxclients.DefaultLimiterConfig()))
	cc.Sessions = idxclients.NewSessionStore(c, bus, k8s.ManagerIndexWorker)
	kv := bus.KV(events.BucketIndexerSessions)
	if err := add(k8s.EveryReplica(func(ctx context.Context) error { return cc.WatchSessions(ctx, kv) })); err != nil {
		return nil, fmt.Errorf("indexarr: add the session watch: %w", err)
	}
	return cc, nil
}

// genericFetcherFor is the download verb's fetcher for a generic Indexer.
// It applies the Indexer's requestDelay to cc's limiter first (generation-
// gated, like every build), because a grab never goes through
// ClientCache.For and the host would otherwise pace at the default until a
// search or poll touched it.
func genericFetcherFor(c client.Client, cc *clientcache.ClientCache) download.FetcherFor {
	fetch := download.NewFetcherFor(c, cc.Limiters())
	return func(ctx context.Context, idx *indexv1alpha1.Indexer) (download.Fetcher, error) {
		cc.ApplyRateLimit(idx, nil, 0)
		return fetch(ctx, idx)
	}
}
