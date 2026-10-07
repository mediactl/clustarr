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

// Package agentdomain is the table of agent domains (spec 2026-10-06
// §9.1.1, §9.2): which durable consumers each domain drains, whether the
// manager's HPA reconciler autoscales it and from what floor, which other
// topology consumers live elsewhere (Fixed), and which consumers keep
// MaxAckPending at one pod's slots because a limiter downstream bounds
// them (Throttled). The autoscale reconciler, the External Metrics API, the
// agent's domain wiring and the guards all read it. It imports only
// pkg/events.
package agentdomain

import "github.com/mediactl/clustarr/pkg/events"

// The autoscaling contract between the installers and the manager
// (§10.2.2).
const (
	// LabelDomain marks an autoscaled Deployment with its domain; the
	// reconciler selects Deployments by it.
	LabelDomain = "autoscale.clustarr.io/domain"
	// AnnotationPaused "true" deletes the domain's HPA and leaves its
	// replicas alone: the supported way to pause an agent.
	AnnotationPaused = "autoscale.clustarr.io/paused"
	// AnnotationMinReplicas raises the HPA's minReplicas above the
	// domain's floor.
	AnnotationMinReplicas = "autoscale.clustarr.io/min-replicas"
	// MetricConsumerLag is the one external metric: NumPending +
	// NumAckPending of a durable, labelled stream and consumer.
	MetricConsumerLag = "clustarr_consumer_lag"
)

// Domain names: `agent --domain <name>`, and markers, its own binary.
const (
	Catalog       = "catalog"
	Events        = "events"
	Metadata      = "metadata"
	Import        = "import"
	Index         = "index"
	Caption       = "caption"
	TorrentEngine = "torrent-engine"
	UsenetEngine  = "usenet-engine"
	Markers       = "markers"
)

// Domain is one row of the table.
type Domain struct {
	// Name is the --domain value and the LabelDomain value.
	Name string
	// Autoscaled is true when the manager owns an HPA for the domain's
	// Deployment; false means fixed replicas.
	Autoscaled bool
	// MinReplicas is the HPA floor; AnnotationMinReplicas may raise it.
	MinReplicas int32
	// Consumers are the static durables the domain drains.
	Consumers []string
}

// Domains is every agent domain and markers, in §3.5.3's order. A fresh
// slice on every call.
func Domains() []Domain {
	return []Domain{
		{Name: Catalog, Autoscaled: true, Consumers: []string{
			events.ConsumerCatalogSearchHigh, events.ConsumerCatalogSearchNorm,
			events.ConsumerCatalogGrab, events.ConsumerCatalogArtworkRender,
		}},
		// events stays at one or more: its three Limits streams are memory
		// with discard-old on single-node NATS (§3.5.4, OD30).
		{Name: Events, Autoscaled: true, MinReplicas: 1, Consumers: []string{
			events.ConsumerCatalogRSSMatcher, events.ConsumerCatalogRedownload,
			events.ConsumerCatalogHistory, events.ConsumerDLQProjector,
		}},
		// One replica: RPC responders and in-process provider limiters
		// (ADR-0007).
		{Name: Metadata, Consumers: []string{
			events.ConsumerCatalogMetadata, events.ConsumerCatalogArtworkFetch,
			events.ConsumerCatalogMarkers,
		}},
		// The R3 probe queue's two lanes ride with the import consumers: a
		// ProbeVersion raise's backlog scales the import domain out.
		{Name: Import, Autoscaled: true, Consumers: []string{
			events.ConsumerImportScan, events.ConsumerImportFile, events.ConsumerImportList,
			events.ConsumerImportRecycle,
			events.ConsumerImportProbeHigh, events.ConsumerImportProbeLow,
		}},
		// One replica: the release index, the RPC responders, the facade
		// and the per-host limiter (OD15).
		{Name: Index, Consumers: []string{events.ConsumerIndexRSS}},
		{Name: Caption, Autoscaled: true, Consumers: []string{
			events.ConsumerCaptionFetchHigh, events.ConsumerCaptionFetchNormal,
		}},
		// Rendered per DownloadClient by the manager; no durables.
		{Name: TorrentEngine},
		{Name: UsenetEngine},
		{Name: Markers, Autoscaled: true, Consumers: []string{events.ConsumerSegmentarrAnalyze}},
	}
}

// Lookup returns the named domain.
func Lookup(name string) (Domain, bool) {
	for _, d := range Domains() {
		if d.Name == name {
			return d, true
		}
	}
	return Domain{}, false
}

// Fixed is every other static topology consumer, with why it is homed
// outside the agent domains. The transcode pools' durables are dynamic
// (events.TranscodeTaskConsumerName) and are not in events.Default().
func Fixed() map[string]string {
	return map[string]string{
		events.ConsumerCatalogSegmentsPlan: "manager: the segment planner reads the manager's Episode and MediaFile indexes (app/catalog/segmentplan, registered by app/catalog/manager)",
		events.ConsumerSquasharrResults:    "manager: squasharr's ResultsConsumer shares TranscodeJob.status's one compare-and-swap path with its reconciler",
		events.ConsumerIntakeCandidate:     "manager: the leader-only candidate inbox, acked after the owner's pass decides (app/intake, ADR-0019 §8.4)",
		events.ConsumerIntakeScan:          "manager: the leader-only scan intake, acked after the scan applier writes (app/intake, ADR-0019 §8.4)",
	}
}

// Throttled is every autoscaled consumer whose throughput a limiter
// downstream sets, mapped to that limiter. Its MaxAckPending stays at one
// pod's slots, so its domain's HPA scales 0..1 (§9.1.1, OD46): more
// replicas would add pods the limiter cannot feed, and for search would
// turn queued work into paced skips that cost an item its live search.
func Throttled() map[string]string {
	const indexarr = "indexarr's per-host limiter in the one index agent (app/indexer/search/fanout.go): every query waits its requestDelay inside a 45 s budget"
	const provider = "the shared subtitle-provider token bucket (app/caption/throttle, KV clustarr-provider-throttle)"
	return map[string]string{
		events.ConsumerCatalogSearchHigh:  indexarr,
		events.ConsumerCatalogSearchNorm:  indexarr,
		events.ConsumerCatalogGrab:        indexarr + "; a grab fetches its payload through rpc.indexarr.download",
		events.ConsumerCaptionFetchHigh:   provider,
		events.ConsumerCaptionFetchNormal: provider,
	}
}
