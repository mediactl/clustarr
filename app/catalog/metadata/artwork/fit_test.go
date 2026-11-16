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
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/pkg/events"
)

// noisy is a w x h picture with detail in it, so its encodings have a size
// worth comparing; alpha below 255 makes its top-left quarter translucent.
func noisy(w, h int, alpha uint8) *image.NRGBA {
	rng := rand.New(rand.NewSource(1))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			a := uint8(255)
			if x < w/2 && y < h/2 {
				a = alpha
			}
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: uint8(rng.Intn(64)), A: a})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}))
	return buf.Bytes()
}

// storedImage reads the item's original of type typ back: its bytes, its
// decoded size and format, and its object info.
func (fx *fixture) storedImage(t *testing.T, typ catalogv1alpha1.ImageType) ([]byte, image.Config, string, events.ObjectInfo) {
	t.Helper()
	info, rc, err := fx.store.Get(fx.ctx, fx.key(typ))
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	require.NoError(t, err)
	return body, cfg, format, info
}

func TestFetchOneFitsEachSizedTypeIntoItsBoxAsJPEG(t *testing.T) {
	for _, tc := range []struct {
		typ          catalogv1alpha1.ImageType
		w, h         int
		wantW, wantH int
	}{
		{catalogv1alpha1.ImageTypePoster, 1000, 1500, 500, 750},
		{catalogv1alpha1.ImageTypePoster, 1200, 1200, 500, 500},
		{catalogv1alpha1.ImageTypeFanart, 1920, 1080, 1280, 720},
		{catalogv1alpha1.ImageTypeBanner, 1000, 185, 757, 140},
		{catalogv1alpha1.ImageTypeThumb, 1920, 1080, 1280, 720},
		{catalogv1alpha1.ImageTypeScreenshot, 1920, 1080, 1280, 720},
	} {
		fx := newFixture(t)
		src := encodePNG(t, noisy(tc.w, tc.h, 255))
		require.NoError(t, fx.fetch(tc.typ, fx.srv.serve("/big.png", "image/png", src)))

		body, cfg, format, info := fx.storedImage(t, tc.typ)
		assert.Equal(t, "jpeg", format, tc.typ)
		assert.Equal(t, [2]int{tc.wantW, tc.wantH}, [2]int{cfg.Width, cfg.Height}, tc.typ)
		assert.Equal(t, "image/jpeg", info.Headers[events.HeaderContentType], tc.typ)
		assert.Equal(t, catalogartwork.FitVersion, info.Metadata[events.ArtworkMetaKeyFit], tc.typ)
		assert.Less(t, len(body), len(src), tc.typ)
	}
}

func TestFetchOneRecordsTheStoredSizeNotTheSources(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster,
		fx.srv.serve("/big.png", "image/png", encodePNG(t, noisy(1000, 1500, 255)))))
	_, _, _, info := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	assert.Equal(t, "500", info.Metadata[events.ArtworkMetaKeyWidth])
	assert.Equal(t, "750", info.Metadata[events.ArtworkMetaKeyHeight])
}

func TestFetchOneNeverScalesAnImageUp(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster,
		fx.srv.serve("/small.png", "image/png", encodePNG(t, noisy(300, 450, 255)))))
	_, cfg, format, info := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	assert.Equal(t, [2]int{300, 450}, [2]int{cfg.Width, cfg.Height})
	assert.Equal(t, "jpeg", format, "an opaque PNG that fits is still stored as the smaller JPEG")
	assert.Equal(t, catalogartwork.FitVersion, info.Metadata[events.ArtworkMetaKeyFit])
}

func TestFetchOneCompressesAJPEGThatFitsOnlyWhenItSavesATenth(t *testing.T) {
	fx := newFixture(t)
	fat := encodeJPEG(t, noisy(400, 600, 255), 100)
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, fx.srv.serve("/fat.jpg", "image/jpeg", fat)))
	body, _, _, _ := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	assert.Less(t, len(body), len(fat)*9/10)

	lean := encodeJPEG(t, noisy(400, 600, 255), 80)
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypeFanart, fx.srv.serve("/lean.jpg", "image/jpeg", lean)))
	body, _, _, info := fx.storedImage(t, catalogv1alpha1.ImageTypeFanart)
	assert.Equal(t, lean, body, "a lean JPEG is not encoded a second time")
	assert.Equal(t, catalogartwork.FitVersion, info.Metadata[events.ArtworkMetaKeyFit], "and is not fetched again for it")
}

func TestFetchOneKeepsATransparentImagesAlpha(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster,
		fx.srv.serve("/clear.png", "image/png", encodePNG(t, noisy(1000, 1500, 0)))))
	body, cfg, format, info := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	assert.Equal(t, "png", format)
	assert.Equal(t, [2]int{500, 750}, [2]int{cfg.Width, cfg.Height})
	assert.Equal(t, "image/png", info.Headers[events.HeaderContentType])
	img, err := png.Decode(bytes.NewReader(body))
	require.NoError(t, err)
	_, _, _, a := img.At(10, 10).RGBA()
	assert.Zero(t, a)
}

func TestFetchOneStoresAnUnsizedTypeAsFetched(t *testing.T) {
	fx := newFixture(t)
	src := encodePNG(t, noisy(2000, 800, 255))
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypeLogo, fx.srv.serve("/logo.png", "image/png", src)))
	body, _, _, info := fx.storedImage(t, catalogv1alpha1.ImageTypeLogo)
	assert.Equal(t, src, body)
	assert.NotContains(t, info.Metadata, events.ArtworkMetaKeyFit)
}
