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

package album_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// musicProfile is a music QualityProfile ranking WAV over FLAC, with its
// cutoff at the named tier.
func musicProfile(name, cutoff string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindMusic,
			Cutoff:    cutoff,
			Tiers: []catalogv1alpha1.Tier{
				{Name: "WAV", Qualities: []string{"WAV"}},
				{Name: "FLAC", Qualities: []string{"FLAC"}},
			},
		},
	}
}

// TestAlbumControllerWakesOnInheritedProfileEdits runs the Album's item
// path as the remediation loop runs it (itemtest.Start) against an Album
// with no profile override of its own, so it is ranked against its
// Artist's. Both ways that
// inherited profile changes must reach it without any other event: an edit
// to the QualityProfile itself (mapQualityProfile's Artist hop), and the
// Artist pointing at another profile (the Artist watch).
func TestAlbumControllerWakesOnInheritedProfileEdits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	r := &album.Reconciler{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorder("album"),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{}},
	}
	require.NoError(t, remediation.RegisterIndexes(ctx, mgr.GetFieldIndexer()))
	require.NoError(t, itemtest.Start(mgr, map[remediation.KeyKind]rollup.Item{remediation.KindAlbum: r}))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c := mgr.GetClient()

	const ns = "album-watch-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "music-root", "/data/media/music")))
	require.NoError(t, c.Create(ctx, musicProfile("album-watch-qp", "FLAC")))
	require.NoError(t, c.Create(ctx, musicProfile("album-watch-qp-strict", "WAV")))
	artistObj := testArtist(ns, "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711", "music-root")
	artistObj.Spec.QualityProfileRef = "album-watch-qp"
	require.NoError(t, c.Create(ctx, artistObj))
	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"},
	}
	require.NoError(t, c.Create(ctx, alb))
	metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
		catalogac.AlbumMetadata().WithTitle("OK Computer").WithRefreshedAt(metav1.Now())))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, metaAC)
	require.NoError(t, err)
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer-flac", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: alb.Name},
			Path:     "/data/media/music/radiohead/OK Computer (1997)",
			Quality:  commonv1.Quality{Name: "FLAC"},
		},
	}))
	key := types.NamespacedName{Namespace: ns, Name: alb.Name}
	cutoffMet := func(want bool) func() bool {
		return func() bool {
			var got catalogv1alpha1.Album
			return c.Get(ctx, key, &got) == nil && got.Status.Quality != nil && got.Status.CutoffMet == want
		}
	}
	require.Eventually(t, cutoffMet(true), 10*time.Second, 20*time.Millisecond, "setup: a FLAC album under a FLAC cutoff never met it")

	var qp catalogv1alpha1.QualityProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "album-watch-qp"}, &qp))
	qp.Spec.Cutoff = "WAV"
	require.NoError(t, c.Update(ctx, &qp))
	require.Eventually(t, cutoffMet(false), 10*time.Second, 20*time.Millisecond,
		"raising the inherited profile's cutoff never reached the Album")

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "album-watch-qp"}, &qp))
	qp.Spec.Cutoff = "FLAC"
	require.NoError(t, c.Update(ctx, &qp))
	require.Eventually(t, cutoffMet(true), 10*time.Second, 20*time.Millisecond)

	var a catalogv1alpha1.Artist
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "radiohead"}, &a))
	a.Spec.QualityProfileRef = "album-watch-qp-strict"
	require.NoError(t, c.Update(ctx, &a))
	require.Eventually(t, cutoffMet(false), 10*time.Second, 20*time.Millisecond,
		"the Artist pointing at another profile never reached the Album")

	// DeadLettered: the DLQ projector's annotation on this object, already
	// in steady state, becomes the condition through the For() predicate's
	// annotation arm alone -- and folding it releases nothing else.
	var before catalogv1alpha1.Album
	require.NoError(t, c.Get(ctx, key, &before))
	annotated := before.DeepCopy()
	if annotated.Annotations == nil {
		annotated.Annotations = map[string]string{}
	}
	annotated.Annotations[k8s.AnnotationDeadLettered] = "clustarr.work.metadata.normal.x@2026-09-23T10:00:00Z"
	require.NoError(t, c.Patch(ctx, annotated, client.MergeFrom(&before)))
	var got catalogv1alpha1.Album
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionDeadLettered)
	}, 10*time.Second, 20*time.Millisecond, "the dead-lettered annotation never became the DeadLettered condition")
	assert.Equal(t, before.Status.CutoffMet, got.Status.CutoffMet)
	assert.Equal(t, before.Status.Quality, got.Status.Quality)
	assert.Equal(t, before.Status.Phase, got.Status.Phase)
	assert.NotNil(t, k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady), "folding DeadLettered released Ready")

	cleared := got.DeepCopy()
	delete(cleared.Annotations, k8s.AnnotationDeadLettered)
	require.NoError(t, c.Patch(ctx, cleared, client.MergeFrom(&got)))
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && k8s.FindCondition(got.Status.Conditions, k8s.ConditionDeadLettered) == nil
	}, 10*time.Second, 20*time.Millisecond, "removing the annotation never cleared the condition")
	// Gap-fix ruling R-5, on this steady-state Album: status.activeDownloadRef
	// derives from the grab entries on the Album's own status.downloads
	// (ADR-0019 §6.11), and this reconciler is the field's only writer. No
	// item watch wakes on an entry write (the stages decide entries inside
	// the pass that applies them), so each leg seeds the entries, waits for
	// the cache to see them, and reconciles the Album itself.
	require.NoError(t, c.Get(ctx, key, &got))
	steady := got.DeepCopy()
	seed := func(phase commonv1.DownloadPhase) {
		t.Helper()
		require.NoError(t, itemtest.SeedEntries(ctx, c, &got, itemtest.Entry("ok-computer-dl", phase)))
		require.Eventually(t, func() bool {
			var g catalogv1alpha1.Album
			return c.Get(ctx, key, &g) == nil && len(g.Status.Downloads) == 1 && g.Status.Downloads[0].Phase == phase
		}, 10*time.Second, 20*time.Millisecond, "the cache never saw the seeded entry")
		_, err := r.ReconcileItem(ctx, key)
		require.NoError(t, err)
	}

	seed(commonv1.DownloadPhasePending)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.ActiveDownloadRef != nil &&
			*got.Status.ActiveDownloadRef == "ok-computer-dl" && got.Status.Phase == catalogv1alpha1.AlbumPhaseDownloading
	}, 10*time.Second, 20*time.Millisecond, "a non-terminal entry must set the ref and read Downloading")
	assert.Equal(t, steady.Status.Quality, got.Status.Quality, "the ref's apply released status.quality")
	assert.Equal(t, steady.Status.CutoffMet, got.Status.CutoffMet)
	var refOwners []string
	for _, e := range got.ManagedFields {
		if e.Subresource == "status" && e.FieldsV1 != nil && strings.Contains(e.FieldsV1.GetRawString(), `"f:activeDownloadRef"`) {
			refOwners = append(refOwners, e.Manager)
		}
	}
	assert.Equal(t, []string{string(k8s.ManagerCatalog)}, refOwners, "status.activeDownloadRef has one writer")

	// Completed is on disk, awaiting import: still the item's grab (R-12).
	seed(commonv1.DownloadPhaseCompleted)
	require.Never(t, func() bool {
		var g catalogv1alpha1.Album
		return c.Get(ctx, key, &g) == nil && (g.Status.ActiveDownloadRef == nil || g.Status.Phase != catalogv1alpha1.AlbumPhaseDownloading)
	}, 500*time.Millisecond, 20*time.Millisecond, "a Completed entry is still working on the Album")

	// Imported is terminal: the ref goes and the phase is the file's again.
	seed(commonv1.DownloadPhaseImported)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.ActiveDownloadRef == nil && got.Status.Phase == steady.Status.Phase
	}, 10*time.Second, 20*time.Millisecond, "a terminal entry must clear the ref and give the phase back")

	// A delayed grab (status.pendingGrab, written by the grab path) wakes the
	// Album on its own and reads Delayed.
	kidA := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "kid-a", Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b8048f2c-2a9c-3c5a-8d4e-6a2a1e0b4a10"},
	}
	require.NoError(t, c.Create(ctx, kidA))
	kidKey := types.NamespacedName{Namespace: ns, Name: "kid-a"}
	require.Eventually(t, func() bool {
		var g catalogv1alpha1.Album
		return c.Get(ctx, kidKey, &g) == nil && g.Status.Phase == catalogv1alpha1.AlbumPhaseWanted
	}, 10*time.Second, 20*time.Millisecond, "setup: the second Album never settled at Wanted")
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogGrab, catalogac.Album("kid-a", ns).WithStatus(catalogac.AlbumStatus().
		WithPendingGrab(catalogac.PendingGrab().WithReleaseTitle("Radiohead - Kid A (2000) [FLAC]").
			WithProtocol(commonv1.ProtocolTorrent).WithGrabAt(metav1.NewTime(time.Now().Add(time.Hour))))))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var g catalogv1alpha1.Album
		return c.Get(ctx, kidKey, &g) == nil && g.Status.Phase == catalogv1alpha1.AlbumPhaseDelayed
	}, 5*time.Second, 20*time.Millisecond, "the pending-grab write never woke the Album into Delayed")
}
