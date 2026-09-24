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
	"sync"
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
		Client: c, Reader: c, Bus: bus,
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
	h := &artwork.Handler{Client: c, Reader: c, Bus: bus, Fetcher: &artwork.Fetcher{Store: bus.ObjectStore(events.BucketArtwork)}}

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

// renderCollector records every RenderOverlay task delivered on a bus.
type renderCollector struct {
	mu   sync.Mutex
	envs []*events.Envelope
}

func collectRenders(t *testing.T, ctx context.Context, bus events.Bus) *renderCollector {
	t.Helper()
	c := &renderCollector{}
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream: events.StreamWorkCatalogarr, Durable: "test-render-collector",
		Filters: []string{events.FilterCatalogArtworkRender},
	}, func(_ context.Context, m events.Message) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.envs = append(c.envs, m.Envelope())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return c
}

func (c *renderCollector) all() []*events.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*events.Envelope(nil), c.envs...)
}

// settled waits for want deliveries, then a little longer, so a test that
// asserts "exactly want" is not fooled by one still in flight.
func (c *renderCollector) settled(t *testing.T, want int) []*events.Envelope {
	t.Helper()
	require.Eventually(t, func() bool { return len(c.all()) >= want }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	return c.all()
}

func assertRender(t *testing.T, env *events.Envelope, uid types.UID, digest string) {
	t.Helper()
	assert.Equal(t, schema.MsgIDForRenderOverlay(uid, digest), env.ID)
	var task schema.RenderOverlayTask
	require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
	assert.Equal(t, "original", task.Reason)
	assert.Equal(t, commonv1.MediaKindMovie, task.MediaRef.Kind)
}

// flakyPublisher fails the first render publish it sees, standing in for a
// broker blip -- or a pod death -- after the apply landed.
type flakyPublisher struct {
	events.Publisher
	failed atomic.Bool
}

func (p *flakyPublisher) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	if strings.HasPrefix(subject, "clustarr.work.catalogarr.artwork.render.") && p.failed.CompareAndSwap(false, true) {
		return events.Receipt{}, errors.New("nats: timeout")
	}
	return p.Publisher.Publish(ctx, subject, e, opts...)
}

// seedMovie creates a Movie whose gateway status already records a stored
// provider poster, and returns it.
func seedMovie(t *testing.T, ctx context.Context, c client.Client, store events.ObjectStore, ns, posterURL string, body []byte) *catalogv1alpha1.Movie {
	t.Helper()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "q", RootFolderRef: "r"},
	}
	require.NoError(t, c.Create(ctx, m))
	info, err := store.Put(ctx, events.ArtworkKey(commonv1.MediaKindMovie, m.UID, "poster", events.ArtworkVariantOriginal),
		bytes.NewReader(body), map[string]string{"Content-Type": "image/png", "Clustarr-Source": "provider", "Clustarr-Source-URL": posterURL})
	require.NoError(t, err)
	at := metav1.NewTime(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC))
	_, err = k8s.PatchStatus(ctx, c, catalogstatus.GatewayManager, catalogac.Movie(m.Name, ns).WithStatus(catalogac.MovieStatus().
		WithMetadata(catalogac.MovieMetadata().WithTitle("Heat").WithRefreshedAt(at).
			WithImages(catalogac.Image().WithType(catalogv1alpha1.ImageTypePoster).WithURL(posterURL))).
		WithArtwork(catalogstatus.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{{
			Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider, SourceURL: posterURL,
			Digest: info.Digest, SizeBytes: info.Size, UpdatedAt: at,
		}})...)))
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(m), m))
	return m
}

func setMovieOverride(t *testing.T, ctx context.Context, c client.Client, m *catalogv1alpha1.Movie, url string) {
	t.Helper()
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(m), m))
	m.Spec.Artwork = nil
	if url != "" {
		m.Spec.Artwork = []catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}
	}
	require.NoError(t, c.Update(ctx, m))
}

// A pass that changes nothing still publishes the render task for the
// poster it has: the task is level-driven, so a render lost on any earlier
// pass is recovered by the next one.
func TestAPassWithAnUnchangedPosterStillPublishesItsRender(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	renders := collectRenders(t, ctx, bus)
	store := bus.ObjectStore(events.BucketArtwork)
	srv := newImageServer(t)
	body := pngBytes(t, 4, 6, color.White)
	posterURL := srv.serve("/poster.png", "image/png", body)
	m := seedMovie(t, ctx, c, store, "render-unchanged", posterURL, body)

	h := &artwork.Handler{Client: c, Reader: c, Bus: bus, Fetcher: &artwork.Fetcher{Store: store, HTTP: srv.Client()}}
	require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, m.Namespace, m.Name)))

	assert.Zero(t, srv.totalHits(), "nothing was stale, nothing was fetched")
	envs := renders.settled(t, 1)
	require.Len(t, envs, 1)
	assertRender(t, envs[0], m.UID, digestOf(body))
}

