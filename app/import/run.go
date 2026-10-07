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

// Package importarr owns everything that enters the library: root-folder
// rescan, import lists and completed-download import. It owns no CRD group of
// its own; its controllers and workers read and write catalog.clustarr.io and
// download.clustarr.io resources under their own field manager (amendment
// §A1.6).
//
// §A1.6 splits importarr across two Deployments that share the RWX `/data`
// volume: `importarr` runs the leader-elected controllers -- the rename
// controller among them moves library files, so it mounts /data too -- and
// `importarr-worker` runs the work.importarr.scan, work.importarr.list and
// work.importarr.fileimport consumers on every replica. `--role all` runs
// both in one process, for kind and for development.
package importarr

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	importagent "github.com/mediactl/clustarr/app/import/agent"
	importmanager "github.com/mediactl/clustarr/app/import/manager"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Service identity, from §2 and amendment §A1.6.
const (
	// ServiceName is the subcommand, the field-manager prefix and the NATS
	// client name.
	ServiceName = "importarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultDataPath is the RWX volume both the importarr and
	// importarr-worker Deployments mount (amendment §A1.6).
	DefaultDataPath = "/data"
)

// Role selects what a replica does. Amendment §A1.6 puts the controllers
// (ImportList, ImportExclusion, LibraryScan, RootFolder schedule) on the
// leader-elected `importarr` Deployment and the work.importarr.* consumers on
// every replica of the scalable `importarr-worker` Deployment.
//
// A Role is normally one of the values [Roles] lists, but it may also be a
// comma-separated combination of them, the way `--role all` runs both halves
// in one process for kind and for development.
type Role string

// The roles amendment §A1.6 lists for `clustarr importarr --role`.
const (
	// RoleController runs the import-list, import-exclusion, library-scan,
	// root-folder schedule and rename controllers, and the retrigger
	// controller (app/import/controller/retrigger). Leader-elected. The rename controller moves library files,
	// so this role needs a writable /data too.
	RoleController Role = "controller"

	// RoleWorker runs the scan, list and fileimport consumers. Every
	// replica runs them, and every replica needs a writable /data: a scan
	// or import worker that cannot write the library must not accept work.
	RoleWorker Role = "worker"

	// RoleAll runs everything in one process, for kind and for development.
	RoleAll Role = "all"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role {
	return []Role{RoleController, RoleWorker, RoleAll}
}

// String returns the flag value.
func (r Role) String() string { return string(r) }

// Split returns the individual roles r names, in the order given, with
// surrounding spaces trimmed. Empty elements are kept rather than dropped, so
// a stray comma is a role [Role.Valid] rejects instead of one it silently
// ignores.
func (r Role) Split() []Role {
	parts := strings.Split(string(r), ",")
	out := make([]Role, 0, len(parts))
	for _, part := range parts {
		out = append(out, Role(strings.TrimSpace(part)))
	}
	return out
}

// Valid reports whether every role r names is one of [Roles], with no
// duplicates.
func (r Role) Valid() bool {
	parts := r.Split()
	seen := make(map[Role]bool, len(parts))
	for _, part := range parts {
		if seen[part] || !slices.Contains(Roles(), part) {
			return false
		}
		seen[part] = true
	}
	return true
}

// Has reports whether r names want, either on its own or as part of a
// combination.
func (r Role) Has(want Role) bool { return slices.Contains(r.Split(), want) }

// RunsControllers reports whether this role reconciles custom resources, and
// therefore whether it should take the leader lease.
func (r Role) RunsControllers() bool { return r.Has(RoleController) || r.Has(RoleAll) }

// RunsWorkers reports whether this role consumes queue work.
func (r Role) RunsWorkers() bool { return r.Has(RoleWorker) || r.Has(RoleAll) }

// Options is everything `clustarr importarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// DataPath is the RWX volume amendment §A1.6 mounts on both
	// Deployments. Empty means [DefaultDataPath].
	DataPath string

	// SampleMaxBytes is the video size floor both the rescan and the
	// completed-download import workers apply (fsops.IsSuspectedSample): a
	// video file smaller than this whose name does not mark it a sample is
	// a SUSPECTED sample, which a rescan lists in LibraryScan.status.unmatched
	// and an import records as a rejection, rather than importing it. Zero
	// disables the size rule.
	//
	// [DefaultOptions] sets fsops.DefaultSampleMaxBytes. A zero-value
	// Options -- a bare struct literal that omits the field -- has the rule
	// OFF, which is the silent failure this field's wiring exists to avoid:
	// every caller that builds Options by hand must pass it explicitly, as
	// cmd/clustarr's --sample-max-bytes does.
	SampleMaxBytes int64

	// TraktBaseURL and PlexBaseURL point the import lists' Trakt and Plex
	// providers somewhere other than their public APIs (--trakt-base-url,
	// --plex-base-url). Empty is each provider's own default
	// (trakt.DefaultBaseURL, Plex Discover). The Trakt one reaches BOTH
	// halves that talk to Trakt -- the ImportList controller's device-code
	// flow and the list worker's watchlist sync and token refresh -- so a
	// device authorization and the syncs it authorizes always name the
	// same host. They exist for the in-cluster fixture config/e2e deploys
	// (a cluster with no egress) and for an operator behind a mirror.
	TraktBaseURL string
	PlexBaseURL  string

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
		Options:        k8s.DefaultOptions(),
		Role:           RoleController,
		DataPath:       DefaultDataPath,
		SampleMaxBytes: fsops.DefaultSampleMaxBytes,
	}
}

