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

package artwork_test

import (
	"bytes"
	"context"
	"errors"
	"image/color"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newEnvtestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// testMessage is the minimal events.Message Handle needs, counting
// heartbeats.
type testMessage struct {
	env        *events.Envelope
	inProgress atomic.Int32
}

func (m *testMessage) Envelope() *events.Envelope               { return m.env }
func (m *testMessage) Subject() string                          { return "" }
func (m *testMessage) Attempt() uint64                          { return 1 }
func (m *testMessage) Ack(context.Context) error                { return nil }
func (m *testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *testMessage) Term(context.Context, string) error       { return nil }
func (m *testMessage) InProgress(context.Context) error {
	m.inProgress.Add(1)
	return nil
}

func fetchTask(t *testing.T, kind commonv1.MediaKind, ns, name string) *testMessage {
	t.Helper()
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.ArtworkFetchTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.ArtworkFetchTask{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}})
	require.NoError(t, err)
	return &testMessage{env: env}
}

// TestArtworkTaskReDeclaresOnlyWhatTheGatewayOwns drives the
// catalogarr-artwork-fetch consumer against an Album that already has the
// gateway's status.metadata and status.artwork AND the Album reconciler's
// status.metadata.selectedReleaseID. The task fetched no metadata, so its
// apply must re-declare the gateway's metadata leaves (or release them) and
// must not declare selectedReleaseID (or co-own it silently).
func TestArtworkTaskReDeclaresOnlyWhatTheGatewayOwns(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns, name = "artwork-task", "ok-computer"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))

	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	store := bus.ObjectStore(events.BucketArtwork)
	srv := newImageServer(t)
	providerBody := pngBytes(t, 4, 6, color.White)
	providerURL := srv.serve("/provider.png", "image/png", providerBody)
	customBody := pngBytes(t, 4, 6, color.Black)
	customURL := srv.serve("/custom.png", "image/png", customBody)
	brokenURL := srv.serveStatus("/broken.png", 500)
	rec := k8sevents.NewFakeRecorder(16)
	h := &artwork.Handler{
		Client: c, Bus: bus,
		Fetcher: &artwork.Fetcher{Store: store, HTTP: srv.Client(), Recorder: rec},
	}

	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433"},
	}
	require.NoError(t, c.Create(ctx, alb))
	key := types.NamespacedName{Namespace: ns, Name: name}

	// The steady state: the gateway's provider poster, stored and recorded
	// in the gateway's one apply, and the reconciler's leaf beside it.
	info, err := store.Put(ctx, events.ArtworkKey(commonv1.MediaKindAlbum, alb.UID, "poster", events.ArtworkVariantOriginal),
		bytes.NewReader(providerBody), map[string]string{"Content-Type": "image/png", "Clustarr-Source": "provider", "Clustarr-Source-URL": providerURL})
	require.NoError(t, err)
	refreshed := metav1.NewTime(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC))
	_, err = k8s.PatchStatus(ctx, c, catalogstatus.GatewayManager, catalogac.Album(name, ns).WithStatus(catalogac.AlbumStatus().
		WithMetadata(catalogac.AlbumMetadata().WithTitle("OK Computer").WithRefreshedAt(refreshed).
			WithImages(catalogac.Image().WithType(catalogv1alpha1.ImageTypePoster).WithURL(providerURL))).
		WithArtwork(catalogstatus.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{{
			Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider, SourceURL: providerURL,
			Digest: info.Digest, SizeBytes: info.Size, UpdatedAt: refreshed,
		}})...)))
	require.NoError(t, err)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Album(name, ns).WithStatus(catalogac.AlbumStatus().
		WithMetadata(catalogac.AlbumMetadata().WithSelectedReleaseID("rel-1"))))
	require.NoError(t, err)

	get := func() *catalogv1alpha1.Album {
		var got catalogv1alpha1.Album
		require.NoError(t, c.Get(ctx, key, &got))
		return &got
	}
	setOverride := func(url string) {
		got := get()
		if url == "" {
			got.Spec.Artwork = nil
		} else {
			got.Spec.Artwork = []catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}
		}
		require.NoError(t, c.Update(ctx, got))
	}
	assertMetadataIntactAndSplit := func(got *catalogv1alpha1.Album) {
		t.Helper()
		require.NotNil(t, got.Status.Metadata)
		assert.Equal(t, "OK Computer", got.Status.Metadata.Title, "the gateway's metadata was re-declared, not released")
		assert.True(t, got.Status.Metadata.RefreshedAt.Equal(&refreshed), "refreshedAt too -- a task that fetched no metadata does not move it")
		require.Len(t, got.Status.Metadata.Images, 1)
		assert.Equal(t, "rel-1", got.Status.Metadata.SelectedReleaseID, "the reconciler's leaf stands")

		gateway, err := catalogstatus.OwnedStatusPaths(got.ManagedFields, catalogstatus.GatewayManager)
		require.NoError(t, err)
		assert.Contains(t, gateway, "metadata.title")
		assert.Contains(t, gateway, "metadata.refreshedAt")
		assert.NotContains(t, gateway, "metadata.selectedReleaseID",
			"extraction re-declares what the gateway owns; a copy of status.metadata would have claimed the reconciler's leaf")
		for _, leaf := range catalogstatus.ArtworkEntryLeaves {
			assert.Contains(t, gateway, "artwork[type=poster]."+leaf)
		}
		reconciler, err := catalogstatus.OwnedStatusPaths(got.ManagedFields, k8s.ManagerCatalogarr)
		require.NoError(t, err)
		assert.Contains(t, reconciler, "metadata.selectedReleaseID")
		for p := range reconciler {
			assert.False(t, strings.HasPrefix(p, "artwork"), "the reconciler co-owns %s", p)
		}
	}

	t.Run("a custom override is fetched and recorded", func(t *testing.T) {
		setOverride(customURL)
		msg := fetchTask(t, commonv1.MediaKindAlbum, ns, name)
		require.NoError(t, h.Handle(ctx, msg))

		got := get()
		require.Len(t, got.Status.Artwork, 1)
		e := got.Status.Artwork[0]
		assert.Equal(t, catalogv1alpha1.ArtworkSourceCustom, e.Source)
		assert.Equal(t, customURL, e.SourceURL)
		assert.Equal(t, digestOf(customBody), e.Digest)
		_, drifted := artwork.Drift(got.Spec.Artwork, got.Status.Artwork)
		assert.False(t, drifted, "the pass clears the drift that triggered it")
		assertMetadataIntactAndSplit(got)
		assert.Empty(t, rec.Events)
	})

	t.Run("a custom override that fails keeps the entry (R3)", func(t *testing.T) {
		before := get().Status.Artwork
		setOverride(brokenURL)
		require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindAlbum, ns, name)),
			"a failed fetch is recorded as an Event, not retried by the queue")

		got := get()
		assert.Equal(t, before, got.Status.Artwork)
		assertMetadataIntactAndSplit(got)
		select {
		case ev := <-rec.Events:
			assert.Contains(t, ev, artwork.ReasonFetchFailed)
		default:
			t.Fatal("no ArtworkFetchFailed Event")
		}
		assert.Zero(t, srv.hitsFor("/provider.png"), "never a fallback to the provider's poster")
	})

	t.Run("removing the override restores the provider poster", func(t *testing.T) {
		setOverride("")
		require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindAlbum, ns, name)))

		got := get()
		require.Len(t, got.Status.Artwork, 1)
		assert.Equal(t, catalogv1alpha1.ArtworkSourceProvider, got.Status.Artwork[0].Source)
		assert.Equal(t, providerURL, got.Status.Artwork[0].SourceURL)
		assert.Equal(t, digestOf(providerBody), got.Status.Artwork[0].Digest)
		assertMetadataIntactAndSplit(got)
	})
}

