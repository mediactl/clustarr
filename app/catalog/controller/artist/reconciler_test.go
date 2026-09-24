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
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
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
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
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

// startCacheOnly starts a real ctrl.Manager's cache (so syncAlbums'
// field-indexed List works) WITHOUT wiring an artist.Reconciler's watches to
// it, so nothing auto-reconciles. Used by tests that call r.Reconcile()
// directly instead of going through a running controller. Mirrors
// series_test.startCacheOnly; the one field index registered here must stay
// in sync with artist.Reconciler.SetupWithManager's own registration.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Album{}, ".spec.artistRef",
		func(o client.Object) []string {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok {
				return nil
			}
			return []string{alb.Spec.ArtistRef}
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

type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	if f.err != nil {
		return events.Receipt{}, f.err
	}
	return events.Receipt{}, nil
}

// fakeAlbumRPC implements the release-group-listing half of the
// reconciler's bus.Request: Kind=MediaKindAlbum,
// IDs={mb-artist: <MusicBrainzID>}, Results [][]byte of JSON
// pkgmetadata.Album, mirroring series_test.fakeEpisodeRPC.
type fakeAlbumRPC struct {
	albums []pkgmetadata.Album
	err    error
}

func (f fakeAlbumRPC) Request(_ context.Context, _ string, _, out any) error {
	if f.err != nil {
		return f.err
	}
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

// combinedBus satisfies the reconciler's narrowed bus interface
// (events.Publisher + Request) by pairing a real events.Publisher with a
// swappable Request implementation, mirroring series_test.combinedBus.
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

// TestArtistReconcilerQueueFull proves ErrQueueFull from Publish sets
// QueueFull=True and requeues after exactly one minute, mirroring
// series_test.TestSeriesReconcilerQueueFull.
func TestArtistReconcilerQueueFull(t *testing.T) {
	ctx := context.Background()
	c := newBareTestClient(t)
	require.NoError(t, c.Create(ctx, testNamespace("artist-queuefull-ns")))

	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "artist-queuefull-ns"},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "none", RootFolderRef: "none"},
	}
	require.NoError(t, c.Create(ctx, a))

	r := &artist.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{err: events.ErrQueueFull}, requester: fakeAlbumRPC{}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "artist-queuefull-ns", Name: "radiohead"}}
	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Artist
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	cond := k8s.FindCondition(got.Status.Conditions, "QueueFull")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	metaReady := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ArtistConditionMetadataReady)
	assert.Nil(t, metaReady, "MetadataReady must be left untouched when the publish never happened")
}

// TestArtistReconcilerNeverClaimsStatusMetadata asserts on managedFields,
// not values, per the standing brief: k8s.PatchStatus forces ownership, so
// an over-claim of status.metadata would be invisible in the object's
// values and would only show up here.
func TestArtistReconcilerNeverClaimsStatusMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("artist-managedfields-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("artist-managedfields-ns", "music-root", "/data/media/music")))

	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "artist-managedfields-ns"},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "none", RootFolderRef: "music-root",
			MetadataProfile: catalogv1alpha1.MusicMetadataProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}},
		},
	}
	require.NoError(t, c.Create(ctx, a))

	// Simulate the metadata gateway's OWN write under its OWN manager,
	// exactly as catalogarr/metadata/worker.go's Handler does, so this
	// reconcile's staleness check reads metaReady=true and this reconciler's
	// own apply has something to (not) collide with.
	metaAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(
		catalogac.ArtistStatus().WithMetadata(
			catalogac.ArtistMetadata().WithName("Radiohead").WithRefreshedAt(metav1.Now()),
		),
	)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Artist
		if err := c.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	r := &artist.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumRPC{}},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var got catalogv1alpha1.Artist
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		return k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.ArtistConditionAlbumsSynced)
	}, 5*time.Second, 10*time.Millisecond)

	fields := managedStatusFieldPaths(got.ManagedFields, "catalogarr")
	require.NotNil(t, fields, "no catalogarr/status entry in managedFields")
	names := statusFieldNames(fields)
	assert.True(t, names["path"], "catalogarr should own status.path")
	assert.True(t, names["albumCount"], "catalogarr should own status.albumCount")
	assert.False(t, names["metadata"], "catalogarr must never claim status.metadata -- that belongs to catalogarr-metadata")

	metaFields := managedStatusFieldPaths(got.ManagedFields, "catalogarr-metadata")
	require.NotNil(t, metaFields, "no catalogarr-metadata/status entry in managedFields")
	metaNames := statusFieldNames(metaFields)
	assert.True(t, metaNames["metadata"])
	assert.False(t, metaNames["path"], "catalogarr-metadata must never claim status.path")
}

