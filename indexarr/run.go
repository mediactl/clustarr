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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Service identity, from §2 and §6.2.
const (
	// ServiceName is the subcommand, the field manager and the NATS client
	// name.
	ServiceName = "indexarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultIndexPath is the SQLite release index on the RWO PVC §6.2
	// mounts.
	DefaultIndexPath = "/index/releases.db"

	// DefaultFacadeBindAddress serves the Torznab facade §6.2 describes:
	// /{indexer}/api, /{indexer}/download and the aggregate /search/api.
	DefaultFacadeBindAddress = ":9696"
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
func (o Options) ManagerOptions() ctrl.Options {
	return o.Options.ManagerOptions(LeaderElectionID, false)
}

// Run starts the manager and blocks until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if err := o.Validate(); err != nil {
		return err
	}

	logger := logging.New(o.Logging)
	ctrl.SetLogger(logging.LogrBridge(logger))
	ctx = logging.NewContext(ctx, logger)

	shutdown, err := tracing.Setup(ctx, o.Tracing)
	if err != nil {
		return fmt.Errorf("indexarr: tracing: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

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

	// TODO(M2): add a "releaseindex" readiness check tied to the SQLite
	// handle being open, per §13. Until the store exists the JetStream ping
	// is the whole readiness gate.
	if err := k8s.AddProbes(mgr, map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
	}); err != nil {
		return err
	}

	if err := setupControllers(mgr, o); err != nil {
		return err
	}
	if err := setupWorkers(mgr, bus, o); err != nil {
		return err
	}

	log.Info("starting", "role", o.Role, "indexPath", o.IndexPath)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("indexarr: manager: %w", err)
	}
	return nil
}

// setupControllers is the registration point for the index reconcilers. It
// registers nothing yet.
//
// TODO(M2): indexer -- validate the definition, probe caps, test login, own
// the session Secret, schedule RSS with WithScheduleAt, and mirror KV health
// into status with Prowlarr's escalation table. (§6.2, §16 M2)
// TODO(M6): indexerdefinition (schema validation and sha256) and indexerproxy
// (http/socks and the FlareSolverr client). (§6.2, §16 M6)
func setupControllers(mgr ctrl.Manager, o Options) error {
	_, _ = mgr, o
	return nil
}

// setupWorkers is the registration point for the search RPC responder, the RSS
// worker, the release index and the Torznab facade, all of which run as
// manager Runnables so they stop with the manager.
//
// TODO(M2): serve rpc.indexarr.search and rpc.indexarr.download; run the RSS
// worker publishing new rows to CLUSTARR_RELEASES; open the SQLite FTS5 store
// at o.IndexPath and run its 10-minute expiry sweep. (§6.2, §16 M2)
// TODO(M6): the Cardigann engine and the Torznab facade on
// o.FacadeBindAddress. (§6.2, §16 M6)
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	_, _, _ = mgr, bus, o
	return nil
}