// The review's lost-render case: the apply lands, the render publish fails,
// the delivery is retried -- and the retry, which finds nothing stale,
// publishes the render anyway.
func TestARenderLostAfterTheApplyIsPublishedOnRedelivery(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	renders := collectRenders(t, ctx, bus)
	store := bus.ObjectStore(events.BucketArtwork)
	srv := newImageServer(t)
	providerBody := pngBytes(t, 4, 6, color.White)
	m := seedMovie(t, ctx, c, store, "render-redelivered", srv.serve("/provider.png", "image/png", providerBody), providerBody)
	customBody := pngBytes(t, 4, 6, color.Black)
	setMovieOverride(t, ctx, c, m, srv.serve("/custom.png", "image/png", customBody))

	flaky := &flakyPublisher{Publisher: bus}
	h := &artwork.Handler{Client: c, Reader: c, Bus: flaky, Fetcher: &artwork.Fetcher{Store: store, HTTP: srv.Client()}}

	require.Error(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, m.Namespace, m.Name)),
		"the failed publish fails the delivery, so it is retried")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(m), m))
	require.Len(t, m.Status.Artwork, 1)
	require.Equal(t, digestOf(customBody), m.Status.Artwork[0].Digest, "the apply had landed before the publish failed")
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, renders.all())

	require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, m.Namespace, m.Name)), "the redelivery")
	assert.Equal(t, 1, srv.hitsFor("/custom.png"), "the redelivery found nothing stale")
	envs := renders.settled(t, 1)
	require.Len(t, envs, 1, "and published the render all the same")
	assertRender(t, envs[0], m.UID, digestOf(customBody))
}

// Dropping the poster (a custom override removed, no provider poster to
// replace it) publishes the render under RenderNoPoster so the renderer
// clears the overlay -- and, while an overlay is still recorded, every pass
// does, so a lost one is recovered too.
func TestAPosterDropPublishesTheNoneRender(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	renders := collectRenders(t, ctx, bus)
	store := bus.ObjectStore(events.BucketArtwork)
	srv := newImageServer(t)

	const ns = "render-none"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 949, QualityProfileRef: "q", RootFolderRef: "r",
			Artwork: []catalogv1alpha1.ArtworkOverride{{
				Type: catalogv1alpha1.ImageTypePoster, URL: srv.serve("/custom.png", "image/png", pngBytes(t, 4, 6, color.Black)),
			}},
		},
	}
	require.NoError(t, c.Create(ctx, m))
	h := &artwork.Handler{Client: c, Reader: c, Bus: bus, Fetcher: &artwork.Fetcher{Store: store, HTTP: srv.Client()}}
	require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, ns, m.Name)))
	require.Len(t, renders.settled(t, 1), 1, "the custom poster's own render")

	// The renderer has rendered it: status.overlay stands.
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrArtwork, catalogac.Movie(m.Name, ns).WithStatus(
		catalogac.MovieStatus().WithOverlay(catalogac.OverlayEntry().WithProfileRef("badges").WithDigest("ov").
			WithRenderedFrom("in").WithUpdatedAt(metav1.NewTime(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC))))))
	require.NoError(t, err)

	setMovieOverride(t, ctx, c, m, "")
	flaky := &flakyPublisher{Publisher: bus}
	h.Bus = flaky
	require.Error(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, ns, m.Name)), "the drop's render publish is lost")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(m), m))
	require.Empty(t, m.Status.Artwork, "the drop itself landed")

	require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, ns, m.Name)), "the redelivery")
	envs := renders.settled(t, 2)
	require.Len(t, envs, 2)
	assertRender(t, envs[1], m.UID, artwork.RenderNoPoster)
	assert.Equal(t, string(m.UID)+"/render/none", envs[1].ID)
}

// Only Movie and Series carry an overlay, so only they get a render task: a
// pass that stores an Album's poster publishes none. Before the fix the
// gateway published one for every kind with a poster and the renderer
// refused each, dead-lettering it. The Movie pass beside it proves the
// collector would have seen one.
func TestANonOverlaidKindPublishesNoRender(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	renders := collectRenders(t, ctx, bus)
	store := bus.ObjectStore(events.BucketArtwork)
	srv := newImageServer(t)
	body := pngBytes(t, 4, 6, color.White)
	posterURL := srv.serve("/poster.png", "image/png", body)

	m := seedMovie(t, ctx, c, store, "render-kinds", posterURL, body)
	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: m.Namespace},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433"},
	}
	require.NoError(t, c.Create(ctx, alb))
	_, err := k8s.PatchStatus(ctx, c, catalogstatus.GatewayManager, catalogac.Album(alb.Name, alb.Namespace).WithStatus(
		catalogac.AlbumStatus().WithMetadata(catalogac.AlbumMetadata().WithTitle("OK Computer").
			WithImages(catalogac.Image().WithType(catalogv1alpha1.ImageTypePoster).WithURL(posterURL)))))
	require.NoError(t, err)

	h := &artwork.Handler{Client: c, Reader: c, Bus: bus, Fetcher: &artwork.Fetcher{Store: store, HTTP: srv.Client()}}
	require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindAlbum, alb.Namespace, alb.Name)))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(alb), alb))
	require.Len(t, alb.Status.Artwork, 1, "the album's poster was stored and recorded")

	require.NoError(t, h.Handle(ctx, fetchTask(t, commonv1.MediaKindMovie, m.Namespace, m.Name)))
	envs := renders.settled(t, 1)
	require.Len(t, envs, 1, "the movie's render, and nothing for the album")
	assertRender(t, envs[0], m.UID, digestOf(body))
}
