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
// black at 80%, its count 56px tall (27% of the box) and centred, in Open
// Sans Bold. The defaults reproduce it; Template.WidthPct scales it at the
// same aspect. A badge puts its logo left of the score on one row, the row
// centred in the box both ways, the digits at the count's height.
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
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
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

	// One row, like Plex's single-line count: the logo, a gap and the
	// score, centred in the box both ways. The score's cap height is
	// ScorePct of the box height (Plex's count is 56px on its 207px box,
	// 27%) and the logo LogoPct of it tall; a row wider than the content
	// area -- a three-character score beside the logo -- shrinks as a
	// whole until it fits.
	lb := logoBounds(b.Logo)
	capTarget := math.Min(float64(scalePct(boxH, t.ScorePct)), float64(contentH))
	logoTarget := math.Min(float64(scalePct(boxH, t.LogoPct)), float64(contentH))
	var r scoreRow
	for scale := 1.0; ; scale *= 0.95 {
		var err error
		r, err = layoutRow(b, lb, capTarget*scale, logoTarget*scale)
		if err != nil {
			return err
		}
		if r.width() <= contentW || scale < 0.2 {
			break
		}
		r.close()
	}
	defer r.close()

	centreY := float64(box.Min.Y) + float64(boxH)/2
	x := box.Min.X + (boxW-r.width())/2
	if r.logoH > 0 {
		top := int(math.Round(centreY - float64(r.logoH)/2))
		dst := image.Rect(x, top, x+r.logoW, top+r.logoH)
		xdraw.CatmullRom.Scale(canvas, dst, b.Logo, lb, xdraw.Over, nil)
		x += r.logoW + r.gap
	}
	if r.face != nil {
		drawScoreText(canvas, b.Score, r.face, float64(x-r.textLeft), centreY+float64(r.capH)/2)
	}
	return nil
}

// scoreRow is one badge's row at one scale: the logo's size, the gap after
// it, and the score's face, cap height and ink extent -- ink, not advance,
// so the visible glyphs are what is centred, not the font's side bearings.
type scoreRow struct {
	logoW, logoH int
	gap          int
	face         font.Face
	capH, textW  int
	textLeft     int // the ink's left edge relative to the pen position
}

func (r scoreRow) width() int { return r.logoW + r.gap + r.textW }

func (r scoreRow) close() {
	if r.face != nil {
		_ = r.face.Close()
	}
}

// layoutRow sizes b's row for a score cap height of capPx and a logo
// logoPx tall. The gap between them is a third of the cap height, the
// space Plex-style labels leave between an icon and its text.
func layoutRow(b Badge, lb image.Rectangle, capPx, logoPx float64) (scoreRow, error) {
	var r scoreRow
	if b.Score != "" {
		face, _, err := faceForCapHeight(math.Max(capPx, 1))
		if err != nil {
			return scoreRow{}, fmt.Errorf("score font: %w", err)
		}
		r.face = face
		r.capH = maxInt(int(math.Round(fixedToFloat(face.Metrics().CapHeight))), 1)
		ink, _ := font.BoundString(face, b.Score)
		r.textLeft = int(math.Floor(fixedToFloat(ink.Min.X)))
		r.textW = int(math.Ceil(fixedToFloat(ink.Max.X))) - r.textLeft
	}
	if !lb.Empty() && logoPx >= 1 {
		r.logoH = maxInt(int(math.Round(logoPx)), 1)
		r.logoW = scaleToHeight(r.logoH, lb)
	}
	if r.logoH > 0 && r.face != nil {
		r.gap = maxInt(int(math.Round(float64(r.capH)/3)), 1)
	}
	return r, nil
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

// scaleToHeight returns the width that preserves src's aspect ratio at
// height h, at least 1px.
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

// drawScoreText draws text in bold white with its left edge at x and its
// baseline at baselineY.
func drawScoreText(canvas *image.NRGBA, text string, face font.Face, x, baselineY float64) {
	drawer := &font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(color.White),
		Face: face,
		Dot:  fixed.Point26_6{X: floatToFixed(x), Y: floatToFixed(baselineY)},
	}
	drawer.DrawString(text)
}

// scoreTTF is Open Sans Bold (SIL OFL 1.1, fonts/OFL.txt), the face of
// Plex's episode count: rendered at the count's 56px height, its "665"
// overlaps the reference screenshot's glyphs at IoU 0.87, against 0.76 for
// Inter Bold and 0.78 for Open Sans SemiBold (2026-09-24). It replaced
// gobold, whose digits are narrower.
//
//go:embed fonts/OpenSans-Bold.ttf
var scoreTTF []byte

// boldFont is scoreTTF, parsed once; each call to faceForCapHeight then
// makes its own font.Face (a Face is not safe for concurrent use, but
// *opentype.Font's methods are, given each Face its own buffer -- see
// x/image/font/opentype).
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

func loadScoreFont() (*opentype.Font, error) {
	boldFont.once.Do(func() {
		boldFont.font, boldFont.err = opentype.Parse(scoreTTF)
	})
	return boldFont.font, boldFont.err
}

// capHeightRatio is the score font's CapHeight as a fraction of its point
// size at 72 DPI (where 1pt == 1px), probed once from a reference size.
// CapHeight scales linearly with point size, so faceForCapHeight inverts
// this ratio directly instead of iterating toward a target.
func capHeightRatio() (float64, error) {
	capRatio.once.Do(func() {
		f, err := loadScoreFont()
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

// faceForCapHeight returns a score font.Face whose CapHeight is
// capHeightPx, and the point size it resolved to (exported as a second
// return value for render_test.go's small-poster assertion, rather than
// duplicating this clamp in a test). Review Focus 4: the resolved size never
// drops below minFontSizePx, however small capHeightPx asks for.
func faceForCapHeight(capHeightPx float64) (font.Face, float64, error) {
	f, err := loadScoreFont()
	if err != nil {
		return nil, 0, err
	}
	ratio, err := capHeightRatio()
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
