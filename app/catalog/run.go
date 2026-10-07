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

// Package catalogarr is the inventory, metadata gateway and release-decision
// service. It owns catalog.clustarr.io.
//
// Everything that ENTERS the library belongs to importarr, not here:
// amendment §A1.2 and §A1.3 moved the completed-download importer, the
// import lists and the root-folder rescan out of this service, along with
// the cross-group Download.status.import write §10 used to assign it. What
// stays is inventory, metadata and deciding which release to grab.
package catalogarr

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	catalogdomain "github.com/mediactl/clustarr/app/catalog/agent/catalog"
	eventsdomain "github.com/mediactl/clustarr/app/catalog/agent/events"
	metadatadomain "github.com/mediactl/clustarr/app/catalog/agent/metadata"
	catalogmanager "github.com/mediactl/clustarr/app/catalog/manager"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

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
	// RoleController runs the catalog controllers and, since R9, the
	// clustarr.io/replay handler and the artwork orphan reaper
	// (app/catalog/manager). Leader-elected.
	RoleController Role = "controller"

	// RoleWorker runs the catalog and events agent domains (Role.domains):
	// the search, grab and artwork-render consumers, and the rss-matcher,
	// redownload, history and DLQ-projector consumers. Every replica runs
	// them. The import and importlist consumers are importarr's (amendment
	// §A1.2, §A1.3).
	RoleWorker Role = "worker"

	// RoleMetadata is the metadata gateway: it owns every outbound metadata
	// client, their rate limiters and the two cache tiers, which is why §3
	// pins it to exactly one replica.
	RoleMetadata Role = "metadata"

	// RoleHistory is the events domain: the RSS matcher, redownload, the
	// history sink and the DLQ projector.
	RoleHistory Role = "history"

	// RoleArtwork runs the catalog agent domain, whose renderer (spec §C.6),
	// the catalogarr-artwork-render consumer, draws rating badges onto
	// stored posters and writes status.overlay; the domain's search and grab
	// consumers come with it. Not leader-elected; it scales by consumer.
	RoleArtwork Role = "artwork"

	// RoleAll runs everything in one process, for kind and for development.
	RoleAll Role = "all"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role {
	return []Role{RoleController, RoleWorker, RoleMetadata, RoleHistory, RoleArtwork, RoleAll}
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
	return r.Has(RoleWorker) || r.Has(RoleMetadata) || r.Has(RoleHistory) || r.Has(RoleArtwork) || r.Has(RoleAll)
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

	// One call stands up the logger, the controller-runtime bridge and
	// the TracerProvider; a failure here is a startup failure.
	ctx, shutdown, err := obs.Bootstrap(ctx, o.Logging, o.Tracing)
	if err != nil {
		return fmt.Errorf("catalogarr: %w", err)
	}
	defer shutdown()

	log := ctrl.LoggerFrom(ctx).WithName(ServiceName)
	k8s.RegisterRESTClientMetrics()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("catalogarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(o.ManagerOptions(), ctx))
	if err != nil {
		return fmt.Errorf("catalogarr: build manager: %w", err)
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
	// §13 names only the JetStream ping. The Kubernetes half matters
	// more here: every catalogarr controller and worker reads through
	// the manager's cache, and an unsynced cache does not fail -- it
	// reports an EMPTY cluster, which reads as "nothing to reconcile"
	// rather than "not ready yet".
	if err := ready.Add("cache", cacheReady); err != nil {
		return err
	}
	if o.Role.RunsControllers() {
		if err := catalogmanager.Register(mgr, bus, catalogmanager.Options{Options: o.Options}); err != nil {
			return err
		}
	}
	if o.Role.RunsWorkers() {
		if err := setupWorkers(ctx, mgr, bus, o, &ready); err != nil {
			return err
		}
	}
	if err := k8s.AddProbes(mgr, &ready, nil); err != nil {
		return err
	}

	log.Info("starting", "role", o.Role, "leaderElection", o.ManagerOptions().LeaderElection)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("catalogarr: manager: %w", err)
	}
	return nil
}

// agentDomain is one catalog agent domain this shim can run (spec §3.5.3).
type agentDomain struct {
	name     string
	register func(context.Context, ctrl.Manager, events.Bus, k8s.Options) (catalogagent.Registration, error)
}

// domains maps --role onto the agent domains it runs. catalogarr's roles do
// not partition the domains: worker is the catalog and events domains
// (search, grab and the renderer; the RSS matcher, redownload, the history
// sink and the DLQ projector), artwork the catalog domain, history the
// events domain, metadata the metadata domain, all every domain. A domain
// two roles name runs once. catalogarr's Deployment runs
// controller,worker,history,artwork -- the catalog and events domains,
// exactly the consumers it ran before.
func (r Role) domains() []agentDomain {
	var out []agentDomain
	if r.Has(RoleWorker) || r.Has(RoleArtwork) || r.Has(RoleAll) {
		out = append(out, agentDomain{"catalog", func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o k8s.Options) (catalogagent.Registration, error) {
			return catalogdomain.Register(ctx, mgr, bus, catalogdomain.Options{Options: o})
		}})
	}
	if r.Has(RoleWorker) || r.Has(RoleHistory) || r.Has(RoleAll) {
		out = append(out, agentDomain{"events", func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o k8s.Options) (catalogagent.Registration, error) {
			return eventsdomain.Register(ctx, mgr, bus, eventsdomain.Options{Options: o})
		}})
	}
	if r.Has(RoleMetadata) || r.Has(RoleAll) {
		out = append(out, agentDomain{"metadata", func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o k8s.Options) (catalogagent.Registration, error) {
			return metadatadomain.Register(ctx, mgr, bus, metadatadomain.Options{Options: o})
		}})
	}
	return out
}

// setupWorkers registers every agent domain o.Role names (the metadata
// gateway is the metadata domain's; the replay handler is the controller
// role's since R9). It then registers the field indexes the domains
// declared, once, and asserts they reached the cache. ready gains each
// domain's checks.
//
// The import and importlist consumers are importarr's
// (work.importarr.fileimport, work.importarr.list -- amendment §A1.6).
func setupWorkers(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options, ready *k8s.Checks) error {
	var indexes []k8s.FieldIndex
	for _, d := range o.Role.domains() {
		reg, err := d.register(ctx, mgr, bus, o.Options)
		if err != nil {
			return fmt.Errorf("catalogarr: %s domain: %w", d.name, err)
		}
		if err := ready.Merge(reg.Ready); err != nil {
			return fmt.Errorf("catalogarr: %s domain: %w", d.name, err)
		}
		indexes = appendNewIndexes(indexes, reg.Indexes)
	}
	if err := catalogagent.RegisterIndexes(ctx, mgr.GetFieldIndexer(), indexes); err != nil {
		return fmt.Errorf("catalogarr: %w", err)
	}
	return catalogagent.AssertIndexes(mgr, indexes)
}

// appendNewIndexes appends each index of more not declared already. The
// catalog and events domains both declare search.IndexDownloadTarget, and a
// process running both registers it once: a second IndexField of one name
// on one kind is an "indexer conflict".
func appendNewIndexes(have, more []k8s.FieldIndex) []k8s.FieldIndex {
	for _, ix := range more {
		if !slices.ContainsFunc(have, func(h k8s.FieldIndex) bool {
			return h.Name == ix.Name && reflect.TypeOf(h.Object) == reflect.TypeOf(ix.Object)
		}) {
			have = append(have, ix)
		}
	}
	return have
}
