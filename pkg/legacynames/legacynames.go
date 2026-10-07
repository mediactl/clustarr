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

// Package legacynames is the table of names chart 0.4.x (the per-service
// release) used and release N replaces; the only Go code allowed to spell one
// (docs/superpowers/specs/2026-10-07-unbranded-names-design.md §3, §7).
//
// It holds data, not behaviour: the Migrator, restore-names, the topology's
// displaced and handover entries, the read-both helpers, the report and the
// tests all read one list. Every value here is one main deploys (spec §2,
// ruling U-R1); a name only the unify branch ever had is not legacy and is
// renamed outright (R-U7). Release N+1 deletes everything but ValuesKeys and
// the three idle durables it retires (spec §4.9).
package legacynames

import "strings"

// Token is one brand of the naming rule (spec §1): Old is the retired
// spelling, New the domain name that replaces it. Case variants follow
// (Catalogarr -> Catalog, CATALOGARR -> CATALOG).
type Token struct {
	Old, New string
}

// Tokens are the seven brands and the derived "squash", in the token map's
// order (spec §1, the plan's Global Constraints): "squasharr" precedes
// "squash", so a rewrite applying them in order never splits a brand.
// The transcode pool durables' "squasharr-transcode-" is not a token
// (ruling R-U2): Durables and PoolDurablePrefix carry it.
var Tokens = []Token{
	{Old: "catalogarr", New: "catalog"},
	{Old: "importarr", New: "import"},
	{Old: "indexarr", New: "index"},
	{Old: "grabarr", New: "grab"},
	{Old: "squasharr", New: "transcode"},
	{Old: "captionarr", New: "caption"},
	{Old: "segmentarr", New: "markers"},
	{Old: "squash", New: "transcode"},
}

// FieldManagers are the 17 server-side-apply field managers release N
// renames, old -> new (spec §2.5). The Migrator rewrites each managedFields
// entry from the key to the value (spec §4.1); restore-names inverts it.
var FieldManagers = map[string]string{
	"catalogarr":          "catalog",
	"catalogarr-series":   "catalog-series",
	"catalogarr-worker":   "catalog-worker",
	"catalogarr-metadata": "catalog-metadata",
	"catalogarr-grab":     "catalog-grab",
	"catalogarr-fanout":   "catalog-fanout",
	"catalogarr-artwork":  "catalog-artwork",
	"catalogarr-classify": "catalog-classify",
	"importarr":           "import",
	"importarr-worker":    "import-worker",
	"indexarr":            "index",
	"indexarr-worker":     "index-worker",
	"grabarr":             "grab",
	"squasharr":           "transcode",
	"squasharr-pool":      "transcode-pool",
	"captionarr":          "caption",
	"captionarr-worker":   "caption-worker",
}

// The two retired field managers (ruling R-U3): no release after N-1 writes
// under either, so neither is ever renamed.
const (
	// MarkersStatusManager was the metadata gateway's marker worker, the
	// writer of MediaFile status.markers; release N's one release apply
	// (F8.4) runs under it.
	MarkersStatusManager = "catalogarr-markers"
	// EngineTelemetryManager was a torrent or usenet engine pod's, which
	// applied Download status telemetry (retired by ADR-0019 A3.6).
	EngineTelemetryManager = "grabarr-engine"
)

// Streams are the six JetStream work streams release N displaces, old ->
// new (spec §2.6, §4.2.1).
var Streams = map[string]string{
	"CLUSTARR_WORK_CATALOGARR": "CLUSTARR_WORK_CATALOG",
	"CLUSTARR_WORK_IMPORTARR":  "CLUSTARR_WORK_IMPORT",
	"CLUSTARR_WORK_INDEXARR":   "CLUSTARR_WORK_INDEX",
	"CLUSTARR_WORK_CAPTIONARR": "CLUSTARR_WORK_CAPTION",
	"CLUSTARR_WORK_SQUASHARR":  "CLUSTARR_WORK_TRANSCODE",
	"CLUSTARR_WORK_SEGMENTARR": "CLUSTARR_WORK_MARKERS",
}

// SubjectTokens are the five work-subject service tokens, old -> new: the
// third token of a clustarr.work.<token>.…, clustarr.rpc.<token>.… or
// clustarr.dlq.<token>.… subject (spec §2.6). The transcode stream's
// subjects already read clustarr.work.transcode.
var SubjectTokens = map[string]string{
	"catalogarr": "catalog",
	"importarr":  "import",
	"indexarr":   "index",
	"captionarr": "caption",
	"segmentarr": "markers",
}

// Durables are the 14 static durables on displaced streams that release N
// creates anew under the new name, old -> new (spec §2.6). The legacy ones
// go with their streams.
var Durables = map[string]string{
	"catalogarr-search-high":    "catalog-search-high",
	"catalogarr-search-normal":  "catalog-search-normal",
	"catalogarr-metadata":       "catalog-metadata",
	"catalogarr-artwork-fetch":  "catalog-artwork-fetch",
	"catalogarr-artwork-render": "catalog-artwork-render",
	"importarr-scan":            "import-scan",
	"importarr-list":            "import-list",
	"importarr-fileimport":      "import-fileimport",
	"indexarr-rss":              "index-rss",
	"captionarr-fetch-high":     "caption-fetch-high",
	"captionarr-fetch-normal":   "caption-fetch-normal",
	"catalogarr-markers":        "catalog-markers",
	"catalogarr-segments-plan":  "catalog-segments-plan",
	"segmentarr-analyze":        "markers-analyze",
}

// Handover are the two durables on kept event streams, old -> new: release
// N creates the new one at the legacy one's ack floor + 1 and leaves the
// legacy one idle for a rollback (spec §4.2.3).
var Handover = map[string]string{
	"catalogarr-history":     "catalog-history",
	"catalogarr-rss-matcher": "catalog-rss-matcher",
}

