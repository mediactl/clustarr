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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// fileRecorder captures the media-file events a reconciler publishes
// (clustarr.evt.catalog.mediafile.*) and ignores everything else it
// publishes: item events, metadata tasks.
type fileRecorder struct {
	mu       sync.Mutex
	subjects []string
	envs     []*events.Envelope
}

func (p *fileRecorder) Publish(_ context.Context, subject string, env *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	if strings.HasPrefix(subject, "clustarr.evt.catalog.mediafile.") {
		p.mu.Lock()
		p.subjects = append(p.subjects, subject)
		p.envs = append(p.envs, env)
		p.mu.Unlock()
	}
	return events.Receipt{}, nil
}

func (p *fileRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.envs)
}

func (p *fileRecorder) at(t *testing.T, i int) (string, *events.Envelope, schema.MediaFileEvent) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Greater(t, len(p.envs), i)
	var evt schema.MediaFileEvent
	require.NoError(t, schema.Decode(p.envs[i].Schema, p.envs[i].Data, &evt))
	return p.subjects[i], p.envs[i], evt
}

// TestAlbumReconcilerPublishesMediaFileEvents: spec §5's
// clustarr.evt.catalog.mediafile.<imported|deleted>.<uid> as a track's
// status.tracks[].fileRef changes (TrackFileTransitions), nothing for a file
// that addresses no track, and nothing on a settled reconcile.
func TestAlbumReconcilerPublishesMediaFileEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	const ns = "album-mfevent-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "music-root", "/data/media/music")))
	createSteadyArtist(t, ctx, c, ns, "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711", "music-root")

	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"},
	}
	require.NoError(t, c.Create(ctx, alb))
	metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
		catalogac.AlbumMetadata().WithTitle("OK Computer").WithRefreshedAt(metav1.Now())))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	key := types.NamespacedName{Namespace: ns, Name: alb.Name}
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Album
		return c.Get(ctx, key, &got) == nil && got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	track := func(rec, title string, pos int32) pkgmetadata.Track {
		return pkgmetadata.Track{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: rec}, Title: title, Position: pos}
	}
	fetched := &pkgmetadata.Album{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: alb.Spec.ReleaseGroupID},
		Releases: []pkgmetadata.AlbumRelease{{
			IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "official"}, Status: "Official", TrackCount: 2,
			Media: []pkgmetadata.Medium{{Position: 1, Tracks: []pkgmetadata.Track{
				track("rec-1", "Airbag", 1), track("rec-2", "Paranoid Android", 2),
			}}},
		}},
	}
	pub := &fileRecorder{}
	r := &album.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: pub, requester: fakeAlbumLookupRPC{album: fetched}},
	}
	reconcileOnce := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		require.NoError(t, err)
	}
	var live catalogv1alpha1.Album
	trackFiles := func(n int32) {
		t.Helper()
		require.Eventually(t, func() bool {
			return c.Get(ctx, key, &live) == nil && len(live.Status.Tracks) == 2 && live.Status.TrackFileCount == n
		}, 5*time.Second, 10*time.Millisecond, "status.tracks never held %d file(s)", n)
	}
	filesSeen := func(n int) {
		t.Helper()
		require.Eventually(t, func() bool {
			var mfs catalogv1alpha1.MediaFileList
			return c.List(ctx, &mfs, client.InNamespace(ns)) == nil && len(mfs.Items) == n
		}, 5*time.Second, 10*time.Millisecond)
	}

	reconcileOnce()
	trackFiles(0)
	assert.Equal(t, 0, pub.count(), "an album with no file announces no file")

	paranoid := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer-paranoid-android", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: alb.Name, Track: "rec-2"},
			Path:     "/data/media/music/radiohead/OK Computer (1997)/02 - Paranoid Android.flac",
			Quality:  commonv1.Quality{Name: "FLAC"},
		},
	}
	require.NoError(t, c.Create(ctx, paranoid))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer-no-track", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: alb.Name},
			Path:     "/data/media/music/radiohead/OK Computer (1997)/01 - Airbag.flac",
			Quality:  commonv1.Quality{Name: "FLAC"},
		},
	}))
	filesSeen(2)
	reconcileOnce()
	require.Equal(t, 1, pub.count(), "only the file addressing a track has a fileRef to announce")
	uid := string(live.UID)
	subject, env, evt := pub.at(t, 0)
	assert.Equal(t, events.CatalogMediaFileSubject(events.ActionImported, uid), subject)
	assert.Equal(t, uid+":mediafile:imported:"+paranoid.Name, env.ID)
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: alb.Name}, evt.Media)
	assert.Equal(t, paranoid.Spec.Path, evt.ImportedPath)
	trackFiles(1)

	reconcileOnce()
	assert.Equal(t, 1, pub.count(), "a settled reconcile must announce nothing")

	require.NoError(t, c.Delete(ctx, paranoid))
	filesSeen(1)
	reconcileOnce()
	require.Equal(t, 2, pub.count())
	subject, env, _ = pub.at(t, 1)
	assert.Equal(t, events.CatalogMediaFileSubject(events.ActionDeleted, uid), subject)
	assert.Equal(t, uid+":mediafile:deleted:"+paranoid.Name, env.ID)
	trackFiles(0)
}