// TestArtistReconcilerCreatesAlbumsFilteredByProfile proves DesiredAlbums'
// MetadataProfile filter runs through the real fan-out (not just the pure
// function): only the release group matching the default profile becomes
// an Album, named deterministically and monitored per AddOptions.Monitor.
func TestArtistReconcilerCreatesAlbumsFilteredByProfile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("artist-fanout-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("artist-fanout-ns", "music-root", "/data/media/music")))

	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "artist-fanout-ns"},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "none", RootFolderRef: "music-root",
			MetadataProfile: catalogv1alpha1.MusicMetadataProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}},
			AddOptions:      catalogv1alpha1.ArtistAddOptions{Monitor: catalogv1alpha1.ArtistMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, a))

	metaAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(
		catalogac.ArtistStatus().WithMetadata(
			catalogac.ArtistMetadata().WithName("Radiohead").WithRefreshedAt(metav1.Now()),
		),
	)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Artist
		if err := c.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	requester := fakeAlbumRPC{albums: []pkgmetadata.Album{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"}, Title: "OK Computer", PrimaryType: "Album"},
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "some-single-id"}, Title: "Creep", PrimaryType: "Single"},
	}}
	r := &artist.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: requester},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	wantName := artist.AlbumName("radiohead", "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9")
	var alb catalogv1alpha1.Album
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: "artist-fanout-ns", Name: wantName}, &alb) == nil
	}, 5*time.Second, 10*time.Millisecond, "the accepted release group's Album was never created")

	assert.Equal(t, "radiohead", alb.Spec.ArtistRef)
	assert.Equal(t, "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9", alb.Spec.ReleaseGroupID)
	require.NotNil(t, alb.Spec.Monitored)
	assert.True(t, *alb.Spec.Monitored)
	assert.Nil(t, alb.Status.Metadata, "the Artist fan-out must never seed status.metadata -- that is the Album's own gateway fetch")

	// The Single was rejected by the default profile and must never become
	// an Album object.
	singleName := artist.AlbumName("radiohead", "some-single-id")
	err = c.Get(ctx, types.NamespacedName{Namespace: "artist-fanout-ns", Name: singleName}, &catalogv1alpha1.Album{})
	assert.Error(t, err, "a Single must be rejected by the default profile's PrimaryTypes={album,ep}")

	// AlbumCount rolls up from the List syncAlbums took BEFORE this pass's
	// own Create, exactly like series.Rollup's episodeCount -- "self
	// correcting... completed by the very next reconcile it triggers"
	// (series.syncEpisodes' own doc comment). No controller is wired to
	// this bare cache client (startCacheOnly), so nothing re-triggers that
	// next reconcile automatically; call it explicitly.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var artistAfter catalogv1alpha1.Artist
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &artistAfter); err != nil {
			return false
		}
		return artistAfter.Status.AlbumCount == 1
	}, 5*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 0, artistAfter.Status.AlbumFileCount, "no track files exist yet")
	assert.NotEmpty(t, artistAfter.Status.Path)
}

