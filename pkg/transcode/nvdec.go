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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/obs/tracing"
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

// nvdecSamples are the formats ProbeDecoders tries. H.264 10-bit is tried
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

// ProbeDecoders measures which formats this process's GPU decodes, by
// encoding a five-frame sample of each in software and decoding it with
// exactly the input options and filter the NVENC tier renders
// ([nvdecInputArgs], scale_cuda), into hevc_nvenc. A format whose sample
// cannot be made is left unmeasured; one whose decode fails is false. Every
// trial failing is a measurement too -- a node with no working NVDEC encodes
// as it did before NVDEC was used -- so ProbeDecoders errors only when it
// cannot run at all.
func ProbeDecoders(ctx context.Context, ffmpegPath string) (Decoders, error) {
	ctx, span := tracing.Start(ctx, "transcode.probe_decoders")
	defer span.End()
	dir, err := os.MkdirTemp("", "clustarr-nvdec-")
	if err != nil {
		tracing.RecordError(span, err)
		return Decoders{}, fmt.Errorf("transcode: probe decoders: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	d := Decoders{Formats: map[string]bool{}}
	for _, s := range nvdecSamples {
		sample := filepath.Join(dir, strings.ReplaceAll(s.key, ":", "-")+".mkv")
		if _, err := runFFmpeg(ctx, ffmpegPath, "-hide_banner", "-nostdin", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=256x256:rate=25", "-frames:v", "5",
			"-pix_fmt", s.pixFmt, "-c:v", s.encoder, "-y", sample); err != nil {
			continue // no encoder for this format here: unmeasured
		}
		args := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, nvdecInputArgs(4)...)
		args = append(args, "-i", sample, "-vf", nvdecScaleFilter, "-c:v", "hevc_nvenc", "-f", "null", "-")
		_, err := runFFmpeg(ctx, ffmpegPath, args...)
		if ctx.Err() != nil {
			return Decoders{}, ctx.Err()
		}
		d.Formats[s.key] = err == nil
	}
	return d, nil
}

func runFFmpeg(ctx context.Context, ffmpegPath string, args ...string) (string, error) {
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

// nvdecScaleFilter converts decoded frames to the 10-bit surfaces
// hevc_nvenc's main10 encodes from, on the GPU: the -pix_fmt p010le the
// software path uses would make ffmpeg copy every frame back to the CPU.
const nvdecScaleFilter = "scale_cuda=format=p010le"

// nvdecInputArgs are the input options that decode on NVDEC and keep frames
// in GPU memory. extra is -extra_hw_frames: hevc_nvenc holds a frame for
// every lookahead and B-frame slot, and a decoder surface pool sized for the
// stream's own references alone runs out under it.
func nvdecInputArgs(extra int32) []string {
	return []string{
		"-hwaccel", "cuda", "-hwaccel_output_format", "cuda",
		"-extra_hw_frames", strconv.Itoa(int(extra)),
	}
}

// nvdecExtraFrames is the -extra_hw_frames for an encode at v: one surface
// per lookahead frame and B-frame, and four more for the encoder's own
// pipelining.
func nvdecExtraFrames(v VideoSpec) int32 {
	return v.RCLookahead + v.BFrames + 4
}