// dataPath returns o.DataPath, falling back to [DefaultDataPath] the way a
// zero-value Options would if it were handed to a manager directly.
func (o Options) dataPath() string {
	if strings.TrimSpace(o.DataPath) != "" {
		return o.DataPath
	}
	return DefaultDataPath
}

// managerOptions is what the import manager registration takes from o.
func managerOptions(o Options) importmanager.Options {
	return importmanager.Options{Options: o.Options, TraktBaseURL: o.TraktBaseURL}
}

// agentOptions is what the import domain's registration takes from o: the
// data path with [Options.dataPath]'s fallback, and the sample size floor
// exactly as given, since 0 disables it.
func agentOptions(o Options) importagent.Options {
	return importagent.Options{
		Options:        o.Options,
		DataDir:        o.dataPath(),
		SampleMaxBytes: o.SampleMaxBytes,
		TraktBaseURL:   o.TraktBaseURL,
		PlexBaseURL:    o.PlexBaseURL,
	}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf(
			"importarr: unknown --role %q, want one of %v, or a comma-separated combination of them",
			o.Role, Roles())
	}
	if strings.TrimSpace(o.DataPath) == "" {
		return fmt.Errorf("importarr: --data-path is required")
	}
	if o.SampleMaxBytes < 0 {
		// The workers read a negative threshold as "disabled", exactly as
		// they read zero; a negative flag is a typo, not a request for that.
		return fmt.Errorf("importarr: --sample-max-bytes must be 0 (disabled) or more, got %d", o.SampleMaxBytes)
	}
	if !o.UsesBus() {
		// The controllers hand scan and import work to the bus, and the
		// workers consume it: every role uses the bus.
		return fmt.Errorf("importarr: --nats-url is required; every role uses the bus")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// Leader election is decided here rather than by the flag alone: amendment
// §A1.6 runs the controllers leader-only on `importarr` and the workers on
// every replica of `importarr-worker`, and a worker that waited for the lease
// would simply never start.
//
// The controller role alone also caches MediaFiles without
// status.mediaInfo ([withoutMediaInfo]): the rename controller watches
// every MediaFile, and the library's probe results would otherwise be most
// of what the `importarr` Deployment holds in memory. Nothing it runs reads
// mediaInfo. A role that runs the workers too keeps the whole object, as
// importarr-worker always has.
//
// No role caches Secrets or ConfigMaps, as grabarr's and indexarr's cache no
// Secrets: the role grants get alone on both, and a cached read starts an
// informer, which needs list and watch on every one in scope. The list
// worker reads an ImportList's credentials Secret and CSV ConfigMap by name,
// and through the cached client that read waited forever on an informer the
// role could not fill, so no ImportList ever synced (2026-10-06).
func (o Options) ManagerOptions() ctrl.Options {
	opts := o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
	opts.Client.Cache = &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}}}
	if o.Role.RunsControllers() && !o.Role.RunsWorkers() {
		if opts.Cache.ByObject == nil {
			opts.Cache.ByObject = map[client.Object]cache.ByObject{}
		}
		opts.Cache.ByObject[&catalogv1alpha1.MediaFile{}] = cache.ByObject{
			Transform: withoutMediaInfo(opts.Cache.DefaultTransform),
		}
	}
	return opts
}

