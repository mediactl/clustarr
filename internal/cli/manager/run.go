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

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/autoscale"
	captionmanager "github.com/mediactl/clustarr/app/caption/manager"
	catalogmanager "github.com/mediactl/clustarr/app/catalog/manager"
	grabmanager "github.com/mediactl/clustarr/app/grab/manager"
	importmanager "github.com/mediactl/clustarr/app/import/manager"
	indexermanager "github.com/mediactl/clustarr/app/indexer/manager"
	squashmanager "github.com/mediactl/clustarr/app/squash/manager"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
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

	// 7. Process-level checks.
	var ready, live k8s.Checks
	if err := ready.Add("jetstream", busconn.ReadyChecker(nc, bus)); err != nil {
		return err
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

// register adds every manager-side component in §3.4.2's fixed order and
// returns their names: catalog, import, index, grab, squash, caption, then
// autoscale.
func register(mgr ctrl.Manager, bus events.Bus, o Options) ([]string, error) {
	type step struct {
		name string
		add  func() error
	}
	steps := []step{
		{"catalog", func() error {
			return catalogmanager.Register(mgr, bus, catalogmanager.Options{Options: o.Options})
		}},
		{"import", func() error {
			return importmanager.Register(mgr, bus, importmanager.Options{Options: o.Options, TraktBaseURL: o.TraktBaseURL})
		}},
		{"index", func() error {
			return indexermanager.Register(mgr, bus, indexermanager.Options{Options: o.Options,
				CardigannDefinitionsDir: o.CardigannDefinitionsDir, CardigannBundled: o.CardigannBundled})
		}},
		{"grab", func() error {
			return grabmanager.Register(mgr, bus, grabmanager.Options{Options: o.Options, DataDir: o.DataDir,
				ScratchDir: o.EngineScratchDir, EngineImage: o.NativeImage, DataClaimName: o.DataClaim,
				EngineServiceAccount: o.EngineServiceAccount})
		}},
		{"squash", func() error {
			return squashmanager.Register(mgr, bus, squashmanager.Options{Options: o.Options, Slots: o.Slots,
				DataDir: o.DataDir, WorkerImage: o.NativeImage, DataClaimName: o.DataClaim,
				IntelRenderGroups: o.IntelRenderGroups, NodeLabelNVIDIA: o.NodeLabelNVIDIA, NodeLabelIntel: o.NodeLabelIntel,
				JobWindow: o.JobWindow, JobRetention: o.JobRetention, GraftConcurrency: o.GraftConcurrency,
				Logging: o.Logging, Tracing: o.Tracing})
		}},
		{"caption", func() error {
			return captionmanager.Register(mgr, bus, captionmanager.Options{Options: o.Options, DataDir: o.DataDir})
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
