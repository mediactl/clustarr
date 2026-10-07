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
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/blocklist"
	"github.com/mediactl/clustarr/app/indexer/clientcache"
	"github.com/mediactl/clustarr/app/indexer/download"
	"github.com/mediactl/clustarr/app/indexer/facade"
	"github.com/mediactl/clustarr/app/indexer/health"
	"github.com/mediactl/clustarr/app/indexer/query"
	"github.com/mediactl/clustarr/app/indexer/search"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/version"
)

// registerWorkers registers the three RPC verbs, the RSS poll consumer and the
// release index's retention sweep, all as manager Runnables so they stop with
// the manager.
//
// The RPC responder and the RSS worker are each a [k8s.EveryReplica],
// directly or through rss.Worker.SetupWithManager: every replica answers
// search RPCs and polls RSS, regardless of leader election. The retention
// sweep is [indexSweeper]: the sweep runs on every replica of the index
// domain, which is fixed at one under both engines (spec §5.12, superseding
// ruling R1).
//
// It returns the three verb bodies it built, so [registerFacade] serves the
// SAME instances the RPC responder does: one download.Service with one
// Definitions dispatch, one search fan-out with one query-limit window. A
// facade holding its own copies would be a second path around both.
func registerWorkers(
	mgr ctrl.Manager, bus events.Bus, store relindex.Store, cc *clientcache.ClientCache,
) (verbs, error) {
	c := mgr.GetClient()

	// app/indexer/download's doc.go documents this construction verbatim.
	//
	// Definitions is the Cardigann half (plan task G1-1). Without it,
	// Service.Handle REFUSES every grab from a spec.definition or
	// spec.definitionRef Indexer with "the Cardigann download path is not
	// configured" -- deliberately, rather than falling back to a plain GET of
	// what is usually a details page -- so an unwired field made every
	// definition-backed indexer searchable and ungrabbable.
	//
	// Fetch applies the Indexer's requestDelay to cc's limiter before each
	// grab (genericFetcherFor): no reconciler in this process writes it.
	dl := &download.Service{
		Client:      c,
		Bus:         bus,
		Fetch:       genericFetcherFor(c, cc),
		Definitions: cc.DefinitionFetcherFor,
	}
	q := &query.Service{Store: store}
	bl := &blocklist.Service{Store: store}
	// ONE health writer for the fan-out and the RSS poll (ADR-0019 §7.5):
	// their read-modify-writes of an indexer's record are one in-process
	// compare-and-swap, and a failure either reports suppresses the indexer
	// for the fan-out until the manager's verdict.
	hw := newHealthWriter(bus)
	svc := &search.Service{
		Client: c,
		Health: hw,
		ClientFor: func(ctx context.Context, idx *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			cli, err := cc.For(ctx, idx)
			if err != nil {
				// Returning `cc.For(...)` directly would hand back a
				// non-nil interface wrapping a nil *torznab.Client on the
				// error path, which is a nil dereference one call later.
				return nil, err
			}
			return cli, nil
		},
		Store:     store,
		Bus:       bus,
		Download:  dl.Handle,
		Query:     q.Handle,
		Blocklist: bl.Handle,
	}

	// search.Serve is index's single registration point for the RPC queue
	// group: it registers clustarr.rpc.index.search, .download, .query and
	// .blocklist in one call. It has no unsubscribe, so its stop drains in-flight
	// fan-outs rather than deregistering the responders.
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := search.Serve(ctx, bus, svc)
		if err != nil {
			return fmt.Errorf("index: serve the index RPC verbs: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return verbs{}, fmt.Errorf("index: add the RPC responder: %w", err)
	}

	if err := rss.NewWorker(rssDeps(c, hw, bus, store, cc)).SetupWithManager(mgr, bus); err != nil {
		return verbs{}, fmt.Errorf("index: subscribe rss: %w", err)
	}

	if err := mgr.Add(indexSweeper{store: store}); err != nil {
		return verbs{}, fmt.Errorf("index: add the release-index sweep: %w", err)
	}

	return verbs{search: svc, query: q, download: dl}, nil
}

// rssDeps is the RSS poll's wiring. It is a function so a test can assert
// what production hands the worker rather than a copy of it.
//
// Bus is not optional in production: besides the firehose, it is the
// clustarr-indexer-limits query ring each of the poll's up to four requests
// reserves on -- the ring the search fan-out reserves on -- so
// spec.limits.queryLimit holds against the indexer's whole traffic. hw is
// the health writer the fan-out shares; the poll reports through it and
// writes no status (ADR-0019 §7.5).
func rssDeps(c client.Reader, hw *health.Writer, bus events.Bus, store relindex.Store, cc *clientcache.ClientCache) rss.Deps {
	return rss.Deps{
		Client: c,
		Health: hw,
		Bus:    bus,
		Index:  store,
		SearcherFor: func(ctx context.Context, idx *indexv1alpha1.Indexer) (rss.Searcher, error) {
			cli, err := cc.For(ctx, idx)
			if err != nil {
				return nil, err
			}
			return cli, nil
		},
	}
}

// newHealthWriter is the index agent's one clustarr-indexer-health writer,
// stamping this pod's name and version on every record, with the local
// suppression the fan-out reads.
func newHealthWriter(bus events.Bus) *health.Writer {
	pod, _ := os.Hostname()
	return &health.Writer{
		Records:  records.NewWriter(bus.KV(events.BucketIndexerHealth), agentrecords.IndexerHealth(), pod, version.String()),
		Suppress: &health.Suppress{},
	}
}

// verbs are the bodies of clustarr.rpc.index.search, .query and
// .download, as registerWorkers built them.
type verbs struct {
	search   *search.Service
	query    *query.Service
	download *download.Service
}

// registerFacade serves the Torznab facade (design §6.2; plan tasks G1-2
// built it, G1-5 wires it) on o.FacadeBindAddress, in this process, over the
// very verb bodies the RPC responder serves -- the facade calls them as plain
// Go functions, so a Torznab search from Sonarr and an automatic search from
// catalog take the same fan-out, dedupe, query-limit window and
// health/backoff.
//
// The facade fails closed. Its keys are the Secret o.FacadeAPIKeySecret
// names, which the manager creates with one random key when it is absent
// (ADR-0019 §7.5, W31); this agent only reads it, by name, every
// [FacadeKeyRereadInterval] ([FacadeKeys]), and the facade answers 503 until
// a key is there -- so a fresh install serves an authenticated facade
// rather than none or an open one, and a rotation needs no restart.
//
// It is a k8s.EveryReplica: it needs no lease, and index is pinned to one
// replica anyway. A bind failure (the port already taken) returns from
// Server.Run, which stops the manager and fails the pod loudly rather than
// running an index whose facade silently is not there.
func registerFacade(ctx context.Context, mgr ctrl.Manager, o Options, v verbs) error {
	if o.FacadeBindAddress == k8s.DisabledBindAddress {
		logging.FromContext(ctx).Info("index: the Torznab facade is disabled",
			"facadeBindAddress", o.FacadeBindAddress)
		return nil
	}
	keys := &FacadeKeys{}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		return keys.Run(ctx, mgr.GetAPIReader(), o.Namespace, o.FacadeAPIKeySecret, FacadeKeyRereadInterval)
	})); err != nil {
		return fmt.Errorf("index: add the facade key reader: %w", err)
	}
	srv, err := facade.New(o.FacadeBindAddress, facade.Config{
		// The cached client: the facade Gets Indexers by name on every
		// request, and index's controllers already hold that informer.
		Client: mgr.GetClient(),
		// facade/doc.go: index's own namespace, where §3's one replica
		// and every chart value and e2e fixture put Indexers. It makes a
		// per-indexer route one Get rather than a cluster-wide List.
		Namespace: o.Namespace,
		Search:    v.search.Search,
		Query:     v.query.Handle,
		Download:  v.download.Handle,
		Keys:      keys.Get,
	})
	if err != nil {
		return fmt.Errorf("index: build the Torznab facade: %w", err)
	}
	if srv == nil {
		// facade.New's documented "disabled" answer, for DisabledBindAddress,
		// which the check above already excluded.
		return nil
	}
	if err := mgr.Add(k8s.EveryReplica(srv.Run)); err != nil {
		return fmt.Errorf("index: add the Torznab facade: %w", err)
	}
	return nil
}
