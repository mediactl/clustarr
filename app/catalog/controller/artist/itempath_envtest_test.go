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
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// countingAlbumRPC is fakeAlbumRPC with a call counter: the child-sync
// memo's proof is how many times the fan-out asked the gateway.
type countingAlbumRPC struct {
	albums []pkgmetadata.Album
	calls  atomic.Int64
}

func (f *countingAlbumRPC) Request(_ context.Context, _ string, _, out any) error {
	f.calls.Add(1)
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		panic("unexpected out type")
	}
	resp.Kind = commonv1.MediaKindAlbum
	for _, alb := range f.albums {
		b, err := json.Marshal(alb)
		if err != nil {
			return err
		}
		resp.Results = append(resp.Results, b)
	}
	return nil
}

type reconcileCounter struct{ n atomic.Int64 }

func (c *reconcileCounter) inc()         { c.n.Add(1) }
func (c *reconcileCounter) count() int64 { return c.n.Load() }

// startItemPath runs a real artist.Reconciler on a real ctrl.Manager backed
// by the envtest apiserver as the remediation loop runs it (itemtest.Start):
// its own Watches -- the Artist, its Albums, the RootFolders -- wired, the
// loop's indexes registered.
func startItemPath(t *testing.T, ctx context.Context, cfg *rest.Config, bus combinedBus) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	counter := &reconcileCounter{}
	r := &artist.Reconciler{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorder("artist"),
		Bus: bus, OnReconcile: counter.inc,
	}
	require.NoError(t, remediation.RegisterIndexes(ctx, mgr.GetFieldIndexer()))
	require.NoError(t, itemtest.Start(mgr, map[remediation.KeyKind]rollup.Item{remediation.KindArtist: r}))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}

// TestArtistItemPath (2026-11-10 design D2, §5.1): the Artist is a
// remediation-loop item key. Its pass publishes its metadata task and
// records the finalizer in one pass; the gateway's write wakes it and the
// fan-out creates its Albums once; an Album's creation wakes it through the
// Album watch, and the rollup -- albumCount, the container phase and
// counts -- follows without a second gateway call (the child-sync memo); an
// Album's phase change wakes it again; catalog owns the rollup fields and
// never status.metadata.
func TestArtistItemPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	realBus := membus.New(nil)
	require.NoError(t, realBus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = realBus.Close() })

	requester := &countingAlbumRPC{albums: []pkgmetadata.Album{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"}, Title: "OK Computer", PrimaryType: "Album"},
	}}
	c, counter := startItemPath(t, ctx, cfg, combinedBus{Publisher: realBus, requester: requester})
	const ns = "artist-itempath-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "music-root", "/data/media/music")))

	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "none", RootFolderRef: "music-root",
			MetadataProfile: catalogv1alpha1.MusicMetadataProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}},
			AddOptions:      catalogv1alpha1.ArtistAddOptions{Monitor: catalogv1alpha1.ArtistMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, a))
	key := client.ObjectKeyFromObject(a)

	wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Artist{}, k8s.MustNewScheme())
	require.NoError(t, err)
	var got catalogv1alpha1.Artist
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, key, &got); err != nil {
			return false
		}
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ArtistConditionMetadataReady)
		return k8s.HasFinalizer(&got, wantFinalizer) && cond != nil && cond.Status == metav1.ConditionFalse && got.Status.ObservedGeneration == got.Generation
	}, 10*time.Second, 20*time.Millisecond, "the finalizer, MetadataReady=False and observedGeneration must all land from the first pass")
	assert.Equal(t, catalogv1alpha1.ContainerPhasePending, got.Status.Phase, "no metadata yet: Pending")
	assert.Empty(t, got.Status.Path, "no metadata, no path")
	assert.False(t, got.Status.AddOptionsApplied, "no fan-out has run: the add-time monitor mode is still owed")

	// The gateway's write wakes the item (the metadata.refreshedAt arm); the
	// fan-out creates the accepted release group's Album.
	metaAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(catalogac.ArtistStatus().WithMetadata(
		catalogac.ArtistMetadata().WithName("Radiohead").WithRefreshedAt(metav1.Now()).WithSchemaVersion(pkgmetadata.SchemaVersion)))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, metaAC)
	require.NoError(t, err)

	albumName := artist.AlbumName("radiohead", "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9")
	albumKey := types.NamespacedName{Namespace: ns, Name: albumName}
	var alb catalogv1alpha1.Album
	require.Eventually(t, func() bool { return c.Get(ctx, albumKey, &alb) == nil }, 10*time.Second, 20*time.Millisecond, "the fan-out never created the Album")
	assert.Equal(t, "radiohead", alb.Spec.ArtistRef)
	assert.Nil(t, alb.Status.Metadata, "the fan-out seeds no Album status")

	// The Album's creation wakes the Artist (the Album watch), whose rollup
	// then counts it, and the container phase reads Ready.
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, key, &got); err != nil {
			return false
		}
		return got.Status.AlbumCount == 1 && got.Status.Phase == catalogv1alpha1.ContainerPhaseReady &&
			k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.ArtistConditionAlbumsSynced) &&
			k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady)
	}, 10*time.Second, 20*time.Millisecond, "the Album's creation never rolled up into its Artist")
	assert.Equal(t, "/data/media/music/Radiohead", got.Status.Path)
	assert.True(t, got.Status.AddOptionsApplied, "the first complete fan-out records addOptionsApplied")
	assert.EqualValues(t, 0, got.Status.AlbumFileCount)
	assert.EqualValues(t, 0, got.Status.MissingChildCount, "an Album with no phase yet is not missing")
	assert.Equal(t, int64(1), requester.calls.Load(), "the child-sync memo: one gateway call for the metadata it read, however many passes ran")

	// An Album's phase moving (what its own reconciler writes once it is
	// wanted) wakes the Artist, which counts it missing.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Album(albumName, ns).WithStatus(
		catalogac.AlbumStatus().WithPhase(commonv1.LeafPhaseWanted)))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.MissingChildCount == 1
	}, 10*time.Second, 20*time.Millisecond, "the Album's phase never reached the Artist's rollup")
	assert.Equal(t, int64(1), requester.calls.Load(), "a rollup wake fetches no list")

	// The field managers: catalog owns the rollup and never the document.
	fields := managedStatusFieldPaths(got.ManagedFields, "catalog")
	require.NotNil(t, fields, "no catalog/status entry in managedFields")
	names := statusFieldNames(fields)
	for _, f := range []string{"path", "albumCount", "phase", "addOptionsApplied", "conditions"} {
		assert.True(t, names[f], "catalog should own status.%s", f)
	}
	assert.False(t, names["metadata"], "catalog must never claim status.metadata")
	metaFields := managedStatusFieldPaths(got.ManagedFields, "catalog-metadata")
	require.NotNil(t, metaFields)
	assert.True(t, statusFieldNames(metaFields)["metadata"])

	// A spec edit refetches the list (the memo's generation input moved).
	// The edit must move the spec: monitorNewItems defaults to all, so
	// writing all is no change, no generation bump and no wake.
	before := requester.calls.Load()
	require.Eventually(t, func() bool {
		var cur catalogv1alpha1.Artist
		if err := c.Get(ctx, key, &cur); err != nil {
			return false
		}
		cur.Spec.MonitorNewItems = catalogv1alpha1.MonitorNewItemsNone
		return c.Update(ctx, &cur) == nil
	}, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return requester.calls.Load() > before }, 10*time.Second, 20*time.Millisecond, "a spec change never refetched the album list")
	assert.Positive(t, counter.count())
}
