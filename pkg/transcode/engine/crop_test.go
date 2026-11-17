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
	"testing"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// cropProfile is a standard profile for the crop runs, quick on libx265.
var cropProfile = standard.Profile{Name: "p", Hash: "h", Quality: 28, Container: transcode.ContainerMKV}

// cropped plans src as the worker does, from a live probe with the task's
// crop target, and encodes it on the CPU.
func cropped(t *testing.T, src string, targetMilli int32) (standard.Result, string) {
	t.Helper()
	in := probeInfo(t, src)
	in.CropAspectMilli = targetMilli
	plan := standard.Plan(in, cropProfile, standard.Hardware{Tier: transcode.TierCPUx265})
	require.Equal(t, standard.DecisionEncode, plan.Decision, plan.Reason)
	require.NotNil(t, plan.Video.Crop, plan.Reason)
	// The tests' quick encoder settings; the crop, the filter and what
	// the output is held to stay the standard's.
	plan.Video.Options = map[string]string{"crf": "28", "preset": "ultrafast", "profile": "main", "x265-params": "log-level=error"}
	out := filepath.Join(t.TempDir(), "o.mp4")
	res, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err, res.LogTail)
	return plan, out
}

// clip is a second or two of video alone in a frame of size, its pixels
// of aspect sar (square when zero), written in process.
func clip(t *testing.T, name, size, encoder string, sar [2]int, d time.Duration) string {
	t.Helper()
	v := testmedia.Video{Source: "testsrc2=size=" + size + ":rate=24", Encoder: encoder, SampleAspect: sar}
	if encoder == "" {
		v.Options = map[string]string{"preset": "ultrafast"}
	}
	return testmedia.Make(t, name, testmedia.Spec{Duration: d, Video: []testmedia.Video{v}})
}

// frame is an output's video as the probe reads it: codec, frame size and
// display aspect ratio.
func frame(t *testing.T, path string) (codec string, w, h, darMilli int32) {
	t.Helper()
	v := probeInfo(t, path).Video[0]
	return v.Codec, v.Width, v.Height, v.DisplayAspectMilli
}

// packets is how many video packets path holds: the frames written. (A
// count of decoded frames reads one short of them on the MP4 the standard
// writes, cropped or not.)
func packets(t *testing.T, path string) int {
	t.Helper()
	return len(testmedia.Packets(t, path))
}

// A series kept widescreen by cropping (spec.aspectRatioFix crop): a 4:3
// file's encode is the middle of its picture in 16:9, every frame of it,
// and the verification that passes it holds the output to that frame. The
// plan is the standard's own, from the probe the worker plans from.
func TestAFullscreenFileIsCroppedToWidescreen(t *testing.T) {
	src := clip(t, "43.mkv", "640x480", "", [2]int{}, 2*time.Second)

	plan, out := cropped(t, src, 1778)
	assert.Equal(t, &standard.Crop{Width: 640, Height: 360, Y: 60}, plan.Video.Crop)
	codec, w, h, dar := frame(t, out)
	assert.Equal(t, "hevc", codec)
	assert.Equal(t, [2]int32{640, 360}, [2]int32{w, h})
	assert.Equal(t, int32(1778), dar)
	assert.Equal(t, packets(t, src), packets(t, out), "the crop drops no frame")

	r, err := Verify(context.Background(), src, out, plan.Expect)
	require.NoError(t, err)
	assert.True(t, r.OK, "%v", r.Problems)

	// The same output against a plan that cropped nothing of it fails on
	// its frame: an encode that lost its crop is not passed as the file.
	whole := plan.Expect
	whole.Width, whole.Height = 640, 480
	r, err = Verify(context.Background(), src, out, whole)
	require.NoError(t, err)
	assert.False(t, r.OK)
	assert.Contains(t, joined(r.Problems), "640x360")
}

