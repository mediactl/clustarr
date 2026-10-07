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

package directgrab_test

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
	"github.com/mediactl/clustarr/app/indexer/controller/directgrab"
	"github.com/mediactl/clustarr/app/indexer/limits"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// steadyState creates an Indexer and drives it to the state the RSS poll and
// the search fan-out leave behind, both of which write under
// k8s.ManagerIndexarrWorker.
func steadyState(
	t *testing.T, ctx context.Context, c client.Client, ns, name string,
) (*indexv1alpha1.Indexer, events.Bus) {
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
				LastRssAt: &rssAt, LastRssNewCount: 7, IndexedReleases: 4211, EscalationLevel: 2,
			})
		}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	require.Equal(t, int64(4211), idx.Status.IndexedReleases, "the steady state did not take")

	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	return idx, bus
}

// grabsOnTheRing is the window count of idx's grab ring.
func grabsOnTheRing(t *testing.T, ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer) int32 {
	t.Helper()
	u, err := limits.Grabs(ctx, bus.KV(events.BucketIndexerLimits), idx, time.Now())
	require.NoError(t, err)
	return u.Count
}

// The verb writes no status at all since status.grabsInWindow became the
// Indexer reconciler's projection of the ring (2026-10-01). It used to apply
// the whole worker set after every grab, and each apply was one more chance
// to release or roll back the search fan-out's and the RSS poll's fields --
// it did both at different times. An apply that does not happen can do
// neither: the object is not written at all, its resourceVersion unmoved.
func TestAGrabWritesNoIndexerStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	idx, bus := steadyState(t, ctx, c, "dl-nostatus", "tr")

	directgrab.CountGrabForTest(ctx, bus, idx, "guid-a")
	directgrab.CountGrabForTest(ctx, bus, idx, "guid-a") // the RPC was retried
	directgrab.CountGrabForTest(ctx, bus, idx, "guid-b")
	require.Equal(t, int32(2), grabsOnTheRing(t, ctx, bus, idx), "one count per GUID")

	var after indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &after))
	require.Equal(t, idx.ResourceVersion, after.ResourceVersion, "the grab wrote the Indexer")
	for _, mf := range after.ManagedFields {
		if mf.FieldsV1 != nil {
			require.NotContains(t, mf.FieldsV1.GetRawString(), "f:grabsInWindow",
				"%s claimed grabsInWindow", mf.Manager)
		}
	}
}
