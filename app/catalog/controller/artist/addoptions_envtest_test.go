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

package artist_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// The first fan-out applies the add-time monitor mode though it runs a pass
// after the Artist was first reconciled (Lidarr's addOptions.monitor; design
// D2): a pass without metadata flips status.addOptionsApplied for nothing,
// since DesiredAlbums reads it to choose between the add-time mode and
// spec.monitorNewItems. Until 2026-11-10 every pass sent the flag true, so
// once the fan-out waited for the document the first real one ran with it
// flipped: under none, future, missing, existing, latest or first every
// album started monitored, spec.monitorNewItems defaulting to all. And with
// the flag false through the MetadataPending pass, the Artist is announced
// added off observedGeneration, once, not on each such pass.
func TestTheFirstAlbumFanOutAppliesTheAddTimeMonitorMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	const ns = "artist-addoptions-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "music-root", "/data/media/music")))

	const releaseGroupID = "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"
	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "none", RootFolderRef: "music-root",
			MetadataProfile: catalogv1alpha1.MusicMetadataProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}},
			AddOptions:      catalogv1alpha1.ArtistAddOptions{Monitor: catalogv1alpha1.ArtistMonitorNone},
		},
	}
	require.NoError(t, c.Create(ctx, a))
	key := client.ObjectKeyFromObject(a)
	require.Eventually(t, func() bool { return c.Get(ctx, key, &catalogv1alpha1.Artist{}) == nil }, 5*time.Second, 10*time.Millisecond)

	rpc := &countingAlbumRPC{albums: []pkgmetadata.Album{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: releaseGroupID}, Title: "OK Computer", PrimaryType: "Album"},
	}}
	pub := &itemRecorder{}
	r := &artist.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: combinedBus{Publisher: pub, requester: rpc}}

	// The metadata-less pass: no list fetched, and the flag not flipped.
	_, err := r.ReconcileItem(ctx, key)
	require.NoError(t, err)
	assert.Zero(t, rpc.calls.Load())
	var got catalogv1alpha1.Artist
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.ObservedGeneration == got.Generation
	}, 5*time.Second, 10*time.Millisecond, "the pass never wrote its status")
	assert.False(t, got.Status.AddOptionsApplied, "no fan-out has run: the add-time monitor mode is still owed")
	assert.Equal(t, catalogv1alpha1.MonitorNewItemsAll, got.Spec.MonitorNewItems, "the CRD default a flipped flag would create the album under")

	// The document, as the loop's metadata stage writes it, then the pass
	// its write wakes: the one fetch, the Album under the add-time mode,
	// and the flag recorded with that first complete fan-out.
	metaAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(catalogac.ArtistStatus().WithMetadata(
		catalogac.ArtistMetadata().WithName("Radiohead").WithRefreshedAt(metav1.Now()).WithSchemaVersion(pkgmetadata.SchemaVersion)))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond, "the metadata document never reached the cache")
	_, err = r.ReconcileItem(ctx, key)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rpc.calls.Load())
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.AddOptionsApplied
	}, 5*time.Second, 10*time.Millisecond, "the first complete fan-out records addOptionsApplied")

	var alb catalogv1alpha1.Album
	albumKey := types.NamespacedName{Namespace: ns, Name: artist.AlbumName(a.Name, releaseGroupID)}
	require.Eventually(t, func() bool { return c.Get(ctx, albumKey, &alb) == nil }, 5*time.Second, 10*time.Millisecond, "the fan-out never created the Album")
	require.NotNil(t, alb.Spec.Monitored, "the first fan-out sets spec.monitored on every Album it creates")
	assert.False(t, *alb.Spec.Monitored, "none: the album starts unmonitored, whatever spec.monitorNewItems says")
	assert.Equal(t, 1, pub.count(), "announced added once, on the first pass, not again while the flag waits for the fan-out")
}