// TestArtistReconcilerNeverTouchesAnExistingAlbumsSpec proves ensureAlbum's
// create-only contract end to end: an Album that already exists keeps
// whatever spec.monitored a user (or an earlier fan-out) set, even though
// the current fan-out pass would otherwise compute a different value.
func TestArtistReconcilerNeverTouchesAnExistingAlbumsSpec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("artist-noupdate-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("artist-noupdate-ns", "music-root", "/data/media/music")))

	a := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "artist-noupdate-ns"},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "none", RootFolderRef: "music-root",
			MetadataProfile: catalogv1alpha1.MusicMetadataProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}},
			MonitorNewItems: catalogv1alpha1.MonitorNewItemsAll,
		},
	}
	require.NoError(t, c.Create(ctx, a))

	metaAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(catalogac.ArtistStatus().WithMetadata(
		catalogac.ArtistMetadata().WithName("Radiohead").WithRefreshedAt(metav1.Now())))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Artist
		if err := c.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	releaseGroupID := "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9"
	name := artist.AlbumName("radiohead", releaseGroupID)
	falseVal := false
	existing := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "artist-noupdate-ns"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: releaseGroupID, Monitored: &falseVal},
	}
	require.NoError(t, c.Create(ctx, existing))
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: "artist-noupdate-ns", Name: name}, &catalogv1alpha1.Album{}) == nil
	}, 5*time.Second, 10*time.Millisecond)

	// addOptionsApplied starts false, so the FIRST fan-out pass would decide
	// monitored via AddOptions.Monitor (unset -> zero value "", which
	// InitialAlbumMonitored's default case reports false) -- consistent
	// with the pre-existing Album's own Monitored=false, so this alone
	// would not distinguish "never touched" from "recomputed the same
	// value". Reconcile twice: the first pass also flips
	// status.addOptionsApplied to true, so the second pass is governed by
	// spec.monitorNewItems=All instead, which WOULD set true for a
	// brand-new album -- proving the untouched false survives only because
	// ensureAlbum's Get-then-switch never reaches its Create branch for an
	// object that already exists.
	requester := fakeAlbumRPC{albums: []pkgmetadata.Album{
		{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: releaseGroupID}, Title: "OK Computer", PrimaryType: "Album"},
	}}
	r := &artist.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: requester},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Artist
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		return got.Status.AddOptionsApplied
	}, 5*time.Second, 10*time.Millisecond)

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var after catalogv1alpha1.Album
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: "artist-noupdate-ns", Name: name}, &after); err != nil {
			return false
		}
		return after.ResourceVersion == existing.ResourceVersion // no write ever happened
	}, 2*time.Second, 10*time.Millisecond)
	require.NotNil(t, after.Spec.Monitored)
	assert.False(t, *after.Spec.Monitored, "an already-existing Album's spec.monitored must never be touched by a later fan-out pass")
}

