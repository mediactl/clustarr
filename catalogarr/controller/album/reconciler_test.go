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
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

func init() {
	ctrl.SetLogger(logging.LogrBridge(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))
}

func newTestConfig(t *testing.T) *rest.Config {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, env.Stop())
	})
	return cfg
}

func newBareTestClient(t *testing.T) client.Client {
	t.Helper()
	cfg := newTestConfig(t)
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// startCacheOnly starts a real ctrl.Manager's cache (so the MediaFile index
// syncTracks-adjacent code paths need works) WITHOUT wiring an
// album.Reconciler's watches to it. Mirrors artist_test.startCacheOnly.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.MediaFile{}, ".spec.mediaRef.album",
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindAlbum {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}))
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Album{}, ".status.activeDownloadRef",
		func(o client.Object) []string {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok || alb.Status.ActiveDownloadRef == nil {
				return nil
			}
			return []string{*alb.Status.ActiveDownloadRef}
		}))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

func testNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func testRootFolder(ns, name, path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindMusic},
	}
}

func testArtist(ns, name, mbid, rootFolder string) *catalogv1alpha1.Artist {
	return &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: mbid, QualityProfileRef: "none", RootFolderRef: rootFolder,
		},
	}
}

type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	if f.err != nil {
		return events.Receipt{}, f.err
	}
	return events.Receipt{}, nil
}

// fakeAlbumLookupRPC implements the SINGLE-release-group lookup half of the
// reconciler's bus.Request (the existing rpc.catalogarr.metadata.lookup
// path Registry.Lookup(kind=album) already serves -- no new RPC verb, see
// this package's doc.go): Kind=MediaKindAlbum,
// IDs={mb-release-group: <ReleaseGroupID>}, Result a single JSON
// pkgmetadata.Album.
type fakeAlbumLookupRPC struct {
	album *pkgmetadata.Album
	err   error
}

func (f fakeAlbumLookupRPC) Request(_ context.Context, _ string, _, out any) error {
	if f.err != nil {
		return f.err
	}
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		panic("unexpected out type")
	}
	resp.Kind = commonv1.MediaKindAlbum
	if f.album != nil {
		b, err := json.Marshal(f.album)
		if err != nil {
			return err
		}
		resp.Result = b
	}
	return nil
}

type combinedBus struct {
	events.Publisher
	requester interface {
		Request(ctx context.Context, subject string, in, out any) error
	}
}

func (c combinedBus) Request(ctx context.Context, subject string, in, out any) error {
	return c.requester.Request(ctx, subject, in, out)
}

func managedStatusFieldPaths(entries []metav1.ManagedFieldsEntry, manager string) map[string]any {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == "status" && e.FieldsV1 != nil {
			var m map[string]any
			if err := json.Unmarshal(e.FieldsV1.GetRawBytes(), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

func statusFieldNames(fields map[string]any) map[string]bool {
	out := map[string]bool{}
	status, ok := fields["f:status"].(map[string]any)
	if !ok {
		return out
	}
	for k := range status {
		if k == "." {
			continue
		}
		out[k[2:]] = true // strip "f:"
	}
	return out
}

func createSteadyArtist(t *testing.T, ctx context.Context, c client.Client, ns, name, mbid, rootFolder string) {
	t.Helper()
	a := testArtist(ns, name, mbid, rootFolder)
	require.NoError(t, c.Create(ctx, a))
	metaAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(catalogac.ArtistStatus().
		WithMetadata(catalogac.ArtistMetadata().WithName(name).WithRefreshedAt(metav1.Now())).
		WithPath("/data/media/music/" + name))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	// Artist.status.path is catalogarr's own field, not the metadata
	// gateway's, but nothing in this test package runs the real
	// artist.Reconciler -- seed it directly under k8s.ManagerCatalogarr so
	// this fixture matches what a real reconcile would have produced.
	pathAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(catalogac.ArtistStatus().WithPath("/data/media/music/" + name))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, pathAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Artist
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil && got.Status.Path != ""
	}, 5*time.Second, 10*time.Millisecond)
}

func TestAlbumReconcilerQueueFull(t *testing.T) {
	ctx := context.Background()
	c := newBareTestClient(t)
	require.NoError(t, c.Create(ctx, testNamespace("album-queuefull-ns")))

	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: "album-queuefull-ns"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"},
	}
	require.NoError(t, c.Create(ctx, alb))

	r := &album.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{err: events.ErrQueueFull}, requester: fakeAlbumLookupRPC{}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "album-queuefull-ns", Name: "ok-computer"}}
	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Album
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	cond := k8s.FindCondition(got.Status.Conditions, "QueueFull")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	metaReady := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.AlbumConditionMetadataReady)
	assert.Nil(t, metaReady, "MetadataReady must be left untouched when the publish never happened")
}

