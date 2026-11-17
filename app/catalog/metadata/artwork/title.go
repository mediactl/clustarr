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

package artwork

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	xdraw "golang.org/x/image/draw"

	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
)

// The title drawing (catalogartwork.TitleVersion 1; owner, 2026-11-17, after
// github.com/elfhosted/PostersPlus): a poster without text takes its item's
// logo at the bottom centre, over a shade that keeps a pale logo legible on
// pale art. Raise TitleVersion when any of these changes.
const (
	// titleMaxWidthPct and titleMaxHeightPct bound the logo, as percentages
	// of the poster's width and height; it is scaled to fit both, never up.
	titleMaxWidthPct  = 78
	titleMaxHeightPct = 24
	// titleBottomPct is the gap under the logo, a percentage of the
	// poster's height.
	titleBottomPct = 5
	// titleShadeFromPct is where the shade begins, a percentage of the
	// poster's height from the top; it deepens to titleShadeAlpha (of 255)
	// at the bottom edge.
	titleShadeFromPct = 52
	titleShadeAlpha   = 185
	// titleOpaque is the least alpha (of 255) that counts as part of the
	// logo, for trimming its margins and judging its colour.
	titleOpaque = 24
	// titleDarkLuma is the luminance (of 255) under which a logo's pixel is
	// dark, and titleDarkPct the share of dark pixels that makes the whole
	// logo dark: it is drawn white, since it sits on the shade.
	titleDarkLuma = 70
	titleDarkPct  = 85
)

// errNoTitle is a logo with nothing to draw.
var errNoTitle = errors.New("the logo has no opaque pixel")

// titled is poster, an image of cfg's size, fitted into box as fit stores a
// poster and with logo drawn at its bottom centre, encoded as a JPEG at
// FitJPEGQuality. The logo's transparent margins are trimmed first, so its
// own padding moves neither its size nor its place, and a logo that is all
// dark is drawn white.
func (f *Fetcher) titled(poster, logo []byte, box catalogartwork.Box, cfg image.Config) (fitted, error) {
	if cfg.Width*cfg.Height > MaxFitPixels {
		return fitted{}, fmt.Errorf("%w: a %dx%d poster is too large to draw on", ErrImageTooLarge, cfg.Width, cfg.Height)
	}
	f.fitMu.Lock()
	defer f.fitMu.Unlock()

	base, _, err := image.Decode(bytes.NewReader(poster))
	if err != nil {
		return fitted{}, fmt.Errorf("%w: the poster does not decode: %w", ErrNotAnImage, err)
	}
	mark, _, err := image.Decode(bytes.NewReader(logo))
	if err != nil {
		return fitted{}, fmt.Errorf("%w: the logo does not decode: %w", ErrNotAnImage, err)
	}

	w, h := box.Size(cfg.Width, cfg.Height)
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == cfg.Width && h == cfg.Height {
		draw.Draw(canvas, canvas.Bounds(), base, base.Bounds().Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(canvas, canvas.Bounds(), base, base.Bounds(), xdraw.Src, nil)
	}
	shade(canvas)
	if err := drawTitle(canvas, mark); err != nil {
		return fitted{}, err
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: FitJPEGQuality}); err != nil {
		return fitted{}, err
	}
	return fitted{body: buf.Bytes(), contentType: "image/jpeg", width: w, height: h}, nil
}

// shade darkens canvas from titleShadeFromPct down, deepening with the
// square of the distance so the art fades rather than meets an edge.
func shade(canvas *image.RGBA) {
	b := canvas.Bounds()
	from := b.Min.Y + b.Dy()*titleShadeFromPct/100
	span := b.Max.Y - from
	if span <= 0 {
		return
	}
	for y := from; y < b.Max.Y; y++ {
		t := float64(y-from+1) / float64(span)
		keep := 255 - int(float64(titleShadeAlpha)*t*t)
		row := canvas.Pix[canvas.PixOffset(b.Min.X, y) : canvas.PixOffset(b.Max.X-1, y)+4]
		for i := 0; i < len(row); i += 4 {
			row[i] = uint8(int(row[i]) * keep / 255)
			row[i+1] = uint8(int(row[i+1]) * keep / 255)
			row[i+2] = uint8(int(row[i+2]) * keep / 255)
		}
	}
}

// drawTitle draws mark at canvas's bottom centre: trimmed to its opaque
// pixels, scaled to fit the title box, and white when it is all dark.
func drawTitle(canvas *image.RGBA, mark image.Image) error {
	src := image.NewNRGBA(mark.Bounds())
	draw.Draw(src, src.Bounds(), mark, mark.Bounds().Min, draw.Src)
	crop, dark := inked(src)
	if crop.Empty() {
		return errNoTitle
	}
	if dark {
		whiten(src, crop)
	}

	cb := canvas.Bounds()
	maxW, maxH := cb.Dx()*titleMaxWidthPct/100, cb.Dy()*titleMaxHeightPct/100
	w, h := crop.Dx(), crop.Dy()
	if w > maxW {
		w, h = maxW, h*maxW/w
	}
	if h > maxH {
		w, h = w*maxH/h, maxH
	}
	if w < 1 || h < 1 {
		return errNoTitle
	}
	x := cb.Min.X + (cb.Dx()-w)/2
	y := cb.Max.Y - cb.Dy()*titleBottomPct/100 - h
	xdraw.CatmullRom.Scale(canvas, image.Rect(x, y, x+w, y+h), src, crop, xdraw.Over, nil)
	return nil
}

// inked is the bounding box of img's opaque pixels, and whether they are
// dark: titleDarkPct of them under titleDarkLuma.
func inked(img *image.NRGBA) (crop image.Rectangle, dark bool) {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	var opaque, darkPixels int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):]
		for x := b.Min.X; x < b.Max.X; x++ {
			p := row[(x-b.Min.X)*4 : (x-b.Min.X)*4+4]
			if p[3] < titleOpaque {
				continue
			}
			opaque++
			if luma(p[0], p[1], p[2]) < titleDarkLuma {
				darkPixels++
			}
			minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x+1), max(maxY, y+1)
		}
	}
	if opaque == 0 {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX, maxY), darkPixels*100 >= opaque*titleDarkPct
}

// luma is the Rec. 601 luminance of an sRGB colour, 0-255.
func luma(r, g, b uint8) int {
	return (299*int(r) + 587*int(g) + 114*int(b)) / 1000
}

// whiten makes every pixel of img within crop white, keeping its alpha.
func whiten(img *image.NRGBA, crop image.Rectangle) {
	white := color.NRGBA{R: 255, G: 255, B: 255}
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = white.R, white.G, white.B
		}
	}
}
