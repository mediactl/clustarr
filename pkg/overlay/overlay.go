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

// Package overlay is the pure poster badge renderer (spec §C.5): given a
// base poster image, a stack of rating Badges and a Template, it draws one
// rounded-rect box per badge -- the source's logo on top, the score in bold
// white beneath -- in the chosen corner, and returns the composited image.
// It touches no cluster, no filesystem beyond its own embedded logos, and no
// network; the artwork-render role (spec §C.6, a later task) is the only
// caller that talks to Kubernetes or an object store.
//
// # Badge shape: Plex's episode-count box
//
// A badge box is Plex's episode-count box, measured to the pixel from the
// owner's reference screenshot (2026-09-24): 237x207 on a 1249x1869 poster
// (18.98% of the poster's width by 16.57% of it), flush with the poster's
// two edges at its corner, square on the two corners that lie on those
// edges and rounded (2% of the poster's width) only on the inner corner,
// black at 80%, its count 56px tall (27% of the box) and centred. The
// defaults reproduce it; Template.WidthPct scales it at the same aspect.
// (Until then the box was square and rounded on three corners, from the
// spec's prose description of the same reference.)
//
// Every badge in a stack takes the anchor badge's shape, even a box
// stacked away from the poster edge and so not literally touching it; the
// alternative would need a "which layout position am I" flag threaded
// through the drawing code. The four-badge golden this package ships
// (test/data/overlay/four_badges.png) shows the result.
package overlay

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// minBoxPx and minFontSizePx are Review Focus 4's small-poster floor: a
// badge box never draws narrower than 24px, and its score text's font never
// renders smaller than 8px, regardless of how small WidthPct/ScorePct would
// otherwise make them on a tiny poster (e.g. a 60x90 thumbnail).
const (
	minBoxPx      = 24
	minFontSizePx = 8
)

// fillColor is the badge box background, black, blended at
// Template.OpacityPct: Plex's episode-count box is black at 80% -- every
// pixel of it in the reference screenshot (2026-09-24) is exactly 0.2x the
// poster beneath, in every channel.
var fillColor = color.NRGBA{}

// plexBoxW and plexBoxH are Plex's episode-count box, measured in pixels
// from the owner's reference screenshot (2026-09-24): 237x207 on a
// 1249x1869 poster, i.e. 18.98% of the poster's width wide and 16.57% of
// it tall. A badge box keeps this aspect at every Template.WidthPct, so
// the default width (19%) reproduces the box to the pixel.
const (
	plexBoxW = 237
	plexBoxH = 207
)

// Render composites badges onto base, in the corner and geometry t
// describes, and returns the result as a new image the same size as base.
// badges[0] is nearest the anchored corner; later badges stack away from it
// along the poster's vertical edge (see the package doc comment for the
// shape every stacked badge takes).
//
// Render never panics on a degenerate geometry (a tiny poster, more badges
// than fit): every drawing call below goes through image/draw, which clips
// to the destination's own bounds, so a box or glyph computed partly or
// wholly off-canvas simply draws the part that is on-canvas -- never out of
// bounds, never a runtime error.
func Render(base image.Image, badges []Badge, t Template) (*image.NRGBA, error) {
	if base == nil {
		return nil, errors.New("overlay: base image is nil")
	}
	bounds := base.Bounds()
	canvas := image.NewNRGBA(bounds)
	draw.Draw(canvas, bounds, base, bounds.Min, draw.Src)

	if len(badges) == 0 {
		return canvas, nil
	}

	posterW := bounds.Dx()
	paddingPx := scalePct(posterW, t.PaddingPct)
	radiusPx := scalePct(posterW, t.RadiusPct)
	boxes := layoutBoxes(bounds, len(badges), t)

	for i, b := range badges {
		if err := drawBadge(canvas, boxes[i], paddingPx, radiusPx, b, t); err != nil {
			return nil, fmt.Errorf("overlay: badge %d (%s): %w", i, b.Source, err)
		}
	}
	return canvas, nil
}

