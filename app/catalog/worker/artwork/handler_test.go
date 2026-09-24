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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
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
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/overlay"
)

var (
	envOnce   sync.Once
	envClient client.Client
	envErr    error
)

// envtestClient starts one control plane for the package's envtests (each
// test takes its own namespace) and skips without the assets.
func envtestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	envOnce.Do(func() {
		env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
		cfg, err := env.Start()
		if err != nil {
			envErr = err
			return
		}
		envClient, envErr = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	})
	require.NoError(t, envErr)
	return envClient
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }

// countingStore counts Puts and lets a test act in the middle of a Put or
// before a Get.
type countingStore struct {
	events.ObjectStore
	puts  atomic.Int32
	onPut func(name string)
	onGet func(name string)
}

func (s *countingStore) Get(ctx context.Context, name string) (events.ObjectInfo, io.ReadCloser, error) {
	if s.onGet != nil {
		s.onGet(name)
	}
	return s.ObjectStore.Get(ctx, name)
}

func (s *countingStore) Put(ctx context.Context, name string, r io.Reader, h map[string]string) (events.ObjectInfo, error) {
	s.puts.Add(1)
	info, err := s.ObjectStore.Put(ctx, name, r, h)
	if s.onPut != nil {
		s.onPut(name)
	}
	return info, err
}

type testMessage struct{ env *events.Envelope }

func (m *testMessage) Envelope() *events.Envelope               { return m.env }
func (m *testMessage) Subject() string                          { return "" }
func (m *testMessage) Attempt() uint64                          { return 1 }
func (m *testMessage) Ack(context.Context) error                { return nil }
func (m *testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *testMessage) Term(context.Context, string) error       { return nil }
func (m *testMessage) InProgress(context.Context) error         { return nil }

func renderTask(t *testing.T, kind commonv1.MediaKind, ns, name string) *testMessage {
	t.Helper()
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.RenderOverlayTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.RenderOverlayTask{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}, Reason: "original"})
	require.NoError(t, err)
	return &testMessage{env: env}
}

func posterPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 0x60, A: 0xFF})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

var critics = map[string]string{"overlay": "critics"}

// fixture is one Movie or Series in the steady state the gateway leaves:
// an original poster stored and recorded in status.artwork beside
// status.metadata with ratings, both in the gateway's one apply.
type fixture struct {
	t       *testing.T
	c       client.Client
	kind    commonv1.MediaKind
	key     types.NamespacedName
	store   *countingStore
	h       *artwork.Handler
	poster  []byte
	info    events.ObjectInfo
	ratings []catalogv1alpha1.Rating
}

func newFixture(t *testing.T, kind commonv1.MediaKind, ns string, badges ...catalogv1alpha1.RatingSource) *fixture {
	t.Helper()
	ctx := context.Background()
	c := envtestClient(t)
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))

	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	store := &countingStore{ObjectStore: bus.ObjectStore(events.BucketArtwork)}

	f := &fixture{t: t, c: c, kind: kind, store: store, poster: posterPNG(t, 200, 300)}
	f.h = &artwork.Handler{Client: c, Reader: c, Store: store}

	p := &catalogv1alpha1.OverlayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "critics", Namespace: ns},
		Spec:       catalogv1alpha1.OverlayProfileSpec{Selector: &metav1.LabelSelector{MatchLabels: critics}},
	}
	for _, b := range badges {
		p.Spec.Badges = append(p.Spec.Badges, catalogv1alpha1.OverlayBadge{Source: b})
	}
	require.NoError(t, c.Create(ctx, p))

	var obj client.Object
	switch kind {
	case commonv1.MediaKindMovie:
		obj = &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns, Labels: critics},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "q", RootFolderRef: "r"},
		}
	default:
		obj = &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "lost", Namespace: ns, Labels: critics},
			Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 73739, QualityProfileRef: "q", RootFolderRef: "r"},
		}
	}
	require.NoError(t, c.Create(ctx, obj))
	f.key = client.ObjectKeyFromObject(obj)

	var err error
	f.info, err = store.ObjectStore.Put(ctx, events.ArtworkKey(kind, obj.GetUID(), "poster", events.ArtworkVariantOriginal),
		bytes.NewReader(f.poster), map[string]string{"Content-Type": "image/png", "Clustarr-Source": "provider", "Clustarr-Source-URL": "https://img.example/p.png"})
	require.NoError(t, err)
	f.gateway([]catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781, Votes: 5000},
		{Source: catalogv1alpha1.RatingSourceMetacritic, ValueCentis: 7600},
	}, true)
	return f
}

