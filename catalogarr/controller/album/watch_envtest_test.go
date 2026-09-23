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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
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

// TestAlbumControllerWakesOnInheritedProfileEdits runs the real, auto-wired
// controller (the only test in this package that calls SetupWithManager --
// controller names are process-global) against an Album with no profile
// override of its own, so it is ranked against its Artist's. Both ways that
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
	require.NoError(t, r.SetupWithManager(mgr))
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
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
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
}
