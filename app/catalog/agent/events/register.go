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

// Package events is the agent's events domain (spec §3.5.3, §3.5.4): the
// consumers of the three Limits streams, which cannot buffer through a scale
// to zero -- catalogarr-rss-matcher (CLUSTARR_RELEASES), catalogarr-redownload
// and catalogarr-history (CLUSTARR_EVENTS), clustarr-dlq-projector
// (CLUSTARR_DLQ).
package events

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/catalog/history"
	historyworker "github.com/mediactl/clustarr/app/catalog/worker/history"
	"github.com/mediactl/clustarr/app/catalog/worker/redownload"
	"github.com/mediactl/clustarr/app/catalog/worker/rssmatcher"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// Options is what the events domain's Register takes.
type Options struct {
	k8s.Options
}

// Register adds the events domain's four consumers. The RSS matcher reads
// the live queue through the search worker's Download target index, so this
// domain declares that index beside its own thirteen (spec §5.7).
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
		{events.ConsumerCatalogRedownload, w.redownload.SetupWithManager},
		{events.ConsumerCatalogHistory, w.sink.SetupWithManager},
		{events.ConsumerDLQProjector, w.dlq.SetupWithManager},
	} {
		if err := s.setup(mgr, bus); err != nil {
			return catalogagent.Registration{}, fmt.Errorf("events domain: subscribe %s: %w", s.durable, err)
		}
	}
	return catalogagent.Registration{Indexes: append(search.FieldIndexes(), rssmatcher.FieldIndexes()...)}, nil
}

type workers struct {
	rss *rssmatcher.Handler
	// redownload is spec §8.3's failed-Download consumer (gap fix Y3): it
	// frees the item's grab lease and publishes a redownload search, which
	// the catalog domain's search worker turns into a grab recorded as
	// grabbedBy=redownload.
	redownload *redownload.Handler
	sink       *historyworker.Sink
	dlq        *historyworker.DLQProjector
}

// buildWorkers builds the four consumers with every seam set. The DLQ reader
// exists only on a JetStream bus; on membus the projector leaves the
// sequence out.
//
// The history sink and the DLQ projector (§13, §16 M6; plan task G1-4 built
// them, G1-5 wired them): the sink, on ConsumerCatalogHistory, projects every
// clustarr.evt.> domain event onto an events.k8s.io Event regarding the CR
// it concerns; the projector, on ConsumerDLQProjector, per ruling R1
// annotates the dead-lettered CR under k8s.ManagerDLQProjector and emits a
// Warning Event -- it never writes status.
//
// Until they were wired, --role history was a valid role that started
// nothing: both durable consumers existed server-side since M0 with no
// subscriber, so every domain event and every dead letter piled up
// unacknowledged on CLUSTARR_EVENTS and CLUSTARR_DLQ while the manifests ran
// `--role controller,worker,history` and reported Ready.
//
// Both subscriptions are k8s.EveryReplica runnables inside their own
// SetupWithManager, so every replica drains the two streams, not only the
// leader.
//
// Each gets its own recorder name, so `kubectl get events` attributes a
// projected domain event and a dead letter to different reporting
// controllers.
//
// The projector gets the DLQ reader the clustarr.io/replay handler reads
// with, to record which sequence to replay (clustarr.io/dead-letter-seq).
// The in-memory bus keeps no stream to read back, so on it the projector
// leaves the sequence out. busconn.Connect returns a JetStream-backed bus,
// and the agent's domain bus forwards its JetStream(), so in production it is
// always wired.
//
// The RSS matcher shares the catalog domain's seams (catalog.buildWorkers'
// doc): the installed topology, the uncached reader for the double-grab
// guard, and its own TheXEM source.
func buildWorkers(mgr ctrl.Manager, bus events.Bus, o Options) (workers, error) {
	c := mgr.GetClient()
	topo := o.BusTopology()
	sceneMaps, err := catalogagent.NewSceneMaps()
	if err != nil {
		return workers{}, err
	}
	rss := rssmatcher.NewHandler(rssmatcher.Deps{
		Client: c, Reader: mgr.GetAPIReader(), Bus: bus, Topology: &topo,
		SceneMaps: sceneMaps, Catalogue: catalogue.LoadedCatalogue(),
	})
	rd := redownload.NewHandler(c, bus)
	rd.Topology = &topo
	reader, _ := history.DLQReaderFor(bus)
	return workers{
		rss:        rss,
		redownload: rd,
		sink:       historyworker.NewSink(historyworker.SinkDeps{Recorder: mgr.GetEventRecorder("catalogarr-history")}),
		dlq: historyworker.NewDLQProjector(historyworker.DLQDeps{
			Client: c, Recorder: mgr.GetEventRecorder("clustarr-dlq-projector"), DLQ: reader,
		}),
	}, nil
}