var refreshed = metav1.NewTime(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC))

// gateway applies the gateway's complete declaration: status.metadata with
// ratings, and -- when withArtwork -- status.artwork's poster entry.
func (f *fixture) gateway(ratings []catalogv1alpha1.Rating, withArtwork bool) {
	f.t.Helper()
	f.ratings = ratings
	var entries []*catalogac.ArtworkEntryApplyConfiguration
	if withArtwork {
		entries = catalogstatus.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{{
			Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider,
			SourceURL: "https://img.example/p.png", Digest: f.info.Digest, SizeBytes: f.info.Size, UpdatedAt: refreshed,
		}})
	}
	rs := make([]*catalogac.RatingApplyConfiguration, 0, len(ratings))
	for _, r := range ratings {
		rs = append(rs, catalogac.Rating().WithSource(r.Source).WithValueCentis(r.ValueCentis).WithVotes(r.Votes))
	}
	var ac k8s.ApplyConfiguration
	switch f.kind {
	case commonv1.MediaKindMovie:
		st := catalogac.MovieStatus().WithMetadata(catalogac.MovieMetadata().WithTitle("Heat").WithRefreshedAt(refreshed).WithRatings(rs...))
		if withArtwork {
			st.WithArtwork(entries...)
		}
		ac = catalogac.Movie(f.key.Name, f.key.Namespace).WithStatus(st)
	default:
		st := catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle("Lost").WithRefreshedAt(refreshed).WithRatings(rs...))
		if withArtwork {
			st.WithArtwork(entries...)
		}
		ac = catalogac.Series(f.key.Name, f.key.Namespace).WithStatus(st)
	}
	_, err := k8s.PatchStatus(context.Background(), f.c, catalogstatus.GatewayManager, ac)
	require.NoError(f.t, err)
}

func (f *fixture) get() artwork.Item {
	f.t.Helper()
	obj, err := artwork.NewObject(f.kind)
	require.NoError(f.t, err)
	require.NoError(f.t, f.c.Get(context.Background(), f.key, obj))
	it, err := artwork.ItemOf(obj)
	require.NoError(f.t, err)
	return it
}

func (f *fixture) handle() error {
	f.t.Helper()
	return f.h.Handle(context.Background(), renderTask(f.t, f.kind, f.key.Namespace, f.key.Name))
}

func (f *fixture) overlayKey() string {
	return events.ArtworkKey(f.kind, f.get().Object.GetUID(), "poster", events.ArtworkVariantOverlay)
}

func (f *fixture) originalKey() string {
	return events.ArtworkKey(f.kind, f.get().Object.GetUID(), "poster", events.ArtworkVariantOriginal)
}

func (f *fixture) profile() *catalogv1alpha1.OverlayProfile {
	f.t.Helper()
	var p catalogv1alpha1.OverlayProfile
	require.NoError(f.t, f.c.Get(context.Background(), types.NamespacedName{Namespace: f.key.Namespace, Name: "critics"}, &p))
	return &p
}

func (f *fixture) wantDigest() string {
	return artwork.InputsDigest(f.info.Digest, artwork.ProfileHash(f.profile().Spec), f.ratings)
}

