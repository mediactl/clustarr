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

// This file is `package overlay`, not `overlay_test` -- a deliberate
// exception to the rest of this repo's convention of an `_internal_test.go`
// suffix for white-box tests (pkg/transcode/x265params_internal_test.go).
// The task brief names this file render_test.go exactly and requires it to
// assert DefaultTemplate() against template.go's named, unexported
// defaultWidthPct et al. constants directly (so a drift between them is a
// named test failure), which only a same-package test can reach.
package overlay

import (
	"bytes"
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

var updateGolden = flag.Bool("update", false, "update golden PNG fixtures under test/data/overlay")

// --- DefaultTemplate / TemplateSpec / TemplateHash -------------------------

func TestDefaultTemplateMatchesNamedConstants(t *testing.T) {
	// Pin the literal values the task brief specifies, independently of
	// DefaultTemplate's own use of them -- a change to the constants
	// themselves is then a failure here, not just a silent flow-through.
	require.Equal(t, 14, defaultWidthPct)
	require.Equal(t, 2, defaultRadiusPct)
	require.Equal(t, 2, defaultPaddingPct)
	require.Equal(t, 60, defaultLogoPct)
	require.Equal(t, 45, defaultScorePct)
	require.Equal(t, 90, defaultOpacityPct)

	want := Template{
		Corner:     CornerBottomRight,
		WidthPct:   defaultWidthPct,
		RadiusPct:  defaultRadiusPct,
		PaddingPct: defaultPaddingPct,
		LogoPct:    defaultLogoPct,
		ScorePct:   defaultScorePct,
		OpacityPct: defaultOpacityPct,
	}
	require.Equal(t, want, DefaultTemplate())
}

func TestTemplateSpecOfAZeroValueSpecMatchesDefaultTemplate(t *testing.T) {
	// A Go-built OverlayProfileSpec{} -- no Corner, no Geometry -- is
	// exactly what a client sends before any apiserver round trip fills in
	// +kubebuilder:default=bottomRight. TemplateSpec must read it the same
	// way DefaultTemplate() does (the typed-client defaulting trap,
	// CLAUDE.md), not as a "" corner or a nil-pointer-panic geometry.
	got := TemplateSpec(catalogv1alpha1.OverlayProfileSpec{})
	require.Equal(t, DefaultTemplate(), got)
}

func TestTemplateSpecReadsExplicitCornerAndGeometry(t *testing.T) {
	width := int32(20)
	radius := int32(4)
	spec := catalogv1alpha1.OverlayProfileSpec{
		Corner: catalogv1alpha1.OverlayCornerTopLeft,
		Geometry: &catalogv1alpha1.OverlayGeometry{
			WidthPercent:  &width,
			RadiusPercent: &radius,
		},
	}
	got := TemplateSpec(spec)
	require.Equal(t, CornerTopLeft, got.Corner)
	require.Equal(t, 20, got.WidthPct)
	require.Equal(t, 4, got.RadiusPct)
	// Fields the spec's Geometry left nil still fall back to the named
	// defaults, per-field (OverlayGeometry's *OrDefault accessors).
	require.Equal(t, defaultPaddingPct, got.PaddingPct)
	require.Equal(t, defaultLogoPct, got.LogoPct)
	require.Equal(t, defaultScorePct, got.ScorePct)
	require.Equal(t, defaultOpacityPct, got.OpacityPct)
}

func TestTemplateSpecCornerMapping(t *testing.T) {
	cases := []struct {
		in   catalogv1alpha1.OverlayCorner
		want Corner
	}{
		{catalogv1alpha1.OverlayCornerBottomRight, CornerBottomRight},
		{catalogv1alpha1.OverlayCornerBottomLeft, CornerBottomLeft},
		{catalogv1alpha1.OverlayCornerTopRight, CornerTopRight},
		{catalogv1alpha1.OverlayCornerTopLeft, CornerTopLeft},
		{"", CornerBottomRight}, // the Go zero value, defaulted by hand
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			got := TemplateSpec(catalogv1alpha1.OverlayProfileSpec{Corner: tc.in})
			require.Equal(t, tc.want, got.Corner)
		})
	}
}

