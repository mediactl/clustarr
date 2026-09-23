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

// Package indexarr aggregates remote indexers into one search engine: a
// Prowlarr-compatible Cardigann/Torznab client in front of a local release
// index. It owns index.clustarr.io.
//
// §3 pins it to exactly one replica with a Recreate strategy and the RWO PVC
// `clustarr-index`, because the release index is a SQLite database on that
// volume and two writers would corrupt it. There is therefore one role.
package indexarr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/controller/indexer"
	"github.com/mediactl/clustarr/indexarr/controller/indexerdefinition"
	"github.com/mediactl/clustarr/indexarr/controller/indexerproxy"
	"github.com/mediactl/clustarr/indexarr/download"
	"github.com/mediactl/clustarr/indexarr/query"
	"github.com/mediactl/clustarr/indexarr/search"
	"github.com/mediactl/clustarr/indexarr/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// Service identity, from §2 and §6.2.
const (
	// ServiceName is the subcommand, the field manager and the NATS client
	// name.
	ServiceName = "indexarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultIndexPath is the SQLite release index on the RWO PVC §6.2
	// mounts. It must equal the PVC's mountPath in
	// config/manager/indexarr.yaml: the pod runs with
	// readOnlyRootFilesystem, so anything outside that mount is unwritable.
	// This was "/index/releases.db", which no manifest ever used and which
	// the read-only root would have refused on the first write.
	DefaultIndexPath = "/var/lib/clustarr/index/releases.db"

	// DefaultFacadeBindAddress serves the Torznab facade §6.2 describes:
	// /{indexer}/api, /{indexer}/download and the aggregate /search/api.
	// It must equal the container port the Service routes to. This was
	// ":9696" -- Prowlarr's port -- while the manifest, the Service's named
	// port and the chart all use 8080, and no flag exists to override it,
	// so the facade would have bound a port nothing routes. The facade
	// itself is M6; the constant is corrected here because it is a landmine
	// in a file this phase edits.
	DefaultFacadeBindAddress = ":8080"
)

// Release-index retention, from §6.2's "`expires_at` sweep every 10 min
// (72h)". [relindex.Store.Prune] is a pure function of its argument and
// schedules nothing on purpose, so the schedule lives here.
const (
	// IndexRetention is how long a release stays in the local index after it
	// was fetched.
	IndexRetention = 72 * time.Hour

	// IndexSweepInterval is how often [relindex.Store.Prune] runs.
	IndexSweepInterval = 10 * time.Minute

	// indexProbeTimeout bounds the readiness probe's Stats call so a wedged
	// SQLite handle fails readiness rather than hanging the probe handler.
	indexProbeTimeout = 5 * time.Second
)

// A CRD default for Indexer.spec, restated here because an apiserver default
// fills a field that is ABSENT FROM THE SUBMITTED JSON, and metav1.Duration is
// a struct that `omitempty` does not elide -- so a typed Go client always
// sends "0s" and is never defaulted. TestIndexerSpecDefaultsMatchTheCRD reads
// the generated schema and fails if it drifts, so this is a mirror rather than
// a second source of truth.
const (
	// DefaultIndexerRequestDelay mirrors Indexer spec.requestDelay's
	// +kubebuilder:default="2s". It is what [defaultLimiterConfig] paces an
	// UNKNOWN host at; see that function for why the fallback must not be
	// unlimited.
	DefaultIndexerRequestDelay = 2 * time.Second
)

// Role selects what a replica does. §6.2 gives indexarr one role: the
// controllers, the search RPC responder, the RSS worker, the release index and
// the Torznab facade all share the single replica's SQLite handle.
type Role string

// The role §6.2 lists for `clustarr indexarr --role`.
const (
	// RoleAll runs the controllers, the search service, the RSS worker and
	// the facade.
	RoleAll Role = "all"
)

// Roles lists the valid --role values.
func Roles() []Role { return []Role{RoleAll} }

// String returns the flag value.
func (r Role) String() string { return string(r) }