func (f *fixture) setLabels(lbls map[string]string) {
	f.t.Helper()
	it := f.get()
	it.Object.SetLabels(lbls)
	require.NoError(f.t, f.c.Update(context.Background(), it.Object))
}

// assertSplit holds each manager to its set on managedFields: the renderer
// owns status.overlay's four leaves when an overlay is recorded and nothing
// otherwise; the gateway owns status.metadata and status.artwork and no
// overlay leaf.
func (f *fixture) assertSplit(it artwork.Item) {
	f.t.Helper()
	renderer, err := catalogstatus.OwnedStatusPaths(it.Object.GetManagedFields(), catalogstatus.RendererManager)
	require.NoError(f.t, err)
	if it.Overlay != nil {
		want := sets.New[string]()
		for _, leaf := range catalogstatus.OverlayEntryLeaves {
			want.Insert("overlay." + leaf)
		}
		assert.Equal(f.t, want, renderer, "the renderer owns exactly status.overlay's leaves")
	} else {
		assert.Empty(f.t, renderer, "with no overlay the renderer owns nothing")
	}
	gateway, err := catalogstatus.OwnedStatusPaths(it.Object.GetManagedFields(), catalogstatus.GatewayManager)
	require.NoError(f.t, err)
	for p := range gateway {
		assert.False(f.t, strings.HasPrefix(p, "overlay"), "the gateway co-owns %s", p)
	}
	assert.Contains(f.t, gateway, "metadata.title")
}

func TestRenderStoresAndRecordsTheOverlay(t *testing.T) {
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindSeries} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, kind, "render-"+string(kind), catalogv1alpha1.RatingSourceMetacritic, catalogv1alpha1.RatingSourceTMDB)
			before := f.get()
			require.Nil(t, before.Overlay)

			require.NoError(t, f.handle())

			info, rc, err := f.store.Get(ctx, f.overlayKey())
			require.NoError(t, err, "the overlay object is stored")
			defer func() { _ = rc.Close() }()
			assert.Equal(t, "image/jpeg", info.Headers["Content-Type"])
			assert.Equal(t, "render", info.Headers["Clustarr-Source"])
			assert.Equal(t, f.wantDigest(), info.Headers["Clustarr-Rendered-From"])
			img, format, err := image.Decode(rc)
			require.NoError(t, err)
			assert.Equal(t, "jpeg", format)
			assert.Equal(t, image.Rect(0, 0, 200, 300), img.Bounds(), "the overlay is the poster's size")

			got := f.get()
			require.NotNil(t, got.Overlay)
			assert.Equal(t, "critics", got.Overlay.ProfileRef)
			assert.Equal(t, info.Digest, got.Overlay.Digest)
			assert.Equal(t, f.wantDigest(), got.Overlay.RenderedFrom)
			assert.False(t, got.Overlay.UpdatedAt.IsZero())
			assert.Equal(t, f.info.Digest, got.PosterDigest, "status.artwork is untouched")

			orig, err := f.store.Info(ctx, f.originalKey())
			require.NoError(t, err)
			assert.Equal(t, f.info.Digest, orig.Digest, "the original is never rewritten")
			assert.Empty(t, orig.Headers["Clustarr-Rendered-From"])
			f.assertSplit(got)
		})
	}
}

