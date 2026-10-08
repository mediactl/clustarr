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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// An index-only task (the search planner marks an item it has searched
// before, 2026-10-06) is answered from the local release index: every
// release an indexer's RSS feed delivered is already there, so a repeat
// sweep spends no indexer query. Which item gets one is the planner's call
// since A4.2 (WantedScan retired).

// setMatrixMetadata gives the fixture's movie a title and year, the search
// text the index is queried with.
func setMatrixMetadata(t *testing.T, ctx context.Context, f *workerFixture) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalogMetadata,
		catalogac.Movie("the-matrix", f.ns).WithStatus(catalogac.MovieStatus().WithMetadata(
			catalogac.MovieMetadata().WithTitle("The Matrix").WithYear(1999).WithRefreshedAt(metav1.Now()))))
	require.NoError(t, err)
	eventually(t, 10*time.Second, "the cache to see the movie's metadata", func() bool {
		var m catalogv1alpha1.Movie
		return f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, &m) == nil &&
			m.Status.Metadata != nil && m.Status.Metadata.Title != ""
	})
}

func TestWorkerSearchesTheIndexNotTheIndexersForAnIndexOnlyTask(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-index-only")
	setMatrixMetadata(t, ctx, f)
	idx := &search.FakeIndexQuery{Response: schema.QueryResponse{Releases: []schema.Release{
		rpcRelease("g-local", "The.Matrix.1999.1080p.BluRay.x264-LOCAL", 50),
	}}}
	f.worker.Index = idx

	item := f.movie(t)
	env := f.envelope(t, schema.SearchTask{
		MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:    schema.SearchReasonMissing,
		IndexOnly: true,
		Item:      item, Seq: 1,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	require.Empty(t, f.rpc.Requests(), "no indexer is asked")
	reqs := idx.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, "The Matrix 1999", reqs[0].Text, "the same text a live text search sends")
	require.NotEmpty(t, reqs[0].Filters["category"], "narrowed to the request's categories")
	require.Positive(t, reqs[0].Limit)

	rec := f.record(t, agentrecords.SearchKey(item.UID))
	require.Equal(t, "the-matrix", rec.Item.Name, "the index's releases are decided and answered for the item")
	require.Len(t, rec.Candidates, 1)
	require.Equal(t, "g-local", rec.Candidates[0].Decision.GUID)
}

func TestWorkerDoesNotQueryTheIndexWithoutATitle(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-index-no-title")
	idx := &search.FakeIndexQuery{}
	f.worker.Index = idx

	item := f.movie(t)
	env := f.envelope(t, schema.SearchTask{
		MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:    schema.SearchReasonMissing,
		IndexOnly: true,
		Item:      item, Seq: 1,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	require.Empty(t, idx.Requests(), "an empty index query matches every release")
	require.Empty(t, f.rpc.Requests())
	rec := f.record(t, agentrecords.SearchKey(item.UID))
	require.Empty(t, rec.Candidates, "answered empty, so the dispatch closes")
}
