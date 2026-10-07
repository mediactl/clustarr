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

// Package mediainfo is the model of a probe: the api/common/v1alpha1
// MediaInfo the MediaFile status carries, Raw (ffprobe's result as
// go-ffprobe types, OD12: the full stream, format and chapter set, the Dolby
// Vision configuration record, and typed SMPTE ST 2086 mastering-display and
// content-light metadata that pkg/transcode's planner needs), the HDR rules
// and the hashes. It runs no program and links no FFmpeg;
// pkg/mediainfo/native probes. See
// docs/superpowers/specs/2026-09-18-clustarr-design.md §7 and
// docs/research/transcode.md §2.
package mediainfo

import (
	"fmt"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// Raw is the unabridged ffprobe result for one file: every stream, the
// container format and chapters, plus the HDR/Dolby-Vision and colour
// detail the CRD-facing MediaInfo cannot hold.
// pkg/transcode.FromProbe(mi *commonv1.MediaInfo, raw *mediainfo.Raw)
// takes Raw as its second argument and folds both into the
// transcode.MediaInfo that transcode.Plan reads (spec §7's
// Planner.Plan(mi, raw, hw) is satisfied by that pair -- there is no
// Planner type); catalogarr's MediaFile status only ever sees the mapped
// MediaInfo a probe (pkg/mediainfo/native's Prober.Probe) returns alongside it.
type Raw struct {
	Format   *ffprobe.Format
	Streams  []*ffprobe.Stream
	Chapters []*ffprobe.Chapter

	// ColorPrimaries, ColorTransfer, ColorSpace and ColorRange are read
	// from the first decoded frame of v:0 (ffprobe's frame probe),
	// more reliable than the stream-level tags for some encoders --
	// docs/research/transcode.md §2.1.
	ColorPrimaries string
	ColorTransfer  string
	ColorSpace     string
	ColorRange     string

	// Dovi is the primary video stream's "DOVI configuration record" side
	// data, nil when the source carries no Dolby Vision RPU.
	Dovi *DoviRecord

	// HasHDR10Plus is true when the first frame carries "HDR Dynamic
	// Metadata SMPTE2094-40 (HDR10+)" side data.
	HasHDR10Plus bool

	// MasteringDisplay is the first frame's SMPTE ST 2086 mastering-display
	// side data, nil when the source carries none.
	MasteringDisplay *MasteringDisplay

	// ContentLight is the first frame's MaxCLL/MaxFALL side data, nil when
	// the source carries none.
	ContentLight *ContentLight

	// FrameErr is why the first video frame could not be read, nil when it
	// was (or there is no video). The colour tags and HDR side data above
	// are then unknown, not absent; IncompleteHDR decides whether that
	// leaves the HDR format unknown too.
	FrameErr error
}

// DoviRecord is the "DOVI configuration record" stream side data
// (libavutil/side_data.c field names; docs/research/transcode.md §2.2).
type DoviRecord struct {
	VersionMajor, VersionMinor       int32
	Profile, Level                   int32
	RPUPresent, ELPresent, BLPresent bool
	BLSignalCompatibilityID          int32
	MDCompression                    string
}

// MasteringDisplay is SMPTE ST 2086 mastering-display metadata.
// Chromaticities are numerators over a fixed denominator of 50000 (CIE
// 1931 xy); luminances are numerators over 10000 (0.0001 cd/m²). These
// are exactly the integers libx265's master-display string carries, so no
// float ever appears here -- the first frame's AVRational side data is
// converted to these integers once, when the probe reads it
// (pkg/mediainfo/native's masteringDisplay: x * 50000 for chromaticities,
// x * 10000 for luminance, rounded).
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
