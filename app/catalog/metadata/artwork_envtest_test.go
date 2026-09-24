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

package metadata_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

// imageHost serves images by path, whatever host the URL names: the
// Fetcher's transport routes every request to it, so the provider document
// can carry real-looking image.tmdb.org URLs.
type imageHost struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies map[string][]byte // path -> PNG; absent path -> 500
}

func newImageHost(t *testing.T) *imageHost {
	t.Helper()
	h := &imageHost{bodies: map[string][]byte{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		body, ok := h.bodies[r.URL.Path]
		h.mu.Unlock()
		if !ok {
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *imageHost) set(path string, body []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if body == nil {
		delete(h.bodies, path)
		return
	}
	h.bodies[path] = body
}

func (h *imageHost) client() *http.Client {
	target, _ := url.Parse(h.srv.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func solidPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	for x := range 4 {
		for y := range 6 {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// docMovieProvider answers every lookup with a copy of doc.
type docMovieProvider struct {
	mu  sync.Mutex
	doc pkgmetadata.Movie
}

func (p *docMovieProvider) set(doc pkgmetadata.Movie) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.doc = doc
}

func (p *docMovieProvider) Name() string                           { return "doc" }
func (p *docMovieProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }

func (p *docMovieProvider) Movie(context.Context, string, string) (*pkgmetadata.Movie, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.doc
	return &d, nil
}

func (p *docMovieProvider) FindMovie(ctx context.Context, _ pkgmetadata.ExternalIDs) (*pkgmetadata.Movie, error) {
	return p.Movie(ctx, "", "")
}

func (p *docMovieProvider) SearchMovies(context.Context, string, int) ([]pkgmetadata.MovieHit, error) {
	return nil, nil
}

func inceptionDoc(posterURL, fanartURL string) pkgmetadata.Movie {
	return pkgmetadata.Movie{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205"}, Title: "Inception", Runtime: 148, Year: 2010,
		Images: []pkgmetadata.Image{
			{Type: pkgmetadata.ImageTypePoster, URL: posterURL},
			{Type: pkgmetadata.ImageTypeFanart, URL: fanartURL},
		},
	}
}

func movieTask(t *testing.T, ns, name string) testMessage {
	t.Helper()
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}})
	require.NoError(t, err)
	return testMessage{env: env}
}

func drain(rec *k8sevents.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// assertArtworkSplit holds every status manager on m to its set:
// status.artwork is the gateway's alone, leaf by leaf, for every entry; the
// gateway owns nothing under status.overlay; and the gateway's top-level
// fields are exactly catalogstatus.GatewayFields. It reads managedFields,
// because an over-claim never shows in the object's values (CLAUDE.md).
func assertArtworkSplit(t *testing.T, m *catalogv1alpha1.Movie) {
	t.Helper()
	gateway, err := catalogstatus.OwnedStatusPaths(m.ManagedFields, catalogstatus.GatewayManager)
	require.NoError(t, err)
	assert.ElementsMatch(t, catalogstatus.GatewayFields, catalogstatus.TopLevel(gateway).UnsortedList(),
		"the gateway owns exactly status.metadata and status.artwork")
	for _, e := range m.Status.Artwork {
		for _, leaf := range catalogstatus.ArtworkEntryLeaves {
			assert.Contains(t, gateway, "artwork[type="+string(e.Type)+"]."+leaf,
				"every leaf of every entry is the gateway's, not just the parent")
		}
	}
	for p := range gateway {
		assert.False(t, strings.HasPrefix(p, "overlay"), "the gateway owns %s under status.overlay", p)
	}

	seen := map[string]bool{}
	for _, mf := range m.ManagedFields {
		if mf.Subresource != "status" || mf.Manager == string(catalogstatus.GatewayManager) || seen[mf.Manager] {
			continue
		}
		seen[mf.Manager] = true
		paths, err := catalogstatus.OwnedStatusPaths(m.ManagedFields, k8s.FieldManager(mf.Manager))
		require.NoError(t, err)
		for p := range paths {
			assert.False(t, strings.HasPrefix(p, "artwork"), "%s co-owns %s", mf.Manager, p)
		}
	}
}

// TestMetadataRefreshNeverReleasesStatusArtwork is the mandatory release
// regression (plan task B2): a Movie that already has status.metadata and
// status.artwork -- written by the real gateway, beside a renderer's
// status.overlay -- goes through refreshes whose fetches fail, and keeps
// every entry.
func TestMetadataRefreshNeverReleasesStatusArtwork(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "artwork-release", "inception"
	newMovie(t, ctx, c, ns, name, 27205)
	key := types.NamespacedName{Namespace: ns, Name: name}

	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	store := bus.ObjectStore(events.BucketArtwork)
	host := newImageHost(t)
	host.set("/t/p/w500/poster-v1.png", solidPNG(t, color.White))
	host.set("/t/p/original/fanart-v1.png", solidPNG(t, color.Black))
	rec := k8sevents.NewFakeRecorder(16)
	fetcher := &artwork.Fetcher{Store: store, HTTP: host.client(), Recorder: rec}

	provider := &docMovieProvider{}
	provider.set(inceptionDoc("https://image.tmdb.org/t/p/w500/poster-v1.png", "https://image.tmdb.org/t/p/original/fanart-v1.png"))
	h := &metadata.Handler{
		Client: c, Reader: c, Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{provider}},
		Cache: noopCache{}, Artwork: fetcher, Bus: bus,
	}

	// Steady state, reached through the real writers.
	require.NoError(t, h.Handle(ctx, movieTask(t, ns, name)))
	overlay := catalogac.OverlayEntry().WithProfileRef("imdb-badges").WithDigest("ov1").WithRenderedFrom("in1").
		WithUpdatedAt(metav1.NewTime(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrArtwork,
		catalogac.Movie(name, ns).WithStatus(catalogac.MovieStatus().WithOverlay(overlay)))
	require.NoError(t, err, "the renderer's status.overlay, standing in for task C3")

	var steady catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, key, &steady))
	require.NotNil(t, steady.Status.Metadata)
	require.Equal(t, "Inception", steady.Status.Metadata.Title)
	require.Len(t, steady.Status.Artwork, 2, "the poster and the fanart were stored and recorded")
	require.NotNil(t, steady.Status.Overlay)
	assertArtworkSplit(t, &steady)
	require.Empty(t, drain(rec))

	t.Run("a metadata provider 500 writes nothing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
		require.NoError(t, err)
		failing := &metadata.Handler{
			Client: c, Reader: c, Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}},
			Cache: noopCache{}, Artwork: fetcher, Bus: bus,
		}
		require.Error(t, failing.Handle(ctx, movieTask(t, ns, name)), "a provider 500 is a retry")

		var got catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, key, &got))
		assert.Equal(t, steady.Status.Artwork, got.Status.Artwork)
		assert.Equal(t, steady.Status.Metadata, got.Status.Metadata)
		assert.Equal(t, steady.Status.Overlay, got.Status.Overlay)
		assertArtworkSplit(t, &got)
	})

	t.Run("a refresh whose image fetches fail keeps every entry", func(t *testing.T) {
		// The provider now publishes a new poster URL, which 500s; the fanart
		// URL is unchanged but its object is gone, and it 500s too -- so both
		// types are fetched, and both fetches fail.
		provider.set(inceptionDoc("https://image.tmdb.org/t/p/w500/poster-v2.png", "https://image.tmdb.org/t/p/original/fanart-v1.png"))
		host.set("/t/p/original/fanart-v1.png", nil)
		require.NoError(t, store.Delete(ctx, events.ArtworkKey(commonv1.MediaKindMovie, steady.UID, "fanart", events.ArtworkVariantOriginal)))

		require.NoError(t, h.Handle(ctx, movieTask(t, ns, name)), "the metadata fetch succeeded; the task is done")

		var got catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, key, &got))
		assert.Equal(t, steady.Status.Artwork, got.Status.Artwork,
			"R3: failed fetches leave every previous entry in place, and the apply that carried the new metadata did not release them")
		require.NotNil(t, got.Status.Metadata)
		assert.Equal(t, "Inception", got.Status.Metadata.Title, "status.metadata is intact")
		require.Len(t, got.Status.Metadata.Images, 2)
		assert.Equal(t, "https://image.tmdb.org/t/p/w500/poster-v2.png", got.Status.Metadata.Images[0].URL,
			"and it is the new document's: the metadata apply landed")
		assert.Equal(t, steady.Status.Overlay, got.Status.Overlay, "the renderer's overlay is untouched")
		assertArtworkSplit(t, &got)

		evs := drain(rec)
		require.Len(t, evs, 2, "one ArtworkFetchFailed per failed type")
		for _, e := range evs {
			assert.Contains(t, e, artwork.ReasonFetchFailed)
			assert.Contains(t, e, "HTTP 500")
		}
	})
}

