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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestAlbumReconcilerRanksByEveryTrackFile proves status.quality,
// status.cutoffMet and status.phase come from every file backing the Album
// (AlbumStatus.Quality: "the lowest quality across the album's imported
// tracks"; Lidarr's CutoffSpecification), not the one file
// rollup.PickMediaFile would pick: a lossy track beside a newer WAV track
// holds the album below a FLAC cutoff, and removing it lets the album meet
// the cutoff again.
func TestAlbumReconcilerRanksByEveryTrackFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	const ns = "album-rank-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "music-root", "/data/media/music")))
	createSteadyArtist(t, ctx, c, ns, "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711", "music-root")
	qp := musicProfile("album-rank-qp", "FLAC")
	qp.Spec.Tiers = append(qp.Spec.Tiers, catalogv1alpha1.Tier{Name: "High", Qualities: []string{"High"}})
	require.NoError(t, c.Create(ctx, qp))

	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: ns},
		Spec: catalogv1alpha1.AlbumSpec{
			ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9",
			QualityProfileRef: ptr.To("album-rank-qp"),
		},
	}
	require.NoError(t, c.Create(ctx, alb))
	metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
		catalogac.AlbumMetadata().WithTitle("OK Computer").WithRefreshedAt(metav1.Now())))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)

	// The WAV track has the greater name, so it is the file
	// rollup.PickMediaFile selects whatever the creation timestamps say
	// (they tie to the second): ranking by that file alone would read the
	// album as WAV and cutoff met.
	trackFile := func(name, q string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: alb.Name},
				Path:     "/data/media/music/radiohead/OK Computer (1997)/" + name,
				Quality:  commonv1.Quality{Name: q},
			},
		}
	}
	lossy := trackFile("ok-computer-01-airbag", "High")
	require.NoError(t, c.Create(ctx, lossy))
	require.NoError(t, c.Create(ctx, trackFile("ok-computer-02-paranoid-android", "WAV")))
	key := types.NamespacedName{Namespace: ns, Name: alb.Name}
	filesSeen := func(n int) func() bool {
		return func() bool {
			var mfs catalogv1alpha1.MediaFileList
			return c.List(ctx, &mfs, client.InNamespace(ns)) == nil && len(mfs.Items) == n
		}
	}
	require.Eventually(t, filesSeen(2), 5*time.Second, 10*time.Millisecond, "setup: the cache never observed both files")

	r := &album.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{}},
	}
	reconcileOnce := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
	}
	var got catalogv1alpha1.Album
	settled := func(q string, met bool, phase catalogv1alpha1.AlbumPhase) func() bool {
		return func() bool {
			return c.Get(ctx, key, &got) == nil && got.Status.Quality != nil && got.Status.Quality.Name == q &&
				got.Status.CutoffMet == met && got.Status.Phase == phase
		}
	}

	reconcileOnce()
	require.Eventually(t, settled("High", false, catalogv1alpha1.AlbumPhaseCutoffUnmet), 5*time.Second, 10*time.Millisecond,
		"one High track beside a WAV one must hold the album at High, below its FLAC cutoff")

	// In steady state: the lossy track goes (an upgrade replaced it), and
	// the album now meets its cutoff from the file that remains.
	require.NoError(t, c.Delete(ctx, lossy))
	require.Eventually(t, filesSeen(1), 5*time.Second, 10*time.Millisecond)
	reconcileOnce()
	require.Eventually(t, settled("WAV", true, catalogv1alpha1.AlbumPhaseImported), 5*time.Second, 10*time.Millisecond,
		"with only the WAV track left, the album must meet its FLAC cutoff")
	assert.Nil(t, got.Status.ActiveDownloadRef)
}