// scalePct returns v scaled by pct/100, rounded down like every other
// geometry calculation here (a badge that is a pixel smaller than the exact
// percentage is invisible; a badge that overflows it is not).
func scalePct(v, pct int) int {
	return v * pct / 100
}

// roundedRectMask returns a w x h alpha mask for a rectangle with one
// rounded corner, the one diagonally opposite anchor, and three right
// angles -- the shape of Plex's episode-count box, whose two corners on the
// poster's edges are square and whose one inner corner is rounded
// (measured, 2026-09-24). Every badge box in a stack takes it (see the
// package doc comment). radius is clamped to fit within the box so
// a large RadiusPct on a small badge degrades to a circle/stadium rather
// than producing overlapping or inverted arcs.
//
// Edges are anti-aliased with a half-pixel linear coverage ramp computed
// with math.Sqrt, which the Go spec and IEEE 754 require to be correctly
// rounded -- deterministic across architectures, unlike this package's
// glyph rendering (see render_test.go's golden comparison for why that one
// needs a tolerance).
func roundedRectMask(w, h, radius int, anchor Corner) *image.Alpha {
	if radius < 0 {
		radius = 0
	}
	if radius > w/2 {
		radius = w / 2
	}
	if radius > h/2 {
		radius = h / 2
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := cornerCoverage(x, y, w, h, radius, anchor)
			mask.SetAlpha(x, y, color.Alpha{A: uint8(c*255 + 0.5)})
		}
	}
	return mask
}

// cornerCoverage returns pixel (x,y)'s coverage (0..1) of a w x h rect whose
// only rounded corner is the one diagonally opposite anchor. A pixel
// outside that corner's radius x radius box is fully covered; a pixel
// inside it is covered by distance from the corner's circle centre.
func cornerCoverage(x, y, w, h, radius int, anchor Corner) float64 {
	var cx, cy float64
	var rounded bool
	switch {
	case x < radius && y < radius:
		rounded = anchor == CornerBottomRight
		cx, cy = float64(radius), float64(radius)
	case x >= w-radius && y < radius:
		rounded = anchor == CornerBottomLeft
		cx, cy = float64(w-radius), float64(radius)
	case x < radius && y >= h-radius:
		rounded = anchor == CornerTopRight
		cx, cy = float64(radius), float64(h-radius)
	case x >= w-radius && y >= h-radius:
		rounded = anchor == CornerTopLeft
		cx, cy = float64(w-radius), float64(h-radius)
	default:
		return 1
	}
	if !rounded {
		return 1
	}
	dx := float64(x) + 0.5 - cx
	dy := float64(y) + 0.5 - cy
	dist := math.Sqrt(dx*dx + dy*dy)
	rf := float64(radius)
	switch {
	case dist <= rf-0.5:
		return 1
	case dist >= rf+0.5:
		return 0
	default:
		return rf + 0.5 - dist
	}
}

// layoutBoxes returns each badge's box rectangle in canvas coordinates.
// Every box is the same size, WidthPct of the poster's width wide and
// plexBoxH/plexBoxW of that tall; index 0 sits flush with the poster edge at
// the anchored corner, and later indices step away from that edge by
// boxH+gap, per badge, along the vertical axis only -- the horizontal
// position (flush with the corner's vertical edge) never changes across the
// stack.
func layoutBoxes(bounds image.Rectangle, n int, t Template) []image.Rectangle {
	posterW := bounds.Dx()
	boxW := scalePct(posterW, t.WidthPct)
	if boxW < minBoxPx {
		boxW = minBoxPx
	}
	boxH := maxInt(int(math.Round(float64(boxW)*plexBoxH/plexBoxW)), 1)
	gap := scalePct(posterW, t.PaddingPct)

	var x int
	switch t.Corner {
	case CornerBottomLeft, CornerTopLeft:
		x = bounds.Min.X
	default: // CornerBottomRight, CornerTopRight
		x = bounds.Max.X - boxW
	}

	boxes := make([]image.Rectangle, n)
	for i := range boxes {
		var y int
		switch t.Corner {
		case CornerTopLeft, CornerTopRight:
			y = bounds.Min.Y + i*(boxH+gap)
		default: // CornerBottomLeft, CornerBottomRight
			y = bounds.Max.Y - boxH - i*(boxH+gap)
		}
		boxes[i] = image.Rect(x, y, x+boxW, y+boxH)
	}
	return boxes
}