func TestUnchangedInputsSkipTheRender(t *testing.T) {
	f := newFixture(t, commonv1.MediaKindMovie, "render-skip", catalogv1alpha1.RatingSourceTMDB)
	require.NoError(t, f.handle())
	first := f.get()
	require.EqualValues(t, 1, f.store.puts.Load())

	require.NoError(t, f.handle())
	again := f.get()
	assert.EqualValues(t, 1, f.store.puts.Load(), "equal inputs: no Put")
	assert.Equal(t, first.Object.GetResourceVersion(), again.Object.GetResourceVersion(), "and no status write")

	// A new rating is a new input.
	f.gateway([]catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 802}}, true)
	require.NoError(t, f.handle())
	assert.EqualValues(t, 2, f.store.puts.Load())
	moved := f.get()
	require.NotNil(t, moved.Overlay)
	assert.Equal(t, f.wantDigest(), moved.Overlay.RenderedFrom)
	assert.NotEqual(t, first.Overlay.RenderedFrom, moved.Overlay.RenderedFrom)
	f.assertSplit(moved)

	// A vote count alone is not.
	f.gateway([]catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 802, Votes: 77}}, true)
	require.NoError(t, f.handle())
	assert.EqualValues(t, 2, f.store.puts.Load())
}

// Ruling (2): every task is judged against the current inputs, whatever it
// says -- so an overlay whose object is current but whose status.overlay
// was never written (a crash between the Put and the apply) is recorded
// without a second render.
func TestACurrentObjectIsRecordedWithoutARender(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindMovie, "render-repair", catalogv1alpha1.RatingSourceTMDB)
	put, err := f.store.ObjectStore.Put(ctx, f.overlayKey(), bytes.NewReader([]byte("rendered-before-a-crash")),
		map[string]string{"Content-Type": "image/jpeg", "Clustarr-Source": "render", "Clustarr-Rendered-From": f.wantDigest()})
	require.NoError(t, err)

	require.NoError(t, f.handle())
	assert.EqualValues(t, 0, f.store.puts.Load())
	got := f.get()
	require.NotNil(t, got.Overlay)
	assert.Equal(t, put.Digest, got.Overlay.Digest)
	assert.Equal(t, f.wantDigest(), got.Overlay.RenderedFrom)
	f.assertSplit(got)
}

// A stale object header re-renders even though status.overlay claims the
// current inputs: the object is what the ui serves.
func TestAStaleObjectIsReRenderedWhateverTheStatusSays(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindMovie, "render-stale-object", catalogv1alpha1.RatingSourceTMDB)
	require.NoError(t, f.handle())
	_, err := f.store.ObjectStore.Put(ctx, f.overlayKey(), bytes.NewReader([]byte("old")),
		map[string]string{"Content-Type": "image/jpeg", "Clustarr-Source": "render", "Clustarr-Rendered-From": "stale"})
	require.NoError(t, err)

	require.NoError(t, f.handle())
	info, err := f.store.Info(ctx, f.overlayKey())
	require.NoError(t, err)
	assert.Equal(t, f.wantDigest(), info.Headers["Clustarr-Rendered-From"])
	assert.Equal(t, info.Digest, f.get().Overlay.Digest)
}

func TestNoProfileRemovesTheOverlay(t *testing.T) {
	for name, drop := range map[string]func(f *fixture){
		"the label is removed": func(f *fixture) { f.setLabels(nil) },
		"the profile is deleted": func(f *fixture) {
			require.NoError(f.t, f.c.Delete(context.Background(), f.profile()))
		},
		"no badge has a rating": func(f *fixture) {
			f.gateway([]catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 830}}, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, commonv1.MediaKindMovie, "render-none-"+strings.ReplaceAll(name, " ", "-"), catalogv1alpha1.RatingSourceTMDB)
			require.NoError(t, f.handle())
			steady := f.get()
			require.NotNil(t, steady.Overlay, "the steady state has an overlay to remove")
			f.assertSplit(steady)

			drop(f)
			require.NoError(t, f.handle())

			_, err := f.store.Info(ctx, f.overlayKey())
			assert.ErrorIs(t, err, events.ErrObjectNotFound, "the overlay object is deleted")
			got := f.get()
			assert.Nil(t, got.Overlay, "status.overlay is cleared")
			assert.Equal(t, f.info.Digest, got.PosterDigest, "status.artwork stands")
			f.assertSplit(got)

			// Nothing to do the second time: no write.
			rv := got.Object.GetResourceVersion()
			require.NoError(t, f.handle())
			assert.Equal(t, rv, f.get().Object.GetResourceVersion())
		})
	}
}

