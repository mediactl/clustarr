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
	"slices"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/delayprofile"
	"github.com/mediactl/clustarr/catalogarr/controller/episode"
	"github.com/mediactl/clustarr/catalogarr/controller/mediafile"
	"github.com/mediactl/clustarr/catalogarr/controller/metadataprovider"
	"github.com/mediactl/clustarr/catalogarr/controller/movie"
	"github.com/mediactl/clustarr/catalogarr/controller/qualityprofile"
	"github.com/mediactl/clustarr/catalogarr/controller/rootfolder"
	searchctl "github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/catalogarr/controller/series"
	"github.com/mediactl/clustarr/catalogarr/controller/wantedcron"
	catalogmetadata "github.com/mediactl/clustarr/catalogarr/metadata"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
	"github.com/mediactl/clustarr/catalogarr/worker/rssmatcher"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
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
	// RoleController runs the catalog controllers. Leader-elected.
	RoleController Role = "controller"

	// RoleWorker runs the search, grab and rss-matcher consumers. Every
	// replica runs them. The import and importlist consumers are
	// importarr's (amendment §A1.2, §A1.3).
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
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
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
	if err := k8s.AddProbes(mgr, map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
		// §13 names only the JetStream ping. The Kubernetes half matters
		// more here: every catalogarr controller and worker reads through
		// the manager's cache, and an unsynced cache does not fail -- it
		// reports an EMPTY cluster, which reads as "nothing to reconcile"
		// rather than "not ready yet".
		"cache": cacheReady,
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

	log.Info("starting", "role", o.Role, "leaderElection", o.ManagerOptions().LeaderElection)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("catalogarr: manager: %w", err)
	}
	return nil
}

// setupControllers registers every catalog reconciler (§6.1, §16 M1), plus
// the wantedcron sweep, which is a manager.Runnable rather than a reconciler
// because it reconciles nothing -- only a clock.
//
// TODO(M6): artist, album, author, book, audiobook, comic, issue.
// (§6.1, §16 M6)
//
// The importer, importlist and importexclusion controllers are NOT here:
// amendment §A1.2/§A1.3 moved them to importarr, whose run.go registers them.
// Building them here would break the MediaFile single-writer split (importarr
// owns what it observed, catalogarr owns what it decided).
//
// Two recorder conventions coexist in this tree and both are wired here.
//
// The reconcilers written in wave 1 (movie, series, episode, mediafile,
// search, and importarr's rootfolderschedule) take a
// k8s.io/client-go/tools/record.EventRecorder, which is what the DEPRECATED
// mgr.GetEventRecorderFor returns and which writes CORE/v1 Events. The
// profile and provider reconcilers take a k8s.io/client-go/tools/events
// EventRecorder, which mgr.GetEventRecorder returns and which writes
// events.k8s.io/v1 -- the API §13 actually asks for. Their RBAC markers
// differ to match, and the generated Role grants both groups.
//
// Until Task C12a the wave 1 controllers declared events.k8s.io while
// writing core/v1, so the generated Role granted a group nobody wrote and
// omitted the one they did: on a real cluster every one of their events would
// have been denied, and no envtest could see it because envtest does not
// enforce RBAC. The markers are now honest. Migrating those six onto
// mgr.GetEventRecorder -- which would retire the deprecated call below and
// leave one convention -- is follow-up work: three of the six are in packages
// other tasks held open while this one ran.
func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	c := mgr.GetClient()
	scheme := mgr.GetScheme()

	if err := (&movie.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorderFor("movie"), //nolint:staticcheck // record.EventRecorder; see the note above
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: movie: %w", err)
	}

	if err := (&series.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorderFor("series"), //nolint:staticcheck // record.EventRecorder; see the note above
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: series: %w", err)
	}

	if err := (&episode.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorderFor("episode"), //nolint:staticcheck // record.EventRecorder; see the note above
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: episode: %w", err)
	}

	if err := mediafile.NewReconciler(c, scheme,
		mgr.GetEventRecorderFor("mediafile"), //nolint:staticcheck // record.EventRecorder; see the note above
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: mediafile: %w", err)
	}

	if err := rootfolder.NewReconciler(c, mgr.GetEventRecorder("rootfolder")).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: rootfolder: %w", err)
	}

	cat := catalogue.LoadedCatalogue()
	if err := qualityprofile.NewReconciler(c, cat,
		mgr.GetEventRecorder("qualityprofile")).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: qualityprofile: %w", err)
	}

	// The 13 built-in TRaSH profiles. Without this the reconciler has nothing
	// to reconcile on a fresh cluster: every Movie's and Series'
	// spec.qualityProfileRef resolves to "not found", so the grab Sink and the
	// RSS matcher both refuse every release and the whole decision path is
	// inert while looking healthy. Leader-elected by its own
	// NeedLeaderElection -- seeding is a cluster singleton, not per replica.
	if err := mgr.Add(&qualityprofile.Bootstrap{Client: c, Catalogue: cat}); err != nil {
		return fmt.Errorf("catalogarr: qualityprofile bootstrap: %w", err)
	}

	if err := delayprofile.NewReconciler(c, mgr.GetEventRecorder("delayprofile")).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: delayprofile: %w", err)
	}

	if err := metadataprovider.NewReconciler(c, mgr.GetEventRecorder("metadataprovider"),
		defaultHTTPClient).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: metadataprovider: %w", err)
	}

	if err := searchctl.NewReconciler(c, bus,
		mgr.GetEventRecorderFor("search"), //nolint:staticcheck // record.EventRecorder; see the note above
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: search: %w", err)
	}

	// §6.1's twelve-hourly missing/cutoff-unmet sweep. It is leader-elected
	// (see Runnable.NeedLeaderElection), so a sweep fires once per cluster
	// rather than once per replica.
	if err := (&wantedcron.Runnable{
		Client:     c,
		Bus:        bus,
		Schedule:   wantedcron.TwelveHourly(),
		Namespaces: o.WatchNamespaces,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: wantedcron: %w", err)
	}

	return nil
}

