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
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
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

// lettered marks a test poster as one that carries a title: the fake
// detector below calls an image text when its top-left pixel is magenta.
var lettered = color.NRGBA{R: 255, B: 255, A: 255}

// withTitle is img with the mark the fake detector reads as text.
func withTitle(img *image.NRGBA) *image.NRGBA {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Min.Y+b.Dy()/10; y++ {
		for x := b.Min.X; x < b.Min.X+b.Dx()/10; x++ {
			img.SetNRGBA(x, y, lettered)
		}
	}
	return img
}

// fakeText is a Fetcher.Text that reads the withTitle mark, and counts its
// calls.
type fakeText struct {
	calls int
	err   error
}

func (d *fakeText) density(_ context.Context, rgb []byte, _, _ int) (int32, error) {
	d.calls++
	if d.err != nil {
		return 0, d.err
	}
	if rgb[0] > 200 && rgb[1] < 80 && rgb[2] > 200 {
		return 30, nil
	}
	return 0, nil
}

// titledSource is a titled source over the given posters, in order.
func titledSource(logo string, plain *catalogartwork.Source, posters ...string) catalogartwork.Source {
	return catalogartwork.Source{
		URL: posters[0], Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: logo, Candidates: posters[1:], Plain: plain,
	}
}

// fetchTitled fetches src as the item's poster with a fake detector, which
// it returns.
func (fx *fixture) fetchTitled(src catalogartwork.Source) (*fakeText, error) {
	d := &fakeText{}
	fx.f.Text = d.density
	return d, fx.f.FetchOne(fx.ctx, fx.ref, catalogv1alpha1.ImageTypePoster, src)
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

func isRedAt(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r>>8 > 170 && g>>8 < 90 && b>>8 < 90
}

var (
	grey = color.NRGBA{R: 128, G: 128, B: 128, A: 255}
	red  = color.NRGBA{R: 230, G: 30, B: 30, A: 255}
)

// A titled poster in which the detector finds no text is stored fitted, as a
// JPEG, with the logo at its bottom centre -- scaled to the title box
// whatever padding the logo carries -- over a shade that leaves the top of
// the art alone, under its source's mark (owner, 2026-11-17).
func TestFetchOneDrawsTheLogoAtTheBottomCentreOfATextlessPoster(t *testing.T) {
	fx := newFixture(t)
	poster := fx.srv.serve("/textless.png", "image/png", encodePNG(t, flat(1000, 1500, grey)))
	// 1600x400 with 100 pixels of padding: 1400x200 of ink, 7:1.
	logo := fx.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(1600, 400, 100, red)))
	src := titledSource(logo, nil, poster)
	d, err := fx.fetchTitled(src)
	require.NoError(t, err)
	assert.Equal(t, 1, d.calls)

	img, info := fx.decoded(t)
	assert.Equal(t, image.Rect(0, 0, 500, 750), img.Bounds())
	assert.Equal(t, "image/jpeg", info.Headers[events.HeaderContentType])
	assert.Equal(t, catalogartwork.FitVersion, info.Metadata[events.ArtworkMetaKeyFit])
	assert.Equal(t, src.TitleMark(), info.Metadata[events.ArtworkMetaKeyTitle])
	assert.Equal(t, "true", info.Metadata[events.ArtworkMetaKeyTitled])
	assert.Equal(t, poster, info.Metadata[events.ArtworkMetaKeySourceURL])
	assert.Equal(t, string(catalogv1alpha1.ArtworkSourceProvider), info.Metadata[events.ArtworkMetaKeySource])

	// The ink is 78% of 500 wide (390) and a seventh as tall (55), centred,
	// its bottom edge 5% (37) above the poster's: x 55..445, y 658..713.
	assert.True(t, isRedAt(img, 250, 685), "the logo's centre")
	assert.True(t, isRedAt(img, 70, 685), "its left end")
	assert.True(t, isRedAt(img, 430, 685), "its right end")
	assert.False(t, isRedAt(img, 40, 685), "left of the logo")
	assert.False(t, isRedAt(img, 460, 685), "right of the logo")
	assert.False(t, isRedAt(img, 250, 640), "above the logo")
	assert.False(t, isRedAt(img, 250, 730), "below the logo")

	// The shade: the art is untouched at the top and darker toward the
	// bottom.
	assert.InDelta(t, 128, lumaAt(img, 250, 100), 4, "the top of the art")
	assert.InDelta(t, 128, lumaAt(img, 250, 370), 4, "above the shade")
	assert.Less(t, lumaAt(img, 20, 600), 110, "into the shade")
	assert.Less(t, lumaAt(img, 20, 740), lumaAt(img, 20, 600), "deeper at the bottom edge")
}

