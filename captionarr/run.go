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

// Package captionarr owns subtitle.clustarr.io: it plans which subtitles a
// MediaFile still wants and fetches them from the configured providers.
//
// §3 runs one controller replica and a fixed pair of fetch workers. The split
// matters because the providers are rate-limited per account, not per pod: the
// workers share a KV token bucket so N of them never exceed the provider's
// limit (§6.5).
package captionarr

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/captionarr/controller/subtitleprofile"
	"github.com/mediactl/clustarr/captionarr/controller/subtitleprovider"
	"github.com/mediactl/clustarr/captionarr/controller/subtitlerequest"
	"github.com/mediactl/clustarr/captionarr/providerset"
	"github.com/mediactl/clustarr/captionarr/worker/fetch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Service identity, from §2 and §6.5.
const (
	// ServiceName is the subcommand, the controller field manager and the
	// NATS client name. Fetch workers apply status under
	// k8s.ManagerCaptionarrWorker instead.
	ServiceName = "captionarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultDataDir is the RWX media volume. Sidecars are written next to
	// their video file as <stem>.<lang>[.forced|.sdh].srt.
	DefaultDataDir = "/data"
)

// Role selects what a replica does.
type Role string

// The roles §6.5 lists for `clustarr captionarr --role`.
const (
	// RoleController runs the SubtitleProfile, SubtitleRequest and
	// SubtitleProvider reconcilers and the adaptive and upgrade cron
	// republishers. Leader-elected.
	RoleController Role = "controller"

	// RoleWorker consumes work.captionarr.fetch.>: it scores and downloads
	// candidates, post-processes them and writes the sidecar.
	RoleWorker Role = "worker"

	// RoleAll runs the controllers and the fetch worker in one process, for
	// `clustarr all`, kind and development -- the shape catalogarr's and
	// importarr's own "all" roles have. Without it `clustarr all` could run
	// only the controller role, which plans and publishes fetch tasks that
	// nothing in the process consumes: it never fetched a subtitle.
	RoleAll Role = "all"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role { return []Role{RoleController, RoleWorker, RoleAll} }

// String returns the flag value.
func (r Role) String() string { return string(r) }

// Valid reports whether r is one of [Roles].
func (r Role) Valid() bool { return r == RoleController || r == RoleWorker || r == RoleAll }

// RunsControllers reports whether this role reconciles custom resources.
func (r Role) RunsControllers() bool { return r == RoleController || r == RoleAll }

// RunsWorkers reports whether this role consumes queue work.
func (r Role) RunsWorkers() bool { return r == RoleWorker || r == RoleAll }

// Options is everything `clustarr captionarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// DataDir is where the RWX media volume is mounted in this process
	// (--data-dir). Every path in a CRD is a logical /data path; both roles
	// map it through this with captionarr/datapath.
	DataDir string

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
		Options: k8s.DefaultOptions(),
		Role:    RoleController,
		DataDir: DefaultDataDir,
	}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf("captionarr: unknown --role %q, want one of %v", o.Role, Roles())
	}
	if o.DataDir == "" {
		return fmt.Errorf("captionarr: --data-dir is required")
	}
	if !o.UsesBus() {
		return fmt.Errorf("captionarr: --nats-url is required; " +
			"the fetch queue and the shared provider throttle both live on the bus")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// Workers never take the lease: §12 runs two of them precisely so fetches
// proceed in parallel, bounded by the shared KV token bucket rather than by
// how many pods are running.
func (o Options) ManagerOptions() ctrl.Options {
	return o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
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
		return fmt.Errorf("captionarr: %w", err)
	}
	defer shutdown()

	log := ctrl.LoggerFrom(ctx).WithName(ServiceName)
	k8s.RegisterRESTClientMetrics()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("captionarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("captionarr: build manager: %w", err)
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

	// Readiness on every replica, leader or not (the cache check is an
	// EveryReplica runnable), and on the caches as well as the bus: every
	// reconciler and the fetch worker read through the manager's cache, and
	// an unsynced cache does not fail -- it reports an empty cluster.
	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return err
	}
	if err := k8s.AddProbes(mgr, map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
		"cache":     cacheReady,
	}); err != nil {
		return err
	}

	if o.Role.RunsControllers() {
		if err := setupControllers(mgr, bus, o); err != nil {
			return err
		}
	}
	if o.Role.RunsWorkers() {
		if err := setupWorkers(mgr, bus, o); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "dataDir", o.DataDir)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("captionarr: manager: %w", err)
	}
	return nil
}

// setupControllers registers captionarr's three reconcilers (§6.5, §16 M5):
//
//   - subtitleprofile validates each SubtitleProfile and ensures one
//     SubtitleRequest per video-kind MediaFile the profile wins, woken by
//     MediaFile.status.probeHash (task F-3);
//   - subtitleprovider validates each SubtitleProvider through
//     captionarr/providerset.Validate and projects the shared
//     clustarr-provider-throttle KV state into its status -- ruling R2 makes
//     it that status's only writer (task F-3);
//   - subtitlerequest plans each request, publishes a fetch task for every
//     language that is due, and runs the adaptive and upgrade cadences
//     (task F-4). It needs the bus -- a nil Bus fails every reconcile that
//     has a task to send -- and --data-dir, through which it reads the media
//     file's directory exactly as the fetch worker does.
//
// Secrets are read through the API reader, never the cache: a cached Get
// would start a cluster-wide Secret informer.
func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	c := mgr.GetClient()
	if err := subtitleprofile.NewReconciler(
		c, mgr.GetScheme(), mgr.GetEventRecorder("subtitleprofile"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("captionarr: subtitleprofile: %w", err)
	}

	provider := subtitleprovider.NewReconciler(c, bus.KV(events.BucketProviderThrottle), mgr.GetEventRecorder("subtitleprovider"))
	provider.Secrets = mgr.GetAPIReader()
	if err := provider.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("captionarr: subtitleprovider: %w", err)
	}

	if err := (&subtitlerequest.Reconciler{
		Client:  c,
		Bus:     bus,
		DataDir: o.DataDir,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("captionarr: subtitlerequest: %w", err)
	}
	return nil
}

// setupWorkers registers the fetch worker (task F-5) on both fetch
// consumers, captionarr-fetch-high and captionarr-fetch-normal. The worker's
// runnables are k8s.EveryReplica: fetch workers are never leader-elected
// (§6.5), so every replica consumes, bounded by the shared KV token bucket
// rather than by how many pods run.
//
// The provider builder lives as long as the process: its client cache is
// what keeps an OpenSubtitles login across fetch tasks, and its KV -- the
// clustarr-provider-throttle bucket -- is what shares that login across
// worker replicas (providerset.TokenCache, throttle.SetAuth). It reads
// Secrets, and the worker re-reads each SubtitleRequest before its status
// apply, through the API reader.
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	providers := providerset.NewBuilder(mgr.GetClient(), mgr.GetAPIReader())
	providers.KV = bus.KV(events.BucketProviderThrottle)
	worker := fetch.NewWorker(mgr.GetClient(), mgr.GetAPIReader(), bus, providers, o.DataDir)
	if err := worker.SetupWithManager(mgr, o.BusTopology()); err != nil {
		return fmt.Errorf("captionarr: fetch worker: %w", err)
	}
	return nil
}
