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
	"image"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"

	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
)

// FitJPEGQuality is the quality an opaque original of a sized type is
// encoded at.
const FitJPEGQuality = 85

// MaxFitPixels is the largest image the fetcher decodes to fit it: a decode
// holds four bytes a pixel, and the metadata agent's memory is small. A
// larger image is stored as fetched.
const MaxFitPixels = 40_000_000

// fitted is an original as it is stored after a fit.
type fitted struct {
	body          []byte
	contentType   string
	width, height int
}

// fit is body, an image of cfg's size, as an original of a sized type is
// stored (owner, 2026-11-16): scaled down into box with its aspect ratio
// kept, never up, and encoded as a JPEG at FitJPEGQuality. An image with
// transparency keeps it: scaled the same way, it is stored as a PNG, and one
// that already fits is left alone. An opaque image that already fits is
// encoded again only when that saves a tenth of its bytes, so a lean JPEG is
// not degraded for nothing. false keeps body as fetched: nothing to gain, an
// image over MaxFitPixels, or one that does not decode in full.
func (f *Fetcher) fit(body []byte, box catalogartwork.Box, cfg image.Config) (fitted, bool) {
	if cfg.Width*cfg.Height > MaxFitPixels {
		return fitted{}, false
	}
	f.fitMu.Lock()
	defer f.fitMu.Unlock()

	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return fitted{}, false
	}
	opaque := true
	if o, ok := img.(interface{ Opaque() bool }); ok {
		opaque = o.Opaque()
	}
	w, h := box.Size(cfg.Width, cfg.Height)
	scaled := w != cfg.Width || h != cfg.Height
	if !scaled && !opaque {
		return fitted{}, false
	}
	if scaled {
		// Catmull-Rom, as the overlay renderer scales: a cheaper kernel
		// aliases the fine detail artwork is full of.
		var dst xdraw.Image
		if opaque {
			dst = image.NewRGBA(image.Rect(0, 0, w, h))
		} else {
			dst = image.NewNRGBA(image.Rect(0, 0, w, h))
		}
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Src, nil)
		img = dst
	}

	var buf bytes.Buffer
	out := fitted{contentType: "image/jpeg", width: w, height: h}
	if opaque {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: FitJPEGQuality})
	} else {
		out.contentType = "image/png"
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return fitted{}, false
	}
	if !scaled && buf.Len() > len(body)*9/10 {
		return fitted{}, false
	}
	out.body = buf.Bytes()
	return out, true
}