// drawBadge draws one badge's box, logo and score text into canvas at box.
// paddingPx is reused both between badges (layoutBoxes' gap) and inside one
// badge (this function's internal margin) -- see Template.PaddingPct's doc
// comment.
func drawBadge(canvas *image.NRGBA, box image.Rectangle, paddingPx, radiusPx int, b Badge, t Template) error {
	boxW, boxH := box.Dx(), box.Dy()

	mask := roundedRectMask(boxW, boxH, radiusPx, t.Corner)
	fill := color.NRGBA{R: fillColor.R, G: fillColor.G, B: fillColor.B, A: uint8(scalePct(255, t.OpacityPct))}
	draw.DrawMask(canvas, box, image.NewUniform(fill), image.Point{}, mask, image.Point{}, draw.Over)

	margin := paddingPx
	if margin < 1 {
		margin = 1
	}
	if 2*margin >= boxW || 2*margin >= boxH {
		margin = maxInt((minInt(boxW, boxH)-1)/2, 0)
	}
	contentW := maxInt(boxW-2*margin, 1)
	contentH := maxInt(boxH-2*margin, 1)

	// The score's cap height is ScorePct of the box height, as
	// OverlayGeometry.scorePercent documents: Plex's count is 56px on its
	// 207px box, 27%. (Until 2026-09-24 it was ScorePct of the height left
	// under the logo, 10px at the defaults on a 500px poster, so the 8px
	// font floor drew every score.)
	var face font.Face
	capH := 0
	if b.Score != "" {
		f, _, err := faceForCapHeight(math.Max(float64(scalePct(boxH, t.ScorePct)), 1))
		if err != nil {
			return fmt.Errorf("score font: %w", err)
		}
		defer func() { _ = f.Close() }()
		face = f
		capH = maxInt(int(math.Round(fixedToFloat(f.Metrics().CapHeight))), 1)
	}

	// The logo is LogoPct of the content width, shrunk to the height the
	// score and the gap above it leave.
	lb := logoBounds(b.Logo)
	logoW, logoH := 0, 0
	if !lb.Empty() {
		room := contentH
		if capH > 0 {
			room = contentH - capH - margin
		}
		if room >= 1 {
			logoW = clampInt(scalePct(contentW, t.LogoPct), 1, contentW)
			logoH = scaleToWidth(logoW, lb)
			if logoH > room {
				logoH = room
				logoW = scaleToHeight(logoH, lb)
			}
		}
	}

	// The logo, the gap and the score's cap height are centred in the box
	// as one group, as Plex centres its count.
	groupH := logoH + capH
	if logoH > 0 && capH > 0 {
		groupH += margin
	}
	top := box.Min.Y + (boxH-groupH)/2
	if logoH > 0 {
		left := box.Min.X + (boxW-logoW)/2
		dst := image.Rect(left, top, left+logoW, top+logoH)
		xdraw.CatmullRom.Scale(canvas, dst, b.Logo, lb, xdraw.Over, nil)
		top += logoH + margin
	}
	if face != nil {
		drawScoreText(canvas, b.Score, face, box, top, capH)
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// logoBounds returns img's bounds, or a zero (Empty) rectangle for a nil
// image -- so drawBadge's "is there a logo to draw" check is one comparison
// instead of a nil check plus a bounds check.
func logoBounds(img image.Image) image.Rectangle {
	if img == nil {
		return image.Rectangle{}
	}
	return img.Bounds()
}

// scaleToWidth returns the height that preserves src's aspect ratio at
// width w, at least 1px.
func scaleToWidth(w int, src image.Rectangle) int {
	sw, sh := src.Dx(), src.Dy()
	if sw <= 0 {
		return 1
	}
	return maxInt(int(math.Round(float64(w)*float64(sh)/float64(sw))), 1)
}

// scaleToHeight is scaleToWidth's inverse.
func scaleToHeight(h int, src image.Rectangle) int {
	sw, sh := src.Dx(), src.Dy()
	if sh <= 0 {
		return 1
	}
	return maxInt(int(math.Round(float64(h)*float64(sw)/float64(sh))), 1)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// drawScoreText draws text in bold white, horizontally centred in box, with
// its cap height vertically centred in the scoreAreaH-tall region starting
// at scoreTop.
func drawScoreText(canvas *image.NRGBA, text string, face font.Face, box image.Rectangle, scoreTop, scoreAreaH int) {
	metrics := face.Metrics()
	capPx := fixedToFloat(metrics.CapHeight)

	drawer := &font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(color.White),
		Face: face,
	}
	textWidthPx := fixedToFloat(drawer.MeasureString(text))

	startX := float64(box.Min.X) + (float64(box.Dx())-textWidthPx)/2
	baselineY := float64(scoreTop) + (float64(scoreAreaH)+capPx)/2

	drawer.Dot = fixed.Point26_6{X: floatToFixed(startX), Y: floatToFixed(baselineY)}
	drawer.DrawString(text)
}

// boldFont is gobold.TTF (golang.org/x/image/font/gofont/gobold), parsed
// once; each call to faceForCapHeight then makes its own font.Face (a Face
// is not safe for concurrent use, but *opentype.Font's methods are, given
// each Face its own buffer -- see x/image/font/opentype).
var (
	boldFont struct {
		once sync.Once
		font *opentype.Font
		err  error
	}
	capRatio struct {
		once  sync.Once
		ratio float64
		err   error
	}
)

const probeFontSizePx = 100

func loadGobold() (*opentype.Font, error) {
	boldFont.once.Do(func() {
		boldFont.font, boldFont.err = opentype.Parse(gobold.TTF)
	})
	return boldFont.font, boldFont.err
}

// goboldCapHeightRatio is gobold's CapHeight as a fraction of its point
// size at 72 DPI (where 1pt == 1px), probed once from a reference size.
// CapHeight scales linearly with point size, so faceForCapHeight inverts
// this ratio directly instead of iterating toward a target.
func goboldCapHeightRatio() (float64, error) {
	capRatio.once.Do(func() {
		f, err := loadGobold()
		if err != nil {
			capRatio.err = err
			return
		}
		probe, err := opentype.NewFace(f, &opentype.FaceOptions{Size: probeFontSizePx, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			capRatio.err = err
			return
		}
		defer func() { _ = probe.Close() }()
		capRatio.ratio = fixedToFloat(probe.Metrics().CapHeight) / probeFontSizePx
	})
	return capRatio.ratio, capRatio.err
}

// faceForCapHeight returns a gobold font.Face whose CapHeight is
// capHeightPx, and the point size it resolved to (exported as a second
// return value for render_test.go's small-poster assertion, rather than
// duplicating this clamp in a test). Review Focus 4: the resolved size never
// drops below minFontSizePx, however small capHeightPx asks for.
func faceForCapHeight(capHeightPx float64) (font.Face, float64, error) {
	f, err := loadGobold()
	if err != nil {
		return nil, 0, err
	}
	ratio, err := goboldCapHeightRatio()
	if err != nil {
		return nil, 0, err
	}
	size := capHeightPx / ratio
	if size < minFontSizePx {
		size = minFontSizePx
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, 0, err
	}
	return face, size, nil
}

func fixedToFloat(v fixed.Int26_6) float64 { return float64(v) / 64 }

func floatToFixed(v float64) fixed.Int26_6 { return fixed.Int26_6(math.Round(v * 64)) }
