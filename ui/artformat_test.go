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
	"bytes"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/ui"
)

// putFixture stores the artwork fixture file under the item's original of
// type t, as content type contentType.
func putFixture(t *testing.T, store events.ObjectStore, uid types.UID, it catalogv1.ImageType, file, contentType string) events.ObjectInfo {
	t.Helper()
	body, err := os.ReadFile("../test/data/artwork/" + file)
	require.NoError(t, err)
	return putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(it), events.ArtworkVariantOriginal),
		contentType, string(body))
}

func fetchArt(srv *ui.Server, target, ifNoneMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// A WebP asked for with nowebp is served as a JPEG under an ETag of its
// own, since PMS keeps the bytes it downloads and its UltraBlur processor
// decodes no WebP (talos, 2026-11-03: 1,151 movies and shows without
// UltraBlur colours). Without nowebp the object is served as it is.
func TestArtServesAWebPAsJPEGForNoWebP(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("a0a0a0a0-0000-0000-0000-000000000001")
	info := putFixture(t, store, uid, catalogv1.ImageTypeFanart, "fanart-8x4.webp", "image/webp")
	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	base := "/art/movie/" + string(uid) + "/fanart?v=" + info.Digest

	rec := fetchArt(srv, base+"&nowebp=1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	img, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err, "the body is a JPEG")
	assert.Equal(t, 8, img.Bounds().Dx())
	assert.Equal(t, `"`+info.Digest+`-nowebp"`, rec.Header().Get("ETag"), "not the object's bytes, so not its ETag")
	assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))

	again := fetchArt(srv, base+"&nowebp=1", "")
	assert.Equal(t, rec.Body.Bytes(), again.Body.Bytes(), "the same bytes every time")

	notModified := fetchArt(srv, base+"&nowebp=1", `"`+info.Digest+`-nowebp"`)
	assert.Equal(t, http.StatusNotModified, notModified.Code)
	assert.Equal(t, http.StatusOK, fetchArt(srv, base+"&nowebp=1", `"`+info.Digest+`"`).Code,
		"the WebP's ETag does not match the JPEG")

	plain := fetchArt(srv, base, "")
	assert.Equal(t, "image/webp", plain.Header().Get("Content-Type"))
	assert.Equal(t, `"`+info.Digest+`"`, plain.Header().Get("ETag"))
	fixture, err := os.ReadFile("../test/data/artwork/fanart-8x4.webp")
	require.NoError(t, err)
	assert.Equal(t, fixture, plain.Body.Bytes(), "a browser still gets the WebP")
}

// A WebP with transparency (a logo) is served as a PNG, keeping its alpha.
func TestArtServesATransparentWebPAsPNGForNoWebP(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("a0a0a0a0-0000-0000-0000-000000000002")
	info := putFixture(t, store, uid, catalogv1.ImageTypeLogo, "logo-alpha-4x2.webp", "image/webp")
	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})

	rec := fetchArt(srv, "/art/movie/"+string(uid)+"/logo?v="+info.Digest+"&nowebp=1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err, "the body is a PNG")
	_, _, _, a := img.At(0, 0).RGBA()
	assert.Zero(t, a, "a transparent pixel stays transparent")
	_, _, _, a = img.At(1, 0).RGBA()
	assert.EqualValues(t, 0xffff, a)
}

// Anything but a WebP is served as it is under nowebp, ETag and all.
func TestArtServesAJPEGUnchangedForNoWebP(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("a0a0a0a0-0000-0000-0000-000000000003")
	info := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypeFanart), events.ArtworkVariantOriginal),
		"image/jpeg", "jpeg-bytes")
	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})

	rec := fetchArt(srv, "/art/movie/"+string(uid)+"/fanart?v="+info.Digest+"&nowebp=1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "jpeg-bytes", rec.Body.String())
	assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	assert.Equal(t, `"`+info.Digest+`"`, rec.Header().Get("ETag"))
}
