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
	"net/http"
	"slices"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/author"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/delayprofile"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/catalog/controller/metadataprovider"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/qualityprofile"
	"github.com/mediactl/clustarr/app/catalog/controller/rootfolder"
	searchctl "github.com/mediactl/clustarr/app/catalog/controller/search"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/app/catalog/history"
	catalogmetadata "github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/app/catalog/worker/redownload"
	"github.com/mediactl/clustarr/app/catalog/worker/rssmatcher"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
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

	// RoleWorker runs the search, grab, rss-matcher and redownload
	// consumers. Every replica runs them. The import and importlist consumers are
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

// setupControllers registers every catalog reconciler (§6.1, §16 M1 and M6),
// plus the wantedcron sweep, which is a manager.Runnable rather than a
// reconciler because it reconciles nothing -- only a clock.
//
// The seven non-video reconcilers (plan tasks G2-2 and G2-3 built them, G2-5
// wires them) sat in their own packages, tested and registered nowhere, until
// this function named them: every Artist, Album, Author, Book, Audiobook,
// Comic and Issue a user created was accepted by the apiserver and then
// never reconciled -- no metadata task, no fan-out, no phase -- with nothing
// in the logs to say so.
//
// The importer, importlist and importexclusion controllers are NOT here:
// amendment §A1.2/§A1.3 moved them to importarr, whose run.go registers them.
// Building them here would break the MediaFile single-writer split (importarr
// owns what it observed, catalogarr owns what it decided).
//
// One recorder convention: every reconciler here takes a
// k8s.io/client-go/tools/events.EventRecorder from mgr.GetEventRecorder, and
// that writes events.k8s.io/v1 -- the API §13 asks for. The deprecated
// mgr.GetEventRecorderFor, which returns a
// k8s.io/client-go/tools/record.EventRecorder and writes core/v1 Events, is
// gone from this tree, so no package needs a groups="" events rule and the
// generated Role grants events only in events.k8s.io.
//
// One core-group events rule survives and must: controller-runtime's own
// leader election still calls GetEventRecorderFor internally
// (pkg/leaderelection/leader_election.go) and writes core/v1 Events for
// acquire/renew. That rule lives in the hand-written leader-election Role
// (config/rbac/leader_election_role.yaml and the chart's copy), not in the
// marker-generated manager ClusterRole, so deleting it because "nothing
// writes core events any more" would break leader election.
//
// The tree arrived at one convention the hard way, and the lesson outlives
// the split. Until Task C12a the wave 1 controllers (movie, series, episode,
// mediafile, search, and importarr's rootfolderschedule) declared
// events.k8s.io while still writing core/v1 through the deprecated recorder,
// so the generated Role granted a group nobody wrote and omitted the one they
// did: on a real cluster every one of their events would have been denied,
// and no envtest could see it, because envtest does not enforce RBAC. C12a
// made the markers honest by moving them back to ""; this task moved the
// recorders instead, and moved each marker in the same commit. The rule that
// falls out of both: the RBAC marker and the recorder type are one change. A
// marker that disagrees with the recorder beside it passes every test in this
// repo and fails only in production.
func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	c := mgr.GetClient()
	scheme := mgr.GetScheme()

	if err := (&movie.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorder("movie"),
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: movie: %w", err)
	}

	if err := (&series.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorder("series"),
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: series: %w", err)
	}

	if err := (&episode.Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: mgr.GetEventRecorder("episode"),
		Bus:      bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: episode: %w", err)
	}

	if err := setupNonVideoControllers(mgr, bus); err != nil {
		return err
	}

	if err := mediafile.NewReconciler(c, scheme,
		mgr.GetEventRecorder("mediafile"),
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
		mgr.GetEventRecorder("search"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: search: %w", err)
	}

	// The operator's forced metadata refresh (clustarr.io/refresh-metadata):
	// one metadata-only controller per kind with metadata of its own, beside
	// the reconcilers that publish the scheduled refreshes.
	if err := catalogmetadata.NewRefresher(catalogmetadata.RefreshDeps{
		Client: c, Bus: bus, Recorder: mgr.GetEventRecorder("metadata-refresh"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: metadata refresh: %w", err)
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

// setupNonVideoControllers registers the seven non-video reconcilers (§4.2,
// §16 M6), each as its package's doc.go prescribes.
//
// Three parents fan out children through the metadata gateway's
// rpc.catalogarr.metadata.lookup: Artist -> Album, Author -> Book and
// Comic -> Issue. So each of those three needs the bus for its
// Request as well as for publishing its own MetadataTask; with a nil Bus
// every reconcile panics into RecoverPanic before it lists a single child.
// Album, Book and Audiobook are metadata targets of their own and publish
// their own MetadataTasks, so they need it too. Issue fetches no metadata
// of its own -- Comic's fan-out writes its provider fields under
// k8s.ManagerCatalogarrFanout (app/catalog/controller/issue's doc.go) -- but
// it publishes its catalog item events like every other kind, and a nil Bus
// publishes nothing, so it gets the bus as well.
//
// Every recorder is named after its kind, the convention movie and series
// set, so `kubectl get events` attributes each Event to the controller that
// raised it.
func setupNonVideoControllers(mgr ctrl.Manager, bus events.Bus) error {
	c := mgr.GetClient()
	scheme := mgr.GetScheme()

	if err := (&artist.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("artist"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: artist: %w", err)
	}
	if err := (&album.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("album"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: album: %w", err)
	}
	if err := (&author.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("author"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: author: %w", err)
	}
	// Book covers both shapes book_types.go allows: fanned out from an
	// Author, and standalone (no spec.authorRef).
	if err := (&book.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("book"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: book: %w", err)
	}
	if err := (&audiobook.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("audiobook"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: audiobook: %w", err)
	}
	if err := (&comic.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("comic"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: comic: %w", err)
	}
	if err := (&issue.Reconciler{
		Client: c, Scheme: scheme, Recorder: mgr.GetEventRecorder("issue"), Bus: bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: issue: %w", err)
	}
	return nil
}

// setupWorkers registers the queue consumers, the metadata gateway and the
// history sink plus DLQ projector.
//
// The import and importlist consumers are importarr's
// (work.importarr.fileimport, work.importarr.list -- amendment §A1.6).
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if o.Role.Has(RoleWorker) || o.Role.Has(RoleAll) {
		if err := setupQueueWorkers(mgr, bus, o); err != nil {
			return err
		}
	}
	if o.Role.Has(RoleMetadata) || o.Role.Has(RoleAll) {
		if err := setupMetadataGateway(mgr, bus); err != nil {
			return err
		}
	}
	if o.Role.Has(RoleHistory) || o.Role.Has(RoleAll) {
		if err := setupHistory(mgr, bus); err != nil {
			return err
		}
	}
	return nil
}

// setupHistory registers RoleHistory's two consumers (§13, §16 M6; plan task
// G1-4 built them, G1-5 wires them): the history sink on
// ConsumerCatalogHistory, projecting every clustarr.evt.> domain event onto
// an events.k8s.io Event regarding the CR it concerns, and the DLQ projector
// on ConsumerDLQProjector, which per ruling R1 annotates the dead-lettered
// CR under k8s.ManagerDLQProjector and emits a Warning Event -- it never
// writes status.
//
// Until this was filled in, --role history was a valid role that started
// nothing: both durable consumers existed server-side since M0 with no
// subscriber, so every domain event and every dead letter piled up
// unacknowledged on CLUSTARR_EVENTS and CLUSTARR_DLQ while the manifests ran
// `--role controller,worker,history` and reported Ready.
//
// Both subscriptions are k8s.EveryReplica runnables inside their own
// SetupWithManager, so every replica of the catalogarr Deployment drains the
// two streams, not only the leader.
//
// Each gets its own recorder name, so `kubectl get events` attributes a
// projected domain event and a dead letter to different reporting
// controllers.
//
// The third piece is the clustarr.io/replay handler (design §5; task X5a
// built it, X14 wires it): a metadata-only controller per annotatable kind
// that reads a dead letter back off CLUSTARR_DLQ and republishes it. Both it
// and the projector get the same DLQ reader -- the projector uses it to
// record which sequence to replay (clustarr.io/dead-letter-seq) -- and
// neither exists without one: the in-memory bus keeps no stream to read
// back, so on it the projector leaves the sequence out and no replay handler
// is registered. k8s.ConnectBus returns a JetStream-backed bus, so in
// production both are always wired.
func setupHistory(mgr ctrl.Manager, bus events.Bus) error {
	if err := history.NewSink(history.SinkDeps{
		Recorder: mgr.GetEventRecorder("catalogarr-history"),
	}).SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: history sink: %w", err)
	}
	reader, replayable := history.DLQReaderFor(bus)
	if err := history.NewDLQProjector(history.DLQDeps{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder("clustarr-dlq-projector"),
		DLQ:      reader,
	}).SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: dlq projector: %w", err)
	}
	if !replayable {
		return nil
	}
	if err := history.NewReplayer(history.ReplayDeps{
		Client:   mgr.GetClient(),
		Bus:      bus,
		DLQ:      reader,
		Recorder: mgr.GetEventRecorder("clustarr-replay"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("catalogarr: dlq replay: %w", err)
	}
	return nil
}

// setupQueueWorkers registers the search, grab, rss-matcher and redownload
// consumers.
//
// Order is load-bearing and is the reason the indexes are registered here
// rather than by whichever worker happens to want them first: the RSS matcher
// reads the live queue through app/catalog/worker/search's Download target
// index and matches releases through its own indexes, and when they are
// missing its lookups degrade rather than fail. See registerWorkerIndexes
// and assertWorkerIndexes, which turn that silent degradation into a
// startup failure.
//
// Three things every consumer here shares, each set once:
//
//   - the bus topology this process installed (o.BusTopology(), the value
//     Run hands k8s.EnsureTopology), which each consumer looks its durable
//     consumer up in. Left unset, each falls back to events.Default(),
//     which is only right while BusTopology's single-node collapse happens
//     to leave consumers untouched -- an invariant nothing enforces;
//   - an uncached reader (mgr.GetAPIReader()) for the grab path's
//     double-grab guard, on every route into it: the search sink, the
//     scheduled grab and the RSS matcher. Through the cache, a Download
//     another path created milliseconds earlier can be missed and grabbed
//     beside (x4a-report "cache window");
//   - one TheXEM scene-numbering source (newSceneMaps), so the search
//     worker and the RSS matcher read a scene number the same way and TheXEM
//     is asked once per series per TTL, not once per consumer.
func setupQueueWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if err := registerWorkerIndexes(context.Background(), mgr); err != nil {
		return err
	}
	if err := assertWorkerIndexes(mgr); err != nil {
		return fmt.Errorf("catalogarr: assert worker indexes: %w", err)
	}

	w, err := buildQueueWorkers(mgr, bus, o)
	if err != nil {
		return err
	}
	if err := w.search.SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: subscribe search: %w", err)
	}
	if err := w.grab.SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: subscribe grab: %w", err)
	}
	if err := w.rss.SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: subscribe rss-matcher: %w", err)
	}
	if err := w.redownload.SetupWithManager(mgr, bus); err != nil {
		return fmt.Errorf("catalogarr: subscribe redownload: %w", err)
	}
	return nil
}

// queueWorkers is the four consumers setupQueueWorkers registers, built
// but not yet subscribed, so a test can inspect exactly what Run hands each
// one (wiring_envtest_test.go's TestQueueWorkersShareRunsWiring).
type queueWorkers struct {
	search *search.Worker
	grab   *grab.Handler
	rss    *rssmatcher.Handler
	// redownload is spec §8.3's failed-Download consumer (gap fix Y3): it
	// frees the item's grab lease and publishes a redownload search, which
	// the search worker above turns into a grab recorded as
	// grabbedBy=redownload.
	redownload *redownload.Handler
}

// buildQueueWorkers builds the search, grab, rss-matcher and redownload
// consumers with every seam setupQueueWorkers' doc comment names set.
func buildQueueWorkers(mgr ctrl.Manager, bus events.Bus, o Options) (queueWorkers, error) {
	c := mgr.GetClient()
	cat := catalogue.LoadedCatalogue()
	topo := o.BusTopology()

	sceneMaps, err := newSceneMaps()
	if err != nil {
		return queueWorkers{}, err
	}

	grabDeps := grab.Deps{Client: c, Reader: mgr.GetAPIReader(), Bus: bus}

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
	searchWorker.Topology = &topo
	searchWorker.SceneMaps = sceneMaps

	grabHandler := grab.NewHandler(grabDeps)
	grabHandler.Topology = &topo

	rss := rssmatcher.NewHandler(rssmatcher.Deps{
		Client:    c,
		Reader:    mgr.GetAPIReader(),
		Bus:       bus,
		Topology:  &topo,
		SceneMaps: sceneMaps,
		Catalogue: cat,
	})

	redownloadHandler := redownload.NewHandler(c, bus)
	redownloadHandler.Topology = &topo

	return queueWorkers{search: searchWorker, grab: grabHandler, rss: rss, redownload: redownloadHandler}, nil
}

// setupMetadataGateway registers RoleMetadata's gateway: every outbound
// metadata client, their rate limiters and the two cache tiers, serving
// rpc.catalogarr.metadata.* and the work.catalogarr.metadata.<tier> consumer;
// and the artwork store's gateway half (spec §B.3-§B.7) -- the Fetcher that
// both the metadata consumer and the catalogarr-artwork-fetch consumer store
// originals through, that consumer itself, and the orphan reaper.
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
//
// The one replica is also what makes artwork.Fetcher.Lock -- an in-process
// lock -- enough to serialise the two artwork consumers per item. The
// reaper, unlike both consumers, IS behind the lease (§B.5): one sweeper
// per cluster, and under --role metadata the process counts as elected.
func setupMetadataGateway(mgr ctrl.Manager, bus events.Bus) error {
	fetcher := &artwork.Fetcher{
		Store:    bus.ObjectStore(events.BucketArtwork),
		HTTP:     artworkHTTPClient,
		Limiter:  artwork.NewHostLimiters(artworkHostRate, artworkHostBurst),
		Recorder: mgr.GetEventRecorder("metadata-gateway"),
	}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := catalogmetadata.Setup(ctx, catalogmetadata.Options{
			Client:     mgr.GetClient(),
			Reader:     mgr.GetAPIReader(),
			Bus:        bus,
			HTTPClient: defaultHTTPClient,
			Artwork:    fetcher,
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

	// The ImportArtwork durable (spec §B.7): a reconciler saw spec.artwork
	// drift from status.artwork. Same Fetcher, so the same per-item lock.
	spec, ok := events.Default().Consumer(events.ConsumerCatalogArtworkFetch)
	if !ok {
		return fmt.Errorf("catalogarr: consumer %q missing from the default topology", events.ConsumerCatalogArtworkFetch)
	}
	fetch := &artwork.Handler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Bus: bus, Fetcher: fetcher}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, spec.Subscription(), fetch.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe %s: %w", events.ConsumerCatalogArtworkFetch, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("catalogarr: add the artwork fetch consumer: %w", err)
	}

	if err := mgr.Add(&artwork.Reaper{
		Store:  bus.ObjectStore(events.BucketArtwork),
		Client: mgr.GetAPIReader(),
	}); err != nil {
		return fmt.Errorf("catalogarr: add the artwork reaper: %w", err)
	}
	return nil
}

// artworkHTTPClient fetches artwork originals. It is not defaultHTTPClient:
// an image of up to artwork.MaxImageBytes from a CDN is a longer transfer
// than a metadata API call, and a stuck one must not hold a gateway handler
// (and the item's artwork lock) past this timeout.
var artworkHTTPClient = &http.Client{Timeout: 60 * time.Second}

// Each image host gets its own token bucket. Image CDNs are not the metadata
// APIs whose MetadataProvider limits the registry applies, and a custom
// spec.artwork URL may name any host; four a second with a burst of four is
// polite to a CDN and still fetches an item's nine types in about two
// seconds.
const (
	artworkHostRate  = 4
	artworkHostBurst = 4
)
