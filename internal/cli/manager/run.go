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

package manager

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/autoscale"
	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	subtitleprofile "github.com/mediactl/clustarr/app/caption/controller/subtitleprofile"
	captionmanager "github.com/mediactl/clustarr/app/caption/manager"
	catalogmanager "github.com/mediactl/clustarr/app/catalog/manager"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	historyworker "github.com/mediactl/clustarr/app/catalog/worker/history"
	"github.com/mediactl/clustarr/app/dispatch"
	grabmanager "github.com/mediactl/clustarr/app/grab/manager"
	importmanager "github.com/mediactl/clustarr/app/import/manager"
	"github.com/mediactl/clustarr/app/import/manager/scanapply"
	indexermanager "github.com/mediactl/clustarr/app/indexer/manager"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/app/intake/advisory"
	livetvmanager "github.com/mediactl/clustarr/app/livetv/manager"
	"github.com/mediactl/clustarr/app/maintenance"
	"github.com/mediactl/clustarr/app/remediation"
	remediationmanager "github.com/mediactl/clustarr/app/remediation/manager"
	"github.com/mediactl/clustarr/app/transcode/admission"
	transcodemanager "github.com/mediactl/clustarr/app/transcode/manager"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/objindex"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/grouplang"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/presence"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/segments/fingerprintmeta"
	"github.com/mediactl/clustarr/pkg/version"
)

// Run starts the manager (spec §3.4.2, in order) and blocks until ctx is
// cancelled. A clean stop returns nil.
func Run(ctx context.Context, o Options) error {
	// 1. Validate before anything touches the cluster.
	if err := o.Validate(); err != nil {
		return err
	}

	// 2. Observability. Tracing's first caller wins, so this is the
	// process's one service name.
	to := o.Tracing
	to.ServiceName = "manager"
	ctx, shutdown, err := obs.Bootstrap(ctx, o.Logging, to)
	if err != nil {
		return err
	}
	defer shutdown()
	// The release groups' dual-audio languages (pkg/grouplang) on the root
	// context every runnable and reconciler inherits, for every reader of a
	// release's languages; the built-in table answers until the bucket is
	// read.
	groups := grouplang.NewIndex()
	ctx = release.WithGroupLanguages(ctx, groups)
	log := logging.FromContext(ctx)

	// 3.
	k8s.RegisterRESTClientMetrics()

	// 4. The manager, on the one merged cache.
	cfg := o.RESTConfig
	if cfg == nil {
		if cfg, err = ctrl.GetConfig(); err != nil {
			return fmt.Errorf("manager: kubeconfig: %w", err)
		}
	}
	opts := managerOptions(o)
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(opts, ctx))
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}

	// 5. The bus.
	bus, nc, err := busconn.Connect(o.NATSURL, "manager", busconn.WithHooks(obs.BusHooks()),
		busconn.WithLogger(logging.FromContext(ctx)))
	if err != nil {
		return err
	}
	// The bus drains last (ADR-0020 §1): this deferred call runs once
	// mgr.Start has returned, every runnable stopped, so the intake's acks
	// after a decision lands still reach the server. It spends what is left
	// of the graceful-shutdown timeout, then falls back to Close.
	defer busconn.ShutdownAfter(ctx, o.ShutdownTimeout(), bus, nc)()

	// 6. The manager is the only process that creates or updates the
	// topology (§5.9); the leader keeps it and backstops the dead letters.
	top := o.BusTopology()
	if err := busconn.EnsureTopology(ctx, bus, top); err != nil {
		return err
	}
	if err := mgr.Add(k8s.LeaderOnly(func(ctx context.Context) error {
		return busconn.KeepTopology(ctx, nc, bus, top)
	})); err != nil {
		return fmt.Errorf("manager: add the topology keeper: %w", err)
	}
	if err := mgr.Add(k8s.LeaderOnly(func(ctx context.Context) error {
		return busconn.WatchDeadLetters(ctx, bus, top)
	})); err != nil {
		return fmt.Errorf("manager: add the dead-letter watcher: %w", err)
	}
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		return groups.Run(ctx, bus.KV(events.BucketReleaseGroups))
	})); err != nil {
		return fmt.Errorf("manager: add the release groups index: %w", err)
	}
	// A standby keeps the failover kinds warm, and every replica reports
	// whether it leads (control-plane placement design §5.4, §5.4.1).
	if err := k8s.WarmInformers(mgr, &catalogv1alpha1.MediaFile{}, &catalogv1alpha1.Episode{},
		&catalogv1alpha1.Movie{}, &catalogv1alpha1.Series{}); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	if err := mgr.Add(k8s.LeaderGauge(mgr.Elected(), metrics.ManagerLeader.WithLabelValues().Set)); err != nil {
		return fmt.Errorf("manager: add the leader gauge: %w", err)
	}

	// 7. Process-level checks.
	ready, live, err := processChecks(mgr, nc, bus)
	if err != nil {
		return err
	}

	// 8. Every component, in a fixed order.
	components, err := register(mgr, bus, o)
	if err != nil {
		return err
	}

	// 9. The one probe registration.
	if err := k8s.AddProbes(mgr, ready, live); err != nil {
		return err
	}

	// 10, 11.
	log.Info("starting", "components", components, "lease", LeaderElectionID, "leaderElection", o.LeaderElect)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	return nil
}

