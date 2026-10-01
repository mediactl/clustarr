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

package transcode

import (
	"sort"
	"strconv"
	"strings"
)

// The NVENC tier decodes on the GPU (NVDEC) when the source is a format the
// device decodes, and keeps every frame in GPU memory from decode to encode:
// before 2026-09-30 it decoded in software and converted to p010 with
// -pix_fmt on the CPU, and an RTX 2070 pod spent ~380% CPU on an x264 source
// with NVDEC at 0%. A source NVDEC cannot decode -- H.264 Hi10P, any 4:2:2 or
// 4:4:4 stream, AV1 before Ampere -- takes that software path still: with
// -hwaccel_output_format cuda, ffmpeg's silent fallback to a software
// decoder hands scale_cuda frames it cannot take, and the job fails.

// Decoders are the source formats a device was measured to decode on NVDEC
// (ProbeDecoders), by NVDECKey. A format the trial could not test -- the
// node's ffmpeg has no encoder to make a sample of it -- is absent, and the
// static list decides it.
type Decoders struct {
	Formats map[string]bool `json:"formats"`
}

// Decodable is the sorted keys d measured as decodable.
func (d *Decoders) Decodable() []string {
	if d == nil {
		return nil
	}
	var out []string
	for k, ok := range d.Formats {
		if ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// staticNVDEC is what every NVDEC from Turing (the RTX 20 series) on decodes
// into 4:2:0 surfaces, from NVIDIA's Video Codec SDK support matrix. AV1
// needs Ampere, so it is decoded on the GPU only where a trial measured it.
var staticNVDEC = map[string]bool{
	"h264:8": true, "hevc:8": true, "hevc:10": true,
	"vp9:8": true, "vp9:10": true, "mpeg2video:8": true, "vc1:8": true,
}

// NVDECKey is v's format as Decoders and the static list key it: the
// ffprobe codec name and the bit depth its pixel format carries, or "" for
// a stream that is not 4:2:0, which no key decodes.
func NVDECKey(v VideoStream) string {
	pf := v.PixFmt
	if !strings.HasPrefix(pf, "yuv420p") && !strings.HasPrefix(pf, "yuvj420p") {
		return ""
	}
	depth := 8
	switch {
	case strings.Contains(pf, "12"):
		depth = 12
	case strings.Contains(pf, "10"):
		depth = 10
	}
	return v.Codec + ":" + strconv.Itoa(depth)
}

// NVDECDecodes reports whether the NVENC tier decodes v on the GPU: the
// device's measured answer when l has one for v's format, else the static
// list.
func NVDECDecodes(l Limits, v VideoStream) bool {
	key := NVDECKey(v)
	if key == "" {
		return false
	}
	if l.NVDEC != nil {
		if ok, measured := l.NVDEC.Formats[key]; measured {
			return ok
		}
	}
	return staticNVDEC[key]
}

// nvdecSample is one format a trial decodes: the key it measures and the
// software encoder and pixel format that make a sample of it.
type nvdecSample struct {
	key, encoder, pixFmt string
}

// NVDECSample is one format the in-process measurement tries on NVDEC: its Decoders key and the software encoder and pixel format
// that make a sample of it.
type NVDECSample struct{ Key, Encoder, PixFmt string }

// NVDECSampleFormats are the formats a device's NVDEC is measured on.
func NVDECSampleFormats() []NVDECSample {
	out := make([]NVDECSample, len(nvdecSamples))
	for i, s := range nvdecSamples {
		out[i] = NVDECSample{Key: s.key, Encoder: s.encoder, PixFmt: s.pixFmt}
	}
	return out
}

// nvdecSamples are the formats a device's NVDEC is measured on. H.264 10-bit is tried
// so a device that does decode it is used; VC-1 has no encoder to make a
// sample with and stays on the static list.
var nvdecSamples = []nvdecSample{
	{"h264:8", "libx264", "yuv420p"},
	{"h264:10", "libx264", "yuv420p10le"},
	{"hevc:8", "libx265", "yuv420p"},
	{"hevc:10", "libx265", "yuv420p10le"},
	{"vp9:8", "libvpx-vp9", "yuv420p"},
	{"vp9:10", "libvpx-vp9", "yuv420p10le"},
	{"av1:8", "libsvtav1", "yuv420p"},
	{"av1:10", "libsvtav1", "yuv420p10le"},
	{"mpeg2video:8", "mpeg2video", "yuv420p"},
}
