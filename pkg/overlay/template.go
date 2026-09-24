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

package overlay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Corner is where a badge stack is anchored on the poster. It mirrors
// catalogv1alpha1.OverlayCorner's four values (TemplateSpec converts
// between them) so this package's exported surface -- Template, Badge,
// FormatScore -- stays plain Go, importing api/catalog/v1alpha1 only inside
// TemplateSpec.
type Corner string

// The four corners a badge stack can anchor to, matching
// catalogv1alpha1.OverlayCorner's values exactly.
const (
	CornerBottomRight Corner = "bottomRight"
	CornerBottomLeft  Corner = "bottomLeft"
	CornerTopRight    Corner = "topRight"
	CornerTopLeft     Corner = "topLeft"
)

// Named geometry defaults. These mirror api/catalog/v1alpha1/defaults.go's
// DefaultOverlay*Percent constants (14, 2, 2, 60, 45, 90) as plain,
// unexported ints -- Template's fields are int, not int32, per the task
// brief's interface -- so TestDefaultTemplateMatchesNamedConstants can
// assert DefaultTemplate() against them by name: a drift between the two
// defaults.go files is then a named test failure, not a silent divergence.
const (
	defaultWidthPct   = 14
	defaultRadiusPct  = 2
	defaultPaddingPct = 2
	defaultLogoPct    = 60
	defaultScorePct   = 45
	defaultOpacityPct = 90
)

// Template is the fully resolved geometry and corner for one poster's badge
// stack. Every size field is a percentage, so the same Template renders
// proportionally identical badges on a 1000x1500 poster and a 500x750
// thumbnail (spec §C.5).
type Template struct {
	Corner Corner
	// WidthPct is the badge box's width, as a percentage of poster width.
	WidthPct int
	// RadiusPct is each badge's corner radius, as a percentage of poster
	// width.
	RadiusPct int
	// PaddingPct is, per api/catalog/v1alpha1/overlayprofile_types.go's
	// OverlayGeometry.PaddingPercent doc comment, "the padding inside each
	// badge" -- and per spec §C.5's prose, also the gap between stacked
	// badges. This package reuses one poster-width-relative pixel value for
	// both: layoutBoxes' inter-badge gap and drawBadge's internal margin.
	PaddingPct int
	// LogoPct is the logo's width, as a percentage of the badge's content
	// width (the box width less its internal margins).
	LogoPct int
	// ScorePct is the score text's cap height, as a percentage of the box
	// height remaining below the logo (spec §C.5: "the remaining box
	// height").
	ScorePct int
	// OpacityPct is the badge box background's opacity, 0-100.
	OpacityPct int
}

// DefaultTemplate is the badge geometry a profile with no Geometry override
// renders: bottom-right corner, mirroring Plex's episode-count box (spec
// §C.5, from the user's reference images), and the six named percentage
// defaults above.
func DefaultTemplate() Template {
	return Template{
		Corner:     CornerBottomRight,
		WidthPct:   defaultWidthPct,
		RadiusPct:  defaultRadiusPct,
		PaddingPct: defaultPaddingPct,
		LogoPct:    defaultLogoPct,
		ScorePct:   defaultScorePct,
		OpacityPct: defaultOpacityPct,
	}
}

// TemplateSpec resolves an OverlayProfileSpec's corner and geometry into a
// Template, through the *OrDefault accessors
// (api/catalog/v1alpha1/defaults.go) so a nil Geometry, or a Geometry whose
// own fields are nil, reads the same default a real apiserver would have
// filled in -- the typed-client defaulting trap CLAUDE.md warns about,
// which is exactly why those accessors exist.
//
// Badges are not part of a Template: the caller (the artwork-render role,
// spec §C.6) builds []Badge separately, from the profile's Badges, the
// item's ratings and Logo.
func TemplateSpec(spec catalogv1alpha1.OverlayProfileSpec) Template {
	return Template{
		Corner:     cornerFromSpec(spec.Corner),
		WidthPct:   int(spec.Geometry.WidthPercentOrDefault()),
		RadiusPct:  int(spec.Geometry.RadiusPercentOrDefault()),
		PaddingPct: int(spec.Geometry.PaddingPercentOrDefault()),
		LogoPct:    int(spec.Geometry.LogoPercentOrDefault()),
		ScorePct:   int(spec.Geometry.ScorePercentOrDefault()),
		OpacityPct: int(spec.Geometry.OpacityPercentOrDefault()),
	}
}

// cornerFromSpec maps OverlayProfileSpec.Corner to a Corner. Corner carries
// +kubebuilder:default=bottomRight, which only reaches a value the
// apiserver has defaulted: a Go-built spec's zero value is "", not
// "bottomRight" (the same typed-client defaulting trap the pointer geometry
// fields dodge with an *OrDefault accessor; Corner is a plain scalar with
// no such accessor, so TemplateSpec applies the default itself).
func cornerFromSpec(c catalogv1alpha1.OverlayCorner) Corner {
	switch c {
	case catalogv1alpha1.OverlayCornerBottomLeft:
		return CornerBottomLeft
	case catalogv1alpha1.OverlayCornerTopRight:
		return CornerTopRight
	case catalogv1alpha1.OverlayCornerTopLeft:
		return CornerTopLeft
	default: // "" (unset, Go zero value) and OverlayCornerBottomRight
		return CornerBottomRight
	}
}

// TemplateHash is the hex SHA-256 of t's canonical form -- what
// OverlayProfileStatus.Hash (overlayprofile_types.go's doc comment: "the
// sha256 of the render-relevant spec (overlay.TemplateSpec)") stores, so a
// change to any geometry percentage or the corner changes the hash and a
// controller comparing hashes can tell a profile's render-relevant fields
// moved without re-deriving what moved.
func TemplateHash(t Template) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"corner=%s;width=%d;radius=%d;padding=%d;logo=%d;score=%d;opacity=%d",
		t.Corner, t.WidthPct, t.RadiusPct, t.PaddingPct, t.LogoPct, t.ScorePct, t.OpacityPct,
	)))
	return hex.EncodeToString(sum[:])
}