// processChecks is the manager's process-level probe set (spec §3.3):
// readiness on JetStream and the cache sync, and `bus` as the liveness check
// of split §3.3 as amended 2026-10-07 -- handlers that ignore their context
// have held a subscription's every slot past its budget, which only a
// restart cures. A nil bus (no NATS) adds no `bus` check.
func processChecks(mgr ctrl.Manager, nc *nats.Conn, bus *natsbus.Bus) (ready, live *k8s.Checks, err error) {
	ready, live = k8s.NewChecks(), k8s.NewChecks()
	if err := ready.Add("jetstream", busconn.ReadyChecker(nc, bus)); err != nil {
		return nil, nil, err
	}
	if bus != nil {
		if err := live.Add("bus", busconn.WedgeChecker(bus)); err != nil {
			return nil, nil, err
		}
	}
	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return nil, nil, err
	}
	if err := ready.Add("cache", cacheReady); err != nil {
		return nil, nil, err
	}
	return ready, live, nil
}

// planes are the manager's shared admission and intake planes (ADR-0019
// §5.4, §8.1), built once by register before any component and handed to
// the components that dispatch or decide.
type planes struct {
	// states is the one 5 s consumer-state read path: the queue gauge, the
	// External Metrics API and the ledger share it.
	states *extmetrics.StateCache
	// ledger admits every task the manager publishes to an agent.
	ledger *dispatch.Ledger
	// inbox holds grab candidates for their owners' passes (S30).
	inbox *intake.Inbox
	// book is the leader-local nak and term record planners render
	// Dispatch.delivery from (R8); tasks is the intake that fills it.
	book  *dispatch.DeliveryBook
	tasks *advisory.Intake
	// projector is the one DLQ projector: the catalog step consumes
	// clustarr-dlq-projector with it, and the advisory intake's second net
	// annotates through it (§8.5).
	projector *historyworker.DLQProjector
	// artwork is the manager's objindex over clustarr-artwork, leader-only
	// (ADR-0019 §7.1, §8.1 S26): the item keys and the Artist and Author
	// reconcilers render status.artwork and status.overlay from it.
	artwork *objindex.Index
	// wanted carries wantedcron's sweep to the remediation loop, and budget
	// is the search planner's first-time live donor budget the sweep
	// refills (§7.3).
	wanted chan schema.ItemRef
	budget *searchplan.DonorBudget
}

// wantedWakeBuffer is the wantedcron sweep's channel buffer: the sweep
// waits for room, so it only smooths the hand-off to the loop's queue.
const wantedWakeBuffer = 1024

