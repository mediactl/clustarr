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

// Package catalog is the agent's catalog domain (spec §3.5.3, as ADR-0019
// amends it): the catalog-search-high, catalog-search-normal and
// catalog-artwork-render consumers on CLUSTARR_WORK_CATALOG. The grab
// worker retired (A4.4): the owner key's grab planner decides every grab.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	renderer "github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/app/catalog/worker/titleindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// Options is what the catalog domain's Register takes; each consumer looks
// its durable up in o.BusTopology().
type Options struct {
	k8s.Options
}

// Register adds the catalog domain's consumers to mgr. It declares the
// field indexes the search worker reads -- the namesake and season lookups
// (titleindex.SearchIndexes, Wave S) -- and registers none; it adds no
// leader-only runnable.
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
	if err := w.render.SetupWithManager(mgr, bus); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("catalog domain: subscribe the artwork renderer: %w", err)
	}
	return catalogagent.Registration{Indexes: titleindex.SearchIndexes()}, nil
}

// workers is the domain's consumers, built but not subscribed, so a test
// can inspect exactly what Register hands each one.
type workers struct {
	search *search.Worker
	render *renderer.Handler
}

// buildWorkers builds them with every seam set. Two things the consumers
// share, each set once:
//
//   - the bus topology (o.BusTopology(), the topology the manager ensures
//     and the agent awaits), which each consumer looks its
//     durable consumer up in. Left unset, each falls back to
//     events.Default(), which is only right while BusTopology's single-node
//     collapse happens to leave consumers untouched -- an invariant nothing
//     enforces;
//   - one TheXEM scene-numbering source (catalogagent.NewSceneMaps), so the
//     search worker reads a scene number the way the RSS matcher does.
//
// The renderer (spec §C.6) reads items and lists OverlayProfiles through
// the uncached API reader too -- the recheck before each status.overlay
// apply must see the gateway's latest ratings and poster, and the profiles
// as they are, which a cache may not have yet. MaxConcurrentRenders stays at
// its default (renderer.DefaultMaxConcurrentRenders): the catalog pod
// runs the controllers beside it under one GOMEMLIMIT.
func buildWorkers(mgr ctrl.Manager, bus events.Bus, o Options) (workers, error) {
	c := mgr.GetClient()
	cat := catalogue.LoadedCatalogue()
	topo := o.BusTopology()
	sceneMaps, err := catalogagent.NewSceneMaps()
	if err != nil {
		return workers{}, err
	}
	// The search worker answers every search in clustarr-searches and
	// writes nothing else (ADR-0019 §7.4): the manager's planners decide.
	pod, _ := os.Hostname()
	kv := bus.KV(events.BucketSearches)
	sw := search.NewWorker(c, search.NewBusSearchRPC(bus), cat)
	sw.Topology, sw.SceneMaps = &topo, sceneMaps
	sw.Records = records.NewWriter(kv, agentrecords.Searches(), pod, version.String())
	sw.Answers = records.NewReader(kv, agentrecords.Searches())
	render := &renderer.Handler{
		Reader:   mgr.GetAPIReader(),
		Store:    bus.ObjectStore(events.BucketArtwork),
		Topology: &topo,
	}
	return workers{search: sw, render: render}, nil
}
