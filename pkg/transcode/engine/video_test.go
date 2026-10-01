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

package engine

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// fastX265 keeps the tests quick; the plan's own options are the standard's.
var fastX265 = map[string]string{"crf": "28", "preset": "ultrafast", "profile": "main10", "x265-params": "log-level=error"}

func videoClip(t *testing.T, name string, args ...string) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	out := filepath.Join(t.TempDir(), name)
	base := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2"}
	run(t, "ffmpeg", append(append(base, args...), out)...)
	return out
}

func encodePlan(v standard.VideoPlan) standard.Result {
	v.Action = "encode"
	return standard.Result{Decision: standard.DecisionEncode, Container: transcode.ContainerMKV, Video: v}
}

func cpuVideo(hdr string, color standard.ColorTags) standard.VideoPlan {
	return standard.VideoPlan{Encoder: "libx265", Options: fastX265, Decode: "cpu", Filter: "format=yuv420p10le", HDR: hdr, Color: color}
}

func frames(t *testing.T, path string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(run(t, "ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", path)))
	require.NoError(t, err)
	return n
}

func entries(t *testing.T, path string, args ...string) string {
	t.Helper()
	return run(t, "ffprobe", append(append([]string{"-v", "error", "-select_streams", "v:0"}, args...), "-of", "default=nw=1", path)...)
}

func TestSDRH264BecomesHEVCMain10(t *testing.T) {
	// FFmpeg 9's encoders take colour from the frames, so the clip is tagged
	// with setparams, not the -color_* output options.
	src := videoClip(t, "sdr.mkv", "-vf", "setparams=color_primaries=bt709:color_trc=bt709:colorspace=bt709", "-c:v", "libx264", "-preset", "veryfast")
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), encodePlan(cpuVideo("sdr", standard.ColorTags{})), src, out, Options{})
	require.NoError(t, err)
	props := entries(t, out, "-show_entries", "stream=codec_name,profile,pix_fmt,color_primaries")
	for _, want := range []string{"codec_name=hevc", "profile=Main 10", "pix_fmt=yuv420p10le", "color_primaries=bt709"} {
		assert.Contains(t, props, want)
	}
	assert.Equal(t, frames(t, src), frames(t, out))
}

func TestHDR10KeepsItsMetadata(t *testing.T) {
	src := videoClip(t, "hdr10.mkv", "-pix_fmt", "yuv420p10le", "-c:v", "libx265",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc",
		"-x265-params", "log-level=error:hdr10=1:repeat-headers=1:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400")
	out := filepath.Join(t.TempDir(), "o.mkv")
	color := standard.ColorTags{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"}
	v := cpuVideo("hdr10", color)
	v.StripSideData = []string{"hdr10plus"}
	_, err := Run(context.Background(), encodePlan(v), src, out, Options{})
	require.NoError(t, err)
	stream := entries(t, out, "-show_entries", "stream_side_data=side_data_type")
	assert.Contains(t, stream, "Mastering display metadata")
	assert.Contains(t, stream, "Content light level metadata")
	assert.Contains(t, entries(t, out, "-read_intervals", "%+#1", "-show_entries", "frame_side_data=side_data_type"), "Mastering display metadata")
	assert.Contains(t, entries(t, out, "-show_entries", "stream=color_transfer"), "color_transfer=smpte2084")
}

func TestHLGKeepsItsTransfer(t *testing.T) {
	src := videoClip(t, "hlg.mkv", "-pix_fmt", "yuv420p10le", "-c:v", "libx264", "-preset", "veryfast",
		"-color_primaries", "bt2020", "-color_trc", "arib-std-b67", "-colorspace", "bt2020nc")
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), encodePlan(cpuVideo("hlg", standard.ColorTags{Primaries: "bt2020", Transfer: "arib-std-b67", Matrix: "bt2020nc", Range: "tv"})), src, out, Options{})
	require.NoError(t, err)
	assert.Contains(t, entries(t, out, "-show_entries", "stream=color_transfer"), "color_transfer=arib-std-b67")
}

