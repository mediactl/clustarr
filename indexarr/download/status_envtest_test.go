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

package download_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/download"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// steadyState creates an Indexer and drives it to the state the RSS poll and
// the search fan-out leave behind, both of which write under
// k8s.ManagerIndexarrWorker. Without this a test cannot observe a release at
// all: a blank object has nothing to release.
func steadyState(
	t *testing.T, ctx context.Context, c client.Client, ns, name string,
) (*indexv1alpha1.Indexer, *download.Service) {
	t.Helper()
	newNamespace(t, ctx, c, ns)

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "https://tr.example",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolTorrent},
		},
	}
	require.NoError(t, c.Create(ctx, idx))

	rssAt := metav1.NewTime(time.Now().Truncate(time.Second))
	require.NoError(t, idxstatus.Patch(ctx, c, k8s.ManagerIndexarrWorker, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			*ac = *idxstatus.WorkerFields(indexv1alpha1.IndexerStatus{
				LastRssAt: &rssAt, LastRssNewCount: 7, IndexedReleases: 4211,
				QueriesInWindow: 11, EscalationLevel: 2,
			})
		}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	require.Equal(t, int32(11), idx.Status.QueriesInWindow, "the steady state did not take")

	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	return idx, &download.Service{Client: c, Bus: bus}
}

func TestGrabCountDoesNotReleaseTheOtherWorkerFields(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	idx, s := steadyState(t, ctx, c, "dl-ssa", "tr")

	s.CountGrabForTest(ctx, idx, "guid-a")

	var after indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &after))
	require.Equal(t, int32(1), after.Status.GrabsInWindow)
	// Every one of these would be zero if the apply had declared less than
	// the complete indexarr-worker set. There is deliberately no second
	// writer of them here: a co-owner would hold each field up and the test
	// would pass whether or not the apply released it.
	require.Equal(t, int32(11), after.Status.QueriesInWindow, "released by a partial apply")
	require.Equal(t, int64(4211), after.Status.IndexedReleases, "released by a partial apply")
	require.Equal(t, int32(7), after.Status.LastRssNewCount, "released by a partial apply")
	require.NotNil(t, after.Status.LastRssAt, "released by a partial apply")
	require.Equal(t, int32(2), after.Status.EscalationLevel, "released by a partial apply")
}

func TestARedeliveredGrabDoesNotDoubleCountInStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	idx, s := steadyState(t, ctx, c, "dl-redeliver", "tr")

	s.CountGrabForTest(ctx, idx, "guid-a")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	require.Equal(t, int32(1), idx.Status.GrabsInWindow)

	s.CountGrabForTest(ctx, idx, "guid-a") // the RPC was retried

	var after indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &after))
	require.Equal(t, int32(1), after.Status.GrabsInWindow)
	require.Equal(t, int32(11), after.Status.QueriesInWindow)
}

// A second, distinct grab moves the projection on and still leaves the rest
// of the worker's set standing.
func TestASecondGrabAdvancesTheProjection(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	idx, s := steadyState(t, ctx, c, "dl-second", "tr")

	s.CountGrabForTest(ctx, idx, "guid-a")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	s.CountGrabForTest(ctx, idx, "guid-b")

	var after indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &after))
	require.Equal(t, int32(2), after.Status.GrabsInWindow)
	require.Equal(t, int64(4211), after.Status.IndexedReleases)
	require.NotNil(t, after.Status.LastRssAt)
}
