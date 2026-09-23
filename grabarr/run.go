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

// Package grabarr owns download.clustarr.io: the DownloadClient and Download
// controllers, plus the embedded torrent and usenet engines that do the
// transfers.
//
// §3 splits it into a controller Deployment and, per DownloadClient, an engine
// StatefulSet (torrent) or Deployment (usenet). The engines are the same binary
// under a different --role, so an engine pod can watch only its own Downloads
// through the download.clustarr.io/engine label.
package grabarr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/controller/download"
	"github.com/mediactl/clustarr/grabarr/controller/downloadclient"
	"github.com/mediactl/clustarr/grabarr/engine"
	"github.com/mediactl/clustarr/grabarr/engine/torrent"
	"github.com/mediactl/clustarr/grabarr/engine/usenet"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Service identity, from §2 and §6.3.
const (
	// ServiceName is the subcommand, the controller field manager and the
	// NATS client name. Engine pods apply status under
	// k8s.ManagerGrabarrEngine instead.
	ServiceName = "grabarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultDataDir is the RWX media volume. Torrent engines write
	// <DataDir>/torrents/<category>/<download> and keep their persisted
	// metainfo under <DataDir>/torrents/.state.
	DefaultDataDir = "/data"

	// DefaultScratchDir is the usenet engine's working area: sparse .part
	// files at yEnc offsets, PAR2 repair and extraction all happen here
	// before the result is copied into DataDir and renamed atomically.
	DefaultScratchDir = "/scratch"
)

// Role selects what a replica does.
type Role string

