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
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/importarr/controller/importexclusion"
	importlistctrl "github.com/mediactl/clustarr/importarr/controller/importlist"
	"github.com/mediactl/clustarr/importarr/controller/libraryscan"
	"github.com/mediactl/clustarr/importarr/controller/rootfolderschedule"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/importarr/worker/importlist"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
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
	// RoleController runs the import-list, import-exclusion, library-scan
	// and root-folder schedule controllers, and fileimport's Retrigger.
	// Leader-elected.
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
	ready := map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
		// §13 lists only the JetStream ping, but a manager whose informers
		// have not synced serves a cold cache: the scan schedule would see no
		// RootFolders and the exclusion controller no exclusions, both of
		// which read as "nothing to do" rather than as "not ready yet".
		"cache": cacheReady,
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

// setupControllers registers importarr's leader-elected reconcilers
// (amendment §A1.2, §A1.3, §A1.6; §16 M1). Each call is the one its package's
// doc.go prescribes.
func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if err := (&libraryscan.Reconciler{
		Client: mgr.GetClient(),
		Bus:    bus,
		Clock:  time.Now,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: libraryscan: %w", err)
	}

	// mgr.GetEventRecorder, not the deprecated mgr.GetEventRecorderFor: the
	// Recorder field is a k8s.io/client-go/tools/events.EventRecorder and
	// writes events.k8s.io/v1, which is what the package's RBAC marker
	// grants. See catalogarr's setupControllers for why the two must move
	// together.
	if err := (&rootfolderschedule.Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder("rootfolderschedule"),
		Clock:    time.Now,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: rootfolderschedule: %w", err)
	}

	if err := (&importexclusion.Reconciler{
		Client: mgr.GetClient(),
		Bus:    bus,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: importexclusion: %w", err)
	}

	// The ImportList controller (plan task G1-3): schedules one
	// work.importarr.list.<name> task per spec.refreshInterval, drives
	// Trakt's device-code flow, and is the sole writer of ImportList.status,
	// which it projects from the list worker's clustarr-progress checkpoint
	// below. HTTPClient and Clock are left nil on purpose: both default
	// (http.DefaultClient, bounded by the reconcile context; time.Now).
	if err := newImportListReconciler(mgr.GetClient(), bus, o).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: importlist: %w", err)
	}

	// fileimport's Retrigger (plan task G2-4 built it, G2-5 wires it), with
	// the call its own doc comment gives. grabarr publishes a Download's
	// ImportTask once, on completion, and the file-import worker acks a
	// Blocked outcome, so without this nothing ever looks again at the
	// catalog.clustarr.io/import-target or import-override annotation a user
	// adds to a Blocked Download -- manual import, design §8.4, would be a
	// documented instruction that does nothing. It publishes to
	// work.importarr.fileimport rather than importing itself, so it needs no
	// /data and runs here, under the lease, not on importarr-worker.
	if err := (&fileimport.Retrigger{Client: mgr.GetClient(), Bus: bus}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: fileimport retrigger: %w", err)
	}

	return nil
}

