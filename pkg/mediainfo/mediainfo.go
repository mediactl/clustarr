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

// Package mediainfo wraps ffprobe to produce the api/common/v1alpha1
// MediaInfo the MediaFile status carries, plus the richer Raw detail (the
// full ffprobe result, the Dolby Vision configuration record, and typed
// SMPTE ST 2086 mastering-display / content-light metadata) that
// api/common/v1alpha1.MediaInfo has no room for and pkg/transcode's
// Planner needs. See docs/superpowers/specs/2026-09-18-clustarr-design.md
// §7 and docs/research/transcode.md §2.
package mediainfo

import "fmt"

// MasteringDisplay is SMPTE ST 2086 mastering-display metadata.
// Chromaticities are numerators over a fixed denominator of 50000 (CIE
// 1931 xy); luminances are numerators over 10000 (0.0001 cd/m²). These
// are exactly the integers libx265's master-display string carries, so no
// float ever appears here -- the ffprobe FlexFloat side-data values are
// converted to these integers once, at parse time (toMasteringDisplay in
// ffprobe.go: x * 50000 for chromaticities, x * 10000 for luminance,
// rounded).
type MasteringDisplay struct {
	GreenX, GreenY, BlueX, BlueY, RedX, RedY, WhiteX, WhiteY int32
	MaxLuminance, MinLuminance                               int32
}

// X265 renders m exactly as libavcodec/libx265.c's handle_mdcv does
// (docs/research/transcode.md §2.2), for libx265's
// -x265-params master-display=... option.
func (m MasteringDisplay) X265() string {
	return fmt.Sprintf("G(%d,%d)B(%d,%d)R(%d,%d)WP(%d,%d)L(%d,%d)",
		m.GreenX, m.GreenY, m.BlueX, m.BlueY, m.RedX, m.RedY, m.WhiteX, m.WhiteY,
		m.MaxLuminance, m.MinLuminance)
}

// ContentLight is MaxCLL/MaxFALL content light level metadata.
type ContentLight struct {
	MaxCLL, MaxFALL int32
}

// X265 renders c for libx265's -x265-params max-cll=... option.
func (c ContentLight) X265() string {
	return fmt.Sprintf("%d,%d", c.MaxCLL, c.MaxFALL)
}