// The durables release N keeps under their legacy names (ruling R-U3).
const (
	// GrabDurable was the grab worker's, on CLUSTARR_WORK_CATALOGARR; it goes
	// with its displaced stream (spec §4.2.2).
	GrabDurable = "catalogarr-grab"
	// RedownloadDurable is the redownload consumer on CLUSTARR_EVENTS,
	// draining through N (A-plan R22), deleted in N+1 (A9.2).
	RedownloadDurable = "catalogarr-redownload"
	// TranscodeResultsDurable was the transcode results consumer on
	// CLUSTARR_WORK_SQUASHARR (loop §7.3.9); it goes with its stream.
	TranscodeResultsDurable = "squasharr-transcode-results"
	// SegmentsResultDurable was the segment results consumer on
	// CLUSTARR_WORK_SEGMENTARR (loop §4.12); it goes with its stream.
	SegmentsResultDurable = "catalogarr-segments-result"
	// PoolDurablePrefix began every transcode pool's durable,
	// "<prefix><profile>-<class>" (ruling R-U2: the new family is
	// "transcode-pool-").
	PoolDurablePrefix = "squasharr-transcode-"
)

// Schemas are the Clustarr-Schema values main publishes that release N
// renames, old -> new (spec §2.6, §4.2.5): a dead letter N-1 wrote still
// decodes through LegacySchemas.
var Schemas = map[string]string{
	"importarr.ScanTask.v1": "import.ScanTask.v1",
	"importarr.ScanTask.v2": "import.ScanTask.v2",
	"importarr.ListTask.v1": "import.ListTask.v1",
	"importarr.ListTask.v2": "import.ListTask.v2",
}

// LeaseIDs are the per-service leader-election leases manager.clustarr.io
// replaces (spec 2026-10-06 §5.8); the legacy-lease gate reads them, and the
// runbook deletes them.
var LeaseIDs = []string{
	"catalogarr.clustarr.io", "importarr.clustarr.io", "indexarr.clustarr.io",
	"grabarr.clustarr.io", "squasharr.clustarr.io", "captionarr.clustarr.io",
}

// Labels, annotations, finalizers and object names chart 0.4.x wrote (spec
// §2.7, §2.10).
const (
	// TaskWithdrawalFinalizer is the TranscodeJob finalizer (ruling R-U3:
	// kept; the Migrator removes it and the kind goes in N+1).
	TaskWithdrawalFinalizer = "squasharr.clustarr.io/task-withdrawal"
	// PoolManagedBy is the app.kubernetes.io/managed-by value on N-1's pool,
	// graft and reduce Jobs, which N deletes (spec §4.4).
	PoolManagedBy = "squasharr"
	// GraftLabel is the label key N-1's graft and reduce Jobs carry.
	GraftLabel = "squasharr.clustarr.io/audiograft"
	// GraftComponent is N-1's graft Jobs' app.kubernetes.io/component.
	GraftComponent = "squasharr-graft"
	// EngineComponent is N-1's engine workloads' app.kubernetes.io/component,
	// part of their immutable selector: N replaces such a workload.
	EngineComponent = "grabarr-engine"
	// EngineServiceAccount is the engine pods' ServiceAccount in chart 0.4.x
	// (kustomize's name; the chart prefixes the release's fullname).
	EngineServiceAccount = "grabarr-engine"
	// FacadeSecret is the Torznab facade's API-key Secret's default name.
	FacadeSecret = "indexarr-facade"
	// FacadeSecretSuffix is the chart's "<fullname>-indexarr-facade" suffix.
	FacadeSecretSuffix = "-indexarr-facade"
)

// SearchOutcomeNames are the status.indexerOutcomes names the search worker
// reserved, old -> new; the Search reconciler reads both through release N
// (spec §2.9, §4.6).
var SearchOutcomeNames = map[string]string{
	"catalogarr/search-worker": "catalog/search-worker",
	"catalogarr/truncated":     "catalog/truncated",
}

// SearchOutcomePrefix began every reserved outcome name.
const SearchOutcomePrefix = "catalogarr/"

// ValuesKeys are chart 0.4.x's ten top-level per-service values keys, which
// chart 0.5.0's closed schema rejects (spec §2.11, §4.7).
var ValuesKeys = []string{
	"catalogarr", "catalogarrMetadata", "importarr", "importarrWorker",
	"indexarr", "grabarr", "squasharr", "captionarr", "captionarrWorker",
	"segmentarrWorker",
}

// Subject maps a legacy subject onto release N's: the third token of a
// clustarr.work.…, clustarr.dlq.… or clustarr.rpc.… subject goes through
// SubjectTokens (the DLQ's durable-derived "catalogarr" service token is
// one of them). Any other subject, and one already new, is returned as is.
func Subject(s string) string {
	parts := strings.SplitN(s, ".", 4)
	if len(parts) < 3 || parts[0] != "clustarr" {
		return s
	}
	switch parts[1] {
	case "work", "dlq", "rpc":
	default:
		return s
	}
	n, ok := SubjectTokens[parts[2]]
	if !ok {
		return s
	}
	parts[2] = n
	return strings.Join(parts, ".")
}

// Manager returns the name release N gives the legacy field manager old,
// and whether old is one it renames (a retired manager is not).
func Manager(old string) (string, bool) {
	n, ok := FieldManagers[old]
	return n, ok
}

// LegacyManager returns the chart 0.4.x name of release N's field manager
// name, and whether it had one.
func LegacyManager(name string) (string, bool) {
	for o, n := range FieldManagers {
		if n == name {
			return o, true
		}
	}
	return "", false
}
