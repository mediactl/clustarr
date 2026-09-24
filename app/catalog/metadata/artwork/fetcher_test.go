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
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// imageServer serves a fixed body and Content-Type per path and counts the
// requests each path received, so a test can say "no fetch happened" by
// number rather than by the absence of a side effect.
type imageServer struct {
	*httptest.Server

	mu     sync.Mutex
	routes map[string]route
	hits   map[string]int
}

type route struct {
	contentType string
	status      int
	body        []byte
}

func newImageServer(t *testing.T) *imageServer {
	t.Helper()
	s := &imageServer{routes: map[string]route{}, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rt, ok := s.routes[r.URL.Path]
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", rt.contentType)
		status := rt.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(rt.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *imageServer) serve(path, contentType string, body []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = route{contentType: contentType, body: body}
	return s.URL + path
}

func (s *imageServer) serveStatus(path string, status int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = route{contentType: "text/plain", status: status, body: []byte("nope")}
	return s.URL + path
}

func (s *imageServer) hitsFor(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func (s *imageServer) totalHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.hits {
		n += h
	}
	return n
}

func pngBytes(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var fetchNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// fixture is a Fetcher over a fresh membus object store, a fake clock and a
// fake recorder, plus the Movie every Sync in this file acts on.
type fixture struct {
	f      *artwork.Fetcher
	store  events.ObjectStore
	rec    *k8sevents.FakeRecorder
	srv    *imageServer
	movie  *catalogv1alpha1.Movie
	ctx    context.Context
	events []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	clock := clockwork.NewFakeClockAt(fetchNow)
	bus := membus.New(clock)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	store := bus.ObjectStore(events.BucketArtwork)
	srv := newImageServer(t)
	rec := k8sevents.NewFakeRecorder(32)
	return &fixture{
		f: &artwork.Fetcher{
			Store:    store,
			HTTP:     srv.Client(),
			Recorder: rec,
			Clock:    clock,
		},
		store: store,
		rec:   rec,
		srv:   srv,
		movie: &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{
			Name: "inception", Namespace: "films", UID: types.UID("8b2c0000-0000-4000-8000-000000000001"),
		}},
		ctx: ctx,
	}
}

func (fx *fixture) sync(overrides []catalogv1alpha1.ArtworkOverride, images []catalogv1alpha1.Image,
	current []catalogv1alpha1.ArtworkEntry,
) ([]catalogv1alpha1.ArtworkEntry, bool) {
	return fx.f.Sync(fx.ctx, fx.movie, commonv1.MediaKindMovie, overrides, images, current)
}

func (fx *fixture) key(t catalogv1alpha1.ImageType) string {
	return events.ArtworkKey(commonv1.MediaKindMovie, fx.movie.UID, string(t), events.ArtworkVariantOriginal)
}

// drainEvents returns every Event recorded so far.
func (fx *fixture) drainEvents() []string {
	for {
		select {
		case e := <-fx.rec.Events:
			fx.events = append(fx.events, e)
		default:
			return fx.events
		}
	}
}

func entryFor(entries []catalogv1alpha1.ArtworkEntry, t catalogv1alpha1.ImageType) (catalogv1alpha1.ArtworkEntry, bool) {
	for _, e := range entries {
		if e.Type == t {
			return e, true
		}
	}
	return catalogv1alpha1.ArtworkEntry{}, false
}

func TestResolveSourcesCustomBeatsProvider(t *testing.T) {
	got := artwork.ResolveSources(
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: "https://example.org/mine.jpg"}},
		[]catalogv1alpha1.Image{
			{Type: catalogv1alpha1.ImageTypePoster, URL: "https://image.tmdb.org/first.jpg"},
			{Type: catalogv1alpha1.ImageTypePoster, URL: "https://image.tmdb.org/second.jpg"},
			{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://image.tmdb.org/fanart-first.jpg"},
			{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://image.tmdb.org/fanart-second.jpg"},
		})
	assert.Equal(t, map[catalogv1alpha1.ImageType]artwork.Source{
		catalogv1alpha1.ImageTypePoster: {URL: "https://example.org/mine.jpg", Kind: catalogv1alpha1.ArtworkSourceCustom},
		catalogv1alpha1.ImageTypeFanart: {URL: "https://image.tmdb.org/fanart-first.jpg", Kind: catalogv1alpha1.ArtworkSourceProvider},
	}, got, "the override wins its type; every other type takes the provider's first image of that type")
}

func TestSyncFetchesTheCustomURLNotTheProviderURL(t *testing.T) {
	fx := newFixture(t)
	provider := fx.srv.serve("/provider.png", "image/png", pngBytes(t, 4, 6, color.White))
	body := jpegBytes(t, 8, 12)
	custom := fx.srv.serve("/custom.jpg", "image/jpeg", body)

	entries, posterChanged := fx.sync(
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: custom}},
		[]catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: provider}},
		nil)

	require.Len(t, entries, 1)
	assert.Equal(t, catalogv1alpha1.ArtworkEntry{
		Type:      catalogv1alpha1.ImageTypePoster,
		Source:    catalogv1alpha1.ArtworkSourceCustom,
		SourceURL: custom,
		Digest:    digestOf(body),
		SizeBytes: int64(len(body)),
		UpdatedAt: metav1.NewTime(fetchNow),
	}, entries[0])
	assert.True(t, posterChanged, "a first poster is a changed poster")
	assert.Equal(t, 1, fx.srv.hitsFor("/custom.jpg"))
	assert.Zero(t, fx.srv.hitsFor("/provider.png"), "the provider's poster is never fetched while an override names another")

	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, digestOf(body), info.Digest)
	assert.Empty(t, fx.drainEvents())
}

