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
	"io"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/bundle"
	"github.com/mediactl/clustarr/app/indexer/bundle/embedded"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/app/indexer/controller/indexerdefinition"
	"github.com/mediactl/clustarr/app/indexer/controller/indexerproxy"
	"github.com/mediactl/clustarr/app/indexer/download"
	"github.com/mediactl/clustarr/app/indexer/facade"
	"github.com/mediactl/clustarr/app/indexer/query"
	"github.com/mediactl/clustarr/app/indexer/search"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
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
	// It must equal the container port the Service routes to (8080, the
	// port named "http" in config/manager/indexarr.yaml and the chart);
	// TestDefaultFacadeBindAddressMatchesTheServicePort pins it. It was once
	// ":9696" -- Prowlarr's port -- which nothing routed to.
	DefaultFacadeBindAddress = ":8080"

	// DefaultFacadeAPIKeySecret is the Secret, in indexarr's own namespace,
	// whose data entries are the API keys the Torznab facade accepts. The
	// facade FAILS CLOSED -- facade.New refuses to build without a key --
	// so indexarr creates this Secret with one random key on first start
	// when it does not exist, the way Prowlarr generates its own API key.
	// config/ uses this name as-is; the chart's carries the release
	// fullname and reaches --facade-api-key-secret through
	// $CLUSTARR_FACADE_API_KEY_SECRET. See [ensureFacadeAPIKeys].
	DefaultFacadeAPIKeySecret = "indexarr-facade"
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

	// IndexPath is the SQLite release index file. Ignored when IndexDSN is
	// set.
	IndexPath string

	// IndexDSN is a Postgres DSN for the release index (--index-dsn,
	// $CLUSTARR_INDEX_DSN). Non-empty selects [relindex.OpenPostgres] and
	// IndexPath is ignored; empty (the default) keeps SQLite at IndexPath.
	// Spec §A.3: with a DSN indexarr may run several replicas, which is why
	// [indexSweeper] declares NeedLeaderElection (ruling R1).
	IndexDSN string

	// FacadeBindAddress serves the Torznab facade. "0" disables it.
	FacadeBindAddress string

	// FacadeAPIKeySecret names the Secret in Namespace whose every non-blank
	// data entry is an API key the facade accepts; see
	// [DefaultFacadeAPIKeySecret]. Only read when the facade is enabled.
	// It is a NAME, never a key: a key in a flag or its default would sit
	// in argv, `ps` and --help.
	FacadeAPIKeySecret string

	// CardigannDefinitionsDir is a Cardigann definition bundle to load as
	// IndexerDefinitions at startup (--cardigann-definitions-dir): a
	// directory of definition YAML files, such as hack/sync-cardigann
	// writes, mounted into the pod. When set it replaces the embedded corpus
	// entirely, so an operator can pin or trim the definitions. See
	// app/indexer/bundle.
	CardigannDefinitionsDir string

	// CardigannBundled loads the corpus compiled into the binary
	// (app/indexer/bundle/embedded, --cardigann-bundled) when
	// CardigannDefinitionsDir is empty. The CLI defaults it on; the zero
	// value, which Go callers such as tests get, loads nothing.
	CardigannBundled bool

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
		Options:            k8s.DefaultOptions(),
		Role:               RoleAll,
		IndexPath:          DefaultIndexPath,
		FacadeBindAddress:  DefaultFacadeBindAddress,
		FacadeAPIKeySecret: DefaultFacadeAPIKeySecret,
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
	// Leader election adds nothing to a service §3 pins to one replica with
	// a Recreate strategy under SQLite, and it would make the restart of
	// the only replica wait out a lease. With --index-dsn indexarr may run
	// several replicas (spec §A.3), and ManagerOptions enables leader
	// election UNCONDITIONALLY in that case -- every controller, and
	// [indexSweeper] (ruling R1), must be a cluster singleton once more than
	// one replica can be reconciling -- so --leader-elect is accepted here,
	// though redundant: Postgres mode elects regardless of the flag's own
	// value. Only the SQLite case (no DSN) still rejects it.
	if o.LeaderElect && o.IndexDSN == "" {
		return fmt.Errorf("indexarr: --leader-elect is supported only with --index-dsn; " +
			"§3 pins indexarr to exactly one replica under SQLite")
	}
	if o.FacadeEnabled() {
		if o.FacadeBindAddress == "" {
			return fmt.Errorf("indexarr: --facade-bind-address is empty; give an address, or %q to disable the facade",
				k8s.DisabledBindAddress)
		}
		if o.FacadeAPIKeySecret == "" {
			return fmt.Errorf("indexarr: --facade-api-key-secret is required while the Torznab facade is enabled; " +
				"it fails closed and will not serve without an API key")
		}
		if o.Namespace == "" {
			return fmt.Errorf("indexarr: the Torznab facade's API-key Secret lives in indexarr's own namespace; "+
				"set --namespace (or $POD_NAMESPACE), or --facade-bind-address=%s to disable the facade",
				k8s.DisabledBindAddress)
		}
	}
	return o.Options.Validate()
}

