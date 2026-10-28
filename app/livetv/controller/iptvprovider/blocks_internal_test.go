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
package iptvprovider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
)

const testAddress = "clustarr-livetv.media.svc.cluster.local:80"

func fakeReconciler(t *testing.T, address string, objs ...*clustarrv1.IPTVProvider) *Reconciler {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clustarrv1.AddToScheme(s))
	b := fake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &Reconciler{Client: b.Build(), Address: address}
}

func activeChannels(numbers ...string) []clustarrv1.IPTVChannel {
	out := make([]clustarrv1.IPTVChannel, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, clustarrv1.IPTVChannel{Spec: clustarrv1.IPTVChannelSpec{Number: n, Active: true}})
	}
	return out
}

// TestPlanBlocksGivesEachBlockItsURI: a block's device is /hdhr/<deviceID>
// on the livetv Service, and its URI is that path on the Service's
// address -- what Plex is given.
func TestPlanBlocksGivesEachBlockItsURI(t *testing.T) {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "news"}}
	max := int32(2)
	p.Spec.DVR.MaxChannels = &max
	r := fakeReconciler(t, testAddress, p)
	pl, err := r.planBlocks(context.Background(), p, activeChannels("1001", "1002", "1003", "1004"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(pl.blocks), 2, "two channels a block")
	for _, b := range pl.blocks {
		assert.Equal(t, "/hdhr/"+b.DeviceID, b.Path, b.Start)
		assert.Equal(t, "http://"+testAddress+"/hdhr/"+b.DeviceID, b.URI, b.Start)
	}

	r = fakeReconciler(t, "", p)
	pl, err = r.planBlocks(context.Background(), p, activeChannels("1001"))
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, "/hdhr/"+pl.blocks[0].DeviceID, pl.blocks[0].Path)
	assert.Empty(t, pl.blocks[0].URI, "no address, no URI: cluster-plex registers nothing")
}

// TestKeptBlocksCarryTheirURITheSameWay: blocks kept because a rule refused
// the next ones (here another, older provider's span) still name their
// path and URI, so blocks a previous manager wrote get them too.
func TestKeptBlocksCarryTheirURITheSameWay(t *testing.T) {
	older := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "sport", CreationTimestamp: metav1.NewTime(time.Unix(100, 0))}}
	older.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1002", DeviceID: "AAAA0001"}}
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "news", CreationTimestamp: metav1.NewTime(time.Unix(200, 0))}}
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "2001", First: "2001", Last: "2001", DeviceID: "BBBB0001"}}
	r := fakeReconciler(t, testAddress, older, p)
	pl, err := r.planBlocks(context.Background(), p, activeChannels("1001", "2001"))
	require.NoError(t, err)
	require.Equal(t, clustarrv1.ReasonNumberRangeOverlap, pl.reason)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, "/hdhr/BBBB0001", pl.blocks[0].Path)
	assert.Equal(t, "http://"+testAddress+"/hdhr/BBBB0001", pl.blocks[0].URI)
}