func TestNVDECAndUploadPathsOnNVENC(t *testing.T) {
	ffmpeg9OrSkip(t)
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	if err != nil {
		t.Skipf("no CUDA device: %v", err)
	}
	defer func() { _ = dev.Close() }()
	src := videoClip(t, "h264.mkv", "-c:v", "libx264", "-preset", "veryfast")
	hi10p := videoClip(t, "hi10p.mkv", "-pix_fmt", "yuv420p10le", "-c:v", "libx264", "-preset", "veryfast")
	nv := map[string]string{"preset": "p7", "rc": "constqp", "qp": "23", "spatial-aq": "1", "temporal-aq": "1", "profile": "main10"}
	for name, c := range map[string]struct {
		src            string
		decode, filter string
	}{
		"nvdec":  {src, "nvdec", "scale_cuda=format=p010le"},
		"upload": {hi10p, "upload", "hwupload,scale_cuda=format=p010le"},
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "o.mkv")
			_, err := Run(context.Background(), encodePlan(standard.VideoPlan{Encoder: "hevc_nvenc", Options: nv, Decode: c.decode, Filter: c.filter, HDR: "sdr"}),
				c.src, out, Options{HWDevice: dev})
			require.NoError(t, err)
			assert.Contains(t, entries(t, out, "-show_entries", "stream=profile"), "profile=Main 10")
			assert.Equal(t, frames(t, c.src), frames(t, out))
		})
	}
}

func TestAGPUPlanWithoutADeviceIsAnError(t *testing.T) {
	src := videoClip(t, "h264.mkv", "-c:v", "libx264", "-preset", "veryfast")
	_, err := Run(context.Background(), encodePlan(standard.VideoPlan{Encoder: "hevc_nvenc", Decode: "nvdec", Filter: "scale_cuda=format=p010le"}),
		src, filepath.Join(t.TempDir(), "o.mkv"), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device")
}

func TestHDR10KeepsItsMetadataOnNVENC(t *testing.T) {
	ffmpeg9OrSkip(t)
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	if err != nil {
		t.Skipf("no CUDA device: %v", err)
	}
	defer func() { _ = dev.Close() }()
	src := videoClip(t, "hdr10.mkv", "-pix_fmt", "yuv420p10le", "-c:v", "libx265",
		"-x265-params", "log-level=error:hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400")
	out := filepath.Join(t.TempDir(), "o.mkv")
	v := standard.VideoPlan{Encoder: "hevc_nvenc", Decode: "nvdec", Filter: "scale_cuda=format=p010le", HDR: "hdr10",
		Options: map[string]string{"preset": "p7", "rc": "constqp", "qp": "23", "profile": "main10"},
		Color:   standard.ColorTags{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"}}
	_, err = Run(context.Background(), encodePlan(v), src, out, Options{HWDevice: dev})
	require.NoError(t, err)
	stream := entries(t, out, "-show_entries", "stream_side_data=side_data_type")
	assert.Contains(t, stream, "Mastering display metadata")
	assert.Contains(t, stream, "Content light level metadata")
	assert.Contains(t, entries(t, out, "-show_entries", "stream=color_transfer"), "color_transfer=smpte2084")
}

// An anamorphic source (a 16:9 DVD: 720x480 at 32:27) keeps its pixel
// aspect, or it would play squeezed to 3:2.
func TestAnAnamorphicSourceKeepsItsAspect(t *testing.T) {
	ffmpeg9OrSkip(t)
	src := filepath.Join(t.TempDir(), "dvd.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=720x480:rate=25:duration=1,setsar=32/27", "-c:v", "mpeg2video", src)
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), encodePlan(cpuVideo("sdr", standard.ColorTags{})), src, out, Options{})
	require.NoError(t, err)
	assert.Contains(t, entries(t, out, "-show_entries", "stream=sample_aspect_ratio"), "sample_aspect_ratio=32:27")
}
