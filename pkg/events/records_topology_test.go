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

package events_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

func findBucket(t *testing.T, top events.Topology, name string) events.BucketSpec {
	t.Helper()
	for _, b := range top.Buckets {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("bucket %s is missing", name)
	return events.BucketSpec{}
}

// bucketCaps is one records bucket's row of a table.
type bucketCaps struct {
	ttl      time.Duration
	maxBytes int64
	maxValue int32
}

// assertRecordsBuckets holds every bucket of want to the records rule and its
// row, on the production topology and on a single node, and returns the
// reservations they total.
func assertRecordsBuckets(t *testing.T, top events.Topology, want map[string]bucketCaps) int64 {
	t.Helper()
	var total int64
	for name, w := range want {
		b := findBucket(t, top, name)
		assert.True(t, b.Records, name)
		assert.True(t, b.Durable, "%s: a records bucket outlives a NATS restart", name)
		assert.EqualValues(t, 1, b.History, name)
		assert.Equal(t, events.StorageFile, b.Storage, name)
		assert.Equal(t, 3, b.Replicas, name)
		assert.Equal(t, w.ttl, b.TTL, name)
		assert.Equal(t, w.maxBytes, b.MaxBytes, name)
		assert.Equal(t, w.maxValue, b.MaxValueSize, name)
		assert.Equal(t, 5*time.Minute, b.LimitMarkerTTL, name)
		cfg := events.KeyValueConfig(b)
		assert.Equal(t, w.maxBytes, cfg.MaxBytes, "%s: Ensure creates it with its MaxBytes", name)
		assert.Equal(t, w.maxValue, cfg.MaxValueSize, "%s: and its MaxValueSize", name)
		single := findBucket(t, top.ForSingleNode(), name)
		assert.Equal(t, events.StorageFile, single.Storage, "%s stays on file on a single node", name)
		assert.Equal(t, w.maxBytes, single.MaxBytes, "%s keeps its cap on a single node", name)
		assert.Equal(t, 1, single.Replicas, name)
		total += b.MaxBytes
	}
	return total
}

// TestRecordsBucketsAreDurableHistoryOneAndBounded is loop spec §4.3's table:
// the remediation loop's six records buckets. The records buckets in all are
// those six and ADR-0019's six agent-written ones
// (TestAgentRecordBucketsAreDurableHistoryOneAndBounded).
func TestRecordsBucketsAreDurableHistoryOneAndBounded(t *testing.T) {
	top := events.Default()
	require.NoError(t, top.Validate())
	week := 7 * 24 * time.Hour
	want := map[string]bucketCaps{
		events.BucketProbes:     {week, 256 * events.MiB, 256 * events.KiB},
		events.BucketTranscodes: {week, 128 * events.MiB, 32 * events.KiB},
		events.BucketGrafts:     {week, 16 * events.MiB, 16 * events.KiB},
		events.BucketSubtitles:  {week, 128 * events.MiB, 8 * events.KiB},
		events.BucketMarkers:    {week, 64 * events.MiB, 16 * events.KiB},
		events.BucketSegments:   {0, 128 * events.MiB, 16 * events.KiB},
	}
	agents := map[string]bool{
		events.BucketTransfers: true, events.BucketEngines: true, events.BucketImports: true,
		events.BucketSearches: true, events.BucketItemMetadata: true, events.BucketIndexerHealth: true,
	}
	for _, b := range top.Buckets {
		if b.Records {
			_, loop := want[b.Name]
			assert.True(t, loop || agents[b.Name], "%s is a records bucket neither table names", b.Name)
		}
	}
	assert.EqualValues(t, 720*events.MiB, assertRecordsBuckets(t, top, want), "§4.3: the loop's reservations total 720 MiB")
	assert.Empty(t, top.Retired, "the cold cutover deletes the old durables; nothing is retired")
}

// Validate holds every Records bucket to §4.15's rule.
func TestValidateHoldsEveryRecordsBucketToTheRule(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mut   func(*events.BucketSpec)
		field string
	}{
		{"not durable", func(b *events.BucketSpec) { b.Durable = false }, ".Durable"},
		{"history kept", func(b *events.BucketSpec) { b.History = 2 }, ".History"},
		{"unbounded", func(b *events.BucketSpec) { b.MaxBytes = 0 }, ".MaxBytes"},
		{"any value size", func(b *events.BucketSpec) { b.MaxValueSize = 0 }, ".MaxValueSize"},
		{"a TTL shorter than a request timeout", func(b *events.BucketSpec) { b.TTL = 24 * time.Hour }, ".TTL"},
		{"a TTL without limit markers", func(b *events.BucketSpec) { b.LimitMarkerTTL = 0 }, ".LimitMarkerTTL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := events.Default()
			for i := range top.Buckets {
				if top.Buckets[i].Name == events.BucketTranscodes {
					tc.mut(&top.Buckets[i])
				}
			}
			err := top.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), events.BucketTranscodes+tc.field)
		})
	}
	top := events.Default()
	for i := range top.Buckets {
		if top.Buckets[i].Name == events.BucketTranscodes {
			top.Buckets[i].TTL = 0
		}
	}
	require.NoError(t, top.Validate(), "TTL 0 keeps records forever, as clustarr-segments does")

	top = events.Default()
	top.Buckets = append(top.Buckets, events.BucketSpec{Name: "clustarr-not-records", History: 5, TTL: time.Hour, LimitMarkerTTL: time.Minute})
	require.NoError(t, top.Validate(), "the rule binds records buckets only")
}