// Carried ruling (1): a task for an item whose poster original is missing
// -- the gateway's "none" task after a custom poster's removal -- means no
// overlay.
func TestAMissingOriginalRemovesTheOverlay(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindSeries, "render-no-original", catalogv1alpha1.RatingSourceTMDB)
	require.NoError(t, f.handle())
	require.NotNil(t, f.get().Overlay)

	// The gateway drops the poster: the object and its entry.
	require.NoError(t, f.store.Delete(ctx, f.originalKey()))
	f.gateway(f.ratings, false)
	require.NoError(t, f.handle())

	_, err := f.store.Info(ctx, f.overlayKey())
	assert.ErrorIs(t, err, events.ErrObjectNotFound)
	got := f.get()
	assert.Nil(t, got.Overlay)
	f.assertSplit(got)
}

// A badge whose source has no rating is omitted, not drawn empty: the
// stored overlay is byte-for-byte the render of the one rated badge.
func TestABadgeWithoutARatingIsOmitted(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindMovie, "render-omit",
		catalogv1alpha1.RatingSourceIMDb, catalogv1alpha1.RatingSourceTMDB)
	require.NoError(t, f.handle())

	info, err := f.store.Info(ctx, f.overlayKey())
	require.NoError(t, err)

	base, _, err := image.Decode(bytes.NewReader(f.poster))
	require.NoError(t, err)
	tmdbLogo, _ := overlay.Logo(overlay.SourceTMDB)
	imdbLogo, _ := overlay.Logo(overlay.SourceIMDb)
	render := func(badges ...overlay.Badge) string {
		img, err := overlay.Render(base, badges, overlay.TemplateSpec(f.profile().Spec))
		require.NoError(t, err)
		var buf bytes.Buffer
		require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
		sum := sha256.Sum256(buf.Bytes())
		return hex.EncodeToString(sum[:])
	}
	one := render(overlay.Badge{Source: overlay.SourceTMDB, Score: "7.8", Logo: tmdbLogo})
	two := render(overlay.Badge{Source: overlay.SourceIMDb, Score: "", Logo: imdbLogo},
		overlay.Badge{Source: overlay.SourceTMDB, Score: "7.8", Logo: tmdbLogo})
	require.NotEqual(t, one, two, "the comparison can tell one badge from two")
	assert.Equal(t, one, info.Digest, "imdb has no rating, so only the tmdb badge is drawn, at JPEG quality 90")
}

// CLAUDE.md: an over-claim is silent while a co-owner keeps applying the
// same value, so each release is tested with the other manager's apply
// deliberately in between. The gateway applies first, then the renderer;
// the gateway then drops status.artwork from its apply -- if the renderer
// had co-owned any artwork leaf, the entry would stand. And the renderer
// clears status.overlay -- if the gateway had co-owned it, it would stand.
func TestTheRendererAndTheGatewayNeverCoOwn(t *testing.T) {
	f := newFixture(t, commonv1.MediaKindMovie, "render-split", catalogv1alpha1.RatingSourceTMDB)
	require.NoError(t, f.handle())
	steady := f.get()
	require.NotNil(t, steady.Overlay)
	require.NotEmpty(t, steady.PosterDigest)
	f.assertSplit(steady)

	// The gateway re-applies its complete set: the overlay is not its to
	// release, and stands.
	f.gateway(f.ratings, true)
	got := f.get()
	require.NotNil(t, got.Overlay, "the gateway's apply released the renderer's status.overlay")
	assert.Equal(t, steady.Overlay.Digest, got.Overlay.Digest)

	// The gateway drops status.artwork: with the renderer owning none of
	// it, the entry goes.
	f.gateway(f.ratings, false)
	got = f.get()
	assert.Empty(t, got.PosterDigest, "status.artwork stood after the gateway released it: the renderer co-owns it")
	require.NotNil(t, got.Overlay, "the renderer's status.overlay stands through the gateway's apply")
	assert.Equal(t, "Heat", got.Object.(*catalogv1alpha1.Movie).Status.Metadata.Title)

	// The gateway restores it, and the renderer releases status.overlay.
	f.gateway(f.ratings, true)
	f.setLabels(nil)
	require.NoError(t, f.handle())
	got = f.get()
	assert.Nil(t, got.Overlay, "status.overlay stood after the renderer released it: the gateway co-owns it")
	assert.Equal(t, f.info.Digest, got.PosterDigest, "the renderer's release left the gateway's status.artwork")
	assert.Equal(t, "Heat", got.Object.(*catalogv1alpha1.Movie).Status.Metadata.Title, "and its status.metadata")
	f.assertSplit(got)
}

