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

package standard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// shaped is a video of a frame and the display aspect ratio the probe read.
func shaped(codec string, w, h, darMilli int32) transcode.VideoStream {
	v := video(codec, "yuv420p", 8, commonv1.HdrFormatNone)
	v.Width, v.Height, v.DisplayAspectMilli = w, h, darMilli
	return v
}

// The rectangle that fills the wanted shape, centred, as a remaster crops
// (owner, 2026-11-17: "prefer the high quality and crop ourselves during
// transcode"): a 4:3 picture loses its top and bottom for 16:9, a 16:9 one
// its sides for 4:3, measured by the picture and not the frame, so a DVD's
// 720x480 is cut by its pixels' own aspect. Sides are even. Nothing is cut
// from a picture already the shape, or with no target.
func TestCropRectFillsTheWantedShape(t *testing.T) {
	for _, c := range []struct {
		name   string
		v      transcode.VideoStream
		target int32
		want   *Crop
	}{
		{"4:3 HD to 16:9", shaped("h264", 1440, 1080, 1333), 1778, &Crop{Width: 1440, Height: 810, X: 0, Y: 134}},
		{"4:3 SD to 16:9", shaped("h264", 960, 720, 1333), 1778, &Crop{Width: 960, Height: 540, X: 0, Y: 90}},
		{"16:9 to 4:3", shaped("h264", 1920, 1080, 1778), 1333, &Crop{Width: 1440, Height: 1080, X: 240, Y: 0}},
		{"a 4:3 DVD frame to 16:9: by its picture", shaped("mpeg2video", 720, 480, 1333), 1778, &Crop{Width: 720, Height: 360, X: 0, Y: 60}},
		{"a 16:9 DVD frame to 4:3", shaped("mpeg2video", 720, 480, 1778), 1333, &Crop{Width: 540, Height: 480, X: 90, Y: 0}},
		{"no display aspect recorded: the frame's", shaped("h264", 1440, 1080, 0), 1778, &Crop{Width: 1440, Height: 810, X: 0, Y: 134}},
		{"a 4K frame to 4:3: exactly, not by thousandths", shaped("hevc", 3840, 2160, 1778), 1333, &Crop{Width: 2880, Height: 2160, X: 480, Y: 0}},
		{"1080p of 1.85:1 to 4:3", shaped("h264", 1920, 1038, 1850), 1333, &Crop{Width: 1384, Height: 1038, X: 268, Y: 0}},
		{"already 16:9", shaped("h264", 1920, 1080, 1778), 1778, nil},
		{"within a hundredth", shaped("h264", 1916, 1080, 1774), 1778, nil},
		{"no target", shaped("h264", 1440, 1080, 1333), 0, nil},
		{"no frame", shaped("h264", 0, 0, 0), 1778, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := cropRect(c.v, c.target)
			assert.Equal(t, c.want, got)
			if got != nil {
				assert.Zero(t, got.Width%2+got.Height%2+got.X%2+got.Y%2, "even sides")
				assert.LessOrEqual(t, got.X+got.Width, c.v.Width)
				assert.LessOrEqual(t, got.Y+got.Height, c.v.Height)
			}
		})
	}
}