// Valid reports whether r is one of [Roles].
func (r Role) Valid() bool { return r == RoleAll }

// RunsControllers reports whether this role reconciles custom resources.
func (r Role) RunsControllers() bool { return r == RoleAll }

// RunsWorkers reports whether this role consumes queue work.
func (r Role) RunsWorkers() bool { return r == RoleAll }

// Options is everything `clustarr indexarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// IndexPath is the SQLite release index file.
	IndexPath string

	// FacadeBindAddress serves the Torznab facade. "0" disables it.
	FacadeBindAddress string

	// Logging configures this process's root logger. The zero value is a
	// reasonable default: JSON to stderr at info level.
	Logging logging.Options

	// Tracing configures the OpenTelemetry SDK. The zero value is a valid,
	// sampling TracerProvider that exports nowhere -- see
	// pkg/obs/tracing.Setup.
	Tracing tracing.Options
}

// DefaultOptions returns the options the Deployment gets with no flags.
func DefaultOptions() Options {
	return Options{
		Options:           k8s.DefaultOptions(),
		Role:              RoleAll,
		IndexPath:         DefaultIndexPath,
		FacadeBindAddress: DefaultFacadeBindAddress,
	}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf("indexarr: unknown --role %q, want one of %v", o.Role, Roles())
	}
	if o.IndexPath == "" {
		return fmt.Errorf("indexarr: --index-path is required")
	}
	if !o.UsesBus() {
		return fmt.Errorf("indexarr: --nats-url is required; the search RPC and the release firehose both use the bus")
	}
	// Leader election adds nothing to a service §3 already pins to one
	// replica with a Recreate strategy, and it would make the restart of the
	// only replica wait out a lease.
	if o.LeaderElect {
		return fmt.Errorf("indexarr: --leader-elect is not supported; §3 pins indexarr to exactly one replica")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// # Secrets are read live and never cached, and that is a decision
//
// Four call sites reach a Secret through the manager's client -- the Indexer
// reconciler's spec.secretRef, the IndexerProxy reconciler's existence check,
// and the download verb's credential and session reads. Wiring them as-is
// starts a CLUSTER-WIDE Secret informer on first use: one watch over every
// Secret in the cluster, every one of them held in this process's memory.
// docs/research/k8s.md:336 offers two ways out, a `ByObject` label selector
// and `client.CacheOptions.DisableFor`. This takes the second, for three
// reasons.
//
//  1. A label selector fails CLOSED and SILENTLY. A cache restricted by
//     selector answers Get for an object that does not match with NotFound --
//     indistinguishable from a Secret that is genuinely absent. Nothing in
//     this repo labels indexer Secrets: not the chart, not the CRD's
//     documentation of spec.secretRef ("Recognised keys: apikey, username,
//     ..."), not the e2e fixtures. Shipping a selector therefore means every
//     existing Indexer reports `secret <ns>/<name> not found` against a
//     Secret the operator can see with kubectl, which is exactly the silent
//     degradation this codebase keeps getting bitten by.
//
//  2. DisableFor removes the exposure rather than narrowing it. There is no
//     watch and no other namespace's Secret is ever resident here; a selector
//     still watches cluster-wide and merely filters what it keeps.
//
//  3. Nothing WATCHES Secrets -- no controller re-reconciles when one
//     changes -- so the informer would be a cache with no invalidation
//     consumer, and a live Get is strictly fresher, which is what a rotated
//     passkey wants.
//
// # The cost, enumerated honestly, and what it forced
//
// The low-frequency readers are the 15-minute reprobe tick per Indexer, the
// 5-minute IndexerProxy recheck and one Get per grab. The HIGH-frequency ones
// are the two this wiring created: indexer.ClientCache.For is the search
// fan-out's ClientFor, called once per candidate indexer per SEARCH, and the
// RSS poll's SearcherFor. A wanted-cron sweep of 200 items across 20 indexers
// is 4,000 live Gets against controller-runtime's default 20 QPS client.
//
// That is not merely slow, and the sharp edge is worth stating: the Get
// happens inside the per-indexer search timeout, and a ClientFor error is a
// named failure outcome, which runs RecordFailure -- escalationLevel, then
// disabledUntil. An apiserver blip or a throttled REST client could therefore
// escalate a perfectly healthy indexer toward disabled, which an informer read
// makes impossible. [indexer.ClientCache] is what closes it: keyed by UID plus
// resourceVersion with a TTL, a fan-out costs one Get per indexer per CHANGE
// rather than per query.
//
// The consequence for RBAC is that nothing in indexarr Lists or Watches a
// Secret any more, and the three component packages that declare the marker
// now ask for `secrets get` alone. The EFFECTIVE grant is unchanged, because
// Clustarr generates one clustarr-manager-role for every ServiceAccount and
// catalogarr's metadata gateway reads Secrets through a cached client, so the
// union keeps list;watch. Splitting the role per service is the fix that
// would cash this in.
func (o Options) ManagerOptions() ctrl.Options {
	opts := o.Options.ManagerOptions(LeaderElectionID, false)
	opts.Client.Cache = &client.CacheOptions{
		DisableFor: []client.Object{&corev1.Secret{}},
	}
	return opts
}

// Run starts the manager and blocks until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if err := o.Validate(); err != nil {
		return err
	}

	// One call stands up the logger, the controller-runtime bridge and
	// the TracerProvider; a failure here is a startup failure.
	ctx, shutdown, err := obs.Bootstrap(ctx, o.Logging, o.Tracing)
	if err != nil {
		return fmt.Errorf("indexarr: %w", err)
	}
	defer shutdown()

	log := ctrl.LoggerFrom(ctx).WithName(ServiceName)
	k8s.RegisterRESTClientMetrics()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("indexarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("indexarr: build manager: %w", err)
	}

	bus, nc, err := k8s.ConnectBus(o.NATSURL, ServiceName, k8s.WithBusHooks(obs.BusHooks()))
	if err != nil {
		return err
	}
	defer nc.Close()
	defer func() {
		if err := bus.Close(); err != nil {
			log.Error(err, "closing the bus")
		}
	}()
	if err := k8s.EnsureTopology(ctx, bus, o.BusTopology()); err != nil {
		return err
	}

	// The release index is opened BEFORE the manager starts, because the RSS
	// worker, the search fan-out and the query verb are all constructed with
	// it and a Store that appeared later would have to be reached through a
	// nil check on every use. Open also creates and migrates the schema, so a
	// success here is the process's proof that the PVC is writable -- §13's
	// "open and writable" half that no cheap periodic probe can restate
	// without writing to the volume every few seconds.
	store, closer, err := relindex.Open(ctx, o.IndexPath)
	if err != nil {
		return fmt.Errorf("indexarr: open the release index at %s: %w", o.IndexPath, err)
	}
	defer func() {
		if err := closer.Close(); err != nil {
			log.Error(err, "closing the release index")
		}
	}()

	// One Limiter for the whole process, behind one ClientCache: the caps
	// probe, the search fan-out, the RSS poll and the download verb all pace
	// against the same bucket per host, and every Torznab client any of them
	// uses comes out of indexer.buildClient, so M6's proxy option cannot
	// reach one path and miss another.
	clients := indexer.NewClientCache(mgr.GetClient(), ratelimit.New(defaultLimiterConfig()))

	ready, err := readinessChecks(mgr, store, k8s.BusReadyChecker(nc, bus))
	if err != nil {
		return err
	}
	if err := k8s.AddProbes(mgr, ready); err != nil {
		return err
	}

	if o.Role.RunsControllers() {
		if err := setupControllers(mgr, bus, clients); err != nil {
			return err
		}
	}
	if o.Role.RunsWorkers() {
		if err := setupWorkers(mgr, bus, store, clients); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "indexPath", o.IndexPath)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("indexarr: manager: %w", err)
	}
	return nil
}

