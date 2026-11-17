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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/pkg/events"
)

// flat is a w x h picture of one colour.
func flat(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// wordmark is a w x h transparent picture with a bar of ink across its
// middle half, inset by pad on every side: a logo with its own padding.
func wordmark(w, h, pad int, ink color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := pad; y < h-pad; y++ {
		for x := pad; x < w-pad; x++ {
			img.SetNRGBA(x, y, ink)
		}
	}
	return img
}

func (fx *fixture) fetchTitled(poster, logo string) error {
	return fx.f.FetchOne(fx.ctx, fx.ref, catalogv1alpha1.ImageTypePoster,
		catalogartwork.Source{URL: poster, Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: logo})
}

// decoded reads the stored poster back as pixels.
func (fx *fixture) decoded(t *testing.T) (image.Image, events.ObjectInfo) {
	t.Helper()
	body, _, _, info := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	img, _, err := image.Decode(bytes.NewReader(body))
	require.NoError(t, err)
	return img, info
}

func lumaAt(img image.Image, x, y int) int {
	r, g, b, _ := img.At(x, y).RGBA()
	return int((299*(r>>8) + 587*(g>>8) + 114*(b>>8)) / 1000)
}

// A poster whose source names a logo is stored fitted, as a JPEG, with the
// logo at its bottom centre -- scaled to the title box whatever padding the
// logo carries -- over a shade that leaves the top of the art alone, and it
// records which logo it was drawn with (owner, 2026-11-17).
func TestFetchOneDrawsTheLogoAtTheBottomCentreOfATextlessPoster(t *testing.T) {
	fx := newFixture(t)
	grey := color.NRGBA{R: 128, G: 128, B: 128, A: 255}
	red := color.NRGBA{R: 230, G: 30, B: 30, A: 255}
	poster := fx.srv.serve("/textless.png", "image/png", encodePNG(t, flat(1000, 1500, grey)))
	// 1600x400 with 100 pixels of padding: 1400x200 of ink, 7:1.
	logo := fx.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(1600, 400, 100, red)))
	require.NoError(t, fx.fetchTitled(poster, logo))

	img, info := fx.decoded(t)
	assert.Equal(t, image.Rect(0, 0, 500, 750), img.Bounds())
	assert.Equal(t, "image/jpeg", info.Headers[events.HeaderContentType])
	assert.Equal(t, catalogartwork.FitVersion, info.Metadata[events.ArtworkMetaKeyFit])
	assert.Equal(t, catalogartwork.TitleMark(logo), info.Metadata[events.ArtworkMetaKeyTitle])
	assert.Equal(t, poster, info.Metadata[events.ArtworkMetaKeySourceURL])

	// The ink is 78% of 500 wide (390) and a seventh as tall (55), centred,
	// its bottom edge 5% (37) above the poster's: x 55..445, y 658..713.
	isRed := func(x, y int) bool {
		r, g, b, _ := img.At(x, y).RGBA()
		return r>>8 > 170 && g>>8 < 90 && b>>8 < 90
	}
	assert.True(t, isRed(250, 685), "the logo's centre")
	assert.True(t, isRed(70, 685), "its left end")
	assert.True(t, isRed(430, 685), "its right end")
	assert.False(t, isRed(40, 685), "left of the logo")
	assert.False(t, isRed(460, 685), "right of the logo")
	assert.False(t, isRed(250, 640), "above the logo")
	assert.False(t, isRed(250, 730), "below the logo")

	// The shade: the art is untouched at the top and darker toward the
	// bottom.
	assert.InDelta(t, 128, lumaAt(img, 250, 100), 4, "the top of the art")
	assert.InDelta(t, 128, lumaAt(img, 250, 370), 4, "above the shade")
	assert.Less(t, lumaAt(img, 20, 600), 110, "into the shade")
	assert.Less(t, lumaAt(img, 20, 740), lumaAt(img, 20, 600), "deeper at the bottom edge")
}

// A logo that is all dark would vanish into the shade, so it is drawn white.
func TestFetchOneDrawsADarkLogoWhite(t *testing.T) {
	fx := newFixture(t)
	poster := fx.srv.serve("/textless.png", "image/png", encodePNG(t, flat(500, 750, color.NRGBA{R: 90, G: 120, B: 160, A: 255})))
	logo := fx.srv.serve("/black.png", "image/png", encodePNG(t, wordmark(800, 200, 0, color.NRGBA{A: 255})))
	require.NoError(t, fx.fetchTitled(poster, logo))
	img, _ := fx.decoded(t)
	assert.Greater(t, lumaAt(img, 250, 670), 230, "the logo, white")
}

// A logo that cannot be had fails the fetch and stores nothing: the poster
// already stored stays, where a textless one without its logo would name
// nothing.
func TestFetchOneWithoutItsLogoStoresNothing(t *testing.T) {
	for name, logoOf := range map[string]func(fx *fixture) string{
		"gone":         func(fx *fixture) string { return fx.srv.serveStatus("/logo.png", 404) },
		"not an image": func(fx *fixture) string { return fx.srv.serve("/logo.png", "image/png", []byte("not a png")) },
		"nothing opaque": func(fx *fixture) string {
			return fx.srv.serve("/logo.png", "image/png", encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 40, 10))))
		},
	} {
		fx := newFixture(t)
		before := fx.srv.serve("/before.png", "image/png", encodePNG(t, noisy(300, 450, 255)))
		require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, before))
		_, _, _, was := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)

		poster := fx.srv.serve("/textless.png", "image/png", encodePNG(t, noisy(1000, 1500, 255)))
		err := fx.fetchTitled(poster, logoOf(fx))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "the title logo", name)

		_, _, _, now := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
		assert.Equal(t, was.Digest, now.Digest, "%s: the stored poster is the one from before", name)
		assert.Equal(t, before, now.Metadata[events.ArtworkMetaKeySourceURL], name)
	}
}

// A poster fetched without a logo records no title, so the plan can tell the
// two apart.
func TestFetchOneWithoutALogoRecordsNoTitle(t *testing.T) {
	fx := newFixture(t)
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster,
		fx.srv.serve("/poster.png", "image/png", encodePNG(t, noisy(1000, 1500, 255)))))
	_, _, _, info := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	_, has := info.Metadata[events.ArtworkMetaKeyTitle]
	assert.False(t, has)
}
