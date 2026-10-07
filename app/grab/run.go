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
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	torrentagent "github.com/mediactl/clustarr/app/grab/agent/torrent"
	usenetagent "github.com/mediactl/clustarr/app/grab/agent/usenet"
	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	grabmanager "github.com/mediactl/clustarr/app/grab/manager"
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

	// PublishDir is where the usenet engine publishes finished content, as
	// <PublishDir>/<category>/<name>; "" means DataDir. The DownloadClient
	// controller passes spec.usenet.publishDir here.
	PublishDir string

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
// No grabarr role caches Secrets. The DownloadClient controller reads a
// usenet engine's provider Secrets by name, with get alone, and the engines
// read theirs through a direct client before the manager starts; a cached
// read would start an informer, which needs list and watch on every Secret
// in scope. DisableFor makes even an accidental read through the manager's
// client a plain get, as indexarr's does.
func (o Options) ManagerOptions() ctrl.Options {
	opts := o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
	opts.Client.Cache = &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}}}
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
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(o.ManagerOptions(), ctx))
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

	var ready k8s.Checks
	if err := ready.Add("jetstream", k8s.BusReadyChecker(nc, bus)); err != nil {
		return err
	}

	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return err
	}
	if err := ready.Add("cache", cacheReady); err != nil {
		return err
	}

	// An engine role must build its embedded download.Client and, for
	// torrent, run [torrent.Engine.ReAttach] to completion -- both
	// synchronously, before mgr.Start returns control -- and gate readyz on
	// it (R4, app/grab/run.go's own long-standing TODO, closed here):
	// reporting ready before re-attach completes lets the Download
	// controller hand this engine work it would then double-download. The
	// usenet client re-attaches inside usenet.BuildClient itself
	// (pkg/download/usenet.New's own doc comment), so by the time
	// usenetagent.Register returns there is nothing left to gate -- only the
	// torrent engine needs an explicit readyz checker.
	if o.Role.IsEngine() {
		clientName, ok := splitEngineIdentity(o.Engine)
		if !ok {
			return fmt.Errorf("grabarr: --engine %q is not \"<client>-<ordinal>\"", o.Engine)
		}
		var reg catalogagent.Registration
		switch o.Role {
		case RoleTorrentEngine:
			reg, err = torrentagent.Register(ctx, mgr, bus, torrentagent.Options{
				Options: o.Options, Client: clientName, Engine: o.Engine, DataDir: o.DataDir, ScratchDir: o.ScratchDir,
			})
		default:
			reg, err = usenetagent.Register(ctx, mgr, bus, usenetagent.Options{
				Options: o.Options, Client: clientName, Engine: o.Engine,
				DataDir: o.DataDir, ScratchDir: o.ScratchDir, PublishDir: o.PublishDir,
			})
		}
		if err != nil {
			return err
		}
		if reg.Close != nil {
			defer func() {
				if cerr := reg.Close(); cerr != nil {
					log.Error(cerr, "closing the embedded download client")
				}
			}()
		}
		if err := ready.Merge(reg.Ready); err != nil {
			return err
		}
	}
	if err := k8s.AddProbes(mgr, &ready, nil); err != nil {
		return err
	}
	if o.Role.RunsControllers() {
		if err := grabmanager.Register(mgr, bus, managerOptions(o)); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "engine", o.Engine, "dataDir", o.DataDir)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("grabarr: manager: %w", err)
	}
	return nil
}

// managerOptions is the grab manager registration's options from this
// process's: the k8s options and the engine workload settings the
// DownloadClient controller stamps onto every engine.
func managerOptions(o Options) grabmanager.Options {
	return grabmanager.Options{
		Options:              o.Options,
		DataDir:              o.DataDir,
		ScratchDir:           o.ScratchDir,
		EngineImage:          o.EngineImage,
		DataClaimName:        o.DataClaimName,
		EngineServiceAccount: o.EngineServiceAccount,
	}
}

// splitEngineIdentity splits "<client>-<ordinal>" (grabarr.Options.Engine's
// documented shape) into the DownloadClient name at the LAST hyphen, mirroring
// app/grab/controller/downloadclient/workload.go's own encoding
// ("${HOSTNAME##*-}" strips everything but the ordinal) -- the client name
// itself may contain hyphens, so only the last separator is meaningful.
func splitEngineIdentity(engine string) (clientName string, ok bool) {
	i := strings.LastIndex(engine, "-")
	if i <= 0 || i == len(engine)-1 {
		return "", false
	}
	return engine[:i], true
}