func TestTemplateHashChangesWithEveryField(t *testing.T) {
	base := DefaultTemplate()
	baseHash := TemplateHash(base)
	require.Equal(t, baseHash, TemplateHash(base), "the same Template must hash the same every time")

	mutate := func(f func(*Template)) Template {
		v := base
		f(&v)
		return v
	}
	variants := []Template{
		mutate(func(v *Template) { v.Corner = CornerTopLeft }),
		mutate(func(v *Template) { v.WidthPct++ }),
		mutate(func(v *Template) { v.RadiusPct++ }),
		mutate(func(v *Template) { v.PaddingPct++ }),
		mutate(func(v *Template) { v.LogoPct++ }),
		mutate(func(v *Template) { v.ScorePct++ }),
		mutate(func(v *Template) { v.OpacityPct++ }),
	}
	seen := map[string]bool{baseHash: true}
	for i, v := range variants {
		h := TemplateHash(v)
		require.Falsef(t, seen[h], "variant %d (%+v) collided with an earlier Template's hash", i, v)
		seen[h] = true
	}
}

// --- Small-poster floor (Review Focus 4) ------------------------------------

func TestLayoutBoxesClampsToMinBoxPxOnASmallPoster(t *testing.T) {
	bounds := image.Rect(0, 0, 60, 90)
	boxes := layoutBoxes(bounds, 4, DefaultTemplate())
	require.Len(t, boxes, 4)
	for i, b := range boxes {
		require.GreaterOrEqualf(t, b.Dx(), minBoxPx, "badge %d width", i)
		require.GreaterOrEqualf(t, b.Dy(), minBoxPx, "badge %d height", i)
	}
}

func TestFaceForCapHeightNeverGoesBelowMinFontSizePx(t *testing.T) {
	_, size, err := faceForCapHeight(0.001)
	require.NoError(t, err)
	require.GreaterOrEqual(t, size, float64(minFontSizePx))
}

func TestRenderOnASmallPosterDoesNotPanicAndKeepsPosterBounds(t *testing.T) {
	base := syntheticPoster(60, 90)
	badges := []Badge{
		testBadge(t, SourceMetacritic, 7600),
		testBadge(t, SourceIMDb, 810),
		testBadge(t, SourceRTCritic, 9400),
		testBadge(t, SourceTMDB, 730),
	}

	var got *image.NRGBA
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Render panicked on a 60x90 poster with 4 badges: %v", r)
			}
		}()
		got, err = Render(base, badges, DefaultTemplate())
	}()
	require.NoError(t, err)

	// Render must not resize the poster, and by construction (every draw
	// call above goes through image/draw, which clips to the destination's
	// own Bounds()) every pixel it touched is inside got -- there is no
	// "outside the image" coordinate for a badge pixel to land on. What
	// this test actually guards is the precondition: a stack of 4 boxes at
	// the 24px floor plus gaps (96+px) taller than the 90px poster, which
	// without the recover() above could have driven a coordinate negative
	// enough to misbehave.
	require.Equal(t, base.Bounds(), got.Bounds())
}

// --- Badge edge cases --------------------------------------------------------

func TestRenderBadgeWithNoLogoOrScoreDrawsOnlyTheBox(t *testing.T) {
	base := syntheticPoster(400, 600)
	badges := []Badge{{Source: SourceIMDb}} // no Score, no Logo
	got, err := Render(base, badges, DefaultTemplate())
	require.NoError(t, err)
	require.Equal(t, base.Bounds(), got.Bounds())

	// The badge box itself should still have been drawn: its near-opaque
	// dark fill must appear somewhere in the bottom-right corner region.
	box := layoutBoxes(base.Bounds(), 1, DefaultTemplate())[0]
	center := got.NRGBAAt(box.Min.X+box.Dx()/2, box.Min.Y+box.Dy()/2)
	require.Lessf(t, int(center.R), 0x60, "expected the badge fill's dark grey at the box center, got %+v", center)
}