// A lost update is not an SSA release (CLAUDE.md): the render is slow work,
// and a gateway apply landing in the middle of it must not be answered by a
// status.overlay recording the inputs from before it. A real second writer
// interleaves mid-render here: the gateway applies new ratings during the
// renderer's Put.
func TestInputsMovingMidRenderRetryInsteadOfRecordingAStaleOverlay(t *testing.T) {
	f := newFixture(t, commonv1.MediaKindMovie, "render-interleave", catalogv1alpha1.RatingSourceTMDB)
	stale := f.wantDigest()
	var once sync.Once
	f.store.onPut = func(string) {
		once.Do(func() {
			f.gateway([]catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 555}}, true)
		})
	}

	err := f.handle()
	require.Error(t, err)
	var retry *events.RetryError
	assert.True(t, errors.As(err, &retry), "a moved input retries the delivery: %v", err)
	assert.ErrorIs(t, err, artwork.ErrInputsMoved)
	got := f.get()
	assert.Nil(t, got.Overlay, "no status.overlay records the inputs from before the gateway's apply")

	require.NoError(t, f.handle(), "the redelivery renders the current inputs")
	got = f.get()
	require.NotNil(t, got.Overlay)
	assert.NotEqual(t, stale, got.Overlay.RenderedFrom)
	assert.Equal(t, f.wantDigest(), got.Overlay.RenderedFrom)
	f.assertSplit(got)
}

// The original is replaced between the plan's Info and the draw's Get: the
// render would draw the new poster under the old poster's inputs digest,
// so it is not drawn at all -- the delivery retries and the retry judges
// the new original.
func TestAnOriginalReplacedBeforeTheDrawIsNotRenderedUnderItsOldDigest(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindMovie, "render-swap", catalogv1alpha1.RatingSourceTMDB)
	replacement := posterPNG(t, 120, 180)
	var once sync.Once
	f.store.onGet = func(name string) {
		once.Do(func() {
			_, err := f.store.ObjectStore.Put(ctx, name, bytes.NewReader(replacement), map[string]string{"Content-Type": "image/png"})
			require.NoError(t, err)
		})
	}

	err := f.handle()
	require.ErrorIs(t, err, artwork.ErrInputsMoved)
	assert.EqualValues(t, 0, f.store.puts.Load(), "nothing was drawn from the replaced original")
	assert.Nil(t, f.get().Overlay)

	require.NoError(t, f.handle())
	info, err := f.store.Info(ctx, f.overlayKey())
	require.NoError(t, err)
	sum := sha256.Sum256(replacement)
	want := artwork.InputsDigest(hex.EncodeToString(sum[:]), artwork.ProfileHash(f.profile().Spec), f.ratings)
	assert.Equal(t, want, info.Headers["Clustarr-Rendered-From"])
	assert.Equal(t, want, f.get().Overlay.RenderedFrom)
}