// The roles §6.3 lists for `clustarr grabarr --role`.
const (
	// RoleController runs the DownloadClient and Download reconcilers and the
	// blocklist sweeper. Leader-elected.
	RoleController Role = "controller"

	// RoleTorrentEngine runs the embedded anacrolix torrent client for one
	// StatefulSet ordinal.
	RoleTorrentEngine Role = "torrent-engine"

	// RoleUsenetEngine runs the embedded usenet pipeline: NNTP pools, yEnc
	// assembly, PAR2 repair and extraction.
	RoleUsenetEngine Role = "usenet-engine"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role { return []Role{RoleController, RoleTorrentEngine, RoleUsenetEngine} }

// String returns the flag value.
func (r Role) String() string { return string(r) }

// Valid reports whether r is one of [Roles].
func (r Role) Valid() bool {
	for _, known := range Roles() {
		if r == known {
			return true
		}
	}
	return false
}

// RunsControllers reports whether this role reconciles custom resources.
func (r Role) RunsControllers() bool { return r == RoleController }

// IsEngine reports whether this role is an engine pod. An engine writes only
// the telemetry subset of Download.status, under k8s.ManagerGrabarrEngine.
func (r Role) IsEngine() bool { return r == RoleTorrentEngine || r == RoleUsenetEngine }

// Options is everything `clustarr grabarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// Engine is this pod's engine identity, "<client>-<ordinal>". §6.3
	// computes it from the StatefulSet ordinal and writes it to
	// Download.status.engine and to the matching label, so an engine can
	// select exactly its own work.
	Engine string

	// DataDir is the RWX media volume.
	DataDir string

	// ScratchDir is the usenet engine's working area.
	ScratchDir string

	// EngineImage is the container image the DownloadClient controller
	// stamps onto the engine StatefulSet/Deployment it creates
	// (CLUSTARR_ENGINE_IMAGE; config/manager/grabarr.yaml sets it on the
	// controller Deployment). Only meaningful for [RoleController]. There is
	// no default -- guessing an image tag would silently run the wrong
	// engine, so [Options.Validate] requires it whenever this role reconciles
	// controllers.
	EngineImage string

	// DataClaimName is the RWX PersistentVolumeClaim the DownloadClient
	// controller mounts at DataDir in every engine workload it creates
	// (--data-claim): the same claim this Deployment mounts. Only meaningful
	// for [RoleController].
	//
	// It is a flag, not the constant it used to be, because the two
	// installers name the claim differently: config/ ships "clustarr-data",
	// which is [downloadclient.DefaultDataClaimName], while the chart names
	// it "<release fullname>-data". Hard-coded, every engine pod under any
	// release name other than "clustarr" mounted a PVC that does not exist
	// and sat in ContainerCreating forever -- the same bug plan task E-4
	// fixed for squasharr's transcode Jobs. The chart sets it through
	// $CLUSTARR_DATA_CLAIM.
	DataClaimName string

	// EngineServiceAccount is the ServiceAccount every engine pod the
	// DownloadClient controller creates runs as (--engine-service-account).
	// Only meaningful for [RoleController]. Like DataClaimName it is a flag
	// because the installers name it differently: config/ creates
	// [downloadclient.DefaultEngineServiceAccount], the chart
	// "<release fullname>-grabarr-engine" ($CLUSTARR_ENGINE_SERVICE_ACCOUNT).
	// Both bind it to the engine's generated ClusterRole
	// (config/rbac/grabarr_engine_role.yaml). Until X14 the pod named none
	// and ran as the namespace's unbound "default" account.
	EngineServiceAccount string

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
		Options:              k8s.DefaultOptions(),
		Role:                 RoleController,
		DataDir:              DefaultDataDir,
		ScratchDir:           DefaultScratchDir,
		DataClaimName:        downloadclient.DefaultDataClaimName,
		EngineServiceAccount: downloadclient.DefaultEngineServiceAccount,
	}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf("grabarr: unknown --role %q, want one of %v", o.Role, Roles())
	}
	if o.Role.IsEngine() && o.Engine == "" {
		return fmt.Errorf("grabarr: --engine is required for --role %s; "+
			"it is the <client>-<ordinal> identity the engine selects its Downloads by", o.Role)
	}
	if !o.Role.IsEngine() && o.Engine != "" {
		return fmt.Errorf("grabarr: --engine is only meaningful for an engine role, not %s", o.Role)
	}
	if o.DataDir == "" {
		return fmt.Errorf("grabarr: --data-dir is required")
	}
	if o.Role == RoleUsenetEngine && o.ScratchDir == "" {
		return fmt.Errorf("grabarr: --scratch-dir is required for --role %s", o.Role)
	}
	if o.Role.RunsControllers() && o.EngineImage == "" {
		return fmt.Errorf("grabarr: --engine-image is required for --role %s; "+
			"it is the image the DownloadClient controller stamps onto the engine "+
			"workloads it creates", o.Role)
	}
	if o.Role.RunsControllers() && o.DataClaimName == "" {
		return fmt.Errorf("grabarr: --data-claim is required for --role %s; "+
			"it is the PersistentVolumeClaim every engine workload mounts at --data-dir", o.Role)
	}
	if o.Role.RunsControllers() && o.EngineServiceAccount == "" {
		return fmt.Errorf("grabarr: --engine-service-account is required for --role %s; "+
			"it is the ServiceAccount every engine pod runs as", o.Role)
	}
	if !o.UsesBus() {
		return fmt.Errorf("grabarr: --nats-url is required; download events and progress both use the bus")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// Engines never take the lease: §3 runs one per StatefulSet ordinal and each
// owns a disjoint set of Downloads, so there is nothing to elect.
//
// Every role's cache holds only the Secrets labelled
// downloadv1alpha1.LabelWatch. The DownloadClient controller watches those
// to restart a usenet engine when its provider credentials rotate, and reads
// every Secret it needs by name through the uncached API reader instead, so
// neither it nor anything else in grabarr ever caches the cluster's other
// Secrets. The engines read theirs through a direct client before the
// manager starts and never through this cache.
func (o Options) ManagerOptions() ctrl.Options {
	opts := o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
	if opts.Cache.ByObject == nil {
		opts.Cache.ByObject = map[client.Object]cache.ByObject{}
	}
	opts.Cache.ByObject[&corev1.Secret{}] = cache.ByObject{
		Label: labels.SelectorFromSet(labels.Set{downloadv1alpha1.LabelWatch: downloadv1alpha1.LabelWatchValue}),
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
		return fmt.Errorf("grabarr: %w", err)
	}
	defer shutdown()

	log := ctrl.LoggerFrom(ctx).WithName(ServiceName)
	k8s.RegisterRESTClientMetrics()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("grabarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("grabarr: build manager: %w", err)
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

	ready := map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
	}

	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return err
	}
	ready["cache"] = cacheReady

	// An engine role must build its embedded download.Client and, for
	// torrent, run [torrent.Engine.ReAttach] to completion -- both
	// synchronously, before mgr.Start returns control -- and gate readyz on
	// it (R4, grabarr/run.go's own long-standing TODO, closed here):
	// reporting ready before re-attach completes lets the Download
	// controller hand this engine work it would then double-download. The
	// usenet client re-attaches inside usenet.BuildClient itself
	// (pkg/download/usenet.New's own doc comment), so by the time
	// setupEngine returns there is nothing left to gate -- only the torrent
	// engine needs an explicit readyz checker.
	var engineClose func() error
	if o.Role.IsEngine() {
		checker, closeFn, err := setupEngine(ctx, mgr, bus, o)
		if err != nil {
			return err
		}
		engineClose = closeFn
		if checker != nil {
			ready["reattach"] = checker
		}
	}
	if engineClose != nil {
		defer func() {
			if cerr := engineClose(); cerr != nil {
				log.Error(cerr, "closing the embedded download client")
			}
		}()
	}

	if err := k8s.AddProbes(mgr, ready); err != nil {
		return err
	}

	if o.Role.RunsControllers() {
		if err := setupControllers(mgr, bus, o); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "engine", o.Engine, "dataDir", o.DataDir)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("grabarr: manager: %w", err)
	}
	return nil
}