// readinessChecks is §13's readiness gate for indexarr: the informer caches
// synced, the SQLite index open and usable, and the bus connected. jetstream
// is passed in rather than built here so this stays callable without a live
// NATS connection.
//
// There is deliberately no fourth gate for the RPC responder, and the reason
// is not that §13 omits one. A readiness gate could not close that window:
// the three verbs are NATS request/reply on the "indexarr" queue group and do
// not travel through the Kubernetes Service at all, so whether this pod is in
// the Service's endpoints has no bearing on whether a responder exists. A
// gate would delay `kubectl rollout status` and prevent not one
// events.ErrNoResponders. catalogarr/worker/search's busSearchRPC already
// turns that error into a 15s retry (§8.8), which covers a rollout, an
// unscheduled pod and a NATS partition alike.
//
// It is a function rather than a block inside [Run] so that a test can drive
// the real thing. The check it registers through k8s.CacheSyncChecker is a
// [k8s.EveryReplica], not a manager.RunnableFunc, and that is the whole
// reason this is worth testing: a bare RunnableFunc has no
// NeedLeaderElection method, so controller-runtime's runnables.Add falls
// through to the leader-election group and the runnable never starts on a
// non-leader replica -- /readyz then fails forever and, with maxSurge 1 /
// maxUnavailable 0, every rollout deadlocks. indexarr forbids leader
// election today, which makes that latent here rather than fatal; it is a
// deployment detail, not a property of this code, so
// TestReadinessPassesOnANonLeaderReplica turns leader election ON and locks
// this manager out of the lease.
func readinessChecks(
	mgr ctrl.Manager, store relindex.Store, jetstream healthz.Checker,
) (map[string]healthz.Checker, error) {
	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return nil, err
	}
	return map[string]healthz.Checker{
		"jetstream": jetstream,
		// §13's "informer caches synced". Every controller and the search
		// fan-out read through the manager's cache, and an unsynced cache
		// does not fail -- it reports an EMPTY cluster, so a search would
		// answer "no indexers" rather than "not ready yet".
		"cache": cacheReady,
		// §13's SQLite half. Stats is relindex's designated readiness call:
		// it is a real query against the handle, so it fails once the handle
		// stops working.
		"releaseindex": IndexReadyChecker(store),
	}, nil
}

