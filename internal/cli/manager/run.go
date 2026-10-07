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

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/autoscale"
	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	captionmanager "github.com/mediactl/clustarr/app/caption/manager"
	catalogmanager "github.com/mediactl/clustarr/app/catalog/manager"
	historyworker "github.com/mediactl/clustarr/app/catalog/worker/history"
	"github.com/mediactl/clustarr/app/dispatch"
	grabmanager "github.com/mediactl/clustarr/app/grab/manager"
	importmanager "github.com/mediactl/clustarr/app/import/manager"
	indexermanager "github.com/mediactl/clustarr/app/indexer/manager"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/app/intake/advisory"
	remediationmanager "github.com/mediactl/clustarr/app/remediation/manager"
	transcodemanager "github.com/mediactl/clustarr/app/transcode/manager"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/presence"
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
	log := logging.FromContext(ctx)

	// 3.
	k8s.RegisterRESTClientMetrics()

	// 4. The manager, on the one merged cache, behind the gated lease lock
	// while the cutover release runs.
	cfg := o.RESTConfig
	if cfg == nil {
		if cfg, err = ctrl.GetConfig(); err != nil {
			return fmt.Errorf("manager: kubeconfig: %w", err)
		}
	}
	opts := managerOptions(o)
	if o.LeaderElect && o.LegacyLeaseCheck {
		lock, err := k8s.NewGatedLeaseLock(cfg, opts.LeaderElectionNamespace, LeaderElectionID)
		if err != nil {
			return fmt.Errorf("manager: legacy lease gate: %w", err)
		}
		opts.LeaderElectionResourceLockInterface = lock
	}
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(opts, ctx))
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}

	// 5. The bus.
	bus, nc, err := busconn.Connect(o.NATSURL, "manager", busconn.WithHooks(obs.BusHooks()))
	if err != nil {
		return err
	}
	defer nc.Close()
	defer func() { _ = bus.Close() }()

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

	// 7. Process-level checks. `bus` is the liveness check of split §3.3 as
	// amended 2026-10-07: handlers that ignore their context have held a
	// subscription's every slot past its budget, which only a restart cures.
	var ready, live k8s.Checks
	if err := ready.Add("jetstream", busconn.ReadyChecker(nc, bus)); err != nil {
		return err
	}
	if bus != nil {
		if err := live.Add("bus", busconn.WedgeChecker(bus)); err != nil {
			return err
		}
	}
	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return err
	}
	if err := ready.Add("cache", cacheReady); err != nil {
		return err
	}

	// 8. Every component, in a fixed order.
	components, err := register(mgr, bus, o)
	if err != nil {
		return err
	}

	// 9. The one probe registration.
	if err := k8s.AddProbes(mgr, &ready, &live); err != nil {
		return err
	}

	// 10, 11.
	log.Info("starting", "components", components, "lease", LeaderElectionID, "leaderElection", o.LeaderElect)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	return nil
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
}

// newPlanes builds the shared planes and adds the leader-only runnables
// among them to mgr.
func newPlanes(mgr ctrl.Manager, bus events.Bus, o Options) (planes, error) {
	admin, ok := bus.(events.StreamAdmin)
	if !ok {
		return planes{}, fmt.Errorf("manager: admission needs a bus that reports consumer state; %T does not", bus)
	}
	p := planes{states: extmetrics.NewStateCache(admin)}
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
	// The intake (§4.3, §8.4): the candidate inbox, acked after the owner's
	// pass decides, and the scan intake, whose applier A6.1 wires; both
	// leader-only, binding the durables EnsureTopology created.
	top := o.BusTopology()
	cand, _ := top.Consumer(events.ConsumerIntakeCandidate)
	p.inbox = intake.NewInbox(cand.MaxAckPending)
	if err := mgr.Add(&intake.CandidateConsumer{Bus: bus, Inbox: p.inbox, Topology: top}); err != nil {
		return planes{}, fmt.Errorf("manager: add the candidate intake: %w", err)
	}
	if err := mgr.Add(&intake.ScanConsumer{Bus: bus, Topology: top}); err != nil {
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
	return p, nil
}

// register adds every manager-side component in §3.4.2's fixed order and
// returns their names: catalog, import, index, grab, squash, caption,
// remediation, then autoscale. The admission and intake planes come first:
// the components that dispatch take them.
func register(mgr ctrl.Manager, bus events.Bus, o Options) ([]string, error) {
	p, err := newPlanes(mgr, bus, o)
	if err != nil {
		return nil, err
	}
	type step struct {
		name string
		add  func() error
	}
	steps := []step{
		{"catalog", func() error {
			return catalogmanager.Register(mgr, bus, catalogmanager.Options{Options: o.Options, DLQProjector: p.projector})
		}},
		{"import", func() error {
			return importmanager.Register(mgr, bus, importmanager.Options{Options: o.Options, TraktBaseURL: o.TraktBaseURL})
		}},
		{"index", func() error {
			return indexermanager.Register(mgr, bus, indexermanager.Options{
				Options:                 o.Options,
				CardigannDefinitionsDir: o.CardigannDefinitionsDir, CardigannBundled: o.CardigannBundled,
			})
		}},
		{"grab", func() error {
			return grabmanager.Register(mgr, bus, grabmanager.Options{
				Options: o.Options, DataDir: o.DataDir,
				ScratchDir: o.EngineScratchDir, EngineImage: o.NativeImage, DataClaimName: o.DataClaim,
				EngineServiceAccount: o.EngineServiceAccount,
			})
		}},
		{"squash", func() error {
			return transcodemanager.Register(mgr, bus, transcodemanager.Options{
				Options: o.Options, Slots: o.Slots,
				DataDir: o.DataDir, WorkerImage: o.NativeImage, DataClaimName: o.DataClaim,
				IntelRenderGroups: o.IntelRenderGroups, NodeLabelNVIDIA: o.NodeLabelNVIDIA, NodeLabelIntel: o.NodeLabelIntel,
				JobWindow: o.JobWindow, JobRetention: o.JobRetention, GraftConcurrency: o.GraftConcurrency,
				Logging: o.Logging, Tracing: o.Tracing,
			})
		}},
		{"caption", func() error {
			return captionmanager.Register(mgr, bus, captionmanager.Options{Options: o.Options, DataDir: o.DataDir})
		}},
		// The MediaFile remediation loop (ADR-0016, loop spec §3.1), after
		// squash and caption: the only writer of MediaFile status.
		{"remediation", func() error {
			return remediationmanager.Register(mgr, bus, remediationmanager.Options{
				Options: o.Options, DataDir: o.DataDir,
				Concurrency: o.RemediationConcurrency, BulkWritesPerSecond: o.RemediationBulkWritesPerSecond,
				IOWorkers: o.RemediationIOWorkers,
				// The planes the item stages admit, settle and wake through
				// (ADR-0019 A3.3; A2's hand-off).
				Dispatch: p.ledger, Inbox: p.inbox, DeliveryWakes: p.tasks.Wakes(), Book: p.book,
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
