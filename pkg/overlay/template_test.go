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
// It asserts DefaultTemplate() against template.go's named, unexported
// defaultWidthPct et al. constants directly (so a drift between them is a
// named test failure), which only a same-package test can reach. It was cut
// from render_test.go when the rasteriser moved to pkg/overlay/render.
package overlay

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// --- DefaultTemplate / TemplateSpec / TemplateHash -------------------------

func TestDefaultTemplateMatchesNamedConstants(t *testing.T) {
	// Pin the literal values the task brief specifies, independently of
	// DefaultTemplate's own use of them -- a change to the constants
	// themselves is then a failure here, not just a silent flow-through.
	require.Equal(t, 19, defaultWidthPct)
	require.Equal(t, 2, defaultRadiusPct)
	require.Equal(t, 2, defaultPaddingPct)
	require.Equal(t, 60, defaultLogoPct)
	require.Equal(t, 27, defaultScorePct)
	require.Equal(t, 80, defaultOpacityPct)

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

// --- CompactTemplateSpec ----------------------------------------------------

func TestCompactTemplateSpec(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	spec := catalogv1alpha1.OverlayProfileSpec{
		Corner:   catalogv1alpha1.OverlayCornerTopLeft,
		Geometry: &catalogv1alpha1.OverlayGeometry{OpacityPercent: i32(60), WidthPercent: i32(22)},
	}
	got := CompactTemplateSpec(spec)
	// Width and score take the compact defaults, not the regular 22 and 27.
	require.Equal(t, int(catalogv1alpha1.DefaultOverlayCompactWidthPercent), got.WidthPct)
	require.Equal(t, int(catalogv1alpha1.DefaultOverlayCompactScorePercent), got.ScorePct)
	require.Greater(t, got.WidthPct, 22)
	// Everything else is the profile's own.
	require.Equal(t, 60, got.OpacityPct)
	require.Equal(t, CornerTopLeft, got.Corner)
	regular := TemplateSpec(spec)
	require.Equal(t, regular.RadiusPct, got.RadiusPct)
	require.Equal(t, regular.PaddingPct, got.PaddingPct)
	require.Equal(t, regular.LogoPct, got.LogoPct)

	// compactGeometry wins, field by field.
	spec.CompactGeometry = &catalogv1alpha1.OverlayGeometry{WidthPercent: i32(33), RadiusPercent: i32(4)}
	got = CompactTemplateSpec(spec)
	require.Equal(t, 33, got.WidthPct)
	require.Equal(t, 4, got.RadiusPct)
	require.Equal(t, int(catalogv1alpha1.DefaultOverlayCompactScorePercent), got.ScorePct)
	require.Equal(t, 60, got.OpacityPct)

	// A profile whose regular badge is already wider than the compact
	// default keeps that width: the compact rendition is never the smaller.
	wide := catalogv1alpha1.OverlayProfileSpec{Geometry: &catalogv1alpha1.OverlayGeometry{WidthPercent: i32(40), ScorePercent: i32(45)}}
	got = CompactTemplateSpec(wide)
	require.Equal(t, 40, got.WidthPct)
	require.Equal(t, 45, got.ScorePct)

	// No geometry at all: the compact defaults over the regular ones.
	got = CompactTemplateSpec(catalogv1alpha1.OverlayProfileSpec{})
	want := DefaultTemplate()
	want.WidthPct = int(catalogv1alpha1.DefaultOverlayCompactWidthPercent)
	want.ScorePct = int(catalogv1alpha1.DefaultOverlayCompactScorePercent)
	require.Equal(t, want, got)
}