// A file to be cropped is encoded, the crop cut by the tier's own filter
// and the output held to the rectangle's size: the software crop filter
// on the CPU, the video processor's crop on QSV whether it decodes on the
// CPU or on VA-API.
func TestACropIsCutByTheEncodesFilter(t *testing.T) {
	in := info(shaped("h264", 1440, 1080, 1333), eac3)
	in.CropAspectMilli = 1778

	p := Plan(in, profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, p.Reason)
	assert.Equal(t, "crop=1440:810:0:134,format=yuv420p", p.Video.Filter)
	assert.Equal(t, &Crop{Width: 1440, Height: 810, Y: 134}, p.Video.Crop)
	assert.Equal(t, int32(1440), p.Expect.Width)
	assert.Equal(t, int32(810), p.Expect.Height)
	assert.Contains(t, p.Reason, "cropped from 1440x1080 to 1440x810 (1.78:1)")
	assert.Equal(t, VideoEncoded, p.Tags[VideoTagKey])

	qsv := Plan(in, profile, Hardware{Tier: transcode.TierQSV})
	require.Equal(t, DecisionEncode, qsv.Decision, qsv.Reason)
	assert.Equal(t, "hwupload=extra_hw_frames=64,vpp_qsv=cw=1440:ch=810:cx=0:cy=134:format=nv12", qsv.Video.Filter)

	uncropped := in
	uncropped.CropAspectMilli = 0
	plain := Plan(uncropped, profile, cpu)
	assert.Equal(t, "format=yuv420p", plain.Video.Filter)
	assert.Nil(t, plain.Video.Crop)
	assert.Zero(t, plain.Expect.Width)
	assert.NotEqual(t, plain.Hash(), p.Hash(), "a cropped plan is another plan")
}

// A crop makes an encode of what would be copied or left alone: compliant
// HEVC, and H.264 a profile keeps. A transcode stays final: it is never
// encoded a second time, cropped or not (its item searches for it anew).
func TestACropEncodesWhatWouldBeCopiedAndNeverATranscode(t *testing.T) {
	hevc := info(shaped("hevc", 1440, 1080, 1333), audio(0, "aac", 2, "stereo", "eng"))
	hevc.Subtitles, hevc.Attachments = nil, nil
	hevc.Format.Name = "mov,mp4,m4a,3gp,3g2,mj2"
	require.Equal(t, DecisionSkip, Plan(hevc, profile, cpu).Decision, "compliant, in the layout: left alone")

	hevc.CropAspectMilli = 1778
	p := Plan(hevc, profile, cpu)
	require.Equal(t, DecisionEncode, p.Decision, p.Reason)
	assert.NotNil(t, p.Video.Crop)

	keeps := profile
	keeps.KeepCodecs = []string{"h264"}
	kept := info(shaped("h264", 1440, 1080, 1333), eac3)
	require.Equal(t, DecisionCopyVideo, Plan(kept, keeps, cpu).Decision)
	kept.CropAspectMilli = 1778
	require.Equal(t, DecisionEncode, Plan(kept, keeps, cpu).Decision, "kept H.264 is encoded to be cropped")

	tagged := hevc
	tagged.Tags = map[string]string{ProfileTagKey: "hevc-mkv@old", VideoTagKey: VideoEncoded}
	skipped := Plan(tagged, profile, cpu)
	require.Equal(t, DecisionSkip, skipped.Decision, skipped.Reason)
	assert.Contains(t, skipped.Reason, "already transcoded")
}

// On a tier whose filter cuts no crop the file is skipped, never encoded
// whole: an uncropped transcode would be a final file of the wrong shape,
// searched for again and encoded the same way for ever. And a cropped
// encode has no kept form when its output is no smaller.
func TestACropIsNeverDroppedForAnEncodeOfTheWholeFrame(t *testing.T) {
	in := info(shaped("h264", 1440, 1080, 1333), eac3)
	in.CropAspectMilli = 1778
	for _, hw := range []Hardware{nvenc, {Tier: transcode.TierVAAPI}} {
		p := Plan(in, profile, hw)
		require.Equal(t, DecisionSkip, p.Decision, "%s: %s", hw.Tier, p.Reason)
		assert.Contains(t, p.Reason, "cropped to 1.78:1")
	}

	p := Plan(in, profile, cpu)
	_, ok := KeepVideo(p, in)
	assert.False(t, ok, "the kept copy would be the whole frame")
	whole := in
	whole.CropAspectMilli = 0
	_, ok = KeepVideo(Plan(whole, profile, cpu), whole)
	assert.True(t, ok, "an uncropped H.264 encode still has its kept form")
}