// newPlanes builds the shared planes and adds the leader-only runnables
// among them to mgr.
func newPlanes(mgr ctrl.Manager, bus events.Bus, o Options) (planes, error) {
	admin, ok := bus.(events.StreamAdmin)
	if !ok {
		return planes{}, fmt.Errorf("manager: admission needs a bus that reports consumer state; %T does not", bus)
	}
	p := planes{
		states: extmetrics.NewStateCache(admin),
		wanted: make(chan schema.ItemRef, wantedWakeBuffer), budget: searchplan.NewDonorBudget(),
	}
	p.ledger = dispatch.New(dispatch.Options{
		Topology: o.BusTopology(),
		States:   p.states,
		Presence: &presence.Reader{KV: bus.KV(events.BucketProgress)},
		Reader:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder("clustarr-dispatch"),
		Pod:      types.NamespacedName{Namespace: os.Getenv("POD_NAMESPACE"), Name: os.Getenv("POD_NAME")},
	})
	if err := mgr.Add(p.ledger); err != nil {
		return planes{}, fmt.Errorf("manager: add the dispatch ledger: %w", err)
	}
	// Every request the manager makes feeds the ledger (ADR-0020 §4): a
	// verb that finds no responder reads as its serving domain's NoAgent.
	if o, ok := bus.(events.RequestObserver); ok {
		o.ObserveRequests(p.ledger.ObserveRequest)
	}
	// The intake (§4.3, §8.4): the candidate inbox, acked after the owner's
	// pass decides, and the scan intake with its applier (A6.1); both
	// leader-only, binding the durables EnsureTopology created.
	top := o.BusTopology()
	cand, _ := top.Consumer(events.ConsumerIntakeCandidate)
	p.inbox = intake.NewInbox(cand.MaxAckPending)
	if err := mgr.Add(&intake.CandidateConsumer{Bus: bus, Inbox: p.inbox, Topology: top}); err != nil {
		return planes{}, fmt.Errorf("manager: add the candidate intake: %w", err)
	}
	if err := mgr.Add(&intake.ScanConsumer{Bus: bus, Topology: top, Applier: newScanApplier(mgr, bus, o)}); err != nil {
		return planes{}, fmt.Errorf("manager: add the scan intake: %w", err)
	}
	// The task-events intake (§8.2): nak and term advisories of every
	// dispatched task into the delivery book, and ack sampling for metrics.
	// Its second net annotates through the DLQ projector the catalog step
	// consumes with (§8.5).
	p.projector = catalogmanager.NewDLQProjector(mgr, bus)
	p.book = dispatch.NewDeliveryBook()
	p.tasks = &advisory.Intake{
		Bus: bus, Admin: admin, Book: p.book, Topology: top, Projector: p.projector,
		ByUID: advisory.CacheResolver{Reader: mgr.GetClient()},
	}
	if err := mgr.Add(p.tasks); err != nil {
		return planes{}, fmt.Errorf("manager: add the task-events intake: %w", err)
	}
	// The artwork index (D13): one watch of the bucket, its creation time
	// checked so a re-created bucket reads as no artwork at once, and
	// metadata-only changes (a backfill) reported too.
	p.artwork = objindex.New(bus.ObjectStore(events.BucketArtwork), objindex.WithMetadataChanges())
	if err := mgr.Add(k8s.LeaderOnly(p.artwork.Run)); err != nil {
		return planes{}, fmt.Errorf("manager: add the artwork index: %w", err)
	}
	return p, nil
}

// newScanApplier is the scan intake's applier (ADR-0019 §7.6, A6.1): the
// manager's writes of what a library scan observed, paced by a limiter at
// the remediation loop's bulk rate (--remediation-bulk-writes-per-second).
func newScanApplier(mgr ctrl.Manager, bus events.Bus, o Options) *scanapply.Applier {
	rate := o.RemediationBulkWritesPerSecond
	return &scanapply.Applier{
		Client: mgr.GetClient(), Reader: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bus: bus,
		Limiter: remediation.NewWriteLimiter(rate, rate), Now: time.Now,
	}
}

