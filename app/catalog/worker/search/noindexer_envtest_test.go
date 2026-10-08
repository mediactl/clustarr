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

package search_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// skipped is index's outcome for an indexer it did not query, for reason.
func skipped(name, reason string) schema.SearchOutcome {
	return schema.SearchOutcome{IndexerRef: schema.Ref{Name: name}, IndexerName: name, Status: schema.SearchOutcomeSkipped, Error: reason}
}

// TestWorkerMarksASearchNoIndexerCouldAnswer is smoke finding 7: a live
// search index answered with no indexer -- none in the namespace, or every
// one disabled, unprobed or in backoff -- is recorded NoIndexer, which the
// planner counts as no attempt; one some indexer could have taken is not.
// Every indexer it could ask paced is AllPaced, whatever else was skipped.
func TestWorkerMarksASearchNoIndexerCouldAnswer(t *testing.T) {
	ok := schema.SearchOutcome{IndexerRef: schema.Ref{Name: "asked"}, IndexerName: "asked", Status: schema.SearchOutcomeOK}
	for i, tc := range []struct {
		name                string
		outcomes            []schema.SearchOutcome
		noIndexer, allPaced bool
	}{
		{name: "no indexer in the namespace", noIndexer: true},
		{name: "every indexer unavailable", noIndexer: true, outcomes: []schema.SearchOutcome{
			skipped("off", schema.SkipReasonDisabled), skipped("auto-off", schema.SkipReasonAutomaticDisabled),
			skipped("new", schema.SkipReasonNoCaps), skipped("backoff", schema.SkipReasonUnhealthy),
		}},
		{name: "the only usable indexer paced", allPaced: true, outcomes: []schema.SearchOutcome{
			skipped("off", schema.SkipReasonDisabled), skipped("busy", schema.SkipReasonPaced),
		}},
		{name: "an indexer asked", outcomes: []schema.SearchOutcome{skipped("off", schema.SkipReasonDisabled), ok}},
		{name: "an indexer skipped for the request", outcomes: []schema.SearchOutcome{
			skipped("off", schema.SkipReasonDisabled), skipped("usenet", "protocol not requested"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newWorkerFixture(t, fmt.Sprintf("worker-noidx-%d", i))
			f.rpc.Response = schema.SearchResponse{Outcomes: tc.outcomes}

			item := f.movie(t)
			env := f.envelope(t, schema.SearchTask{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
				Reason:   schema.SearchReasonMissing, Item: item, Seq: 1,
			})
			require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))
			require.Len(t, f.rpc.Requests(), 1, "a live search asks index")

			rec := f.record(t, agentrecords.SearchKey(item.UID))
			require.Equal(t, tc.noIndexer, rec.NoIndexer, "NoIndexer")
			require.Equal(t, tc.allPaced, rec.AllPaced, "AllPaced")
		})
	}

	t.Run("an index-only search counts", func(t *testing.T) {
		ctx := context.Background()
		f := newWorkerFixture(t, "worker-noidx-index")
		setMatrixMetadata(t, ctx, f)
		f.worker.Index = &search.FakeIndexQuery{}

		item := f.movie(t)
		env := f.envelope(t, schema.SearchTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			Reason:   schema.SearchReasonMissing, IndexOnly: true, Item: item, Seq: 1,
		})
		require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))
		require.Empty(t, f.rpc.Requests())
		rec := f.record(t, agentrecords.SearchKey(item.UID))
		require.False(t, rec.NoIndexer, "the release index was searched: an attempt")
		require.False(t, rec.AllPaced)
	})
}
