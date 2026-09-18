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

// Package catalogarr is the inventory, metadata, import-list, decision and
// import service. It owns catalog.clustarr.io and is the only service that
// writes to another group's status (Download.status.import, §10).
package catalogarr

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// tracingShutdownTimeout bounds how long Run waits for the OpenTelemetry
// exporter to flush and close on the way out. It is not tied to
// GracefulShutdownTimeout: that governs the controller-runtime manager's own
// drain, which has already completed by the time this runs.
const tracingShutdownTimeout = 5 * time.Second

// Service identity, from §2 and §6.1.
const (
	// ServiceName is the subcommand, the field-manager prefix and the NATS
	// client name.
	ServiceName = "catalogarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"
)

// Role selects what a replica does. §3's topology runs the controllers under a
// leader lease and the queue workers on every replica, so one Deployment can
// scale its workers without ever running two active controller sets.
//
// A Role is normally one of the values §6.1 lists, but it may also be a
// comma-separated combination of them. §3's topology table puts the
// controllers, the queue workers and the history sink in ONE `catalogarr`
// Deployment while pinning the metadata gateway to its own single-replica
// `catalogarr-metadata` Deployment, and "all" cannot express that: it would
// start a second metadata gateway whose in-process rate limiters then double
// Clustarr's outbound request rate. The manifests therefore run the first
// Deployment as --role controller,worker,history.
type Role string

// The roles §6.1 lists for `clustarr catalogarr --role`.
const (
	// RoleController runs the catalog controllers. Leader-elected.
	RoleController Role = "controller"

	// RoleWorker runs the search, grab, import, importlist and rss-matcher
	// consumers. Every replica runs them.
	RoleWorker Role = "worker"

	// RoleMetadata is the metadata gateway: it owns every outbound metadata
	// client, their rate limiters and the two cache tiers, which is why §3
	// pins it to exactly one replica.
	RoleMetadata Role = "metadata"

	// RoleHistory is the history sink and DLQ projector: EVENTS into
	// events.k8s.io Events on the owning CR.
	RoleHistory Role = "history"

	// RoleAll runs everything in one process, for kind and for development.
	RoleAll Role = "all"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role {
	return []Role{RoleController, RoleWorker, RoleMetadata, RoleHistory, RoleAll}
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
func (r Role) RunsWorkers() bool {
	return r.Has(RoleWorker) || r.Has(RoleMetadata) || r.Has(RoleHistory) || r.Has(RoleAll)
}

// Options is everything `clustarr catalogarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

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
	return Options{Options: k8s.DefaultOptions(), Role: RoleController}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf(
			"catalogarr: unknown --role %q, want one of %v, or a comma-separated combination of them",
			o.Role, Roles())
	}
	if !o.UsesBus() {
		// Every catalogarr role either publishes or consumes queue work:
		// even the controllers hand searches and imports to the bus.
		return fmt.Errorf("catalogarr: --nats-url is required; every role uses the bus")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// Leader election is decided here rather than by the flag alone: §3 runs the
// controllers leader-only and the workers on every replica, and a worker that
// waited for the lease would simply never start.
func (o Options) ManagerOptions() ctrl.Options {
	return o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
}

// Run starts the manager and blocks until ctx is cancelled, which is what the
// signal handler does on SIGTERM. It returns nil on a clean shutdown.
func Run(ctx context.Context, o Options) error {
	if err := o.Validate(); err != nil {
		return err
	}

	logger := logging.New(o.Logging)
	ctrl.SetLogger(logging.LogrBridge(logger))
	ctx = logging.NewContext(ctx, logger)

	shutdown, err := tracing.Setup(ctx, o.Tracing)
	if err != nil {
		return fmt.Errorf("catalogarr: tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), tracingShutdownTimeout)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			logging.FromContext(ctx).Warn("tracing shutdown", "err", err)
		}
	}()

	log := ctrl.LoggerFrom(ctx).WithName(ServiceName)
	k8s.RegisterRESTClientMetrics()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("catalogarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("catalogarr: build manager: %w", err)
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

	if err := k8s.AddProbes(mgr, map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
	}); err != nil {
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
		return fmt.Errorf("catalogarr: manager: %w", err)
	}
	return nil
}

// setupControllers is the registration point for every catalog reconciler.
// It registers nothing yet: M0 ships the CRDs, this binary and pkg/k8s, and
// the reconcilers land in the milestones below.
//
// TODO(M1): movie, series, episode, mediafile, rootfolder, qualityprofile,
// delayprofile, metadataprovider, importexclusion, search. (§6.1, §16 M1)
// TODO(M3): importer -- Watches Downloads, Completed/Seeding/Failed via
// k8s.StatusFieldIn, and the only cross-group status write in the project.
// (§6.1, §10, §16 M3)
// TODO(M6): artist, album, author, book, audiobook, comic, issue, importlist.
// (§6.1, §16 M6)
//
// wantedcron (the 12h missing/cutoff-unmet scan) lands with M1 as a manager
// Runnable rather than a reconciler.
func setupControllers(mgr ctrl.Manager, o Options) error {
	_, _ = mgr, o
	return nil
}

// setupWorkers is the registration point for the queue consumers, the metadata
// gateway and the history sink.
//
// TODO(M1): search, grab and rss-matcher consumers with the KV grab lease and
// delay profiles; the metadata gateway (RoleMetadata) serving
// rpc.catalogarr.metadata.* with per-provider rate limiters and the otter/KV
// cache tiers. (§6.1, §16 M1)
// TODO(M3): the import consumer (CompletedDownloadService port). (§16 M3)
// TODO(M6): the importlist consumer, and the history sink plus DLQ projector
// (RoleHistory) writing events.k8s.io Events on the owning CR. (§13, §16 M6)
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	_, _, _ = mgr, bus, o
	return nil
}