func TestTasksThatCannotRender(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindMovie, "render-edge", catalogv1alpha1.RatingSourceTMDB)

	t.Run("an item that is gone is acknowledged", func(t *testing.T) {
		require.NoError(t, f.h.Handle(ctx, renderTask(t, commonv1.MediaKindMovie, f.key.Namespace, "no-such-movie")))
		assert.EqualValues(t, 0, f.store.puts.Load())
	})
	t.Run("a kind without an overlay is discarded", func(t *testing.T) {
		err := f.h.Handle(ctx, renderTask(t, commonv1.MediaKindAlbum, f.key.Namespace, "ok-computer"))
		var discard *events.DiscardError
		assert.True(t, errors.As(err, &discard), "%v", err)
	})
	t.Run("an undecodable task is discarded", func(t *testing.T) {
		err := f.h.Handle(ctx, &testMessage{env: &events.Envelope{Key: "a/b", Schema: "catalog.RenderOverlayTask.v1", Data: []byte("{")}})
		var discard *events.DiscardError
		assert.True(t, errors.As(err, &discard), "%v", err)
	})
}

// An original that no longer decodes is "no overlay", like a missing one:
// leaving the previous overlay would have the ui keep serving it ahead of
// the original it no longer describes.
func TestAnUndecodableOriginalClearsTheOverlay(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindMovie, "render-undecodable", catalogv1alpha1.RatingSourceTMDB)
	require.NoError(t, f.handle())
	require.NotNil(t, f.get().Overlay, "the steady state has an overlay to remove")

	_, err := f.store.ObjectStore.Put(ctx, f.originalKey(), strings.NewReader("<html>"), map[string]string{"Content-Type": "image/png"})
	require.NoError(t, err)
	require.NoError(t, f.handle(), "a clear is the task's outcome, not a failure to retry")

	_, err = f.store.Info(ctx, f.overlayKey())
	assert.ErrorIs(t, err, events.ErrObjectNotFound, "the overlay object is deleted")
	got := f.get()
	assert.Nil(t, got.Overlay, "status.overlay is cleared")
	assert.Equal(t, f.info.Digest, got.PosterDigest, "status.artwork is the gateway's and stands")
	f.assertSplit(got)

	rv := got.Object.GetResourceVersion()
	require.NoError(t, f.handle())
	assert.Equal(t, rv, f.get().Object.GetResourceVersion(), "nothing left to clear: no write")
}

// staleProfiles is a lagging informer: it answers OverlayProfile lists with
// a snapshot taken before the profile was edited.
type staleProfiles struct {
	client.Client
	profiles []catalogv1alpha1.OverlayProfile
}

func (s staleProfiles) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if l, ok := list.(*catalogv1alpha1.OverlayProfileList); ok {
		l.Items = nil
		for i := range s.profiles {
			l.Items = append(l.Items, *s.profiles[i].DeepCopy())
		}
		return nil
	}
	return s.Client.List(ctx, list, opts...)
}

// The render's decision -- and the recheck before its apply above all --
// reads profiles uncached: a lagging informer must not get an overlay
// recorded under a profile hash the profile no longer has.
func TestProfilesAreReadUncached(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, commonv1.MediaKindMovie, "render-uncached", catalogv1alpha1.RatingSourceTMDB)
	var before catalogv1alpha1.OverlayProfileList
	require.NoError(t, f.c.List(ctx, &before, client.InNamespace(f.key.Namespace)))
	f.h.Client = staleProfiles{Client: f.c, profiles: before.Items}

	p := f.profile()
	p.Spec.Corner = catalogv1alpha1.OverlayCornerTopLeft
	require.NoError(t, f.c.Update(ctx, p))
	fresh := f.wantDigest()
	require.NotEqual(t, artwork.InputsDigest(f.info.Digest, artwork.ProfileHash(before.Items[0].Spec), f.ratings), fresh,
		"the edit moves the profile hash, so a stale list is visible")

	require.NoError(t, f.handle())
	got := f.get()
	require.NotNil(t, got.Overlay)
	assert.Equal(t, fresh, got.Overlay.RenderedFrom, "recorded under the profile as it is, not as the informer last saw it")
	info, err := f.store.Info(ctx, f.overlayKey())
	require.NoError(t, err)
	assert.Equal(t, fresh, info.Headers["Clustarr-Rendered-From"])
}