func TestAlbumReconcilerArtistNotFound(t *testing.T) {
	ctx := context.Background()
	c := newBareTestClient(t)
	require.NoError(t, c.Create(ctx, testNamespace("album-noartist-ns")))

	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: "album-noartist-ns"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "does-not-exist", ReleaseGroupID: "rg-1"},
	}
	require.NoError(t, c.Create(ctx, alb))

	metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
		catalogac.AlbumMetadata().WithTitle("OK Computer").WithRefreshedAt(metav1.Now())))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)

	r := &album.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "album-noartist-ns", Name: "ok-computer"}}
	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Album
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	cond := k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "ArtistNotFound", cond.Reason)
}

func TestAlbumReconcilerNeverClaimsStatusMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("album-managedfields-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("album-managedfields-ns", "music-root", "/data/media/music")))
	createSteadyArtist(t, ctx, c, "album-managedfields-ns", "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711", "music-root")

	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: "album-managedfields-ns"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"},
	}
	require.NoError(t, c.Create(ctx, alb))

	metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
		catalogac.AlbumMetadata().WithTitle("OK Computer").WithReleaseDate(metav1.NewTime(time.Date(1997, 6, 16, 0, 0, 0, 0, time.UTC))).WithRefreshedAt(metav1.Now())))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Album
		if err := c.Get(ctx, types.NamespacedName{Namespace: alb.Namespace, Name: alb.Name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	r := &album.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: alb.Namespace, Name: alb.Name}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var got catalogv1alpha1.Album
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		return k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady)
	}, 5*time.Second, 10*time.Millisecond)

	fields := managedStatusFieldPaths(got.ManagedFields, "catalogarr")
	require.NotNil(t, fields, "no catalogarr/status entry in managedFields")
	names := statusFieldNames(fields)
	assert.True(t, names["path"], "catalogarr should own status.path")
	assert.True(t, names["phase"], "catalogarr should own status.phase")
	assert.False(t, names["metadata"], "catalogarr must never claim status.metadata -- that belongs to catalogarr-metadata")

	metaFields := managedStatusFieldPaths(got.ManagedFields, "catalogarr-metadata")
	require.NotNil(t, metaFields, "no catalogarr-metadata/status entry in managedFields")
	metaNames := statusFieldNames(metaFields)
	assert.True(t, metaNames["metadata"])
	assert.False(t, metaNames["path"], "catalogarr-metadata must never claim status.path")

	assert.Equal(t, "/data/media/music/radiohead/OK Computer (1997)", got.Status.Path,
		"the album segment joins onto the Artist's own resolved path, not a re-rendered artist segment")
}

func TestAlbumReconcilerBuildsTracksFromTheExistingSingleLookupRPC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("album-tracks-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("album-tracks-ns", "music-root", "/data/media/music")))
	createSteadyArtist(t, ctx, c, "album-tracks-ns", "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711", "music-root")

	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: "album-tracks-ns"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"},
	}
	require.NoError(t, c.Create(ctx, alb))
	metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
		catalogac.AlbumMetadata().WithTitle("OK Computer").WithRefreshedAt(metav1.Now())))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Album
		if err := c.Get(ctx, types.NamespacedName{Namespace: alb.Namespace, Name: alb.Name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	fetched := &pkgmetadata.Album{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"},
		Releases: []pkgmetadata.AlbumRelease{{
			IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "rel-1"},
			Media: []pkgmetadata.Medium{{Position: 1, Tracks: []pkgmetadata.Track{
				{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-1"}, Title: "Airbag", Position: 1},
				{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "rec-2"}, Title: "Paranoid Android", Position: 2},
			}}},
		}},
	}
	r := &album.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{album: fetched}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: alb.Namespace, Name: alb.Name}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var got catalogv1alpha1.Album
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		return len(got.Status.Tracks) > 0
	}, 5*time.Second, 10*time.Millisecond, "the track-listing RPC sync never landed status.tracks")

	require.Len(t, got.Status.Tracks, 2)
	assert.Equal(t, "rec-1", got.Status.Tracks[0].RecordingID)
	assert.Equal(t, "Airbag", got.Status.Tracks[0].Title)
	assert.EqualValues(t, 0, got.Status.TrackFileCount, "no track has an imported file yet")
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.AlbumConditionInvalid))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, "TracksSynced"))
}

func TestAlbumReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("album-transient-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("album-transient-ns", "music-root", "/data/media/music")))
	createSteadyArtist(t, ctx, c, "album-transient-ns", "radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711", "music-root")

	driveToReady := func(t *testing.T, name string) reconcile.Request {
		t.Helper()
		alb := &catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "album-transient-ns"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-" + name},
		}
		require.NoError(t, c.Create(ctx, alb))
		metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
			catalogac.AlbumMetadata().WithTitle("Steady State").WithRefreshedAt(metav1.Now())))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Album
			if err := c.Get(ctx, types.NamespacedName{Namespace: "album-transient-ns", Name: name}, &got); err != nil {
				return false
			}
			return got.Status.Metadata != nil
		}, 5*time.Second, 10*time.Millisecond)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "album-transient-ns", Name: name}}
		r := &album.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{}}}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		var got catalogv1alpha1.Album
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Status.Path != ""
		}, 5*time.Second, 10*time.Millisecond, "setup: album never resolved its path")
		require.True(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
		return req
	}

	t.Run("QueueFull on a stale-metadata publish does not release the steady state", func(t *testing.T) {
		req := driveToReady(t, "queuefull-steady")

		var before catalogv1alpha1.Album
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		oldRefresh := metav1.NewTime(time.Now().Add(-10 * 24 * time.Hour))
		staleAC := catalogac.Album(before.Name, before.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
			catalogac.AlbumMetadata().WithTitle("Steady State").WithRefreshedAt(oldRefresh)))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, staleAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Album
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil || got.Status.Metadata == nil {
				return false
			}
			return got.Status.Metadata.RefreshedAt.Time.Before(time.Now().Add(-5 * 24 * time.Hour))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &album.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{err: events.ErrQueueFull}, requester: fakeAlbumLookupRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Album
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, "QueueFull")
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
		assert.Equal(t, before.Status.Phase, after.Status.Phase, "QueueFull must not release Phase")
	})

	t.Run("ArtistNotFound does not release the steady state", func(t *testing.T) {
		// AlbumSpec.ArtistRef is immutable (its own CEL rule), so this
		// scenario cannot reuse driveToReady's shared "radiohead" artist --
		// mutating spec.artistRef on an existing Album is rejected outright
		// by the apiserver. Give this sub-test its own throwaway Artist
		// instead, drive an Album against it, then delete the ARTIST (not
		// the Album, and not the immutable ref) to force the NotFound this
		// scenario needs. The Album carries no ownerReference to this
		// Artist (it was created directly by this test, not by the real
		// artist.Reconciler's fan-out), so deleting the Artist does not
		// cascade-delete it.
		createSteadyArtist(t, ctx, c, "album-transient-ns", "the-throwaway-artist", "mbid-throwaway", "music-root")
		alb := &catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "noartist-steady", Namespace: "album-transient-ns"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "the-throwaway-artist", ReleaseGroupID: "rg-noartist-steady"},
		}
		require.NoError(t, c.Create(ctx, alb))
		metaAC := catalogac.Album(alb.Name, alb.Namespace).WithStatus(catalogac.AlbumStatus().WithMetadata(
			catalogac.AlbumMetadata().WithTitle("Steady State").WithRefreshedAt(metav1.Now())))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Album
			if err := c.Get(ctx, types.NamespacedName{Namespace: "album-transient-ns", Name: "noartist-steady"}, &got); err != nil {
				return false
			}
			return got.Status.Metadata != nil
		}, 5*time.Second, 10*time.Millisecond)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "album-transient-ns", Name: "noartist-steady"}}
		r := &album.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{}}}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		var before catalogv1alpha1.Album
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &before); err != nil {
				return false
			}
			return before.Status.Path != ""
		}, 5*time.Second, 10*time.Millisecond, "setup: album never resolved its path")
		require.True(t, k8s.IsConditionTrue(before.Status.Conditions, k8s.ConditionReady))

		var throwawayArtist catalogv1alpha1.Artist
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "album-transient-ns", Name: "the-throwaway-artist"}, &throwawayArtist))
		require.NoError(t, c.Delete(ctx, &throwawayArtist))
		require.Eventually(t, func() bool {
			return apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: "album-transient-ns", Name: "the-throwaway-artist"}, &catalogv1alpha1.Artist{}))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &album.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumLookupRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Album
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
			return cond != nil && cond.Reason == "ArtistNotFound"
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "ArtistNotFound must not release Path")
		assert.Equal(t, before.Status.Phase, after.Status.Phase, "ArtistNotFound must not release Phase")
	})
}