// --- Golden renders ----------------------------------------------------------

func TestRenderGoldenOneMetacriticBadge(t *testing.T) {
	base := syntheticPoster(400, 600)
	badges := []Badge{testBadge(t, SourceMetacritic, 7600)}
	got, err := Render(base, badges, DefaultTemplate())
	require.NoError(t, err)
	assertGolden(t, "one_metacritic_badge", got)
}

func TestRenderGoldenFourBadgesStacked(t *testing.T) {
	base := syntheticPoster(400, 600)
	badges := []Badge{
		testBadge(t, SourceMetacritic, 7600),
		testBadge(t, SourceIMDb, 810),
		testBadge(t, SourceRTCritic, 9400),
		testBadge(t, SourceTMDB, 730),
	}
	got, err := Render(base, badges, DefaultTemplate())
	require.NoError(t, err)
	assertGolden(t, "four_badges", got)
}

func TestRenderGoldenEachCorner(t *testing.T) {
	corners := []struct {
		name   string
		corner Corner
	}{
		{"bottom_right", CornerBottomRight},
		{"bottom_left", CornerBottomLeft},
		{"top_right", CornerTopRight},
		{"top_left", CornerTopLeft},
	}
	for _, tc := range corners {
		t.Run(tc.name, func(t *testing.T) {
			base := syntheticPoster(400, 600)
			tmpl := DefaultTemplate()
			tmpl.Corner = tc.corner
			badges := []Badge{testBadge(t, SourceTrakt, 880)}
			got, err := Render(base, badges, tmpl)
			require.NoError(t, err)
			assertGolden(t, "corner_"+tc.name, got)
		})
	}
}

func TestRenderGoldenAlphaBase(t *testing.T) {
	base := syntheticPosterWithAlpha(400, 600)
	badges := []Badge{testBadge(t, SourceLetterboxd, 420)}
	got, err := Render(base, badges, DefaultTemplate())
	require.NoError(t, err)
	assertGolden(t, "alpha_base", got)
}

// --- Test fixtures and helpers ----------------------------------------------

// syntheticPoster is a deterministic 2D gradient: red rises left to right,
// green rises top to bottom, blue is constant -- generated in code (no
// stored input fixture) so the golden PNGs under test/data/overlay/ are the
// only binary test data this package carries beyond the embedded logos.
func syntheticPoster(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 255 / maxInt(w-1, 1)),
				G: uint8(y * 255 / maxInt(h-1, 1)),
				B: 128,
				A: 255,
			})
		}
	}
	return img
}

// syntheticPosterWithAlpha is the same gradient but with alpha rising left
// to right (transparent to opaque), PNG-encoded and decoded back -- so the
// alpha-base golden exercises Render against a genuine decoded image.Image
// with a real alpha channel, as spec §C.6 step 4's "Get the original
// poster, decode, Render" does, not a synthetic *image.NRGBA built in
// memory and never round-tripped through the PNG codec.
func syntheticPosterWithAlpha(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(y * 255 / maxInt(h-1, 1)),
				G: 180,
				B: uint8(255 - x*255/maxInt(w-1, 1)),
				A: uint8(x * 255 / maxInt(w-1, 1)),
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("syntheticPosterWithAlpha: encode: " + err.Error()) // test-only fixture; a synthetic image always encodes
	}
	decoded, err := png.Decode(&buf)
	if err != nil {
		panic("syntheticPosterWithAlpha: decode: " + err.Error())
	}
	return decoded
}