// defaultLimiterConfig is the bucket [ratelimit.Limiter] hands to a host it
// has never been given a Config for, and getting it wrong is silent.
//
// The obvious construction is ratelimit.New(ratelimit.Config{}) -- every real
// per-host config arrives later, from the Indexer reconciler's SetConfig, so
// the default looks like it is never consulted. It is: pkg/ratelimit falls
// back to the Limiter's `defaults` for any key without its own Config, and
// Config.RPS <= 0 is rate.Inf. Every window in which a host has no Config yet
// is therefore a window with NO pacing at all -- the whole interval between
// process start and that Indexer's first reconcile, a fresh host added by an
// edit, and any key spelled differently from the reconciler's. Against a
// private tracker that is a ban, not a slowdown.
//
// The rate is derived from the CRD's own default for spec.requestDelay rather
// than from a fresh literal, so an operator who changes the default in
// api/index/v1alpha1 moves this too (and TestIndexerSpecDefaultsMatchTheCRD
// fails if the mirror ever stops matching).
//
// This does NOT overrule an explicit `requestDelay: 0s`, which the CRD
// permits and the Indexer reconciler maps to RPS 0 on purpose -- "do not pace
// me". SetConfig installs a config FOR THAT KEY, and a key with its own
// config never reads `defaults`. The fallback only ever applies to a host
// nobody has decided about yet, where "pace it like the CRD's default" is the
// only safe guess.
func defaultLimiterConfig() ratelimit.Config {
	return ratelimit.Config{RPS: 1 / DefaultIndexerRequestDelay.Seconds(), Burst: 1}
}