// Topology.Retired names a durable a release removed, on a stream that stays.
// Default() retires nothing (the cold cutover's runbook deletes the old
// durables), but Ensure still deletes and purges whatever a release names, so
// Validate still holds each entry.
func TestValidateChecksRetiredConsumers(t *testing.T) {
	ok := events.Default()
	ok.Retired = []events.RetiredConsumer{{Stream: events.StreamWorkMarkers, Durable: "catalog-example-retired", Purge: "clustarr.work.markers.result.>"}}
	require.NoError(t, ok.Validate())
	assert.Equal(t, ok.Retired, ok.ForSingleNode().Retired, "ForSingleNode keeps the retired list")
	noPurge := events.Default()
	noPurge.Retired = []events.RetiredConsumer{{Stream: events.StreamWorkMarkers, Durable: "catalog-example-retired"}}
	require.NoError(t, noPurge.Validate(), "a purge is optional")
	for _, tc := range []struct {
		name string
		r    events.RetiredConsumer
		want string
	}{
		{"no durable", events.RetiredConsumer{Stream: events.StreamWorkMarkers}, "is required"},
		{"an unknown stream", events.RetiredConsumer{Stream: "CLUSTARR_NO_SUCH", Durable: "x"}, "unknown stream"},
		{"still a consumer", events.RetiredConsumer{Stream: events.StreamDLQ, Durable: events.ConsumerDLQProjector}, "both a consumer and retired"},
		{"a purge outside the stream", events.RetiredConsumer{Stream: events.StreamWorkMarkers, Durable: "x", Purge: "clustarr.work.transcode.result.>"}, "outside stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := events.Default()
			top.Retired = []events.RetiredConsumer{tc.r}
			err := top.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestMergeRetiredKeepsTheReportThatSawTheDurable(t *testing.T) {
	r := events.RetiredConsumer{Stream: "CLUSTARR_WORK_EXAMPLE", Durable: "example-retired"}
	first := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	seen := map[string]events.RetiredReport{}
	events.MergeRetired(seen, []events.RetiredReport{{RetiredConsumer: r, Existed: true, Lag: 3, At: first}})
	events.MergeRetired(seen, []events.RetiredReport{{RetiredConsumer: r, At: first.Add(time.Minute)}})
	got := events.SortedRetired(seen)
	require.Len(t, got, 1)
	assert.True(t, got[0].Existed)
	assert.EqualValues(t, 3, got[0].Lag)
	assert.Equal(t, first, got[0].At, "the first report that found it")

	gone := events.RetiredConsumer{Stream: "CLUSTARR_WORK_EXAMPLE", Durable: "another-retired"}
	events.MergeRetired(seen, []events.RetiredReport{{RetiredConsumer: gone, At: first}})
	events.MergeRetired(seen, []events.RetiredReport{{RetiredConsumer: gone, At: first.Add(time.Hour)}})
	other := events.RetiredConsumer{Stream: "CLUSTARR_AAA", Durable: "z"}
	events.MergeRetired(seen, []events.RetiredReport{{RetiredConsumer: other}})
	got = events.SortedRetired(seen)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"CLUSTARR_AAA/z", "CLUSTARR_WORK_EXAMPLE/another-retired", "CLUSTARR_WORK_EXAMPLE/example-retired"},
		[]string{got[0].Stream + "/" + got[0].Durable, got[1].Stream + "/" + got[1].Durable, got[2].Stream + "/" + got[2].Durable},
		"sorted by stream, then durable")
	assert.False(t, got[1].Existed)
	assert.Equal(t, first.Add(time.Hour), got[1].At, "never found: the latest report")
}