// setupControllers registers the DownloadClient and Download reconcilers,
// plus the blocklist sweeper (§6.3, §16 M3; plan tasks D2-3, D2-4, D2-8a).
func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	dcReconciler := downloadclient.NewReconciler(
		mgr.GetClient(), mgr.GetEventRecorder("downloadclient"), o.DataDir, o.ScratchDir, o.EngineImage,
	)
	// NewReconciler defaults the claim to config/'s "clustarr-data"; the
	// chart's is "<release fullname>-data", so the flag must win or every
	// engine under any other release name mounts a claim that does not exist.
	dcReconciler.DataClaimName = o.DataClaimName
	dcReconciler.Engine = engineRuntime(o)
	// By name, uncached: the manager's Secret cache holds only labelled ones.
	dcReconciler.SecretReader = mgr.GetAPIReader()
	if err := dcReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("grabarr: downloadclient: %w", err)
	}
	if err := downloadclient.NewBlocklistSweeper(
		mgr.GetClient(), mgr.GetEventRecorder("downloadclient-blocklist"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("grabarr: downloadclient blocklist sweeper: %w", err)
	}

	dlReconciler := download.NewReconciler(mgr.GetClient(), mgr.GetEventRecorder("download"), o.DataDir)
	// download.Reconciler.Bus is events.Publisher, not the full events.Bus:
	// see its doc comment -- NewReconciler leaves it nil for callers that
	// exercise only Phase=Assigned, but a real deployment must wire a real
	// bus or a completed Download is never imported.
	dlReconciler.Bus = bus
	if err := dlReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("grabarr: download: %w", err)
	}
	return nil
}

// engineRuntime is what the DownloadClient controller stamps onto every
// engine pod from this process (downloadclient.EngineRuntime): the engine
// ServiceAccount, this controller's own bus address and single-node
// setting -- the engine joins the same JetStream the controller does -- and
// its $UMASK (design §11), the same pass-through squasharr gives its Jobs.
func engineRuntime(o Options) downloadclient.EngineRuntime {
	return downloadclient.EngineRuntime{
		ServiceAccountName: o.EngineServiceAccount,
		NATSURL:            o.NATSURL,
		BusSingleNode:      o.BusSingleNode,
		Umask:              os.Getenv("UMASK"),
	}
}