// withoutMediaInfo is the controller role's cache transform for MediaFile:
// the manager's DefaultTransform first, then status.mediaInfo dropped. A
// per-object transform replaces DefaultTransform rather than adding to it
// (controller-runtime's cache defaultConfig), so it is chained here, and
// anything pkg/k8s.ManagerOptions adds to the default later still reaches
// MediaFiles. With no default it strips managedFields, as every cache does.
func withoutMediaInfo(next toolscache.TransformFunc) toolscache.TransformFunc {
	if next == nil {
		next = cache.TransformStripManagedFields()
	}
	return func(obj any) (any, error) {
		obj, err := next(obj)
		if err != nil {
			return obj, err
		}
		if mf, ok := obj.(*catalogv1alpha1.MediaFile); ok {
			mf.Status.MediaInfo = nil
		}
		return obj, nil
	}
}

// Run starts the manager and blocks until ctx is cancelled, which is what the
// signal handler does on SIGTERM. It returns nil on a clean shutdown.
func Run(ctx context.Context, o Options) error {
	if err := o.Validate(); err != nil {
		return err
	}

	// One call stands up the logger, the controller-runtime bridge and
	// the TracerProvider; a failure here is a startup failure.
	ctx, shutdown, err := obs.Bootstrap(ctx, o.Logging, o.Tracing)
	if err != nil {
		return fmt.Errorf("importarr: %w", err)
	}
	defer shutdown()

	log := ctrl.LoggerFrom(ctx).WithName(ServiceName)
	k8s.RegisterRESTClientMetrics()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("importarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(o.ManagerOptions(), ctx))
	if err != nil {
		return fmt.Errorf("importarr: build manager: %w", err)
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

	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return err
	}
	var ready k8s.Checks
	if err := ready.Add("jetstream", k8s.BusReadyChecker(nc, bus)); err != nil {
		return err
	}
	// §13 lists only the JetStream ping, but a manager whose informers
	// have not synced serves a cold cache: the scan schedule would see no
	// RootFolders and the exclusion controller no exclusions, both of
	// which read as "nothing to do" rather than as "not ready yet".
	if err := ready.Add("cache", cacheReady); err != nil {
		return err
	}
	if o.Role.RunsControllers() {
		if err := importmanager.Register(mgr, bus, managerOptions(o)); err != nil {
			return fmt.Errorf("importarr: %w", err)
		}
	}
	if o.Role.RunsWorkers() {
		// A scan or import worker that cannot write the library must not
		// accept work (amendment §A1.6): the import domain's import.data
		// check, which also covers the controllers in --role all.
		reg, err := importagent.Register(ctx, mgr, bus, agentOptions(o))
		if err != nil {
			return fmt.Errorf("importarr: %w", err)
		}
		if err := ready.Merge(reg.Ready); err != nil {
			return err
		}
		if err := catalogagent.RegisterIndexes(ctx, mgr.GetFieldIndexer(), reg.Indexes); err != nil {
			return fmt.Errorf("importarr: %w", err)
		}
		if err := catalogagent.AssertIndexes(mgr, reg.Indexes); err != nil {
			return fmt.Errorf("importarr: %w", err)
		}
	} else {
		// The controller role's own /data gate, kept for importarr's
		// controller Deployment until cmd/manager drops it (spec §3.3, OD7).
		// Its rename controller moves library files.
		if err := ready.Add("data", k8s.DataReadyChecker(o.dataPath())); err != nil {
			return err
		}
	}
	if err := k8s.AddProbes(mgr, &ready, nil); err != nil {
		return err
	}

	log.Info("starting", "role", o.Role, "leaderElection", o.ManagerOptions().LeaderElection)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("importarr: manager: %w", err)
	}
	return nil
}