// IndexReadyChecker reports whether the release index is still usable, and is
// §13's "the SQLite index open and writable" readiness gate.
//
// It is a READ. The writable half is proved once, by [relindex.Open], which
// creates and migrates the schema before this checker is ever registered -- a
// process whose volume is read-only never reaches mgr.Start. Re-proving it on
// every probe would mean a write to the PVC every few seconds for the life of
// the pod, which buys a failure mode (the volume going read-only under a
// running pod) that no Clustarr deployment produces.
//
// Stats is relindex's own nominated probe: its doc says so, it runs a real
// query against the handle, and it deliberately tolerates a missing file when
// sizing the volume so a stat() race cannot flap readiness.
func IndexReadyChecker(store relindex.Store) healthz.Checker {
	return func(req *http.Request) error {
		if store == nil {
			return errors.New("release index: no store was opened")
		}
		ctx, cancel := context.WithTimeout(req.Context(), indexProbeTimeout)
		defer cancel()
		if _, err := store.Stats(ctx); err != nil {
			return fmt.Errorf("release index: %w", err)
		}
		return nil
	}
}

// setupControllers registers indexarr's three reconcilers (§6.2, §16 M2 and
// M6). Each package's doc.go documents the exact call; these are those calls.
//
// All three take a k8s.io/client-go/tools/record.EventRecorder -- the
// DEPRECATED mgr.GetEventRecorderFor, which writes CORE/v1 Events -- and
// their +kubebuilder:rbac markers declare `groups=""` to match. catalogarr's
// setupControllers carries the long note on why two recorder conventions
// coexist in this tree.
//
// limiters is the ONE process-wide *ratelimit.Limiter. The Indexer reconciler
// is its only writer (it is the only reader of spec.requestDelay); the search
// fan-out, the RSS poll and the download verb share the instance and only
// Wait on it, so all four pace against one bucket per host.
//
// bus is not optional in production, even though indexer.NewReconciler
// tolerates nil by logging a warning. The reconciler SEEDS the first RssTask
// (ruling R36) and the RSS worker schedules every one after it, so a nil bus
// means no chain ever starts and the release firehose publishes nothing --
// which is the entire point of the worker. TestTheIndexerReconcilerGetsARealBus
// turns "someone notices a warning" into a failing test.
//
// TODO(M6): the Cardigann login test and the owned session Secret for
// indexer, and proxy routing for indexerproxy. (§6.2, §16 M6)
func setupControllers(mgr ctrl.Manager, bus events.Bus, clients *indexer.ClientCache) error {
	c := mgr.GetClient()

	idxReconciler := indexer.NewReconciler(
		c,
		mgr.GetEventRecorderFor("indexer"), //nolint:staticcheck // record.EventRecorder; see the note above
		clients.Limiters(),
		bus,
	)
	// So a deleted Indexer does not leave its built client, and that
	// client's idle connections, in the cache forever.
	idxReconciler.Clients = clients
	if err := idxReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexer: %w", err)
	}

	if err := indexerdefinition.NewReconciler(
		c,
		mgr.GetEventRecorderFor("indexerdefinition"), //nolint:staticcheck // record.EventRecorder; see the note above
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexerdefinition: %w", err)
	}

	// The nil *http.Client is indexerproxy.NewReconciler's documented
	// "http.DefaultClient". It is deliberate rather than an omission: the
	// prober bounds every probe with a context derived from
	// spec.requestTimeout, and an http.Client.Timeout here would be a hard
	// cap UNDER that, silently ignoring an operator who asked for longer.
	if err := indexerproxy.NewReconciler(
		c,
		mgr.GetEventRecorderFor("indexerproxy"), //nolint:staticcheck // record.EventRecorder; see the note above
		nil,
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexerproxy: %w", err)
	}

	return nil
}