// register adds every manager-side component in §3.4.2's fixed order and
// returns their names: catalog, import, index, grab, transcode, caption,
// remediation, livetv (Live TV spec 2026-10-07), then autoscale. The
// admission and intake planes come first: the components that dispatch
// take them.
func register(mgr ctrl.Manager, bus events.Bus, o Options) ([]string, error) {
	p, err := newPlanes(mgr, bus, o)
	if err != nil {
		return nil, err
	}
	// The object-store maintenance loop (ADR-0020 §5): leader-only, the
	// orphan purge over every object store of the topology on the artwork
	// reaper's cadence and grace.
	if err := mgr.Add(objectStoreMaintenance(bus, o)); err != nil {
		return nil, fmt.Errorf("manager: add the object-store maintenance loop: %w", err)
	}
	type step struct {
		name string
		add  func() error
	}
	// The loop signals the slim SubtitleProfile reconciler through one Wake
	// (loop spec §2.13): caption registers it, the loop calls Signal.
	wake := subtitleprofile.NewWake(subtitleprofile.WakeDebounce)
	// The transcode admission ledger (loop spec §5.5), built once: the loop
	// observes into it and takes grants from it, transcode-admission
	// rebuilds and passes it, and the slim TranscodeProfile reconciler
	// sizes the pools from it.
	ledger := admission.NewLedger(admission.Options{Slots: o.Slots, Window: o.JobWindow, GraftConcurrency: o.GraftConcurrency})
	steps := []step{
		{"catalog", func() error {
			return catalogmanager.Register(mgr, bus, catalogmanager.Options{
				Options: o.Options, DLQProjector: p.projector, Artwork: p.artwork,
				WantedWakes: p.wanted, DonorBudget: p.budget, Inbox: p.inbox,
			})
		}},
		{"import", func() error {
			return importmanager.Register(mgr, bus, importmanager.Options{Options: o.Options, TraktBaseURL: o.TraktBaseURL})
		}},
		{"index", func() error {
			return indexermanager.Register(mgr, bus, indexermanager.Options{
				Options:                 o.Options,
				CardigannDefinitionsDir: o.CardigannDefinitionsDir, CardigannBundled: o.CardigannBundled,
				FacadeAPIKeySecret: o.FacadeAPIKeySecret,
			})
		}},
		{"grab", func() error {
			return grabmanager.Register(mgr, bus, grabOptions(o))
		}},
		{"transcode", func() error {
			return transcodemanager.Register(mgr, bus, transcodeOptions(o, ledger))
		}},
		{"caption", func() error {
			return captionmanager.Register(mgr, bus, captionmanager.Options{Options: o.Options, ProfileWake: wake})
		}},
		// The MediaFile remediation loop (ADR-0016, loop spec §3.1), after
		// transcode and caption: the only writer of MediaFile status.
		{"remediation", func() error {
			return remediationmanager.Register(mgr, bus, remediationmanager.Options{
				Options: o.Options, DataDir: o.DataDir,
				Concurrency: o.RemediationConcurrency, BulkWritesPerSecond: o.RemediationBulkWritesPerSecond,
				IOWorkers: o.RemediationIOWorkers,
				// The planes the item stages admit, settle and wake through
				// (ADR-0019 A3.3; A2's hand-off).
				Dispatch: p.ledger, Inbox: p.inbox, DeliveryWakes: p.tasks.Wakes(), Book: p.book,
				Artwork: p.artwork,
				// The search planner's (A4.2).
				WantedWakes: p.wanted, DonorBudget: p.budget, SearchLivePerMinute: o.SearchLivePerMinute,
				SubtitleProfileWake: wake.Signal,
				Ledger:              ledger,
				// A standalone graft Job runs on the cpu pool's pod template
				// (loop spec §4.12): the transcode manager's own.
				GraftPool: transcodemanager.PoolConfig(transcodeOptions(o, ledger)),
			})
		}},
		{"livetv", func() error {
			return livetvmanager.Register(mgr, bus, livetvmanager.Options{
				Options:     o.Options,
				BindAddress: o.LiveTVBindAddress, Service: o.LiveTVService,
			})
		}},
		// The autoscale step runs whatever --autoscale says (§9.4): QueueGauge
		// is always added, and with --autoscale=false Register's leader-only
		// pass deletes every HPA a previous release's manager labelled.
		// Enabled, not the presence of the step, is what turns the
		// reconciler, the External Metrics server, CertManager and
		// NamespaceGuard on (W4.74).
		{"autoscale", func() error {
			admin, ok := bus.(events.StreamAdmin)
			if !ok {
				return fmt.Errorf("manager: autoscaling needs a bus that reports consumer state; %T does not", bus)
			}
			return autoscale.Register(mgr, admin, autoscale.Options{
				Enabled:     o.Autoscale,
				Namespace:   o.Namespace,
				BindAddress: o.ExternalMetricsBindAddress,
				ServiceName: o.ExternalMetricsService,
				SecretName:  o.ExternalMetricsSecret,
				Cache:       p.states,
				Dispatch:    p.ledger,
			})
		}},
	}
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		if err := s.add(); err != nil {
			return nil, fmt.Errorf("manager: register %s: %w", s.name, err)
		}
		names = append(names, s.name)
	}
	return names, nil
}

