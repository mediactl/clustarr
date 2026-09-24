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

package ui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/ui"
)

// newArtStore builds an in-memory artwork bucket exactly like the metadata
// gateway's own fixture (app/catalog/metadata/artwork/fetcher_test.go's
// newFixture): a membus, Ensure'd with the default topology so
// events.BucketArtwork exists, then bound.
func newArtStore(t *testing.T) events.ObjectStore {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default()))
	return bus.ObjectStore(events.BucketArtwork)
}

// putArt writes body to store under key with contentType and returns the
// resulting ObjectInfo, so a test can assert the digest it later expects
// handleArt to serve and set as the response's ETag.
func putArt(t *testing.T, store events.ObjectStore, key, contentType, body string) events.ObjectInfo {
	t.Helper()
	info, err := store.Put(t.Context(), key, strings.NewReader(body),
		map[string]string{events.HeaderContentType: contentType})
	require.NoError(t, err)
	return info
}

func artPath(uid types.UID) string {
	return "/art/movie/" + string(uid) + "/poster"
}

// TestHandleArtPrefersOverlayOverOriginal: with both variants stored,
// handleArt serves the rendered overlay, not the metadata gateway's
// original -- the renderer's badge is meant to be what a viewer actually
// sees.
func TestHandleArtPrefersOverlayOverOriginal(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("11111111-1111-1111-1111-111111111111")
	putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")
	overlay := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOverlay),
		"image/png", "overlay-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath(uid), nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "overlay-bytes", rec.Body.String())
	require.Equal(t, `"`+overlay.Digest+`"`, rec.Header().Get("ETag"))
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
}

// TestHandleArtFallsBackToOriginal: with only the original stored (no
// overlay rendered yet), handleArt serves it.
func TestHandleArtFallsBackToOriginal(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("22222222-2222-2222-2222-222222222222")
	original := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath(uid), nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-bytes", rec.Body.String())
	require.Equal(t, `"`+original.Digest+`"`, rec.Header().Get("ETag"))
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

// TestHandleArt404WhenBothVariantsMissing: neither object written -> 404,
// not a broken image and not a 500.
func TestHandleArt404WhenBothVariantsMissing(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("33333333-3333-3333-3333-333333333333")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath(uid), nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtNilOptionsArtworkIs404: a ui process with no NATS endpoint
// reachable (Options.Artwork left nil, exactly like a nil Reader) answers
// 404 for every /art request rather than panicking.
func TestHandleArtNilOptionsArtworkIs404(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath("abc"), nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtUnknownKindIs404NotServerError: a {kind} the MediaKind enum
// does not carry must not reach events.ArtworkKey, which panics on an
// unrecognised part -- 404, never a 500.
func TestHandleArtUnknownKindIs404NotServerError(t *testing.T) {
	store := newArtStore(t)
	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/spaceship/abc/poster", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtUnknownTypeIs404: a {type} the ImageType enum does not carry
// is 404 too, for the same reason.
func TestHandleArtUnknownTypeIs404(t *testing.T) {
	store := newArtStore(t)
	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/movie/abc/backdrop", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtIfNoneMatchIs304WithNoBody: a conditional request carrying
// the object's own ETag gets 304 and no body, sparing the transfer entirely.
func TestHandleArtIfNoneMatchIs304WithNoBody(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("44444444-4444-4444-4444-444444444444")
	info := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	req := httptest.NewRequest(http.MethodGet, artPath(uid), nil)
	req.Header.Set("If-None-Match", `"`+info.Digest+`"`)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Body.String())
}

// TestHandleArtCacheControlIsImmutableOnlyWhenVMatchesServedDigest: the
// route's whole point is a URL that is safe to cache forever once its ?v
// names the digest actually served, and never otherwise -- no ?v, and a
// stale ?v naming a digest this object no longer carries, both revalidate.
func TestHandleArtCacheControlIsImmutableOnlyWhenVMatchesServedDigest(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("55555555-5555-5555-5555-555555555555")
	info := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})

	matching := httptest.NewRecorder()
	srv.Handler().ServeHTTP(matching, httptest.NewRequest(http.MethodGet, artPath(uid)+"?v="+info.Digest, nil))
	require.Equal(t, "public, max-age=31536000, immutable", matching.Header().Get("Cache-Control"))

	noV := httptest.NewRecorder()
	srv.Handler().ServeHTTP(noV, httptest.NewRequest(http.MethodGet, artPath(uid), nil))
	require.Equal(t, "no-cache", noV.Header().Get("Cache-Control"))

	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodGet, artPath(uid)+"?v=stale-digest", nil))
	require.Equal(t, "no-cache", stale.Header().Get("Cache-Control"))
}
