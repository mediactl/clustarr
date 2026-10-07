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

package events

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// The records buckets the remediation loop requests through (loop spec
// 2026-10-06 §4.3): one per remediation, keyed by RecordKey (subtitles by
// RecordSubKey), every write a compare-and-swap (pkg/records), nobody
// deleting a record. clustarr-probes (probe.go) and clustarr-segments
// (topology.go) are records buckets too.
const (
	BucketTranscodes = "clustarr-transcodes"
	BucketGrafts     = "clustarr-grafts"
	BucketSubtitles  = "clustarr-subtitles"
	BucketMarkers    = "clustarr-markers"
)

// Each records bucket's caps (§4.3). The reservations total 720 MiB, all on
// the file store (they are Durable), against config/nats' 20 GiB.
const (
	ProbesMaxBytes         = 256 * MiB
	ProbesMaxValueSize     = 256 * KiB
	TranscodesMaxBytes     = 128 * MiB
	TranscodesMaxValueSize = 32 * KiB
	GraftsMaxBytes         = 16 * MiB
	GraftsMaxValueSize     = 16 * KiB
	SubtitlesMaxBytes      = 128 * MiB
	SubtitlesMaxValueSize  = 8 * KiB
	MarkersMaxBytes        = 64 * MiB
	MarkersMaxValueSize    = 16 * KiB
	SegmentsMaxBytes       = 128 * MiB
	SegmentsMaxValueSize   = 16 * KiB
)

const (
	// RecordsTTL retires an inbox record nobody rewrote for a week; it
	// outlives every request timeout.
	RecordsTTL = 7 * 24 * time.Hour
	// RecordsMinTTL is the shortest TTL Validate accepts on a records bucket.
	RecordsMinTTL = 7 * 24 * time.Hour
)

// The records buckets ADR-0019 adds (design §4.3): one writer per key, the
// agent, through records.Writer; the manager reads them through
// records.Reader and never writes there.
const (
	// BucketTransfers: one transfer record per grab entry UID, by the
	// engine holding the transfer.
	BucketTransfers = "clustarr-transfers"
	// BucketEngines: one engine record per DownloadClient UID and ordinal.
	BucketEngines = "clustarr-engines"
	// BucketImports: one inspect and one execute record per entry UID.
	BucketImports = "clustarr-imports"
	// BucketSearches: one search record per task UID.
	BucketSearches = "clustarr-searches"
	// BucketItemMetadata: one metadata record per item UID.
	BucketItemMetadata = "clustarr-item-metadata"
	// BucketIndexerHealth: one health record per Indexer UID.
	BucketIndexerHealth = "clustarr-indexer-health"
)

// Each agent-written records bucket's caps (design §4.3). The reservations
// total 1,040 MiB, all on the file store (they are Durable).
const (
	TransfersMaxBytes         = 256 * MiB
	TransfersMaxValueSize     = 512 * KiB
	EnginesMaxBytes           = 8 * MiB
	EnginesMaxValueSize       = 16 * KiB
	ImportsMaxBytes           = 128 * MiB
	ImportsMaxValueSize       = 512 * KiB
	SearchesMaxBytes          = 128 * MiB
	SearchesMaxValueSize      = 512 * KiB
	ItemMetadataMaxBytes      = 512 * MiB
	ItemMetadataMaxValueSize  = 512 * KiB
	IndexerHealthMaxBytes     = 8 * MiB
	IndexerHealthMaxValueSize = 8 * KiB
)

// recordBucket is one records bucket's spec: Durable, History 1, file
// storage, limit markers, RecordsTTL, bounded (loop spec §4.3).
func recordBucket(name, desc string, maxBytes int64, maxValue int32) BucketSpec {
	return BucketSpec{
		Name: name, Description: desc, TTL: RecordsTTL, History: 1, Storage: StorageFile, Replicas: 3,
		LimitMarkerTTL: 5 * time.Minute, Durable: true, Records: true, MaxBytes: maxBytes, MaxValueSize: maxValue,
	}
}