// setupEngine is the registration point for the transfer engines: it builds
// this replica's embedded download.Client, registers the engine's Download
// reconciler, its orphan [torrent.Reaper]/[usenet.Reaper] -- a
// manager.Runnable that NOTHING else registers (plan task D2-8b, commit
// d5c01d2) -- and its [engine.ProgressPublisher], and returns the readyz
// checker and shutdown callback, if any, for [Run] to wire in. (§6.3, §16 M3; plan tasks D2-5, D2-6, D2-8)
func setupEngine(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (healthz.Checker, func() error, error) {
	clientName, ok := splitEngineIdentity(o.Engine)
	if !ok {
		return nil, nil, fmt.Errorf(
			"grabarr: --engine %q is not \"<client>-<ordinal>\"", o.Engine)
	}

	switch o.Role {
	case RoleTorrentEngine:
		return setupTorrentEngine(ctx, mgr, bus, o, clientName)
	case RoleUsenetEngine:
		return setupUsenetEngine(ctx, mgr, bus, o, clientName)
	default:
		return nil, nil, fmt.Errorf("grabarr: %s is not an engine role", o.Role)
	}
}

// splitEngineIdentity splits "<client>-<ordinal>" (grabarr.Options.Engine's
// documented shape) into the DownloadClient name at the LAST hyphen, mirroring
// grabarr/controller/downloadclient/workload.go's own encoding
// ("${HOSTNAME##*-}" strips everything but the ordinal) -- the client name
// itself may contain hyphens, so only the last separator is meaningful.
// directClient builds a client.Client that talks to the apiserver directly,
// bypassing the manager's informer cache entirely. Both engine setup
// functions need one for their one-off pre-mgr.Start reads: mgr.GetClient()
// is cache-backed and, proven empirically (grabarr's own envtest wiring
// case, cmd/clustarr/start_envtest_test.go), does NOT lazily start the one
// informer such a Get would need -- it fails outright with "the cache is not
// started, can not read objects". mgr.GetAPIReader() would work for a plain
// Get, but usenet.BuildClient also needs the full client.Client shape (it
// reads Secrets through the same client), so this builds one from the
// manager's own Config/Scheme/RESTMapper rather than mixing reader types.
func directClient(mgr ctrl.Manager) (client.Client, error) {
	c, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
	if err != nil {
		return nil, fmt.Errorf("grabarr: build direct apiserver client: %w", err)
	}
	return c, nil
}

func splitEngineIdentity(engine string) (clientName string, ok bool) {
	i := strings.LastIndex(engine, "-")
	if i <= 0 || i == len(engine)-1 {
		return "", false
	}
	return engine[:i], true
}

// setupTorrentEngine builds the embedded anacrolix client from the named
// DownloadClient's spec.torrent, re-attaches it synchronously (R4), and
// registers [torrent.Reconciler] and [torrent.Reaper].
func setupTorrentEngine(
	ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options, clientName string,
) (healthz.Checker, func() error, error) {
	// mgr.GetAPIReader(), not mgr.GetClient(): this runs before mgr.Start,
	// and the cache-backed client's own error is unambiguous about why --
	// "the cache is not started, can not read objects" -- it does not
	// lazily start the one informer it needs the way some controller-runtime
	// callers assume. GetAPIReader talks to the apiserver directly and needs
	// no cache at all.
	var dc downloadv1alpha1.DownloadClient
	if err := mgr.GetAPIReader().Get(ctx, types.NamespacedName{Namespace: o.Namespace, Name: clientName}, &dc); err != nil {
		return nil, nil, fmt.Errorf("grabarr: get DownloadClient %s/%s: %w", o.Namespace, clientName, err)
	}
	if dc.Spec.Protocol != commonv1alpha1.ProtocolTorrent || dc.Spec.Torrent == nil {
		return nil, nil, fmt.Errorf(
			"grabarr: DownloadClient %s/%s is not a torrent client", o.Namespace, clientName)
	}

	// §6.3 / grabarr.DefaultDataDir's own doc comment: torrent content lives
	// under <DataDir>/torrents/<category>/<download>, persisted re-attach
	// state under <DataDir>/torrents/.state.
	torrentDataDir := filepath.Join(o.DataDir, "torrents")
	stateDir := filepath.Join(torrentDataDir, ".state")

	enableDHT := dc.Spec.Torrent.EnableDHT == nil || *dc.Spec.Torrent.EnableDHT
	rawClient, err := dltorrent.New(dltorrent.Config{
		DataDir:      torrentDataDir,
		ListenPort:   int(dc.Spec.Torrent.ListenPort),
		NoDHT:        !enableDHT,
		StallTimeout: torrent.StallTimeout(dc.Spec.Torrent),
		// Seed keeps a completed torrent uploading once finished; a
		// DownloadClient exists to run the engine spec.seedCriteria (via
		// Download.spec.seedCriteria/DownloadClientSpec.Torrent.Seed)
		// governs, so the client-wide switch is always on.
		Seed:   true,
		Logger: logging.FromContext(ctx),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("grabarr: build torrent client: %w", err)
	}

	e := &torrent.Engine{Client: rawClient, StateDir: stateDir}
	if _, err := e.ReAttach(ctx); err != nil {
		_ = rawClient.Close()
		return nil, nil, fmt.Errorf("grabarr: torrent engine re-attach: %w", err)
	}

	r := &torrent.Reconciler{
		Client:   mgr.GetClient(),
		Engine:   e,
		EngineID: o.Engine,
		StateDir: stateDir,
		Resolver: torrent.NewBusIndexerResolver(bus),
		// Uncached: the Episodes a pack targets are read once per Download,
		// at its first Add, for file selection -- not worth an Episode
		// informer in every engine pod.
		EpisodeReader: mgr.GetAPIReader(),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("grabarr: torrent-engine reconciler: %w", err)
	}

	// [torrent.Reaper] needs mgr.GetCache() for its cache-sync gate (its own
	// doc comment: "D2-8's wiring must set this to mgr.GetCache(), or this
	// guard does nothing") and NeedLeaderElection()==false already makes it
	// run on every replica -- mgr.Add is enough, no k8s.EveryReplica wrapper
	// needed, unlike a bare func.
	reaper := &torrent.Reaper{
		Client:   mgr.GetClient(),
		Engine:   e,
		EngineID: o.Engine,
		Cache:    mgr.GetCache(),
	}
	if err := mgr.Add(reaper); err != nil {
		return nil, nil, fmt.Errorf("grabarr: torrent reaper: %w", err)
	}

	// Design spec §5's 1 Hz telemetry into clustarr-progress, gated on
	// re-attach like everything else that reads the client.
	if err := mgr.Add(&engine.ProgressPublisher{
		Client:   mgr.GetClient(),
		Download: rawClient,
		EngineID: o.Engine,
		KV:       bus.KV(events.BucketProgress),
		Cache:    mgr.GetCache(),
		Ready:    e.Ready,
	}); err != nil {
		return nil, nil, fmt.Errorf("grabarr: torrent progress publisher: %w", err)
	}

	return e.HealthzCheck, rawClient.Close, nil
}

// setupUsenetEngine builds the embedded pkg/download/usenet client -- which
// re-attaches synchronously inside usenet.BuildClient (R4 is satisfied by
// construction; see doc.go) -- and registers [usenet.Reconciler] and
// [usenet.Reaper].
func setupUsenetEngine(
	ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options, clientName string,
) (healthz.Checker, func() error, error) {
	// usenet.BuildClient Gets the DownloadClient (and every provider's
	// Secret) before mgr.Start, so it needs a client that talks to the
	// apiserver directly rather than mgr.GetClient()'s cache-backed one --
	// see [directClient]'s doc comment.
	direct, err := directClient(mgr)
	if err != nil {
		return nil, nil, err
	}
	cl, dc, err := usenet.BuildClient(ctx, direct, o.Namespace, clientName, o.DataDir, o.ScratchDir)
	if err != nil {
		return nil, nil, fmt.Errorf("grabarr: build usenet client: %w", err)
	}

	r := &usenet.Reconciler{
		Client:     mgr.GetClient(),
		Download:   cl,
		Resolver:   &usenet.Resolver{RPC: bus},
		Recorder:   mgr.GetEventRecorder("usenet-engine"),
		Engine:     o.Engine,
		Categories: dc.Spec.Categories,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return nil, nil, fmt.Errorf("grabarr: usenet-engine reconciler: %w", err)
	}

	reaper := &usenet.Reaper{
		Client:   mgr.GetClient(),
		Download: cl,
		Engine:   o.Engine,
		Cache:    mgr.GetCache(),
	}
	if err := mgr.Add(reaper); err != nil {
		return nil, nil, fmt.Errorf("grabarr: usenet reaper: %w", err)
	}

	// Design spec §5's 1 Hz telemetry into clustarr-progress. BuildClient has
	// already re-attached, so there is no readiness gate to wait for.
	if err := mgr.Add(&engine.ProgressPublisher{
		Client:   mgr.GetClient(),
		Download: cl,
		EngineID: o.Engine,
		KV:       bus.KV(events.BucketProgress),
		Cache:    mgr.GetCache(),
	}); err != nil {
		return nil, nil, fmt.Errorf("grabarr: usenet progress publisher: %w", err)
	}

	return nil, cl.Close, nil
}