// FacadeEnabled reports whether this process serves the Torznab facade.
func (o Options) FacadeEnabled() bool {
	return o.Role.RunsWorkers() && o.FacadeBindAddress != k8s.DisabledBindAddress
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
//
// # Leader election is keyed on IndexDSN, not on o.LeaderElect
//
// Contrast every sibling service's own ManagerOptions, which honours the
// flag directly: under SQLite (empty IndexDSN) §3 pins indexarr to one
// replica and Validate rejects --leader-elect outright, so there is nothing
// to elect; under Postgres (spec §A.3) indexarr may run several replicas,
// and every controller plus [indexSweeper] (ruling R1) must be a cluster
// singleton the moment a second replica can exist -- not merely when an
// operator remembers to pass the flag. Options.Validate still accepts
// --leader-elect when IndexDSN is set, but it is a no-op there: this is
// always on.
func (o Options) ManagerOptions() ctrl.Options {
	opts := o.Options.ManagerOptions(LeaderElectionID, o.IndexDSN != "")
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
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(o.ManagerOptions(), ctx))
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
	// nil check on every use. Open (or OpenPostgres) also creates and
	// migrates the schema, so a success here is the process's proof that the
	// index is open and writable -- the PVC under SQLite, the configured
	// database under Postgres -- §13's "open and writable" half that no
	// cheap periodic probe can restate without writing to the index every
	// few seconds.
	//
	// A non-empty IndexDSN selects Postgres and IndexPath is ignored (spec
	// §A.3); empty keeps SQLite, the default.
	var (
		store  relindex.Store
		closer io.Closer
	)
	if o.IndexDSN != "" {
		log.Info("indexarr: release index on Postgres; --index-path ignored")
		store, closer, err = relindex.OpenPostgres(ctx, o.IndexDSN)
		if err != nil {
			return fmt.Errorf("indexarr: open the release index at the configured --index-dsn: %w", err)
		}
	} else {
		store, closer, err = relindex.Open(ctx, o.IndexPath)
		if err != nil {
			return fmt.Errorf("indexarr: open the release index at %s: %w", o.IndexPath, err)
		}
	}
	defer func() {
		if err := closer.Close(); err != nil {
			log.Error(err, "closing the release index")
		}
	}()

	// One Limiter for the whole process, behind one ClientCache: the caps
	// probe, the search fan-out, the RSS poll and the download verb all pace
	// against the same bucket per host, and every wire client any of them
	// uses -- Torznab or Cardigann -- comes out of the indexer package's one
	// builder, so spec.proxyRef cannot reach one path and miss another.
	clients := indexer.NewClientCache(mgr.GetClient(), ratelimit.New(defaultLimiterConfig()))
	// The KV half of the session store: without it the cache reads a
	// definition-backed Indexer's login session from the owned Secret only,
	// which is correct but a live apiserver GET per client build. The
	// reconciler writes both (indexer.NewReconciler builds its own store
	// from the same bus), so reads and writes see the same two tiers.
	clients.Sessions = indexer.NewSessionStore(mgr.GetClient(), bus)

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
		if err := setupBundle(mgr, o); err != nil {
			return err
		}
	}
	if o.Role.RunsWorkers() {
		verbs, err := setupWorkers(mgr, bus, store, clients)
		if err != nil {
			return err
		}
		if err := setupFacade(ctx, mgr, o, verbs); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "indexPath", o.IndexPath,
		"facade", o.FacadeBindAddress, "facadeEnabled", o.FacadeEnabled())
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
// events.ErrNoResponders. app/catalog/worker/search's busSearchRPC already
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

