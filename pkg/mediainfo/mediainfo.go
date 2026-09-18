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

import (
	"context"
	"fmt"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Raw is the unabridged ffprobe result for one file: every stream, the
// container format and chapters, plus the HDR/Dolby-Vision and colour
// detail the CRD-facing MediaInfo cannot hold. pkg/transcode.Planner.Plan
// takes Raw as its second argument; catalogarr's MediaFile status only
// ever sees the mapped MediaInfo Probe returns alongside it.
type Raw struct {
	Format   *ffprobe.Format
	Streams  []*ffprobe.Stream
	Chapters []*ffprobe.Chapter

	// ColorPrimaries, ColorTransfer, ColorSpace and ColorRange are read
	// from the first decoded frame (the second ffprobe call, mergeFrame),
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

// buildRaw copies pd's streams/format/chapters onto Raw and extracts the
// primary video stream's Dolby Vision record. mergeFrame adds the
// frame-level merge on top.
func buildRaw(pd *ffprobe.ProbeData) *Raw {
	raw := &Raw{Format: pd.Format, Streams: pd.Streams, Chapters: pd.Chapters}
	if v := pd.FirstVideoStream(); v != nil {
		raw.Dovi = parseDoviRecord(v.SideDataList)
	}
	return raw
}

// Probe runs ffprobe twice against path -- once for the container,
// streams and chapters, once for the first decoded frame's colour tags
// and HDR side data (docs/research/transcode.md §2.1) -- and returns
// both the api/common/v1alpha1 MediaInfo the MediaFile status carries
// and the Raw detail pkg/transcode needs.
func Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *Raw, error) {
	ctx, span := tracing.Start(ctx, "mediainfo.Probe")
	defer span.End()

	pd, err := ffprobe.ProbeURL(ctx, path)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, fmt.Errorf("mediainfo: probe %s: %w", path, err)
	}
	raw := buildRaw(pd)

	if pd.FirstVideoStream() != nil {
		frames, ferr := runFrameProbe(ctx, path)
		if ferr != nil {
			// Best-effort: HDR/colour detail degrades rather than failing
			// the whole probe over a second call some inputs can't satisfy.
			logging.FromContext(ctx).WarnContext(ctx,
				"mediainfo: frame probe failed, HDR detail may be incomplete",
				"path", path, "error", ferr)
		} else {
			mergeFrame(raw, frames)
		}
	}

	return toMediaInfo(raw), raw, nil
}

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