// setupWorkers registers the queue consumers and the metadata gateway.
//
// TODO(M6): the history sink plus DLQ projector (RoleHistory) writing
// events.k8s.io Events on the owning CR. (§13, §16 M6)
//
// The import and importlist consumers are importarr's
// (work.importarr.fileimport, work.importarr.list -- amendment §A1.6).
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if o.Role.Has(RoleWorker) || o.Role.Has(RoleAll) {
		if err := setupQueueWorkers(mgr, bus); err != nil {
			return err
		}
	}
	if o.Role.Has(RoleMetadata) || o.Role.Has(RoleAll) {
		if err := setupMetadataGateway(mgr, bus); err != nil {
			return err
		}
	}
	return nil
}

// setupQueueWorkers registers the search, grab and rss-matcher consumers.
//
// Order is load-bearing and is the reason the indexes are registered here
// rather than by whichever worker happens to want them first: the RSS matcher
// reads the blocklist and the queue through catalogarr/worker/search's three
// Download indexes, and when they are missing its lookups degrade to "not
// blocklisted, empty queue" with a warning rather than an error. See
// registerWorkerIndexes and assertWorkerIndexes, which turn that silent
// degradation into a startup failure.
func setupQueueWorkers(mgr ctrl.Manager, bus events.Bus) error {
	c := mgr.GetClient()
	cat := catalogue.LoadedCatalogue()

	if err := registerWorkerIndexes(context.Background(), mgr); err != nil {
		return err
	}
	if err := assertWorkerIndexes(mgr); err != nil {
		return fmt.Errorf("catalogarr: assert worker indexes: %w", err)
	}

	grabDeps := grab.Deps{Client: c, Bus: bus}

	// The bridge from the search worker's ranked results to a grab (§8.2's
	// two halves). Both resolvers must be set: grab.Sink logs a warning and
	// DROPS an approved release when either is nil, which would make the
	// whole automatic-search path a silent no-op.
	sink := grab.Sink{
		Deps: grabDeps,
		ResolveProfile: func(ctx context.Context, name string) (quality.Profile, error) {
			return resolveQualityProfile(ctx, c, name, cat)
		},
		ResolveDelay: func(ctx context.Context, ns string, ref *string, tags []string) (catalogv1alpha1.DelayProfileSpec, error) {
			return resolveDelayProfile(ctx, c, ns, ref, tags)
		},
	}

	searchWorker := search.NewWorker(c, search.NewBusSearchRPC(bus), cat)
	searchWorker.Sink = sink
	if err := searchWorker.SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: subscribe search: %w", err)
	}

	if err := grab.NewHandler(grabDeps).SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: subscribe grab: %w", err)
	}

	if err := rssmatcher.NewHandler(rssmatcher.Deps{
		Client:    c,
		Bus:       bus,
		Catalogue: cat,
	}).SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: subscribe rss-matcher: %w", err)
	}

	return nil
}

// setupMetadataGateway registers RoleMetadata's gateway: every outbound
// metadata client, their rate limiters and the two cache tiers, serving
// rpc.catalogarr.metadata.* and the work.catalogarr.metadata.<tier> consumer.
//
// It is a Runnable rather than a direct call because catalogmetadata.Setup
// Lists MetadataProviders through the manager's client: called before
// mgr.Start it would read an unsynced cache and build a registry with no
// providers in it. Runnables added this way start only after the caches have
// synced, and the subscription's lifetime is then the manager's.
//
// §3 pins this role to exactly one replica -- its in-process rate limiters
// are what keep Clustarr inside every provider's quota -- which is why
// `--role all` is not what the manifests run for the main Deployment. It is
// pinned by the Deployment's replica count, NOT by the leader lease, so the
// runnable is a k8s.EveryReplica: a plain manager.RunnableFunc would go behind
// the lease (see EveryReplica). The metadata Deployment runs --role metadata,
// which does not elect, and controller-runtime treats a non-electing process
// as elected -- so the gateway did start there. The exposure is a role that
// elects and also serves metadata: one replica would serve, the rest idle.
func setupMetadataGateway(mgr ctrl.Manager, bus events.Bus) error {
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := catalogmetadata.Setup(ctx, catalogmetadata.Options{
			Client:     mgr.GetClient(),
			Bus:        bus,
			HTTPClient: defaultHTTPClient,
		})
		if err != nil {
			return fmt.Errorf("catalogarr: metadata gateway: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("catalogarr: add the metadata gateway: %w", err)
	}
	return nil
}