// TestArtistReconcilerTransientFailuresPreserveSteadyState is the Artist
// analogue of series_test.TestSeriesReconcilerTransientFailuresPreserveSteadyState.
func TestArtistReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("artist-transient-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("artist-transient-ns", "music-root", "/data/media/music")))

	driveToReady := func(t *testing.T, name, mbid string) reconcile.Request {
		t.Helper()
		a := &catalogv1alpha1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "artist-transient-ns"},
			Spec: catalogv1alpha1.ArtistSpec{
				MusicBrainzID: mbid, QualityProfileRef: "none", RootFolderRef: "music-root",
				MetadataProfile: catalogv1alpha1.MusicMetadataProfile{PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"}},
				AddOptions:      catalogv1alpha1.ArtistAddOptions{Monitor: catalogv1alpha1.ArtistMonitorAll},
			},
		}
		require.NoError(t, c.Create(ctx, a))

		metaAC := catalogac.Artist(a.Name, a.Namespace).WithStatus(catalogac.ArtistStatus().WithMetadata(
			catalogac.ArtistMetadata().WithName("Steady State").WithRefreshedAt(metav1.Now())))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Artist
			if err := c.Get(ctx, types.NamespacedName{Namespace: "artist-transient-ns", Name: name}, &got); err != nil {
				return false
			}
			return got.Status.Metadata != nil
		}, 5*time.Second, 10*time.Millisecond)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "artist-transient-ns", Name: name}}
		requester := fakeAlbumRPC{albums: []pkgmetadata.Album{
			{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "rg-" + name}, Title: "Album One", PrimaryType: "Album"},
		}}
		r := &artist.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: combinedBus{Publisher: fakePublisher{}, requester: requester}}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		wantName := artist.AlbumName(name, "rg-"+name)
		require.Eventually(t, func() bool {
			return c.Get(ctx, types.NamespacedName{Namespace: "artist-transient-ns", Name: wantName}, &catalogv1alpha1.Album{}) == nil
		}, 5*time.Second, 10*time.Millisecond, "setup: album fan-out never created its Album")

		// AlbumCount rolls up from the List syncAlbums took before this
		// pass's own Create; a second reconcile completes it, mirroring
		// series.syncEpisodes' own "self correcting" doc comment. No
		// controller is wired to this bare cache client, so nothing
		// re-triggers that next reconcile automatically.
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		var got catalogv1alpha1.Artist
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Status.AlbumCount == 1
		}, 5*time.Second, 10*time.Millisecond, "setup: artist never rolled up the accepted album")
		require.NotEmpty(t, got.Status.Path)
		require.True(t, k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.ArtistConditionAlbumsSynced))
		return req
	}

	t.Run("QueueFull on a stale-metadata publish does not release the steady state", func(t *testing.T) {
		req := driveToReady(t, "queuefull-steady", "mbid-queuefull")

		var before catalogv1alpha1.Artist
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		oldRefresh := metav1.NewTime(time.Now().Add(-10 * 24 * time.Hour))
		staleAC := catalogac.Artist(before.Name, before.Namespace).WithStatus(catalogac.ArtistStatus().WithMetadata(
			catalogac.ArtistMetadata().WithName("Steady State").WithRefreshedAt(oldRefresh)))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, staleAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Artist
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil || got.Status.Metadata == nil {
				return false
			}
			return got.Status.Metadata.RefreshedAt.Time.Before(time.Now().Add(-5 * 24 * time.Hour))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &artist.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{err: events.ErrQueueFull}, requester: fakeAlbumRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Artist
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, "QueueFull")
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
		assert.Equal(t, before.Status.AlbumCount, after.Status.AlbumCount, "QueueFull must not release AlbumCount")
		assert.Equal(t, before.Status.AlbumFileCount, after.Status.AlbumFileCount, "QueueFull must not release AlbumFileCount")
	})

	t.Run("RootFolderNotFound does not release the steady state", func(t *testing.T) {
		req := driveToReady(t, "rootfolder-steady", "mbid-rootfolder")

		var before catalogv1alpha1.Artist
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		var withMissingRoot catalogv1alpha1.Artist
		require.NoError(t, c.Get(ctx, req.NamespacedName, &withMissingRoot))
		withMissingRoot.Spec.RootFolderRef = "does-not-exist"
		require.NoError(t, c.Update(ctx, &withMissingRoot))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Artist
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Spec.RootFolderRef == "does-not-exist"
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &artist.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Artist
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
			return cond != nil && cond.Reason == "RootFolderNotFound"
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "RootFolderNotFound must not release Path")
		assert.Equal(t, before.Status.AlbumCount, after.Status.AlbumCount, "RootFolderNotFound must not release AlbumCount")
		assert.Equal(t, before.Status.AlbumFileCount, after.Status.AlbumFileCount, "RootFolderNotFound must not release AlbumFileCount")
	})

	t.Run("the dead-lettered annotation folds into DeadLettered without releasing the steady state", func(t *testing.T) {
		req := driveToReady(t, "deadletter-steady", "mbid-deadletter")
		var before catalogv1alpha1.Artist
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))
		annotated := before.DeepCopy()
		if annotated.Annotations == nil {
			annotated.Annotations = map[string]string{}
		}
		annotated.Annotations[k8s.AnnotationDeadLettered] = "clustarr.work.metadata.normal.x@2026-09-23T10:00:00Z"
		require.NoError(t, c.Patch(ctx, annotated, client.MergeFrom(&before)))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Artist
			return c.Get(ctx, req.NamespacedName, &got) == nil && got.Annotations[k8s.AnnotationDeadLettered] != ""
		}, 5*time.Second, 10*time.Millisecond)

		r := &artist.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeAlbumRPC{}},
		}
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		var got catalogv1alpha1.Artist
		require.Eventually(t, func() bool {
			return c.Get(ctx, req.NamespacedName, &got) == nil && k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionDeadLettered)
		}, 5*time.Second, 10*time.Millisecond, "the annotation never became the DeadLettered condition")
		assert.Equal(t, before.Status.Path, got.Status.Path)
		assert.Equal(t, before.Status.AlbumCount, got.Status.AlbumCount)
		assert.True(t, got.Status.AddOptionsApplied)

		cleared := got.DeepCopy()
		delete(cleared.Annotations, k8s.AnnotationDeadLettered)
		require.NoError(t, c.Patch(ctx, cleared, client.MergeFrom(&got)))
		require.Eventually(t, func() bool {
			var g catalogv1alpha1.Artist
			return c.Get(ctx, req.NamespacedName, &g) == nil && g.Annotations[k8s.AnnotationDeadLettered] == ""
		}, 5*time.Second, 10*time.Millisecond)
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return c.Get(ctx, req.NamespacedName, &got) == nil && k8s.FindCondition(got.Status.Conditions, k8s.ConditionDeadLettered) == nil
		}, 5*time.Second, 10*time.Millisecond, "removing the annotation never cleared the condition")
	})
}