// setupWorkers registers the work.importarr.* consumers (amendment §A1.6):
// scan, fileimport and list.
//
// The list worker creates Movie and Series only today; a spec.kinds entry
// naming a kind its provider cannot yield is refused at admission (R-10),
// and one it can yield but no catalog writer exists for fails on status
// (importarr/worker/importlist's syncKind), rather than being skipped.
//
// Both file-reading workers get o.SampleMaxBytes through [newScanWorker] and
// [newImportWorker]; see Options.SampleMaxBytes.
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	// The spec.path field index the incremental fingerprint check reads. It
	// must be registered before the manager starts, which is why it is here
	// and not inside the worker's own Handle.
	if err := rescan.IndexMediaFileByPath(context.Background(), mgr); err != nil {
		return fmt.Errorf("importarr: index mediafile path: %w", err)
	}

	spec, ok := o.BusTopology().Consumer(events.ConsumerImportScan)
	if !ok {
		return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportScan)
	}
	worker := newScanWorker(mgr.GetClient(), bus, o)
	sub := spec.Subscription()
	// k8s.EveryReplica, not manager.RunnableFunc: amendment §A1.6 runs the
	// scan consumer on EVERY replica of importarr-worker, and a bare
	// RunnableFunc has no NeedLeaderElection method, so controller-runtime
	// puts it behind the leader lease. importarr-worker does not run leader
	// election, and with it disabled controller-runtime treats the process as
	// elected and starts those runnables anyway -- so the subscription did
	// open there. The exposure is any importarr replica that DOES elect:
	// exactly one of them would have opened the subscription.
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, sub, worker.Handle)
		if err != nil {
			return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportScan, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("importarr: add %s consumer: %w", events.ConsumerImportScan, err)
	}

	// The completed-download import worker (amendment §A1.2, §A1.6; plan
	// tasks D2-7/D2-8), on ConsumerImportFile ("importarr-fileimport",
	// R6). Wiring exactly as fileimport's own doc.go prescribes: the
	// spec.mediaRef.target field index must be registered before the
	// manager starts, and the subscription runs on EVERY replica for the
	// same reason the scan consumer above does.
	if err := fileimport.IndexMediaFileByTarget(context.Background(), mgr); err != nil {
		return fmt.Errorf("importarr: index mediafile target: %w", err)
	}
	importSpec, ok := o.BusTopology().Consumer(events.ConsumerImportFile)
	if !ok {
		return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportFile)
	}
	importWorker := newImportWorker(mgr.GetClient(), mgr.GetAPIReader(), bus, o)
	importSub := importSpec.Subscription()
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, importSub, importWorker.Handle)
		if err != nil {
			return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportFile, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("importarr: add %s consumer: %w", events.ConsumerImportFile, err)
	}

	// The import-list sync worker (amendment §A1.3, §A1.6; plan task G1-3),
	// on ConsumerImportList ("importarr-list"): fetch, dedupe, drop what an
	// ImportExclusion blocks, then create or update catalog items under
	// k8s.ManagerImportarrWorker. It never writes ImportList.status -- it
	// checkpoints a Result to clustarr-progress for the controller above to
	// project. EVERY replica, for the same reason as the two consumers above.
	listSpec, ok := o.BusTopology().Consumer(events.ConsumerImportList)
	if !ok {
		return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportList)
	}
	listWorker := newListWorker(mgr.GetClient(), bus, o)
	listSub := listSpec.Subscription()
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, listSub, listWorker.Handle)
		if err != nil {
			return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportList, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("importarr: add %s consumer: %w", events.ConsumerImportList, err)
	}

	// The recycle-bin sweeper (task X7a built it, X14 wires it): the one
	// consumer of RootFolder.spec.recycleBin.cleanupDays, emptying each bin
	// of the dated folders past retention. It reads and deletes under /data,
	// so it runs here on the worker role -- the importarr controller
	// Deployment mounts no /data -- and on every replica: a sweep only
	// removes date-named folders past retention, so two replicas racing on
	// one folder cost a harmless second RemoveAll, and a leader lease would
	// buy nothing but a replica that never sweeps.
	if err := mgr.Add(k8s.EveryReplica(fileimport.NewRecycleSweeper(mgr.GetClient()).Run)); err != nil {
		return fmt.Errorf("importarr: add the recycle-bin sweeper: %w", err)
	}

	return nil
}

// newScanWorker builds the work.importarr.scan handler with o's sample size
// floor. rescan.NewWorker already defaults the floor, so the assignment
// matters exactly when o carries a different one -- a non-default
// --sample-max-bytes, or 0 to disable the rule.
func newScanWorker(c client.Client, bus events.Bus, o Options) *rescan.Worker {
	w := rescan.NewWorker(c, bus)
	w.SampleMaxBytes = o.SampleMaxBytes
	return w
}

// newListWorker builds the work.importarr.list handler with o's Trakt and
// Plex base URLs; see Options.TraktBaseURL.
func newListWorker(c client.Client, bus events.Bus, o Options) *importlist.Worker {
	w := importlist.NewWorker(c, bus)
	w.TraktBaseURL = o.TraktBaseURL
	w.PlexBaseURL = o.PlexBaseURL
	return w
}

// newImportListReconciler builds the ImportList controller with o's Trakt
// base URL, the same host newListWorker gives the list worker, so the
// device-code flow it drives authorizes the host the syncs then reach.
func newImportListReconciler(c client.Client, bus events.Bus, o Options) *importlistctrl.Reconciler {
	return &importlistctrl.Reconciler{Client: c, Bus: bus, TraktBaseURL: o.TraktBaseURL}
}

// newImportWorker builds the work.importarr.fileimport handler with o's
// sample size floor, for the reason [newScanWorker] gives, and the
// manager's API reader, which a movie's existing files are read through
// (fileimport.Worker.APIReader).
func newImportWorker(c client.Client, api client.Reader, bus events.Bus, o Options) *fileimport.Worker {
	w := fileimport.NewWorker(c, bus)
	w.APIReader = api
	w.SampleMaxBytes = o.SampleMaxBytes
	return w
}