// addMovie puts a second Movie beside the fixture's, in the same steady
// state: labelled, its original stored and recorded with a rating.
func (f *fixture) addMovie(name string) types.NamespacedName {
	f.t.Helper()
	ctx := context.Background()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.key.Namespace, Labels: critics},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 1, QualityProfileRef: "q", RootFolderRef: "r"},
	}
	require.NoError(f.t, f.c.Create(ctx, m))
	info, err := f.store.ObjectStore.Put(ctx, events.ArtworkKey(commonv1.MediaKindMovie, m.UID, "poster", events.ArtworkVariantOriginal),
		bytes.NewReader(f.poster), map[string]string{"Content-Type": "image/png"})
	require.NoError(f.t, err)
	_, err = k8s.PatchStatus(ctx, f.c, catalogstatus.GatewayManager, catalogac.Movie(name, f.key.Namespace).WithStatus(catalogac.MovieStatus().
		WithMetadata(catalogac.MovieMetadata().WithTitle(name).
			WithRatings(catalogac.Rating().WithSource(catalogv1alpha1.RatingSourceTMDB).WithValueCentis(700))).
		WithArtwork(catalogstatus.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{{
			Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider,
			SourceURL: "https://img.example/" + name, Digest: info.Digest, SizeBytes: info.Size, UpdatedAt: refreshed,
		}})...)))
	require.NoError(f.t, err)
	return client.ObjectKeyFromObject(m)
}

// Nothing but MaxConcurrentRenders bounds the render consumer's
// concurrency below its MaxAckPending, and a decode, render and encode
// holds tens of megabytes. With the draw made to block, no more draws run
// at once than the limit -- 1 when set, DefaultMaxConcurrentRenders when
// unset -- and every task still completes once they are let go.
func TestConcurrentDrawsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		tasks int
		want  int32
	}{
		{"one", 1, 2, 1},
		{"the default", 0, 3, artwork.DefaultMaxConcurrentRenders},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, commonv1.MediaKindMovie, "render-bounded-"+strings.ReplaceAll(tc.name, " ", "-"), catalogv1alpha1.RatingSourceTMDB)
			f.h.MaxConcurrentRenders = tc.limit
			keys := []types.NamespacedName{f.key}
			for i := 1; i < tc.tasks; i++ {
				keys = append(keys, f.addMovie(fmt.Sprintf("movie-%d", i)))
			}

			var active, peak, started atomic.Int32
			release := make(chan struct{})
			f.store.onGet = func(name string) { // the draw's first step, inside the bound
				if !strings.HasSuffix(name, "/original") {
					return
				}
				started.Add(1)
				n := active.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				<-release
				active.Add(-1)
			}

			errs := make(chan error, len(keys))
			for _, k := range keys {
				msg := renderTask(t, commonv1.MediaKindMovie, k.Namespace, k.Name) // require, not in the goroutine
				go func() { errs <- f.h.Handle(context.Background(), msg) }()
			}
			require.Eventually(t, func() bool { return started.Load() >= tc.want }, 10*time.Second, 5*time.Millisecond)
			time.Sleep(300 * time.Millisecond) // time for a draw past the bound to start, were it allowed to
			assert.Equal(t, tc.want, started.Load(), "a draw started past MaxConcurrentRenders")
			close(release)
			for range keys {
				require.NoError(t, <-errs)
			}
			assert.Equal(t, tc.want, peak.Load(), "peak concurrent draws")
			assert.EqualValues(t, tc.tasks, started.Load(), "every task drew once let go")
		})
	}
}