func TestSyncSkipsAnUnchangedSourceWhoseObjectIsPresent(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/poster.png", "image/png", pngBytes(t, 4, 6, color.White))
	images := []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}

	first, _ := fx.sync(nil, images, nil)
	require.Len(t, first, 1)
	require.Equal(t, 1, fx.srv.totalHits())

	second, posterChanged := fx.sync(nil, images, first)
	assert.Equal(t, first, second, "an unchanged source keeps its entry verbatim")
	assert.False(t, posterChanged)
	assert.Equal(t, 1, fx.srv.totalHits(), "an unchanged source URL with its object present is not fetched again")
}

func TestSyncRefetchesAnUnchangedSourceWhoseObjectIsMissing(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/poster.png", "image/png", pngBytes(t, 4, 6, color.White))
	images := []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}

	first, _ := fx.sync(nil, images, nil)
	require.Len(t, first, 1)
	require.NoError(t, fx.store.Delete(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster)))

	second, posterChanged := fx.sync(nil, images, first)
	require.Len(t, second, 1)
	assert.Equal(t, 2, fx.srv.hitsFor("/poster.png"), "a missing object is fetched even though its source URL is unchanged")
	assert.False(t, posterChanged, "the same bytes again are not a changed poster")
	_, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err, "the object is back")
}

// assertFailedFetchKeepsPrevious is the shared shape of every rejected
// fetch (R3, Review Focus 1): the previous entry and the previous object
// stand exactly as they were, and one ArtworkFetchFailed Event says why.
func assertFailedFetchKeepsPrevious(t *testing.T, fx *fixture, badURL, wantInNote string) {
	t.Helper()
	good := pngBytes(t, 4, 6, color.White)
	goodURL := fx.srv.serve("/good.png", "image/png", good)
	previous, _ := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: goodURL}}, nil)
	require.Len(t, previous, 1)
	fx.drainEvents()
	fx.events = nil

	entries, posterChanged := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: badURL}}, previous)

	assert.Equal(t, previous, entries, "a failed fetch leaves the previous entry exactly as it was")
	assert.False(t, posterChanged)
	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, digestOf(good), info.Digest, "a failed fetch leaves the previous object exactly as it was")

	evs := fx.drainEvents()
	require.Len(t, evs, 1, "exactly one Event per failed fetch")
	assert.True(t, strings.HasPrefix(evs[0], "Warning "+artwork.ReasonFetchFailed+" "), evs[0])
	assert.Contains(t, evs[0], wantInNote)
}

func TestSyncRejectsAnHTMLPage(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/login", "text/html; charset=utf-8", []byte("<html>sign in</html>"))
	assertFailedFetchKeepsPrevious(t, fx, bad, "not an image")
}

func TestSyncRejectsAnImageTypedBodyThatDoesNotDecode(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/liar.png", "image/png", []byte("<html>definitely a png</html>"))
	assertFailedFetchKeepsPrevious(t, fx, bad, "not an image")
}

func TestSyncRejectsABodyOverMaxImageBytes(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/huge.png", "image/png", bytes.Repeat([]byte{0}, artwork.MaxImageBytes+1))
	assertFailedFetchKeepsPrevious(t, fx, bad, pkgmetadata.ErrResponseTooLarge.Error())
}

func TestFetchOneReportsErrResponseTooLarge(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/huge.png", "image/png", bytes.Repeat([]byte{0}, artwork.MaxImageBytes+1))
	_, err := artwork.FetchOne(fx.f, fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster), catalogv1alpha1.ImageTypePoster,
		artwork.Source{URL: bad, Kind: catalogv1alpha1.ArtworkSourceProvider})
	require.ErrorIs(t, err, pkgmetadata.ErrResponseTooLarge)
}

func TestSyncRejectsAnImageWiderThan8000Pixels(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/wide.png", "image/png", pngBytes(t, 9000, 1, color.Black))
	assertFailedFetchKeepsPrevious(t, fx, bad, "too large")
}

