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

package mediainfo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// parseDoviRecord extracts the "DOVI configuration record" side data
// from a video stream's side data list. go-ffprobe.v2 v2.3.1 has no
// typed struct for this side_data_type, so it decodes as
// *ffprobe.SideDataUnknown (a map[string]interface{} in disguise);
// Tags(*unknown) recovers the GetInt/GetString accessors.
func parseDoviRecord(list ffprobe.SideDataList) *DoviRecord {
	data, err := list.FindSideData("DOVI configuration record")
	if err != nil {
		return nil
	}
	unknown, ok := data.(*ffprobe.SideDataUnknown)
	if !ok {
		return nil
	}
	tags := ffprobe.Tags(*unknown)
	geti := func(k string) int32 {
		v, _ := tags.GetInt(k)
		return int32(v)
	}
	compression, _ := tags.GetString("dv_md_compression")
	return &DoviRecord{
		VersionMajor:            geti("dv_version_major"),
		VersionMinor:            geti("dv_version_minor"),
		Profile:                 geti("dv_profile"),
		Level:                   geti("dv_level"),
		RPUPresent:              geti("rpu_present_flag") != 0,
		ELPresent:               geti("el_present_flag") != 0,
		BLPresent:               geti("bl_present_flag") != 0,
		BLSignalCompatibilityID: geti("dv_bl_signal_compatibility_id"),
		MDCompression:           compression,
	}
}

// frameProbeData is the shape of the second ffprobe call's JSON.
// go-ffprobe.v2's ProbeData has no field for ffprobe's "frames" array, so
// this call is issued directly with os/exec (see runFrameProbe) and
// decoded into this package-local type.
type frameProbeData struct {
	Frames []frameEntry `json:"frames"`
}

type frameEntry struct {
	ColorPrimaries string               `json:"color_primaries"`
	ColorTransfer  string               `json:"color_transfer"`
	ColorSpace     string               `json:"color_space"`
	ColorRange     string               `json:"color_range"`
	SideDataList   ffprobe.SideDataList `json:"side_data_list"`
}

// hdr10PlusSideDataType is the frame-level side_data_type ffprobe prints
// for SMPTE 2094-40 dynamic metadata (docs/research/transcode.md §2.2's
// side-data table).
const hdr10PlusSideDataType = "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"

// mergeFrame layers the second ffprobe call's first-frame colour tags and
// HDR side data onto raw. docs/research/transcode.md §2.1: "HDR static
// metadata + real colour tags come from the first decoded frame."
func mergeFrame(raw *Raw, frames frameProbeData) {
	if len(frames.Frames) == 0 {
		return
	}
	f := frames.Frames[0]
	raw.ColorPrimaries = f.ColorPrimaries
	raw.ColorTransfer = f.ColorTransfer
	raw.ColorSpace = f.ColorSpace
	raw.ColorRange = f.ColorRange

	for _, sd := range f.SideDataList {
		switch sd.Type {
		case ffprobe.SideDataTypeMasteringDisplayMetadata:
			if m, ok := sd.Data.(*ffprobe.SideDataMasteringDisplayMetadata); ok {
				raw.MasteringDisplay = toMasteringDisplay(m)
			}
		case ffprobe.SideDataTypeContentLightLevel:
			if c, ok := sd.Data.(*ffprobe.SideDataContentLightLevel); ok {
				raw.ContentLight = toContentLight(c)
			}
		case hdr10PlusSideDataType:
			raw.HasHDR10Plus = true
		}
	}
}

// toMasteringDisplay converts ffprobe's FlexFloat SMPTE ST 2086 side data
// into MasteringDisplay's fixed-denominator integers -- chromaticities as
// numerators over 50000, luminances as numerators over 10000, exactly
// what libavcodec/libx265.c's handle_mdcv prints (docs/research/
// transcode.md §2.2). This is the one place that FlexFloat -> int32
// rounding happens.
func toMasteringDisplay(m *ffprobe.SideDataMasteringDisplayMetadata) *MasteringDisplay {
	return &MasteringDisplay{
		GreenX: round32(m.GreenX * 50000), GreenY: round32(m.GreenY * 50000),
		BlueX: round32(m.BlueX * 50000), BlueY: round32(m.BlueY * 50000),
		RedX: round32(m.RedX * 50000), RedY: round32(m.RedY * 50000),
		WhiteX: round32(m.WhitePointX * 50000), WhiteY: round32(m.WhitePointY * 50000),
		MaxLuminance: round32(m.MaxLuminance * 10000), MinLuminance: round32(m.MinLuminance * 10000),
	}
}

// toContentLight converts ffprobe's Content light level metadata side
// data into ContentLight.
func toContentLight(c *ffprobe.SideDataContentLightLevel) *ContentLight {
	return &ContentLight{MaxCLL: int32(c.MaxContent), MaxFALL: int32(c.MaxAverage)}
}

// round32 rounds an ffprobe FlexFloat to the nearest int32.
func round32(f ffprobe.FlexFloat) int32 {
	return int32(math.Round(float64(f)))
}

// runFrameProbe issues the second ffprobe call: the first decoded
// frame's colour tags and HDR side data, per docs/research/transcode.md
// §2.1's second command, verbatim. go-ffprobe.v2 cannot express this
// call (no Frames field on ProbeData), so it bypasses the library.
func runFrameProbe(ctx context.Context, path string) (frameProbeData, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error", "-print_format", "json",
		"-select_streams", "v:0", "-show_frames", "-read_intervals", "%+#1",
		"-show_entries", "frame=pix_fmt,color_primaries,color_transfer,color_space,color_range,side_data_list",
		path)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return frameProbeData{}, fmt.Errorf("mediainfo: ffprobe frame probe: %w: %s", err, stderr.String())
	}
	var fd frameProbeData
	if err := json.Unmarshal(out.Bytes(), &fd); err != nil {
		return frameProbeData{}, fmt.Errorf("mediainfo: parse frame probe json: %w", err)
	}
	return fd, nil
}
