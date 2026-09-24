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

package v1alpha1

// These accessors read the scalar fields that are pointers only so a Go
// client can send an explicit zero, with each field's +kubebuilder:default
// applied. The apiserver fills an absent field on write, but an object built
// in Go and never round-tripped through it -- a unit test's fixture, a spec a
// controller renders -- holds nil, which must still read as the default.
// Every consumer reads these fields through here, so the default is restated
// once, next to the marker it mirrors (api/subtitle/v1alpha1/defaults.go is
// the same convention). They are plain Go methods and generate nothing.

// DefaultRecycleBinCleanupDays mirrors RecycleBin.cleanupDays'
// +kubebuilder:default.
const DefaultRecycleBinCleanupDays int32 = 7

// CleanupDaysOrDefault is recycleBin.cleanupDays; unset means
// DefaultRecycleBinCleanupDays, and an explicit 0 disables the cleanup.
func (r RecycleBin) CleanupDaysOrDefault() int32 {
	if r.CleanupDays == nil {
		return DefaultRecycleBinCleanupDays
	}
	return *r.CleanupDays
}

// OverlayGeometry's fields are reached through an optional pointer
// (OverlayProfileSpec.Geometry), unlike RecycleBin and IndexerSpec above, so
// every accessor below has a pointer receiver and treats a nil *OverlayGeometry
// the same as one whose own field is nil: both mean "unset, use the default".

// Overlay geometry defaults, each mirroring the +kubebuilder marker its field
// would carry if a typed Go client could ever reach it (percentages of
// poster width unless noted; see the field doc comments in
// overlayprofile_types.go).
const (
	// DefaultOverlayWidthPercent mirrors OverlayGeometry.widthPercent's default.
	DefaultOverlayWidthPercent int32 = 19
	// DefaultOverlayRadiusPercent mirrors OverlayGeometry.radiusPercent's default.
	DefaultOverlayRadiusPercent int32 = 2
	// DefaultOverlayPaddingPercent mirrors OverlayGeometry.paddingPercent's default.
	DefaultOverlayPaddingPercent int32 = 2
	// DefaultOverlayLogoPercent mirrors OverlayGeometry.logoPercent's default
	// (percentage of the badge's box width).
	DefaultOverlayLogoPercent int32 = 60
	// DefaultOverlayScorePercent mirrors OverlayGeometry.scorePercent's default
	// (percentage of the badge's box height).
	DefaultOverlayScorePercent int32 = 27
	// DefaultOverlayOpacityPercent mirrors OverlayGeometry.opacityPercent's default.
	DefaultOverlayOpacityPercent int32 = 80
)

// WidthPercentOrDefault is geometry.widthPercent; a nil geometry or a nil
// field means DefaultOverlayWidthPercent.
func (g *OverlayGeometry) WidthPercentOrDefault() int32 {
	if g == nil || g.WidthPercent == nil {
		return DefaultOverlayWidthPercent
	}
	return *g.WidthPercent
}

// RadiusPercentOrDefault is geometry.radiusPercent; a nil geometry or a nil
// field means DefaultOverlayRadiusPercent.
func (g *OverlayGeometry) RadiusPercentOrDefault() int32 {
	if g == nil || g.RadiusPercent == nil {
		return DefaultOverlayRadiusPercent
	}
	return *g.RadiusPercent
}

// PaddingPercentOrDefault is geometry.paddingPercent; a nil geometry or a nil
// field means DefaultOverlayPaddingPercent.
func (g *OverlayGeometry) PaddingPercentOrDefault() int32 {
	if g == nil || g.PaddingPercent == nil {
		return DefaultOverlayPaddingPercent
	}
	return *g.PaddingPercent
}

// LogoPercentOrDefault is geometry.logoPercent; a nil geometry or a nil field
// means DefaultOverlayLogoPercent.
func (g *OverlayGeometry) LogoPercentOrDefault() int32 {
	if g == nil || g.LogoPercent == nil {
		return DefaultOverlayLogoPercent
	}
	return *g.LogoPercent
}

// ScorePercentOrDefault is geometry.scorePercent; a nil geometry or a nil
// field means DefaultOverlayScorePercent.
func (g *OverlayGeometry) ScorePercentOrDefault() int32 {
	if g == nil || g.ScorePercent == nil {
		return DefaultOverlayScorePercent
	}
	return *g.ScorePercent
}

// OpacityPercentOrDefault is geometry.opacityPercent; a nil geometry or a nil
// field means DefaultOverlayOpacityPercent.
func (g *OverlayGeometry) OpacityPercentOrDefault() int32 {
	if g == nil || g.OpacityPercent == nil {
		return DefaultOverlayOpacityPercent
	}
	return *g.OpacityPercent
}
