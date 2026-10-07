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

// Package catalog is the agent's catalog domain (spec §3.5.3): the
// catalogarr-search-high, catalogarr-search-normal, catalogarr-grab and
// catalogarr-artwork-render consumers on CLUSTARR_WORK_CATALOGARR.
package catalog

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	renderer "github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// Options is what the catalog domain's Register takes; each consumer looks
// its durable up in o.BusTopology().
type Options struct {
	k8s.Options
}

// Register adds the catalog domain's consumers to mgr. It declares the one
// field index they read and registers none; it adds no leader-only runnable.
func Register(_ context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("catalog domain: Register needs the bus")
	}
	w, err := buildWorkers(mgr, bus, o)
	if err != nil {
		return catalogagent.Registration{}, err
	}
	if err := w.search.SetupWithManager(mgr, bus); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalog domain: subscribe search: %w", err)
	}
	if err := w.grab.SetupWithManager(mgr, bus); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalog domain: subscribe grab: %w", err)
	}
	if err := w.render.SetupWithManager(mgr, bus); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalog domain: subscribe the artwork renderer: %w", err)
	}
	return catalogagent.Registration{Indexes: search.FieldIndexes()}, nil
}

// workers is the domain's three consumers, built but not subscribed, so a
// test can inspect exactly what Register hands each one.
type workers struct {
	search *search.Worker
	grab   *grab.Handler
	render *renderer.Handler
}

// buildWorkers builds them with every seam set. Three things the consumers
// share, each set once:
//
//   - the bus topology this process installed (o.BusTopology(), the value
//     the process hands k8s.EnsureTopology), which each consumer looks its
//     durable consumer up in. Left unset, each falls back to
//     events.Default(), which is only right while BusTopology's single-node
//     collapse happens to leave consumers untouched -- an invariant nothing
//     enforces;
//   - an uncached reader (mgr.GetAPIReader()) for the grab path's
//     double-grab guard, on every route into it: the search sink and the
//     scheduled grab (the events domain's RSS matcher is the third). Through
//     the cache, a Download another path created milliseconds earlier can be
//     missed and grabbed beside (x4a-report "cache window");
//   - one TheXEM scene-numbering source (catalogagent.NewSceneMaps), so the
//     search worker reads a scene number the way the RSS matcher does.
//
// The renderer (spec §C.6) reads items and lists OverlayProfiles through
// the uncached API reader too -- the recheck before each status.overlay
// apply must see the gateway's latest ratings and poster, and the profiles
// as they are, which a cache may not have yet. MaxConcurrentRenders stays at
// its default (renderer.DefaultMaxConcurrentRenders): the catalogarr pod
// runs the controllers beside it under one GOMEMLIMIT.
func buildWorkers(mgr ctrl.Manager, bus events.Bus, o Options) (workers, error) {
	c := mgr.GetClient()
	cat := catalogue.LoadedCatalogue()
	topo := o.BusTopology()
	sceneMaps, err := catalogagent.NewSceneMaps()
	if err != nil {
		return workers{}, err
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
	sw := search.NewWorker(c, search.NewBusSearchRPC(bus), cat)
	sw.Sink, sw.Topology, sw.SceneMaps = sink, &topo, sceneMaps
	gh := grab.NewHandler(grabDeps)
	gh.Topology = &topo
	render := &renderer.Handler{
		Client:   c,
		Reader:   mgr.GetAPIReader(),
		Store:    bus.ObjectStore(events.BucketArtwork),
		Topology: &topo,
	}
	return workers{search: sw, grab: gh, render: render}, nil
}
