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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	indexagent "github.com/mediactl/clustarr/app/indexer/agent"
	"github.com/mediactl/clustarr/app/indexer/clientcache"
	idxclients "github.com/mediactl/clustarr/app/indexer/clients"
	indexermanager "github.com/mediactl/clustarr/app/indexer/manager"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// Service identity, from §2 and §6.2.
const (
	// ServiceName is the subcommand, the field manager and the NATS client
	// name.
	ServiceName = "indexarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultIndexPath is the index domain's; see indexagent.DefaultIndexPath.
	DefaultIndexPath = indexagent.DefaultIndexPath

	// DefaultFacadeBindAddress is the index domain's; see
	// indexagent.DefaultFacadeBindAddress.
	DefaultFacadeBindAddress = indexagent.DefaultFacadeBindAddress

	// DefaultFacadeAPIKeySecret is the index domain's; see
	// indexagent.DefaultFacadeAPIKeySecret.
	DefaultFacadeAPIKeySecret = indexagent.DefaultFacadeAPIKeySecret

	// FacadeAPIKeyField is the index domain's; see
	// indexagent.FacadeAPIKeyField.
	FacadeAPIKeyField = indexagent.FacadeAPIKeyField
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
	// [Options.ManagerOptions] then elects a leader.
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
	// election UNCONDITIONALLY in that case -- every controller, and the
	// Cardigann bundle loader (R9), must be a cluster singleton once more
	// than one replica can be reconciling -- so --leader-elect is accepted here,
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
// are the two this wiring created: clientcache.ClientCache.For is the search
// fan-out's ClientFor, called once per candidate indexer per SEARCH, and the
// RSS poll's SearcherFor. A wanted-cron sweep of 200 items across 20 indexers
// is 4,000 live Gets against controller-runtime's default 20 QPS client.
//
// That is not merely slow, and the sharp edge is worth stating: the Get
// happens inside the per-indexer search timeout, and a ClientFor error is a
// named failure outcome, which runs RecordFailure -- escalationLevel, then
// disabledUntil. An apiserver blip or a throttled REST client could therefore
// escalate a perfectly healthy indexer toward disabled, which an informer read
// makes impossible. [clientcache.ClientCache] is what closes it: keyed by UID plus
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
// and every controller plus the Cardigann bundle loader (R9) must be a
// cluster singleton the moment a second replica can exist -- not merely when an
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

	// One Limiter and one ClientCache for --role all, shared by the manager's
	// Indexer reconciler and the index domain until W4.25 (R8) gives each its
	// own. The KV half of the session store: without it the cache reads a
	// definition-backed Indexer's login session from the owned Secret only,
	// which is correct but a live apiserver GET per client build.
	clients := clientcache.NewClientCache(mgr.GetClient(), ratelimit.New(idxclients.DefaultLimiterConfig()))
	clients.Sessions = idxclients.NewSessionStore(mgr.GetClient(), bus)

	reg, err := indexagent.Register(ctx, mgr, bus, agentOptions(o, clients))
	if err != nil {
		return fmt.Errorf("indexarr: %w", err)
	}
	defer func() {
		if err := reg.Close(); err != nil {
			log.Error(err, "closing the release index")
		}
	}()
	ready, err := readinessChecks(mgr, k8s.BusReadyChecker(nc, bus), reg)
	if err != nil {
		return err
	}
	if err := k8s.AddProbes(mgr, ready, nil); err != nil {
		return err
	}
	if err := indexermanager.Register(mgr, bus, managerOptions(o, clients)); err != nil {
		return fmt.Errorf("indexarr: %w", err)
	}

	log.Info("starting", "role", o.Role, "indexPath", o.IndexPath,
		"facade", o.FacadeBindAddress, "facadeEnabled", o.FacadeEnabled())
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("indexarr: manager: %w", err)
	}
	return nil
}

// agentOptions: Validate already enforced the facade rules; a disabled
// facade reaches the agent as DisabledBindAddress.
func agentOptions(o Options, cc *clientcache.ClientCache) indexagent.Options {
	addr := o.FacadeBindAddress
	if !o.FacadeEnabled() {
		addr = k8s.DisabledBindAddress
	}
	return indexagent.Options{Options: o.Options, IndexPath: o.IndexPath, IndexDSN: o.IndexDSN,
		FacadeBindAddress: addr, FacadeAPIKeySecret: o.FacadeAPIKeySecret, Clients: cc}
}

func managerOptions(o Options, cc *clientcache.ClientCache) indexermanager.Options {
	return indexermanager.Options{Options: o.Options, CardigannDefinitionsDir: o.CardigannDefinitionsDir,
		CardigannBundled: o.CardigannBundled, Limiter: cc.Limiters(), ForgetClient: cc.Forget}
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
//
// The index domain's own check, index.releaseindex, arrives in reg.Ready and
// is merged after the process checks.
func readinessChecks(
	mgr ctrl.Manager, jetstream healthz.Checker, reg catalogagent.Registration,
) (*k8s.Checks, error) {
	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return nil, err
	}
	ready := k8s.NewChecks()
	if err := ready.Add("jetstream", jetstream); err != nil {
		return nil, err
	}
	// §13's "informer caches synced". Every controller and the search
	// fan-out read through the manager's cache, and an unsynced cache
	// does not fail -- it reports an EMPTY cluster, so a search would
	// answer "no indexers" rather than "not ready yet".
	if err := ready.Add("cache", cacheReady); err != nil {
		return nil, err
	}
	if err := ready.Merge(reg.Ready); err != nil {
		return nil, err
	}
	return ready, nil
}