// agentRecordBuckets are ADR-0019's six agent-written records buckets.
func agentRecordBuckets() []BucketSpec {
	r := recordBucket
	return []BucketSpec{
		r(BucketTransfers, "One transfer record per grab entry, written by the engine holding it.", TransfersMaxBytes, TransfersMaxValueSize),
		r(BucketEngines, "One engine record per DownloadClient instance, written by that engine pod.", EnginesMaxBytes, EnginesMaxValueSize),
		r(BucketImports, "One inspect and one execute record per grab entry, written by the import agent.", ImportsMaxBytes, ImportsMaxValueSize),
		r(BucketSearches, "One search record per search task, written by the search agent.", SearchesMaxBytes, SearchesMaxValueSize),
		r(BucketItemMetadata, "One metadata record per catalog item, written by the metadata gateway.", ItemMetadataMaxBytes, ItemMetadataMaxValueSize),
		r(BucketIndexerHealth, "One health record per Indexer, written by the index agent.", IndexerHealthMaxBytes, IndexerHealthMaxValueSize),
	}
}

func recordBuckets() []BucketSpec {
	r := recordBucket
	return []BucketSpec{
		r(BucketTranscodes, "One transcode record per MediaFile UID: the loop's request, the pool worker's claim and answer.", TranscodesMaxBytes, TranscodesMaxValueSize),
		r(BucketGrafts, "One graft record per MediaFile UID: the loop's request, the graft Job's answer.", GraftsMaxBytes, GraftsMaxValueSize),
		r(BucketSubtitles, "One subtitle record per MediaFile UID and language key: the loop's request, the caption domain's answer.", SubtitlesMaxBytes, SubtitlesMaxValueSize),
		r(BucketMarkers, "One TheIntroDB markers record per MediaFile UID: the loop's request, the metadata domain's answer.", MarkersMaxBytes, MarkersMaxValueSize),
	}
}

// RetiredConsumer is a durable a release removed. Ensure deletes it, and its
// dead-letter watcher, and purges Purge (when set) from Stream, every time
// and idempotently, so a results stream nobody reads can never fill.
type RetiredConsumer struct {
	Stream  string
	Durable string
	Purge   string
}

// RetiredReport is what Ensure found of one retired durable.
type RetiredReport struct {
	RetiredConsumer
	// Existed is whether the durable was still on the broker.
	Existed bool
	// Lag is its Pending + AckPending then: results nobody will apply
	// (loop spec §7.3.9; the pre-deploy drain gate keeps it at zero).
	Lag uint64
	// At is when Ensure retired it.
	At time.Time
}

// RetiredReporter is a bus that remembers what its Ensure retired; the
// release-N Migrator (F8.3) counts an undrained one.
type RetiredReporter interface {
	RetiredReports() []RetiredReport
}

// Retire reads each retired durable's lag, deletes it (DeleteSubscription
// takes its dead-letter watcher too) and purges its filter. A durable or
// stream already gone is not an error.
func Retire(ctx context.Context, admin StreamAdmin, retired []RetiredConsumer, now time.Time) ([]RetiredReport, error) {
	var out []RetiredReport
	var errs []error
	for _, r := range retired {
		rep := RetiredReport{RetiredConsumer: r, At: now}
		st, err := admin.ConsumerState(ctx, r.Stream, r.Durable)
		switch {
		case err == nil:
			rep.Existed, rep.Lag = true, st.Lag()
		case errors.Is(err, ErrConsumerNotFound), errors.Is(err, ErrStreamNotFound):
		default:
			errs = append(errs, fmt.Errorf("events: retire %s/%s: %w", r.Stream, r.Durable, err))
			continue
		}
		if err := admin.DeleteSubscription(ctx, r.Stream, r.Durable); err != nil {
			errs = append(errs, fmt.Errorf("events: retire %s/%s: %w", r.Stream, r.Durable, err))
			continue
		}
		if r.Purge != "" {
			if err := admin.PurgeSubject(ctx, r.Stream, r.Purge); err != nil && !errors.Is(err, ErrStreamNotFound) {
				errs = append(errs, fmt.Errorf("events: retire %s/%s: purge %s: %w", r.Stream, r.Durable, r.Purge, err))
				continue
			}
		}
		out = append(out, rep)
	}
	return out, errors.Join(errs...)
}

// MergeRetired keeps, per retired durable, the first report that found it on
// the broker, else the latest.
func MergeRetired(seen map[string]RetiredReport, reports []RetiredReport) {
	for _, r := range reports {
		k := r.Stream + "/" + r.Durable
		if prev, ok := seen[k]; ok && prev.Existed {
			continue
		}
		seen[k] = r
	}
}

// SortedRetired lists seen by stream and durable.
func SortedRetired(seen map[string]RetiredReport) []RetiredReport {
	out := make([]RetiredReport, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b RetiredReport) int {
		return cmp.Or(cmp.Compare(a.Stream, b.Stream), cmp.Compare(a.Durable, b.Durable))
	})
	return out
}