func TestSyncRejectsAServerError(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serveStatus("/down.png", http.StatusInternalServerError)
	assertFailedFetchKeepsPrevious(t, fx, bad, "500")
}

func TestSyncStoresExactlyTheThreeHeaders(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/poster.png", "image/png; charset=binary", pngBytes(t, 4, 6, color.White))

	entries, _ := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}, nil)
	require.Len(t, entries, 1)

	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"Content-Type":        "image/png",
		"Clustarr-Source":     "provider",
		"Clustarr-Source-URL": url,
	}, info.Headers)
}

func TestSyncDecodesAWebP(t *testing.T) {
	fx := newFixture(t)
	body, err := os.ReadFile("../../../../test/data/artwork/poster-4x6.webp")
	require.NoError(t, err)
	url := fx.srv.serve("/poster.webp", "image/webp", body)

	entries, posterChanged := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}, nil)
	require.Len(t, entries, 1, "a webp served as image/webp decodes (golang.org/x/image/webp is registered)")
	assert.Equal(t, digestOf(body), entries[0].Digest)
	assert.True(t, posterChanged)
	assert.Empty(t, fx.drainEvents())
}

func TestSyncPosterChangedOnlyWhenThePosterDigestDiffers(t *testing.T) {
	fx := newFixture(t)
	white := pngBytes(t, 4, 6, color.White)
	black := pngBytes(t, 4, 6, color.Black)
	posterA := fx.srv.serve("/a.png", "image/png", white)
	posterSameBytes := fx.srv.serve("/a-mirror.png", "image/png", white)
	posterB := fx.srv.serve("/b.png", "image/png", black)
	fanart := fx.srv.serve("/fanart.png", "image/png", black)

	first, changed := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: posterA}}, nil)
	require.True(t, changed)

	// A new fanart alone is not a changed poster.
	second, changed := fx.sync(nil, []catalogv1alpha1.Image{
		{Type: catalogv1alpha1.ImageTypePoster, URL: posterA},
		{Type: catalogv1alpha1.ImageTypeFanart, URL: fanart},
	}, first)
	require.Len(t, second, 2)
	assert.False(t, changed, "only the poster's digest decides")

	// A new URL serving the same bytes is refetched but is not a changed poster.
	third, changed := fx.sync(nil, []catalogv1alpha1.Image{
		{Type: catalogv1alpha1.ImageTypePoster, URL: posterSameBytes},
		{Type: catalogv1alpha1.ImageTypeFanart, URL: fanart},
	}, second)
	p, ok := entryFor(third, catalogv1alpha1.ImageTypePoster)
	require.True(t, ok)
	assert.Equal(t, posterSameBytes, p.SourceURL)
	assert.False(t, changed, "same digest from a new URL is not a changed poster")

	// Different bytes are.
	_, changed = fx.sync(nil, []catalogv1alpha1.Image{
		{Type: catalogv1alpha1.ImageTypePoster, URL: posterB},
		{Type: catalogv1alpha1.ImageTypeFanart, URL: fanart},
	}, third)
	assert.True(t, changed)
}

func TestSyncNeverFallsBackToTheProviderWhenACustomURLFails(t *testing.T) {
	fx := newFixture(t)
	provider := fx.srv.serve("/provider.png", "image/png", pngBytes(t, 4, 6, color.White))
	custom := fx.srv.serveStatus("/custom.png", http.StatusNotFound)

	entries, changed := fx.sync(
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: custom}},
		[]catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: provider}},
		nil)

	assert.Empty(t, entries, "R3: the user asked for that image; a silent substitute is a guess")
	assert.False(t, changed)
	assert.Zero(t, fx.srv.hitsFor("/provider.png"))
	_, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.ErrorIs(t, err, events.ErrObjectNotFound)
	require.Len(t, fx.drainEvents(), 1)
}

func TestSyncDropsACustomEntryWhoseOverrideIsGoneAndNoProviderImageExists(t *testing.T) {
	fx := newFixture(t)
	custom := fx.srv.serve("/logo.png", "image/png", pngBytes(t, 4, 6, color.White))
	first, _ := fx.sync([]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypeLogo, URL: custom}}, nil, nil)
	require.Len(t, first, 1)

	second, _ := fx.sync(nil, nil, first)
	assert.Empty(t, second, "a custom entry whose override was removed, with no provider image to replace it, is dropped")
	_, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypeLogo))
	require.ErrorIs(t, err, events.ErrObjectNotFound, "and its original goes with it")
}

func TestSyncKeepsAProviderEntryTheProviderStoppedListing(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/banner.png", "image/png", pngBytes(t, 4, 6, color.White))
	first, _ := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypeBanner, URL: url}}, nil)
	require.Len(t, first, 1)

	second, _ := fx.sync(nil, nil, first)
	assert.Equal(t, first, second, "a provider that did not list a type this time is not a reason to drop art already stored")
}