// A poster listed without a language may carry a title all the same (TMDB's
// first for Your Name): the detector reads each in order, a poster with
// text or one that cannot be had is passed over, and the first without text
// takes the logo.
func TestFetchOnePassesOverAPosterThatCarriesText(t *testing.T) {
	fx := newFixture(t)
	lettered := fx.srv.serve("/lettered.png", "image/png", encodePNG(t, withTitle(flat(1000, 1500, grey))))
	gone := fx.srv.serveStatus("/gone.png", 404)
	clean := fx.srv.serve("/clean.png", "image/png", encodePNG(t, flat(1000, 1500, grey)))
	never := fx.srv.serve("/never.png", "image/png", encodePNG(t, flat(1000, 1500, grey)))
	logo := fx.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(1600, 400, 100, red)))
	src := titledSource(logo, &catalogartwork.Source{URL: lettered, Kind: catalogv1alpha1.ArtworkSourceProvider}, lettered, gone, clean, never)

	d, err := fx.fetchTitled(src)
	require.NoError(t, err)
	assert.Equal(t, 2, d.calls, "the lettered poster and the clean one; nothing after the first without text")

	img, info := fx.decoded(t)
	assert.Equal(t, clean, info.Metadata[events.ArtworkMetaKeySourceURL])
	assert.Equal(t, src.TitleMark(), info.Metadata[events.ArtworkMetaKeyTitle])
	assert.Equal(t, "true", info.Metadata[events.ArtworkMetaKeyTitled])
	assert.True(t, isRedAt(img, 250, 685), "the logo")
}

// When every candidate carries text the item takes its plain poster as it
// is, with no logo over its own title, stored under the same mark so the
// plan does not ask again.
func TestFetchOneFallsBackToThePlainPosterWhenEveryCandidateCarriesText(t *testing.T) {
	fx := newFixture(t)
	a := fx.srv.serve("/a.png", "image/png", encodePNG(t, withTitle(flat(1000, 1500, grey))))
	b := fx.srv.serve("/b.png", "image/png", encodePNG(t, withTitle(flat(1000, 1500, grey))))
	set := fx.srv.serve("/set.png", "image/png", encodePNG(t, flat(1000, 1500, color.NRGBA{R: 40, G: 90, B: 160, A: 255})))
	logo := fx.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(1600, 400, 100, red)))
	src := titledSource(logo, &catalogartwork.Source{URL: set, Kind: catalogv1alpha1.ArtworkSourceCustom}, a, b)

	d, err := fx.fetchTitled(src)
	require.NoError(t, err)
	assert.Equal(t, 2, d.calls)

	img, info := fx.decoded(t)
	assert.Equal(t, set, info.Metadata[events.ArtworkMetaKeySourceURL])
	assert.Equal(t, string(catalogv1alpha1.ArtworkSourceCustom), info.Metadata[events.ArtworkMetaKeySource])
	assert.Equal(t, src.TitleMark(), info.Metadata[events.ArtworkMetaKeyTitle])
	_, drawn := info.Metadata[events.ArtworkMetaKeyTitled]
	assert.False(t, drawn)
	assert.Equal(t, catalogartwork.FitVersion, info.Metadata[events.ArtworkMetaKeyFit])
	assert.False(t, isRedAt(img, 250, 685), "no logo")
	assert.InDelta(t, lumaAt(img, 250, 100), lumaAt(img, 250, 740), 6, "no shade")

	// With no plain poster either, there is nothing to store.
	fx2 := newFixture(t)
	only := fx2.srv.serve("/a.png", "image/png", encodePNG(t, withTitle(flat(500, 750, grey))))
	logo2 := fx2.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(800, 200, 0, red)))
	_, err = fx2.fetchTitled(titledSource(logo2, nil, only))
	require.Error(t, err)
	_, err = fx2.store.Info(fx2.ctx, fx2.key(catalogv1alpha1.ImageTypePoster))
	assert.ErrorIs(t, err, events.ErrObjectNotFound)
}

