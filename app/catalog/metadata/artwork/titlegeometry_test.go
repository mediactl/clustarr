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
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
)

// greyCanvas is a w x h canvas of mid grey.
func greyCanvas(w, h int) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(canvas.Pix); i += 4 {
		canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2], canvas.Pix[i+3] = 128, 128, 128, 255
	}
	return canvas
}

// whiteBox is the bounding box of canvas's near-white pixels: where an
// opaque white mark landed on a grey canvas, whatever the shade did under it.
func whiteBox(canvas *image.RGBA) image.Rectangle {
	box := image.Rectangle{}
	b := canvas.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := canvas.RGBAAt(x, y)
			if c.R > 240 && c.G > 240 && c.B > 240 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

func TestDrawTitled_Geometry(t *testing.T) {
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for _, tc := range []struct {
		name     string
		geometry artwork.TitleGeometry
	}{
		{"regular", artwork.RegularTitle},
		{"compact", artwork.CompactTitle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.geometry
			// A wide mark is bounded by the width, a tall one by the height;
			// both sit BottomPct above the bottom edge.
			wide := greyCanvas(500, 750)
			require.NoError(t, artwork.DrawTitled(wide, flat(1000, 100, white), g))
			box := whiteBox(wide)
			assert.Equal(t, 500*g.MaxWidthPct/100, box.Dx(), "a wide mark's width")
			assert.Equal(t, 750-750*g.BottomPct/100, box.Max.Y, "a wide mark's bottom edge")
			assert.InDelta(t, 500-box.Max.X, box.Min.X, 1, "centred")

			tall := greyCanvas(500, 750)
			require.NoError(t, artwork.DrawTitled(tall, flat(100, 1000, white), g))
			box = whiteBox(tall)
			assert.Equal(t, 750*g.MaxHeightPct/100, box.Dy(), "a tall mark's height")
			assert.Equal(t, 750-750*g.BottomPct/100, box.Max.Y, "a tall mark's bottom edge")
		})
	}

	// The compact title is the larger one, and the regular one is what it
	// has been since TitleVersion 1.
	assert.Equal(t, artwork.TitleGeometry{MaxWidthPct: 78, MaxHeightPct: 24, BottomPct: 5}, artwork.RegularTitle)
	assert.Greater(t, artwork.CompactTitle.MaxWidthPct, artwork.RegularTitle.MaxWidthPct)
	assert.Greater(t, artwork.CompactTitle.MaxHeightPct, artwork.RegularTitle.MaxHeightPct)
}

// A logo smaller than its box is drawn larger on the compact poster, up to
// the box and at most four times its own size; the regular poster never
// scales a logo up, which is what keeps its bytes (owner, 2026-11-28: "we
// should make the logo readable in both scales").
func TestDrawTitled_ASmallLogoIsEnlargedOnTheCompactPoster(t *testing.T) {
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for _, tc := range []struct {
		name         string
		markW, markH int
		geometry     artwork.TitleGeometry
		wantW, wantH int
	}{
		// On 500x750 the compact box is 450x240 and the regular one 390x180.
		{"regular keeps a small logo's size", 200, 40, artwork.RegularTitle, 200, 40},
		{"compact fills the width", 200, 40, artwork.CompactTitle, 450, 90},
		{"compact fills the height", 40, 100, artwork.CompactTitle, 96, 240},
		{"compact stops at four times", 50, 10, artwork.CompactTitle, 200, 40},
		{"compact still shrinks a large one", 1000, 100, artwork.CompactTitle, 450, 45},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canvas := greyCanvas(500, 750)
			require.NoError(t, artwork.DrawTitled(canvas, flat(tc.markW, tc.markH, white), tc.geometry))
			box := whiteBox(canvas)
			assert.InDelta(t, tc.wantW, box.Dx(), 2, "width")
			assert.InDelta(t, tc.wantH, box.Dy(), 2, "height")
			assert.InDelta(t, 750-750*tc.geometry.BottomPct/100, box.Max.Y, 1, "bottom edge")
			assert.InDelta(t, 500-box.Max.X, box.Min.X, 2, "centred")
		})
	}
}

// The compact title's box and its version move together: a stored compact
// poster records CompactTitleVersion (Source.CompactMark), and one drawn
// with other numbers under the same version is never fetched again. Change
// the numbers here only with the version.
func TestTheCompactTitleBoxIsItsVersions(t *testing.T) {
	assert.Equal(t, "1", catalogartwork.CompactTitleVersion)
	assert.Equal(t, artwork.TitleGeometry{MaxWidthPct: 90, MaxHeightPct: 32, BottomPct: 5, MaxEnlargePct: 400}, artwork.CompactTitle,
		"raise catalogartwork.CompactTitleVersion with these")
}

// The regular title is drawn as it was before the geometry became a value:
// the golden is the output of the code at ee882309 for these inputs. A
// difference here is a new digest for every titled poster, and a refresh of
// the whole library in Plex.
func TestDrawTitled_RegularUnchanged(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 500, 750))
	for y := range 750 {
		for x := range 500 {
			canvas.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / 499), G: uint8(y * 255 / 749), B: 128, A: 255})
		}
	}
	mark := wordmark(600, 200, 20, color.NRGBA{R: 220, G: 40, B: 40, A: 255})
	require.NoError(t, artwork.DrawTitled(canvas, mark, artwork.RegularTitle))

	f, err := os.Open("../../../../test/data/artwork/title-regular-golden.png")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	golden, err := png.Decode(f)
	require.NoError(t, err)
	require.Equal(t, canvas.Bounds(), golden.Bounds())
	differ := 0
	for y := range 750 {
		for x := range 500 {
			gr, gg, gb, _ := golden.At(x, y).RGBA()
			c := canvas.RGBAAt(x, y)
			if uint8(gr>>8) != c.R || uint8(gg>>8) != c.G || uint8(gb>>8) != c.B {
				differ++
			}
		}
	}
	assert.Zero(t, differ, "pixels that differ from the golden")
}