// TestMetadataRefreshPublishesOneRenderPerChangedPoster: every refresh
// publishes the RenderOverlay task for its poster, keyed by the digest, so a
// changed digest is one new task and an unchanged one is absorbed as a
// duplicate.
func TestMetadataRefreshPublishesOneRenderPerChangedPoster(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "artwork-render", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	var mu sync.Mutex
	var renders []*events.Envelope
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream: events.StreamWorkCatalogarr, Durable: "test-render-collector",
		Filters: []string{events.FilterCatalogArtworkRender},
	}, func(_ context.Context, m events.Message) error {
		mu.Lock()
		defer mu.Unlock()
		renders = append(renders, m.Envelope())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(renders)
	}

	host := newImageHost(t)
	white, black := solidPNG(t, color.White), solidPNG(t, color.Black)
	host.set("/p1.png", white)
	host.set("/p2.png", black)
	host.set("/f.png", black)
	provider := &docMovieProvider{}
	provider.set(inceptionDoc("https://img.example/p1.png", "https://img.example/f.png"))
	h := &metadata.Handler{
		Client: c, Reader: c, Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{provider}},
		Cache: noopCache{}, Bus: bus,
		Artwork: &artwork.Fetcher{Store: bus.ObjectStore(events.BucketArtwork), HTTP: host.client()},
	}

	require.NoError(t, h.Handle(ctx, movieTask(t, ns, name)))
	require.Eventually(t, func() bool { return count() == 1 }, 2*time.Second, 5*time.Millisecond)

	require.NoError(t, h.Handle(ctx, movieTask(t, ns, name)))
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, count(),
		"an unchanged poster republishes under the same Msg-Id, which the duplicate window absorbs")

	provider.set(inceptionDoc("https://img.example/p2.png", "https://img.example/f.png"))
	require.NoError(t, h.Handle(ctx, movieTask(t, ns, name)))
	require.Eventually(t, func() bool { return count() == 2 }, 2*time.Second, 5*time.Millisecond)

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	mu.Lock()
	last := renders[1]
	mu.Unlock()
	var poster catalogv1alpha1.ArtworkEntry
	for _, e := range got.Status.Artwork {
		if e.Type == catalogv1alpha1.ImageTypePoster {
			poster = e
		}
	}
	assert.Equal(t, schema.MsgIDForRenderOverlay(got.UID, poster.Digest), last.ID)
	var task schema.RenderOverlayTask
	require.NoError(t, schema.Decode(last.Schema, last.Data, &task))
	assert.Equal(t, schema.RenderOverlayTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}, Reason: "original",
	}, task)
}
