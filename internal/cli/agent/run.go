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

package agent

import (
	"context"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Run starts one domain (spec §3.5.2, in order) and blocks until ctx is
// cancelled. A clean stop returns nil.
func Run(ctx context.Context, o Options) error {
	// 1. Exactly one known domain.
	register, ok := domains[o.Domain]
	if !ok {
		return errDomain
	}
	// 2.
	if err := o.Validate(); err != nil {
		return err
	}

	// 3. Observability, named for the domain.
	to := o.Tracing
	to.ServiceName = "agent-" + o.Domain
	ctx, shutdown, err := obs.Bootstrap(ctx, o.Logging, to)
	if err != nil {
		return err
	}
	defer shutdown()

	// 4.
	k8s.RegisterRESTClientMetrics()

	// 5. The manager: no lease, non-leader controllers, Secret and
	// ConfigMap uncached.
	cfg := o.RESTConfig
	if cfg == nil {
		if cfg, err = ctrl.GetConfig(); err != nil {
			return fmt.Errorf("agent: kubeconfig: %w", err)
		}
	}
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(managerOptions(o), ctx))
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}

	// 6. The bus.
	raw, nc, err := busconn.Connect(o.NATSURL, "agent-"+o.Domain, busconn.WithHooks(obs.BusHooks()))
	if err != nil {
		return err
	}
	defer nc.Close()
	defer func() { _ = raw.Close() }()

	// 7. Wait for the manager's topology; an agent never ensures it.
	if err := busconn.AwaitTopology(ctx, raw, events.Default(), o.TopologyWait); err != nil {
		return err
	}
	slots, err := events.ParseSlotOverrides(os.Getenv(events.SlotsEnv))
	if err != nil {
		return fmt.Errorf("$%s: %w", events.SlotsEnv, err)
	}
	var bus events.Bus = domainBus{Bus: raw, drain: o.DrainTimeout, slots: slots}
	if o.WrapBus != nil {
		bus = o.WrapBus(bus)
	}

	// 8. Process-level checks. `bus` is the liveness check of split §3.3 as
	// amended 2026-10-07: handlers that ignore their context have held a
	// subscription's every slot past its budget, which only a restart cures.
	var ready, live k8s.Checks
	if err := ready.Add("jetstream", busconn.ReadyChecker(nc, raw)); err != nil {
		return err
	}
	if raw != nil {
		if err := live.Add("bus", busconn.WedgeChecker(raw)); err != nil {
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

	// 9. The domain. Its field indexes are declared, not registered: the
	// process registers each once and proves it reached the cache.
	reg, err := register(ctx, mgr, bus, o)
	if err != nil {
		return fmt.Errorf("agent: register %s: %w", o.Domain, err)
	}
	if reg.Close != nil {
		defer func() {
			if cerr := reg.Close(); cerr != nil {
				logging.FromContext(ctx).Error("closing the domain", "domain", o.Domain, "err", cerr)
			}
		}()
	}
	if err := registerIndexes(ctx, mgr.GetFieldIndexer(), reg.Indexes); err != nil {
		return fmt.Errorf("agent %s: %w", o.Domain, err)
	}
	if err := catalogagent.AssertIndexes(mgr, reg.Indexes); err != nil {
		return fmt.Errorf("agent %s: %w", o.Domain, err)
	}
	if err := ready.Merge(reg.Ready); err != nil {
		return err
	}
	if err := live.Merge(reg.Live); err != nil {
		return err
	}

	// 10. The one probe registration.
	if err := k8s.AddProbes(mgr, &ready, &live); err != nil {
		return err
	}

	// 11.
	logging.FromContext(ctx).Info("starting", "domain", o.Domain, "drain", o.DrainTimeout)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("agent %s: %w", o.Domain, err)
	}
	return nil
}