// objectStoreMaintenance is the manager's object-store maintenance loop
// over o's topology; a bus that is not an ObjectStoreAdmin purges nothing.
// Its audits backfill each store's metadata (ADR-0021 §3): the artwork
// store's is the item keys' (ADR-0019 D13), the fingerprint store's is this
// loop's.
func objectStoreMaintenance(bus events.Bus, o Options) *maintenance.ObjectStores {
	m := &maintenance.ObjectStores{Topology: o.BusTopology()}
	if admin, ok := bus.(events.ObjectStoreAdmin); ok {
		m.Admin = admin
	}
	fingerprints := bus.ObjectStore(events.ObjectStoreFingerprints)
	writtenBy := "manager@" + version.String()
	m.Audits = append(m.Audits, maintenance.Audit{
		Name: events.ObjectStoreFingerprints,
		Run: func(ctx context.Context) (int, error) {
			return fingerprintmeta.BackfillStore(ctx, fingerprints, writtenBy)
		},
	})
	return m
}

// grabOptions is the grab manager's registration: the engines it renders
// and the runtime they get.
func grabOptions(o Options) grabmanager.Options {
	return grabmanager.Options{
		Options: o.Options, DataDir: o.DataDir,
		ScratchDir: o.EngineScratchDir, EngineImage: o.NativeImage, DataClaimName: o.DataClaim,
		EngineServiceAccount:     o.EngineServiceAccount,
		EngineMemoryLimitTorrent: o.EngineMemoryLimitTorrent, EngineMemoryLimitUsenet: o.EngineMemoryLimitUsenet,
		WorkerPlacement: o.WorkerPlacement,
	}
}

// transcodeOptions is the transcode manager's registration: its pools, the
// shared admission ledger and the graft slots it sizes. The remediation
// loop renders its graft Jobs from the same options' pool configuration.
func transcodeOptions(o Options, ledger *admission.Ledger) transcodemanager.Options {
	return transcodemanager.Options{
		Options: o.Options, Slots: o.Slots,
		DataDir: o.DataDir, WorkerImage: o.NativeImage, DataClaimName: o.DataClaim,
		IntelRenderGroups: o.IntelRenderGroups, NodeLabelNVIDIA: o.NodeLabelNVIDIA, NodeLabelIntel: o.NodeLabelIntel,
		WorkerPlacement: o.WorkerPlacement,
		JobWindow:       o.JobWindow, GraftConcurrency: o.GraftConcurrency, Ledger: ledger,
		VideoLease: o.VideoTranscodeLease,
		Logging:    o.Logging, Tracing: o.Tracing,
	}
}
