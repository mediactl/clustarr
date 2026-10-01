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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Segment detection (spec 2026-10-01): plans, analysis tasks and results
// ride the catalogarr work stream; raw results and fingerprints are kept on
// file storage, since only re-analysis rebuilds them.
func TestSegmentsTopology(t *testing.T) {
	top := Default()
	require.NoError(t, top.Validate())

	for _, c := range []struct {
		name, filter string
		maxAck       int
	}{
		{ConsumerCatalogSegmentsPlan, FilterCatalogSegmentsPlan, 8},
		{ConsumerSegmentarrAnalyze, FilterCatalogSegmentsAnalyze, 4},
		{ConsumerCatalogSegmentsResult, FilterCatalogSegmentsResult, 32},
	} {
		spec, ok := top.Consumer(c.name)
		require.True(t, ok, c.name)
		assert.Equal(t, StreamWorkCatalogarr, spec.Stream, c.name)
		assert.Equal(t, []string{c.filter}, spec.Filters, c.name)
		assert.Equal(t, c.maxAck, spec.MaxAckPending, c.name)
	}
	analyze, _ := top.Consumer(ConsumerSegmentarrAnalyze)
	assert.Equal(t, 30*time.Minute, analyze.AckWait, "a season task runs up to 30 min, with heartbeats")

	assert.Equal(t, "clustarr.work.catalogarr.segments-plan.normal.media-dexter-s01", WorkSegmentsPlanSubject("media/dexter-s01"))
	assert.Equal(t, "clustarr.work.catalogarr.segments-analyze.normal.k", WorkSegmentsAnalyzeSubject("k"))
	assert.Equal(t, "clustarr.work.catalogarr.segments-result.normal.k", WorkSegmentsResultSubject("k"))
	_, err := ScheduleSubject(WorkSegmentsPlanSubject("media/dexter-s01"))
	require.NoError(t, err, "a plan is scheduled 5 min ahead")

	var bucket *BucketSpec
	for i := range top.Buckets {
		if top.Buckets[i].Name == BucketSegments {
			bucket = &top.Buckets[i]
		}
	}
	require.NotNil(t, bucket, BucketSegments)
	assert.True(t, bucket.Durable)

	var store *ObjectStoreSpec
	for i := range top.ObjectStores {
		if top.ObjectStores[i].Name == ObjectStoreFingerprints {
			store = &top.ObjectStores[i]
		}
	}
	require.NotNil(t, store, ObjectStoreFingerprints)
	assert.Equal(t, StorageFile, store.Storage)
	assert.EqualValues(t, GiB, store.MaxBytes)
	assert.Equal(t, 90*24*time.Hour, store.MaxAge)
	assert.Equal(t, 90*24*time.Hour, ObjectStoreConfig(*store).TTL, "Ensure creates it with the age limit")
}
