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

// Package events is the agent's events domain (spec §3.5.3, §3.5.4, as
// ADR-0019 §7.7 amends them): the consumers of the Limits streams that
// cannot buffer through a scale to zero: only catalog-rss-matcher
// (CLUSTARR_RELEASES), which sends its matches to their owners as
// candidates. The redownload consumer retired (A4.4: a redownload is the
// owner's search planner's, R25), and the history sink (catalog-history) and
// the DLQ projector (clustarr-dlq-projector) moved into the manager,
// leader-only (app/catalog/manager's registerHistory).
package events

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/catalog/worker/rssmatcher"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// Options is what the events domain's Register takes.
type Options struct {
	k8s.Options
}

// Register adds the events domain's consumer and declares the RSS
// matcher's thirteen field indexes (spec §5.7).
func Register(_ context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("events domain: Register needs the bus")
	}
	w, err := buildWorkers(mgr, bus, o)
	if err != nil {
		return catalogagent.Registration{}, err
	}
	for _, s := range []struct {
		durable string
		setup   func(ctrl.Manager, events.Bus) error
	}{
		{events.ConsumerCatalogRSSMatcher, w.rss.SetupWithManager},
	} {
		if err := s.setup(mgr, bus); err != nil {
			return catalogagent.Registration{}, fmt.Errorf("events domain: subscribe %s: %w", s.durable, err)
		}
	}
	return catalogagent.Registration{Indexes: rssmatcher.FieldIndexes()}, nil
}

type workers struct {
	rss *rssmatcher.Handler
}

// buildWorkers builds the consumer with every seam set: the installed
// topology and its own TheXEM source (catalog.buildWorkers' doc).
func buildWorkers(mgr ctrl.Manager, bus events.Bus, o Options) (workers, error) {
	c := mgr.GetClient()
	topo := o.BusTopology()
	sceneMaps, err := catalogagent.NewSceneMaps()
	if err != nil {
		return workers{}, err
	}
	rss := rssmatcher.NewHandler(rssmatcher.Deps{
		Client: c, Bus: bus, Topology: &topo,
		SceneMaps: sceneMaps, Catalogue: catalogue.LoadedCatalogue(),
	})
	return workers{rss: rss}, nil
}