func TestSyncReplacesTheProviderImageWhenACustomOverrideIsRemoved(t *testing.T) {
	fx := newFixture(t)
	provider := fx.srv.serve("/provider.png", "image/png", pngBytes(t, 4, 6, color.White))
	custom := fx.srv.serve("/custom.png", "image/png", pngBytes(t, 4, 6, color.Black))
	images := []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: provider}}

	first, _ := fx.sync([]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: custom}}, images, nil)
	require.Len(t, first, 1)
	require.Equal(t, catalogv1alpha1.ArtworkSourceCustom, first[0].Source)

	second, changed := fx.sync(nil, images, first)
	require.Len(t, second, 1)
	assert.Equal(t, catalogv1alpha1.ArtworkSourceProvider, second[0].Source)
	assert.Equal(t, provider, second[0].SourceURL)
	assert.True(t, changed)
}

func TestSyncUsesTheLimiterForTheImageHost(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/poster.png", "image/png", pngBytes(t, 4, 6, color.White))
	var hosts []string
	fx.f.Limiter = func(host string) *rate.Limiter {
		hosts = append(hosts, host)
		return rate.NewLimiter(rate.Inf, 1)
	}
	entries, _ := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}, nil)
	require.Len(t, entries, 1)
	assert.Equal(t, []string{strings.TrimPrefix(fx.srv.URL, "http://")}, hosts,
		"one limiter lookup per fetch, keyed by the image URL's host")
}

func TestMergeKeepsAnotherWritersEntriesAndAppliesOnlyThisSyncsChanges(t *testing.T) {
	at := metav1.NewTime(fetchNow)
	later := metav1.NewTime(fetchNow.Add(time.Minute))
	poster := catalogv1alpha1.ArtworkEntry{Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider, SourceURL: "https://p/1", Digest: "p1", SizeBytes: 1, UpdatedAt: at}
	fanart := catalogv1alpha1.ArtworkEntry{Type: catalogv1alpha1.ImageTypeFanart, Source: catalogv1alpha1.ArtworkSourceProvider, SourceURL: "https://f/1", Digest: "f1", SizeBytes: 1, UpdatedAt: at}
	logo := catalogv1alpha1.ArtworkEntry{Type: catalogv1alpha1.ImageTypeLogo, Source: catalogv1alpha1.ArtworkSourceCustom, SourceURL: "https://l/1", Digest: "l1", SizeBytes: 1, UpdatedAt: at}

	newPoster := poster
	newPoster.SourceURL, newPoster.Digest, newPoster.UpdatedAt = "https://p/2", "p2", later
	otherFanart := fanart
	otherFanart.SourceURL, otherFanart.Digest, otherFanart.UpdatedAt = "https://f/2", "f2", later
	banner := catalogv1alpha1.ArtworkEntry{Type: catalogv1alpha1.ImageTypeBanner, Source: catalogv1alpha1.ArtworkSourceCustom, SourceURL: "https://b/1", Digest: "b1", SizeBytes: 1, UpdatedAt: later}

	before := []catalogv1alpha1.ArtworkEntry{poster, fanart, logo}
	after := []catalogv1alpha1.ArtworkEntry{newPoster, fanart} // this Sync replaced the poster and dropped the logo
	fresh := []catalogv1alpha1.ArtworkEntry{poster, otherFanart, logo, banner}

	assert.Equal(t, []catalogv1alpha1.ArtworkEntry{newPoster, otherFanart, banner}, artwork.Merge(before, after, fresh),
		"this Sync's poster and its dropped logo win; the fanart and banner another writer recorded meanwhile stand")
}

func TestLockSerialisesOneItemAndHonoursTheContext(t *testing.T) {
	f := &artwork.Fetcher{}
	ctx := context.Background()
	unlock, err := f.Lock(ctx, "a")
	require.NoError(t, err)

	other, err := f.Lock(ctx, "b")
	require.NoError(t, err, "another item is not blocked")
	other()

	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err = f.Lock(short, "a")
	require.ErrorIs(t, err, context.DeadlineExceeded, "the same item waits for the holder")

	unlock()
	unlock() // idempotent
	again, err := f.Lock(ctx, "a")
	require.NoError(t, err)
	again()
}

func TestSyncStoresTheDecodedFormatNotTheServersLabel(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/mislabelled.jpg", "image/jpeg", pngBytes(t, 4, 6, color.White))

	entries, _ := fx.sync(nil, []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypePoster, URL: url}}, nil)
	require.Len(t, entries, 1)
	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, "image/png", info.Headers["Content-Type"], "a PNG labelled image/jpeg is stored as what it is")
}