func TestArtworkTaskDiscards(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	h := &artwork.Handler{Client: c, Bus: bus, Fetcher: &artwork.Fetcher{Store: bus.ObjectStore(events.BucketArtwork)}}

	var discard *events.DiscardError
	err := h.Handle(ctx, fetchTask(t, commonv1.MediaKindEpisode, "tv", "s01e01"))
	require.True(t, errors.As(err, &discard), "an Episode has no artwork: %v", err)

	err = h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, "nowhere", "gone"))
	require.True(t, errors.As(err, &discard), "a deleted item: %v", err)
	require.ErrorIs(t, err, artwork.ErrItemGone)

	bad := &testMessage{env: &events.Envelope{Key: "no-slash", Schema: schema.ArtworkFetchTask{}.Schema(), Data: []byte(`{}`)}}
	require.True(t, errors.As(h.Handle(ctx, bad), &discard))
}

func TestKeepAliveHeartbeatsUntilStopped(t *testing.T) {
	msg := &testMessage{}
	stop := artwork.KeepAlive(context.Background(), msg, 5*time.Millisecond)
	require.Eventually(t, func() bool { return msg.inProgress.Load() >= 2 }, time.Second, time.Millisecond)
	stop()
	n := msg.inProgress.Load()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, n, msg.inProgress.Load(), "no heartbeat after stop returns")
	stop() // idempotent
}

// TestReaperAgainstARealAPIServer runs the reaper's metadata-only List
// (PartialObjectMetadataList, uncached, paged) against a real apiserver,
// which the fake client in reaper_test.go only imitates.
func TestReaperAgainstARealAPIServer(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "reap"}}))
	live := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "reap"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "q", RootFolderRef: "r"},
	}
	require.NoError(t, c.Create(ctx, live))
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "dune", Namespace: "reap"},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL893415W"},
	}
	require.NoError(t, c.Create(ctx, book))

	fx := newReapFixture(t)
	keep := []string{
		fx.put(t, commonv1.MediaKindMovie, live.UID, "poster", events.ArtworkVariantOriginal),
		fx.put(t, commonv1.MediaKindMovie, live.UID, "poster", events.ArtworkVariantOverlay),
		fx.put(t, commonv1.MediaKindBook, book.UID, "poster", events.ArtworkVariantOriginal),
	}
	fx.put(t, commonv1.MediaKindMovie, "deleted-long-ago", "fanart", events.ArtworkVariantOriginal)
	fx.put(t, commonv1.MediaKindBook, live.UID, "poster", events.ArtworkVariantOriginal) // a Movie's UID under book/
	fx.clock.Advance(grace + time.Minute)

	deleted, err := fx.reaper(c).Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	assert.ElementsMatch(t, keep, fx.names(t))
}
