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
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
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

// fixture is a Fetcher over a fresh membus object store and a fake clock,
// plus the Movie every FetchOne in this file stores an original for. The
// fetcher writes objects only (ADR-0019 A5.2): which URL it fetches is the
// item key's plan (catalogartwork.PlanFetch, whose tests carry the
// selection rules), and a failure is the handler's to record.
type fixture struct {
	f     *artwork.Fetcher
	store events.ObjectStore
	srv   *imageServer
	ref   catalogartwork.ObjectRef
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	clock := clockwork.NewFakeClockAt(fetchNow)
	bus := membus.New(clock)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	store := bus.ObjectStore(events.BucketArtwork)
	srv := newImageServer(t)
	return &fixture{
		f:     &artwork.Fetcher{Store: store, HTTP: srv.Client(), Clock: clock},
		store: store,
		srv:   srv,
		ref: catalogartwork.ObjectRef{
			Kind: commonv1.MediaKindMovie, UID: types.UID("8b2c0000-0000-4000-8000-000000000001"),
			Namespace: "films", Name: "inception",
		},
		ctx: ctx,
	}
}

// fetch stores url as the item's original of type t, from a provider.
func (fx *fixture) fetch(t catalogv1alpha1.ImageType, url string) error {
	return fx.f.FetchOne(fx.ctx, fx.ref, t, catalogartwork.Source{URL: url, Kind: catalogv1alpha1.ArtworkSourceProvider})
}

func (fx *fixture) key(t catalogv1alpha1.ImageType) string {
	return events.ArtworkKey(fx.ref.Kind, fx.ref.UID, string(t), events.ArtworkVariantOriginal)
}

func TestFetchOneStoresTheNamedSourceWithItsMetadata(t *testing.T) {
	fx := newFixture(t)
	body := jpegBytes(t, 8, 12)
	custom := fx.srv.serve("/custom.jpg", "image/jpeg", body)

	require.NoError(t, fx.f.FetchOne(fx.ctx, fx.ref, catalogv1alpha1.ImageTypePoster,
		catalogartwork.Source{URL: custom, Kind: catalogv1alpha1.ArtworkSourceCustom}))

	assert.Equal(t, 1, fx.srv.hitsFor("/custom.jpg"))
	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, digestOf(body), info.Digest)
	assert.Equal(t, string(catalogv1alpha1.ArtworkSourceCustom), info.Headers[artwork.HeaderSource])
	assert.Equal(t, custom, info.Headers[artwork.HeaderSourceURL])
	at, ok := catalogartwork.ParseUpdatedAt(info.Metadata)
	require.True(t, ok, "the manager renders status.artwork[].updatedAt from the object's metadata")
	assert.Equal(t, fetchNow, at)
	assert.True(t, catalogartwork.MetaCurrent(info))
}

// assertFailedFetchKeepsPrevious is the shared shape of every rejected
// fetch (R3, Review Focus 1): FetchOne says why, the failure is the
// source's (not artwork.ErrStore, which the handler redelivers rather than
// records), and the previous object stands exactly as it was.
func assertFailedFetchKeepsPrevious(t *testing.T, fx *fixture, badURL, wantInErr string) {
	t.Helper()
	good := pngBytes(t, 4, 6, color.White)
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, fx.srv.serve("/good.png", "image/png", good)))

	err := fx.fetch(catalogv1alpha1.ImageTypePoster, badURL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), wantInErr)
	assert.False(t, errors.Is(err, artwork.ErrStore), "the source failed, not the store: %v", err)

	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, digestOf(good), info.Digest, "a failed fetch leaves the previous object exactly as it was")
}

func TestFetchOneRejectsAnHTMLPage(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/login", "text/html; charset=utf-8", []byte("<html>sign in</html>"))
	assertFailedFetchKeepsPrevious(t, fx, bad, "not an image")
}

func TestFetchOneRejectsAnImageTypedBodyThatDoesNotDecode(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/liar.png", "image/png", []byte("<html>definitely a png</html>"))
	assertFailedFetchKeepsPrevious(t, fx, bad, "not an image")
}

func TestFetchOneRejectsABodyOverMaxImageBytes(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/huge.png", "image/png", bytes.Repeat([]byte{0}, artwork.MaxImageBytes+1))
	assertFailedFetchKeepsPrevious(t, fx, bad, pkgmetadata.ErrResponseTooLarge.Error())
}

func TestFetchOneReportsErrResponseTooLarge(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/huge.png", "image/png", bytes.Repeat([]byte{0}, artwork.MaxImageBytes+1))
	err := fx.fetch(catalogv1alpha1.ImageTypePoster, bad)
	require.ErrorIs(t, err, pkgmetadata.ErrResponseTooLarge)
}

func TestFetchOneRejectsAnImageWiderThan8000Pixels(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/wide.png", "image/png", pngBytes(t, 9000, 1, color.Black))
	assertFailedFetchKeepsPrevious(t, fx, bad, "too large")
}

func TestFetchOneRejectsAServerError(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serveStatus("/down.png", http.StatusInternalServerError)
	assertFailedFetchKeepsPrevious(t, fx, bad, "500")
}

func TestFetchOneStoresExactlyTheThreeHeaders(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/poster.png", "image/png; charset=binary", pngBytes(t, 4, 6, color.White))

	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, url))

	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"Content-Type":        "image/png",
		"Clustarr-Source":     "provider",
		"Clustarr-Source-URL": url,
	}, info.Headers)
}

