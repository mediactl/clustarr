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
// volume: `importarr` runs the leader-elected controllers, and
// `importarr-worker` runs the work.importarr.scan, work.importarr.list and
// work.importarr.fileimport consumers on every replica. `--role all` runs
// both in one process, for kind and for development.
package importarr

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
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
	// RoleController runs the import-list, import-exclusion, library-scan
	// and root-folder schedule controllers. Leader-elected.
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

// RunsWorkers reports whether this role consumes queue work, and therefore
// whether it needs a writable /data.
func (r Role) RunsWorkers() bool { return r.Has(RoleWorker) || r.Has(RoleAll) }

// Options is everything `clustarr importarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// DataPath is the RWX volume amendment §A1.6 mounts on both
	// Deployments. Empty means [DefaultDataPath].
	DataPath string

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
		Options:  k8s.DefaultOptions(),
		Role:     RoleController,
		DataPath: DefaultDataPath,
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
func (o Options) ManagerOptions() ctrl.Options {
	return o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
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
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("importarr: build manager: %w", err)
	}

	bus, nc, err := k8s.ConnectBus(o.NATSURL, ServiceName)
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
	if o.Role.RunsWorkers() {
		// A scan or import worker that cannot write the library must not
		// accept work (amendment §A1.6).
		ready["data"] = DataReadyChecker(o.dataPath())
	}
	if err := k8s.AddProbes(mgr, ready); err != nil {
		return err
	}

	if o.Role.RunsControllers() {
		if err := setupControllers(mgr, o); err != nil {
			return err
		}
	}
	if o.Role.RunsWorkers() {
		if err := setupWorkers(mgr, bus, o); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "leaderElection", o.ManagerOptions().LeaderElection)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("importarr: manager: %w", err)
	}
	return nil
}

// DataReadyChecker reports whether path is a writable directory. It is the
// readiness gate for [RoleWorker]: /data is a RWX volume mounted from the
// cluster, and on a dev box or a misconfigured Deployment it may simply not
// exist, which must fail readiness with a clear message rather than panic
// the process.
func DataReadyChecker(path string) healthz.Checker {
	return func(_ *http.Request) error {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("data path %s: %w", path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("data path %s is not a directory", path)
		}
		probe, err := os.CreateTemp(path, ".importarr-ready-*")
		if err != nil {
			return fmt.Errorf("data path %s is not writable: %w", path, err)
		}
		name := probe.Name()
		if cerr := probe.Close(); cerr != nil {
			_ = os.Remove(name)
			return fmt.Errorf("data path %s: closing probe file: %w", path, cerr)
		}
		if err := os.Remove(name); err != nil {
			return fmt.Errorf("data path %s: removing probe file %s: %w", path, filepath.Base(name), err)
		}
		return nil
	}
}

// setupControllers is the registration point for the import-list,
// import-exclusion, library-scan and root-folder reconcilers. It registers
// nothing yet: M0 ships the CRDs, this binary and pkg/k8s, and the
// reconcilers land in the milestones below.
//
// TODO(M1): ImportList, ImportExclusion, LibraryScan, RootFolder schedule
// controllers. (amendment §A1.2, §A1.3; §6.1, §16 M1)
// TODO(M3): the importer -- Watches Downloads, Completed/Seeding/Failed via
// k8s.StatusFieldIn, and the only cross-group status write in the project
// (Download.status.import). Moved here from catalogarr by amendment §A1.2;
// it is the reason MediaFile has a split writer, with importarr owning
// status.file and status.probe (what it observed) and catalogarr owning
// status.quality and status.formatScore (what it decided).
// (§6.1, §10, §16 M3)
func setupControllers(mgr ctrl.Manager, o Options) error {
	_, _ = mgr, o
	return nil
}

// setupWorkers is the registration point for the work.importarr.scan,
// work.importarr.list and work.importarr.fileimport consumers (amendment
// §A1.6). It registers nothing yet.
//
// TODO(M1): the scan and list consumers (work.importarr.scan,
// work.importarr.list), chunked by directory so a large library scan is not
// one multi-hour unit of work. The scanner never guesses: an unattributable
// file goes to LibraryScan.status.unmatched with the reason, never a
// speculative item. (amendment §A1.3, §A1.6; §16 M1)
// TODO(M3): the fileimport consumer (work.importarr.fileimport), the
// CompletedDownloadService port, and the Download.status.import write it
// performs. Moved here from catalogarr by amendment §A1.2. (§16 M3)
// TODO(M6): the import-list consumer's non-video sources, alongside the
// non-video inventory kinds. (§16 M6)
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	_, _, _ = mgr, bus, o
	return nil
}