// setupWorkers registers the three RPC verbs, the RSS poll consumer and the
// release index's retention sweep, all as manager Runnables so they stop with
// the manager.
//
// Every one of them is a [k8s.EveryReplica], directly or through
// rss.Worker.SetupWithManager. indexarr forbids leader election (see
// Options.Validate), so a bare manager.RunnableFunc would in fact start here
// -- controller-runtime treats a non-electing process as elected. That is a
// deployment detail a future change can invalidate silently, and it is why
// Phase C's readiness deadlock survived review, so nothing here relies on it.
//
// TODO(M6): the Cardigann engine and the Torznab facade on
// o.FacadeBindAddress. (§6.2, §16 M6)
func setupWorkers(mgr ctrl.Manager, bus events.Bus, store relindex.Store, clients *indexer.ClientCache) error {
	c := mgr.GetClient()

	// indexarr/download's doc.go documents this construction verbatim.
	dl := &download.Service{
		Client: c,
		Bus:    bus,
		Fetch:  download.NewFetcherFor(c, clients.Limiters()),
	}
	q := &query.Service{Store: store}
	svc := &search.Service{
		Client: c,
		ClientFor: func(ctx context.Context, idx *indexv1alpha1.Indexer) (search.IndexerClient, error) {
			cli, err := clients.For(ctx, idx)
			if err != nil {
				// Returning `clients.For(...)` directly would hand back a
				// non-nil interface wrapping a nil *torznab.Client on the
				// error path, which is a nil dereference one call later.
				return nil, err
			}
			return cli, nil
		},
		Store:    store,
		Bus:      bus,
		Download: dl.Handle,
		Query:    q.Handle,
	}

	// search.Serve is indexarr's single registration point for the RPC queue
	// group: it registers clustarr.rpc.indexarr.search, .download and .query
	// in one call. It has no unsubscribe, so its stop drains in-flight
	// fan-outs rather than deregistering the responders.
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := search.Serve(ctx, bus, svc)
		if err != nil {
			return fmt.Errorf("indexarr: serve the index RPC verbs: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("indexarr: add the RPC responder: %w", err)
	}

	if err := rss.NewWorker(rss.Deps{
		Client: c,
		Bus:    bus,
		Index:  store,
		SearcherFor: func(ctx context.Context, idx *indexv1alpha1.Indexer) (rss.Searcher, error) {
			cli, err := clients.For(ctx, idx)
			if err != nil {
				return nil, err
			}
			return cli, nil
		},
	}).SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("indexarr: subscribe rss: %w", err)
	}

	if err := mgr.Add(k8s.EveryReplica(sweepReleaseIndex(store))); err != nil {
		return fmt.Errorf("indexarr: add the release-index sweep: %w", err)
	}

	return nil
}

// sweepReleaseIndex is §6.2's "`expires_at` sweep every 10 min (72h)".
// relindex.Open starts no goroutines and Prune reads no clock, both
// deliberately, so the schedule is here.
//
// It sweeps once on startup before it starts ticking: indexarr is pinned to a
// Recreate rollout, so a restart is the moment the index is most likely to be
// holding a backlog older than the window, and a first sweep ten minutes in
// leaves that backlog answering searches until then.
//
// A failed sweep is logged and the ticker continues. Returning the error would
// take the whole manager down over a transient SQLITE_BUSY, and the index is a
// cache (ADR-0003): the cost of a missed sweep is disk, not correctness.
//
// The return type is a plain func rather than a k8s.EveryReplica so the
// conversion stays visible at the mgr.Add call site, where all three of
// indexarr's runnables read the same way and the leader-election property is
// the thing a reader is checking.
func sweepReleaseIndex(store relindex.Store) func(context.Context) error {
	return func(ctx context.Context) error {
		pruneOnce(ctx, store)
		ticker := time.NewTicker(IndexSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				pruneOnce(ctx, store)
			}
		}
	}
}

// pruneOnce drops every release fetched more than [IndexRetention] ago.
func pruneOnce(ctx context.Context, store relindex.Store) {
	ctx, span := tracing.Start(ctx, "indexarr.relindex.prune")
	defer span.End()

	log := logging.FromContext(ctx)
	deleted, err := store.Prune(ctx, time.Now().Add(-IndexRetention))
	if err != nil {
		tracing.RecordError(span, err)
		log.Error("indexarr: pruning the release index", "error", err)
		return
	}
	if deleted > 0 {
		log.Info("indexarr: pruned the release index",
			"deleted", deleted, "retention", IndexRetention.String())
	}
}