// A DVD's frame is cropped by its picture: 720x480 at 8:9 is 4:3, and its
// 16:9 middle is 720x360 of the same pixels, which play 16:9 with the
// pixel aspect the encode keeps. The other way, a 16:9 frame loses its
// sides for a series kept fullscreen.
func TestACropKeepsThePixelAspectAndCutsTheSidesForFullscreen(t *testing.T) {
	dvd := clip(t, "dvd.mkv", "720x480", "mpeg2video", [2]int{8, 9}, time.Second)
	_, _, _, dar := frame(t, dvd)
	require.Equal(t, int32(1333), dar, "the clip is a 4:3 picture in a 3:2 frame")
	plan, out := cropped(t, dvd, 1778)
	assert.Equal(t, &standard.Crop{Width: 720, Height: 360, Y: 60}, plan.Video.Crop)
	_, w, h, dar := frame(t, out)
	assert.Equal(t, [2]int32{720, 360}, [2]int32{w, h})
	assert.Equal(t, int32(1778), dar)
	assert.Equal(t, "8:9", testmedia.Inspect(t, out).Streams[0].SampleAspectRatio)

	wide := clip(t, "169.mkv", "640x360", "", [2]int{}, time.Second)
	plan, out = cropped(t, wide, 1333)
	assert.Equal(t, &standard.Crop{Width: 480, Height: 360, X: 80}, plan.Video.Crop)
	_, w, h, dar = frame(t, out)
	assert.Equal(t, [2]int32{480, 360}, [2]int32{w, h})
	assert.Equal(t, int32(1333), dar)
}

// The Intel pool's crop is the video processor's own (vpp_qsv's cw, ch, cx
// and cy), on both of its paths: frames uploaded from a CPU decode, and
// surfaces decoded on VA-API and mapped to QSV. Each writes the rectangle
// and passes the verification that holds the output to it. Skipped where
// no QSV device opens.
func TestTheIntelEncodeCropsInItsVideoProcessor(t *testing.T) {
	src := clip(t, "43.mkv", "640x480", "", [2]int{}, 2*time.Second)
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeQSV, "")
	if err != nil {
		t.Skipf("no QSV device: %v", err)
	}
	defer func() { _ = dev.Close() }()
	in := probeInfo(t, src)
	in.CropAspectMilli = 1778

	paths := map[string]standard.Hardware{"upload": {Tier: transcode.TierQSV}}
	opts := map[string]Options{"upload": {HWDevice: dev}}
	if node := selfcheck.IntelRenderNode(); node != "" {
		if dd, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeVAAPI, node); err == nil {
			defer func() { _ = dd.Close() }()
			paths["vaapi"] = standard.Hardware{Tier: transcode.TierQSV, Limits: transcode.Limits{
				VAAPI: &transcode.Decoders{Formats: map[string]bool{"h264:8": true}},
			}}
			opts["vaapi"] = Options{HWDevice: dev, DecodeDevice: dd}
		}
	}
	for name, hw := range paths {
		t.Run(name, func(t *testing.T) {
			plan := standard.Plan(in, cropProfile, hw)
			require.Equal(t, standard.DecisionEncode, plan.Decision, plan.Reason)
			require.Contains(t, plan.Video.Filter, "vpp_qsv=cw=640:ch=360:cx=0:cy=60:")
			if name == "vaapi" {
				require.Equal(t, "vaapi", plan.Video.Decode)
			}
			out := filepath.Join(t.TempDir(), "o.mp4")
			res, err := Run(context.Background(), plan, src, out, opts[name])
			require.NoError(t, err, res.LogTail)
			codec, w, h, _ := frame(t, out)
			assert.Equal(t, "hevc", codec)
			assert.Equal(t, [2]int32{640, 360}, [2]int32{w, h})
			assert.Equal(t, packets(t, src), packets(t, out))
			r, err := Verify(context.Background(), src, out, plan.Expect)
			require.NoError(t, err)
			assert.True(t, r.OK, "%v", r.Problems)
		})
	}
}