// A logo that is all dark would vanish into the shade, so it is drawn white.
func TestFetchOneDrawsADarkLogoWhite(t *testing.T) {
	fx := newFixture(t)
	poster := fx.srv.serve("/textless.png", "image/png", encodePNG(t, flat(500, 750, color.NRGBA{R: 90, G: 120, B: 160, A: 255})))
	logo := fx.srv.serve("/black.png", "image/png", encodePNG(t, wordmark(800, 200, 0, color.NRGBA{A: 255})))
	_, err := fx.fetchTitled(titledSource(logo, nil, poster))
	require.NoError(t, err)
	img, _ := fx.decoded(t)
	assert.Greater(t, lumaAt(img, 250, 670), 230, "the logo, white")
}

// What cannot be judged is not guessed: without its logo, without a
// detector, or with one that fails, the fetch fails and stores nothing, so
// the poster already stored stays -- never a textless one that names
// nothing, nor a logo under a title nobody looked for.
func TestFetchOneStoresNothingItCannotJudge(t *testing.T) {
	boom := errors.New("the runtime is gone")
	for name, tc := range map[string]struct {
		logo   func(fx *fixture) string
		detect func(fx *fixture)
		want   error
		says   string
	}{
		"the logo is gone":     {logo: func(fx *fixture) string { return fx.srv.serveStatus("/logo.png", 404) }, says: "the title logo"},
		"the logo is no image": {logo: func(fx *fixture) string { return fx.srv.serve("/logo.png", "image/png", []byte("not a png")) }, says: "the title logo"},
		"the logo has nothing in it": {logo: func(fx *fixture) string {
			return fx.srv.serve("/logo.png", "image/png", encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 40, 10))))
		}, says: "the title logo"},
		"no detector":        {detect: func(fx *fixture) { fx.f.Text = nil }, want: artwork.ErrNoTextDetector},
		"the detector fails": {detect: func(fx *fixture) { fx.f.Text = (&fakeText{err: boom}).density }, want: boom, says: "for text"},
	} {
		fx := newFixture(t)
		before := fx.srv.serve("/before.png", "image/png", encodePNG(t, noisy(300, 450, 255)))
		require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, before))
		_, _, _, was := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)

		poster := fx.srv.serve("/textless.png", "image/png", encodePNG(t, noisy(1000, 1500, 255)))
		logo := fx.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(800, 200, 0, red)))
		if tc.logo != nil {
			logo = tc.logo(fx)
		}
		fx.f.Text = (&fakeText{}).density
		if tc.detect != nil {
			tc.detect(fx)
		}
		plain := &catalogartwork.Source{URL: before, Kind: catalogv1alpha1.ArtworkSourceProvider}
		err := fx.f.FetchOne(fx.ctx, fx.ref, catalogv1alpha1.ImageTypePoster, titledSource(logo, plain, poster))
		require.Error(t, err, name)
		if tc.want != nil {
			assert.ErrorIs(t, err, tc.want, name)
		}
		assert.Contains(t, err.Error(), tc.says, name)
		assert.NotErrorIs(t, err, artwork.ErrStore, "%s: the source's failure, not the store's", name)

		_, _, _, now := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
		assert.Equal(t, was.Digest, now.Digest, "%s: the stored poster is the one from before", name)
		_, marked := now.Metadata[events.ArtworkMetaKeyTitle]
		assert.False(t, marked, name)
	}
}

// A poster fetched for a source with no logo records no title and is never
// read for text.
func TestFetchOneWithoutALogoRecordsNoTitle(t *testing.T) {
	fx := newFixture(t)
	d := &fakeText{}
	fx.f.Text = d.density
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster,
		fx.srv.serve("/poster.png", "image/png", encodePNG(t, noisy(1000, 1500, 255)))))
	_, _, _, info := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	_, has := info.Metadata[events.ArtworkMetaKeyTitle]
	assert.False(t, has)
	assert.Zero(t, d.calls)
}