func TestFetchOneDecodesAWebP(t *testing.T) {
	fx := newFixture(t)
	body, err := os.ReadFile("../../../../test/data/artwork/poster-4x6.webp")
	require.NoError(t, err)
	url := fx.srv.serve("/poster.webp", "image/webp", body)

	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, url),
		"a webp served as image/webp decodes (golang.org/x/image/webp is registered)")
	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, digestOf(body), info.Digest)
}

// A custom URL that fails stores nothing, and the fetcher never substitutes
// the provider's image (R3: the user asked for that image; a silent
// substitute is a guess). It fetches exactly the URL it was named.
func TestFetchOneOfAFailingCustomURLStoresNothing(t *testing.T) {
	fx := newFixture(t)
	fx.srv.serve("/provider.png", "image/png", pngBytes(t, 4, 6, color.White))
	custom := fx.srv.serveStatus("/custom.png", http.StatusNotFound)

	err := fx.f.FetchOne(fx.ctx, fx.ref, catalogv1alpha1.ImageTypePoster,
		catalogartwork.Source{URL: custom, Kind: catalogv1alpha1.ArtworkSourceCustom})
	require.Error(t, err)
	assert.Zero(t, fx.srv.hitsFor("/provider.png"))
	_, err = fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.ErrorIs(t, err, events.ErrObjectNotFound)
}

// Drop deletes the original of a type whose override was removed with no
// provider image to replace it (the plan names it, PlanFetch's Drop), and
// an absent object is success.
func TestDropDeletesTheOriginal(t *testing.T) {
	fx := newFixture(t)
	custom := fx.srv.serve("/logo.png", "image/png", pngBytes(t, 4, 6, color.White))
	require.NoError(t, fx.f.FetchOne(fx.ctx, fx.ref, catalogv1alpha1.ImageTypeLogo,
		catalogartwork.Source{URL: custom, Kind: catalogv1alpha1.ArtworkSourceCustom}))

	require.NoError(t, fx.f.Drop(fx.ctx, fx.ref, catalogv1alpha1.ImageTypeLogo))
	_, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypeLogo))
	require.ErrorIs(t, err, events.ErrObjectNotFound, "the original goes with its override")
	require.NoError(t, fx.f.Drop(fx.ctx, fx.ref, catalogv1alpha1.ImageTypeLogo), "dropping an absent original is success")
}

func TestFetchOneUsesTheLimiterForTheImageHost(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/poster.png", "image/png", pngBytes(t, 4, 6, color.White))
	var hosts []string
	fx.f.Limiter = func(host string) *rate.Limiter {
		hosts = append(hosts, host)
		return rate.NewLimiter(rate.Inf, 1)
	}
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, url))
	assert.Equal(t, []string{strings.TrimPrefix(fx.srv.URL, "http://")}, hosts,
		"one limiter lookup per fetch, keyed by the image URL's host")
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

func TestFetchOneStoresTheDecodedFormatNotTheServersLabel(t *testing.T) {
	fx := newFixture(t)
	url := fx.srv.serve("/mislabelled.jpg", "image/jpeg", pngBytes(t, 4, 6, color.White))

	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, url))
	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypePoster))
	require.NoError(t, err)
	assert.Equal(t, "image/png", info.Headers["Content-Type"], "a PNG labelled image/jpeg is stored as what it is")
}

// TMDB serves some logos as SVG (kind-cluster-plex, 2026-10-05: Heat, Her,
// La La Land...), which the store refuses, and every one of them failed
// "not an image: content type image/svg+xml". TMDB also renders each as a
// PNG at the same path with a .png extension (2000 px wide), so an SVG from
// a host with a PNG rendition is converted by fetching that, and the object
// keeps the provider's SVG URL as its source -- so the item key's plan
// (PlanFetch: same URL, same kind) finds it unchanged and asks for no
// refetch.
func TestFetchOneStoresThePNGRenditionOfAnSVGLogo(t *testing.T) {
	fx := newFixture(t)
	u, err := url.Parse(fx.srv.URL)
	require.NoError(t, err)
	fx.f.SVGRenditionHosts = []string{u.Host}
	svg := fx.srv.serve("/t/p/original/logo.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	png := pngBytes(t, 20, 4, color.Black)
	fx.srv.serve("/t/p/original/logo.png", "image/png", png)

	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypeLogo, svg))

	assert.Equal(t, 1, fx.srv.hitsFor("/t/p/original/logo.png"))
	info, err := fx.store.Info(fx.ctx, fx.key(catalogv1alpha1.ImageTypeLogo))
	require.NoError(t, err)
	assert.Equal(t, digestOf(png), info.Digest)
	assert.Equal(t, "image/png", info.Headers[artwork.HeaderContentType])
	assert.Equal(t, svg, info.Headers[artwork.HeaderSourceURL],
		"the object names the provider's image, so the plan finds it unchanged")
}

// An SVG from a host with no PNG rendition is still refused, saying why.
func TestFetchOneRejectsAnSVGFromAHostWithNoPNGRendition(t *testing.T) {
	fx := newFixture(t)
	bad := fx.srv.serve("/logo.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	assertFailedFetchKeepsPrevious(t, fx, bad, "SVG")
}
