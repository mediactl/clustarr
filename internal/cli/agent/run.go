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
	"strconv"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	torrentagent "github.com/mediactl/clustarr/app/grab/agent/torrent"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/presence"
	"github.com/mediactl/clustarr/pkg/version"
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
	// Presence (ADR-0019 §5.3, ruling R14): this pod's liveness, slots and
	// capabilities in clustarr-progress, for the manager's admission.
	pw, err := presenceWriter(o, bus, slots)
	if err != nil {
		return fmt.Errorf("agent %s: %w", o.Domain, err)
	}
	if err := mgr.Add(k8s.EveryReplica(pw.Run)); err != nil {
		return fmt.Errorf("agent %s: add the presence writer: %w", o.Domain, err)
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

// presenceWriter is the domain's presence writer (ADR-0019 §5.3): its pod
// name as the torrent engine derives it ($POD_NAME, else the hostname), its
// node from $NODE_NAME, the slots of every durable it binds (the engines
// bind none: their engine record is their capacity report), and the
// capabilities it can prove.
func presenceWriter(o Options, bus events.Bus, overrides map[string]int) (*presence.Writer, error) {
	pod, err := torrentagent.PodName(os.Getenv, os.Hostname)
	if err != nil {
		return nil, fmt.Errorf("presence: pod name: %w", err)
	}
	w := &presence.Writer{
		KV:      bus.KV(events.BucketProgress),
		Domain:  o.Domain,
		Pod:     pod,
		Node:    os.Getenv("NODE_NAME"),
		Version: version.String(),
	}
	if d, ok := agentdomain.Lookup(o.Domain); ok && len(d.Consumers) > 0 {
		top := events.Default()
		w.Slots = map[string]int{}
		for _, name := range d.Consumers {
			c, ok := top.Consumer(name)
			if !ok {
				continue
			}
			w.Durables = append(w.Durables, name)
			w.Slots[name] = events.SlotsFor(c, overrides)
		}
	}
	w.Capabilities = func() map[string]string { return capabilities(o) }
	return w, nil
}

// capabilities is what the domain proves while it runs (split §10.1.1's
// self-check facts): FFmpeg 9 for the domains that decode (import's probe,
// caption's embedded extraction), and /data for those that read or write
// the library. Configured providers are not reported yet: no admission
// asks for one (ruling A2-3).
func capabilities(o Options) map[string]string {
	out := map[string]string{}
	switch o.Domain {
	case agentdomain.Import, agentdomain.Caption:
		if rep, err := ffruntime.Load(); err == nil {
			out["ffmpeg"] = strconv.Itoa(rep.FFmpegMajor)
		}
	}
	switch o.Domain {
	case agentdomain.Import, agentdomain.Caption, agentdomain.TorrentEngine, agentdomain.UsenetEngine:
		if fi, err := os.Stat(o.DataDir); err == nil && fi.IsDir() {
			out["data"] = "mounted"
		}
	}
	return out
}