// setupControllers registers indexarr's reconcilers (§6.2, §16 M2 and M6):
// Indexer, IndexerDefinition and IndexerProxy, plus the direct-grab counter
// that watches Downloads. Each package's doc.go documents the exact call;
// these are those calls.
//
// All three take a k8s.io/client-go/tools/events.EventRecorder from
// mgr.GetEventRecorder, which writes events.k8s.io/v1 Events, and their
// +kubebuilder:rbac markers declare `groups=events.k8s.io` to match. The
// deprecated mgr.GetEventRecorderFor is not used anywhere in this tree;
// catalogarr's setupControllers carries the long note on why the marker and
// the recorder type have to move in the same commit.
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
// The Cardigann login and the owned session Secret (plan task G1-1) live
// inside the Indexer reconciler, which NewReconciler wires to the bus's
// clustarr-indexer-sessions bucket. Proxy routing -- spec.proxyRef and every
// IndexerProxy whose spec.selector matches the Indexer -- is app/indexer/proxy's,
// applied by the one client builder every path shares and by the download
// fetcher, not by the IndexerProxy reconciler, which only probes reachability.
func setupControllers(mgr ctrl.Manager, bus events.Bus, clients *indexer.ClientCache) error {
	c := mgr.GetClient()

	idxReconciler := indexer.NewReconciler(
		c,
		mgr.GetEventRecorder("indexer"),
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
		mgr.GetEventRecorder("indexerdefinition"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexerdefinition: %w", err)
	}

	// Grabs whose source is a direct torrentURL/magnetURL/nzbURL never reach
	// rpc.indexarr.download, so this counts them into the same grab ring
	// from the Download's creation; see download.DirectGrabReconciler.
	if err := (&download.DirectGrabReconciler{Client: c, Bus: bus}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: direct-grab accounting: %w", err)
	}

	// The nil *http.Client is indexerproxy.NewReconciler's documented
	// "http.DefaultClient". It is deliberate rather than an omission: the
	// prober bounds every probe with a context derived from
	// spec.requestTimeout, and an http.Client.Timeout here would be a hard
	// cap UNDER that, silently ignoring an operator who asked for longer.
	if err := indexerproxy.NewReconciler(
		c,
		mgr.GetEventRecorder("indexerproxy"),
		nil,
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("indexarr: indexerproxy: %w", err)
	}

	return nil
}

// setupBundle registers the Cardigann bundle loader: from the directory
// --cardigann-definitions-dir names when it is set, otherwise from the
// corpus embedded in the binary when --cardigann-bundled is on (task X14;
// X8a built cardigann.LoadBundle and left its consumer to the wiring). It
// applies every definition the bundle accepts as a labelled
// IndexerDefinition, once, after the caches sync, and leaves any same-named
// IndexerDefinition that is not the bundle's alone (see app/indexer/bundle). A
// bundle directory that cannot be read stops the manager: the operator asked
// for definitions that are not there.
func setupBundle(mgr ctrl.Manager, o Options) error {
	loader := &bundle.Loader{Client: mgr.GetClient(), Dir: o.CardigannDefinitionsDir}
	switch {
	case o.CardigannDefinitionsDir != "":
	case o.CardigannBundled:
		fsys, err := embedded.FS()
		if err != nil {
			return fmt.Errorf("indexarr: %w", err)
		}
		loader.Dir, loader.FS = "(embedded)", fsys
	default:
		return nil
	}
	if err := mgr.Add(k8s.EveryReplica(loader.Run)); err != nil {
		return fmt.Errorf("indexarr: add the Cardigann bundle loader: %w", err)
	}
	return nil
}

// setupWorkers registers the three RPC verbs, the RSS poll consumer and the
// release index's retention sweep, all as manager Runnables so they stop with
// the manager.
//
// The RPC responder and the RSS worker are each a [k8s.EveryReplica],
// directly or through rss.Worker.SetupWithManager: every replica answers
// search RPCs and polls RSS, regardless of leader election. The retention
// sweep is [indexSweeper] instead (ruling R1): a Postgres-backed index
// (IndexDSN) may run several replicas, and only one of them may prune, so it
// declares NeedLeaderElection.
//
// Options.ManagerOptions enables leader election precisely when IndexDSN is
// set (see its own doc comment), so indexSweeper's NeedLeaderElection binds
// for real under Postgres: exactly one replica runs it, and every
// controller-runtime controller -- which also declares NeedLeaderElection by
// default -- stops double-reconciling the moment a second replica exists.
// Under SQLite the manager still never elects, so a non-electing process is
// treated as elected and indexSweeper runs immediately regardless -- under
// SQLite's single replica that single replica is always "the leader" and
// nothing observable changes, exactly as it would for a bare
// manager.RunnableFunc. indexSweeper still declares NeedLeaderElection
// itself rather than leaning on that SQLite-only coincidence, because the
// coincidence stops holding the moment IndexDSN is set.
//
// It returns the three verb bodies it built, so [setupFacade] serves the
// SAME instances the RPC responder does: one download.Service with one
// Definitions dispatch, one search fan-out with one query-limit window. A
// facade holding its own copies would be a second path around both.
func setupWorkers(
	mgr ctrl.Manager, bus events.Bus, store relindex.Store, clients *indexer.ClientCache,
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
	dl := &download.Service{
		Client:      c,
		Bus:         bus,
		Fetch:       download.NewFetcherFor(c, clients.Limiters()),
		Definitions: clients.DefinitionFetcherFor,
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
		return verbs{}, fmt.Errorf("indexarr: add the RPC responder: %w", err)
	}

	if err := rss.NewWorker(rssDeps(c, bus, store, clients)).SetupWithManager(mgr, bus); err != nil {
		return verbs{}, fmt.Errorf("indexarr: subscribe rss: %w", err)
	}

	if err := mgr.Add(indexSweeper{store: store}); err != nil {
		return verbs{}, fmt.Errorf("indexarr: add the release-index sweep: %w", err)
	}

	return verbs{search: svc, query: q, download: dl}, nil
}

// rssDeps is the RSS poll's wiring. It is a function so a test can assert
// what production hands the worker rather than a copy of it.
//
// CountQuery is not optional in production: it is what puts the poll's up to
// four requests per poll into the same query ring the search fan-out counts
// into (spec §5's <indexer-uid>.query in clustarr-indexer-limits), so
// status.queriesInWindow -- and the RateLimited condition and
// spec.limits.queryLimit read from it -- see the indexer's whole traffic.
func rssDeps(c client.Client, bus events.Bus, store relindex.Store, clients *indexer.ClientCache) rss.Deps {
	limits := bus.KV(events.BucketIndexerLimits)
	return rss.Deps{
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
		CountQuery: func(ctx context.Context, idx *indexv1alpha1.Indexer, now time.Time) (int32, error) {
			return search.CountQuery(ctx, limits, idx, now)
		},
	}
}

// verbs are the bodies of clustarr.rpc.indexarr.search, .query and
// .download, as setupWorkers built them.
type verbs struct {
	search   *search.Service
	query    *query.Service
	download *download.Service
}

// setupFacade serves the Torznab facade (design §6.2; plan tasks G1-2 built
// it, G1-5 wires it) on o.FacadeBindAddress, in this process, over the very
// verb bodies the RPC responder serves -- the facade calls them as plain Go
// functions, so a Torznab search from Sonarr and an automatic search from
// catalogarr take the same fan-out, dedupe, query-limit window and
// health/backoff.
//
// The facade fails closed: facade.New refuses to build without an API key.
// The keys come from the Secret o.FacadeAPIKeySecret names, which
// [ensureFacadeAPIKeys] creates with one random key when it is absent, so a
// fresh install serves an authenticated facade rather than none or an open
// one. They are read ONCE, here, before the manager starts: rotating a key is
// an edit to the Secret and a restart of indexarr's one replica.
//
// It is a k8s.EveryReplica: it needs no lease, and indexarr is pinned to one
// replica anyway. A bind failure (the port already taken) returns from
// Server.Run, which stops the manager and fails the pod loudly rather than
// running an indexarr whose facade silently is not there.
func setupFacade(ctx context.Context, mgr ctrl.Manager, o Options, v verbs) error {
	if !o.FacadeEnabled() {
		logging.FromContext(ctx).Info("indexarr: the Torznab facade is disabled",
			"facadeBindAddress", o.FacadeBindAddress)
		return nil
	}
	keys, err := ensureFacadeAPIKeys(ctx, mgr.GetAPIReader(), mgr.GetClient(), o.Namespace, o.FacadeAPIKeySecret)
	if err != nil {
		return err
	}
	srv, err := facade.New(o.FacadeBindAddress, facade.Config{
		// The cached client: the facade Gets Indexers by name on every
		// request, and indexarr's controllers already hold that informer.
		Client: mgr.GetClient(),
		// facade/doc.go: indexarr's own namespace, where §3's one replica
		// and every chart value and e2e fixture put Indexers. It makes a
		// per-indexer route one Get rather than a cluster-wide List.
		Namespace: o.Namespace,
		Search:    v.search.Search,
		Query:     v.query.Handle,
		Download:  v.download.Handle,
		APIKeys:   keys,
	})
	if err != nil {
		return fmt.Errorf("indexarr: build the Torznab facade: %w", err)
	}
	if srv == nil {
		// facade.New's documented "disabled" answer, for DisabledBindAddress,
		// which FacadeEnabled already excluded.
		return nil
	}
	if err := mgr.Add(k8s.EveryReplica(srv.Run)); err != nil {
		return fmt.Errorf("indexarr: add the Torznab facade: %w", err)
	}
	return nil
}

// indexSweeper is §6.2's "`expires_at` sweep every 10 min (72h)".
// relindex.Open/OpenPostgres starts no goroutines and Prune reads no clock,
// both deliberately, so the schedule is here.
//
// It sweeps once on startup before it starts ticking: indexarr is pinned to a
// Recreate rollout under SQLite, so a restart is the moment the index is most
// likely to be holding a backlog older than the window, and a first sweep ten
// minutes in leaves that backlog answering searches until then.
//
// A failed sweep is logged and the ticker continues. Returning the error would
// take the whole manager down over a transient SQLITE_BUSY, and the index is a
// cache (ADR-0003): the cost of a missed sweep is disk, not correctness.
//
// It is a typed Runnable, not a k8s.EveryReplica, because it is NOT one:
// ruling R1 makes it a cluster singleton, since a Postgres-backed index
// (IndexDSN) may run several replicas and Prune racing across two of them is
// wasted work, not corruption, but still work worth doing once. Declaring
// NeedLeaderElection states that regardless of whether today's indexarr
// manager actually enables leader election (see setupWorkers's doc comment).
type indexSweeper struct {
	store relindex.Store
}

// Start implements manager.Runnable.
func (s indexSweeper) Start(ctx context.Context) error {
	pruneOnce(ctx, s.store)
	ticker := time.NewTicker(IndexSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pruneOnce(ctx, s.store)
		}
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: ruling R1,
// the retention sweep is leader-only under both engines.
func (indexSweeper) NeedLeaderElection() bool { return true }

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