// testBadge builds a Badge for source, through the real FormatScore and
// Logo this package exports rather than a hand-built fixture -- the
// falsification discipline CLAUDE.md names ("a test built from a fixture
// shaped like the answer cannot fail"): if FormatScore or Logo regresses,
// every golden test using this helper regresses with it instead of quietly
// keeping its own separately-computed "correct" values.
func testBadge(t *testing.T, source string, centis int32) Badge {
	t.Helper()
	score, ok := FormatScore(source, centis)
	require.True(t, ok, "FormatScore(%s, %d) reported no score", source, centis)
	logo, ok := Logo(source)
	require.True(t, ok, "no embedded logo for source %s", source)
	return Badge{Source: source, Score: score, Logo: logo}
}

// goldenTolerance is applied per RGBA channel, out of 255. See assertGolden.
const goldenTolerance = 24

// assertGolden compares got against the PNG fixture at
// test/data/overlay/<name>.png, or writes got there when -update is passed
// (go test ./pkg/overlay/... -run <TestName> -update; review the PNG by
// hand before committing it -- the task report records the exact commands
// used to generate this package's own goldens).
//
// The comparison allows a small per-channel tolerance rather than requiring
// a bit-identical match. This package's own box geometry (roundedRectMask,
// cornerCoverage) is pure math.Sqrt, which IEEE 754 requires to be
// correctly rounded and so is deterministic across architectures. Glyph
// rasterization is not: golang.org/x/image/font/opentype rides
// golang.org/x/image/vector, which ships amd64 assembly acceleration
// (vector/acc_amd64.s) alongside a portable Go fallback for every other
// architecture, so a glyph's anti-aliased edge pixels can legitimately
// differ by a few levels between an amd64 runner and an arm64 one, even
// though both outlines are "correct". A tolerance this generous (up to
// 24/255 per channel) absorbs that without masking an actual layout,
// scale or color regression, which moves whole regions of pixels by far
// more than a handful of edge levels.
func assertGolden(t *testing.T, name string, got *image.NRGBA) {
	t.Helper()
	path := filepath.Join("..", "..", "test", "data", "overlay", name+".png")

	if *updateGolden {
		f, err := os.Create(path)
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		require.NoError(t, png.Encode(f, got))
		return
	}

	f, err := os.Open(path)
	require.NoErrorf(t, err, "missing golden %s -- run `go test ./pkg/overlay/... -run %s -update` once, then hand-review the PNG before committing it", path, t.Name())
	defer func() { _ = f.Close() }()
	wantImg, err := png.Decode(f)
	require.NoError(t, err)
	want := toNRGBA(wantImg)

	require.Equal(t, want.Bounds(), got.Bounds(), "golden %s: size mismatch", name)

	var mismatches, maxDiff int
	b := got.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			gc := got.NRGBAAt(x, y)
			wc := want.NRGBAAt(x, y)
			d := channelDiff(gc, wc)
			if d > maxDiff {
				maxDiff = d
			}
			if d > goldenTolerance {
				mismatches++
				if mismatches <= 5 {
					t.Logf("golden %s: pixel (%d,%d) got=%+v want=%+v maxChannelDiff=%d", name, x, y, gc, wc, d)
				}
			}
		}
	}
	require.Zerof(t, mismatches, "golden %s: %d of %d pixels exceeded tolerance %d (largest diff seen: %d)",
		name, mismatches, b.Dx()*b.Dy(), goldenTolerance, maxDiff)
}

func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok {
		return n
	}
	b := img.Bounds()
	out := image.NewNRGBA(b)
	draw.Draw(out, b, img, b.Min, draw.Src)
	return out
}

func channelDiff(a, b color.NRGBA) int {
	d := absInt(int(a.R) - int(b.R))
	d = maxInt(d, absInt(int(a.G)-int(b.G)))
	d = maxInt(d, absInt(int(a.B)-int(b.B)))
	d = maxInt(d, absInt(int(a.A)-int(b.A)))
	return d
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
