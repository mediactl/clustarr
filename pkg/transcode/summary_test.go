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

package transcode_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestFromSummaryMapsEveryPlanInput(t *testing.T) {
	profile, compat := int32(8), int32(1)
	mi := &commonv1.MediaInfo{
		Container: "matroska", VideoCodec: "h264", VideoProfile: "High 10", PixelFormat: "yuv420p10le",
		VideoBitDepth: 10, Width: 3840, Height: 2160, FpsMilli: 23976, VideoBitrateKbps: 40000,
		Hdr: commonv1.HdrFormatDolbyVisionHDR10, DoviProfile: &profile, DoviBLCompatID: &compat,
		RuntimeMillis: 7_261_500,
		Audio: []commonv1.AudioStream{
			{Index: 1, Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Channels: 8, Language: "eng", Default: true},
			{Index: 2, Codec: "ac3", Channels: 2, Language: "eng", Title: "Director", Commentary: true},
		},
		Subtitles: []commonv1.SubtitleStream{
			{Index: 3, Codec: "subrip", Language: "eng", Forced: true},
			{Index: 4, Codec: "xsub", Language: "fre"}, // bitmap though pkg/mediainfo does not flag it
		},
		Attachments: 2,
	}

	info, err := transcode.FromSummary("/data/media/movies/X (2020)/X (2020).mkv", mi)
	require.NoError(t, err)

	assert.Equal(t, "/data/media/movies/X (2020)/X (2020).mkv", info.Path)
	assert.Equal(t, "matroska", info.Format.Name)
	assert.Equal(t, 7261500*time.Millisecond, info.Format.Duration)
	require.Len(t, info.Video, 1)
	v := info.Video[0]
	assert.Equal(t, "h264", v.Codec)
	assert.Equal(t, "High 10", v.Profile)
	assert.Equal(t, "yuv420p10le", v.PixFmt)
	assert.Equal(t, int32(2160), v.Height)
	assert.Equal(t, transcode.Rational{Num: 23976, Den: 1000}, v.FrameRate)
	assert.Equal(t, commonv1.HdrFormatDolbyVisionHDR10, v.HDR.Format)
	require.NotNil(t, v.HDR.DolbyVision)
	assert.Equal(t, int32(8), v.HDR.DolbyVision.Profile)
	assert.Equal(t, int32(1), v.HDR.DolbyVision.BLSignalCompatibilityID)
	assert.Equal(t, []string{"bt2020", "smpte2084", "bt2020nc", "tv"},
		[]string{v.ColorPrimaries, v.ColorTransfer, v.ColorSpace, v.ColorRange},
		"the colour an HDR10-family format is defined by")

	require.Len(t, info.Audio, 2)
	assert.Equal(t, int32(0), info.Audio[0].Index, "type-relative, not the container-wide 1")
	assert.True(t, info.Audio[0].Lossless)
	assert.True(t, info.Audio[0].Atmos)
	assert.True(t, info.Audio[0].Disposition.Default)
	assert.Equal(t, int32(1), info.Audio[1].Index)
	assert.True(t, info.Audio[1].Disposition.Comment)
	assert.Equal(t, "Director", info.Audio[1].Title)

	require.Len(t, info.Subtitles, 2)
	assert.Equal(t, int32(0), info.Subtitles[0].Index)
	assert.False(t, info.Subtitles[0].Bitmap)
	assert.True(t, info.Subtitles[0].Disposition.Forced)
	assert.True(t, info.Subtitles[1].Bitmap, "xsub is a bitmap codec to the renderer whatever the summary says")
	assert.Len(t, info.Attachments, 2)
}

func TestFromSummaryRefusesNoVideo(t *testing.T) {
	_, err := transcode.FromSummary("/x.mkv", nil)
	require.Error(t, err)
	_, err = transcode.FromSummary("/x.flac", &commonv1.MediaInfo{Container: "flac"})
	require.Error(t, err)
}

// The renderer's HDR arguments come from the HDR format, never from the
// stream's own tags, which the stored summary does not carry: a stream
// tagged wrongly, or not at all, renders the same argv as a correct one.
func TestHDRArgsAreKeyedOnTheFormatNotTheStreamTags(t *testing.T) {
	stream := func(prim, trc, space, rng string) transcode.MediaInfo {
		return transcode.MediaInfo{
			Path:   "/data/media/movies/X/X.mkv",
			Format: transcode.FormatInfo{Duration: 2 * time.Hour},
			Video: []transcode.VideoStream{{
				Codec: "h264", PixFmt: "yuv420p10le", Height: 2160, FrameRate: fps24(),
				ColorPrimaries: prim, ColorTransfer: trc, ColorSpace: space, ColorRange: rng,
				HDR: transcode.HDRInfo{Format: commonv1.HdrFormatHDR10},
			}},
		}
	}
	tagged, err := transcode.Plan(stream("bt2020", "smpte2084", "bt2020nc", "tv"), defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	untagged, err := transcode.Plan(stream("", "", "", ""), defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	assert.Equal(t, transcode.Args(tagged), transcode.Args(untagged))
	joined := strings.Join(transcode.Args(untagged), " ")
	assert.Contains(t, joined, "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv",
		"an untagged HDR10 source must still get a complete, valid setparams filter")
	assert.Contains(t, joined, "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:range=limited")
	assert.NotContains(t, joined, "master-display", "libx265 carries the mastering display from the source's side data")
}

// PlanMeta.OutputPath moves the .part beside the final output, so the
// caller's rename stays in one directory; empty keeps it beside the source.
func TestPlanWritesThePartBesideTheOutputPath(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/data/media/movies/X (2020)/X (2020).mp4",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video:  []transcode.VideoStream{{Codec: "h264", PixFmt: "yuv420p", Height: 1080, FrameRate: fps24()}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/X (2020)/X (2020).part.mkv", plan.Output)

	meta := testMeta
	meta.OutputPath = "/data/media/movies/X (2020)/X (2020) - hevc.mkv"
	plan, err = transcode.Plan(info, defaultProfile(), testCaps, meta)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/X (2020)/X (2020) - hevc.part.mkv", plan.Output)
}

func requireLibx265(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	out, err := exec.Command("/usr/bin/ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil || !strings.Contains(string(out), "libx265") {
		t.Skip("this ffmpeg has no libx265")
	}
}

// hdr10Source generates a short HDR10 HEVC clip -- BT.2020/PQ tags plus
// mastering-display and content-light SEI -- with two audio tracks and a
// text subtitle, the way a real UHD source is laid out.
func hdr10Source(t *testing.T, dir string) string {
	t.Helper()
	srt := filepath.Join(dir, "subs.srt")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello\n"), 0o644))
	src := filepath.Join(dir, "Source (2020).mkv")
	gen := exec.Command("/usr/bin/ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=500:sample_rate=48000:duration=2",
		"-i", srt,
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s",
		"-vf", "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "log-level=none:hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:range=limited:"+
			"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400",
		"-c:a", "ac3", "-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=spa", "-metadata:s:s:0", "language=eng",
		src)
	out, err := gen.CombinedOutput()
	require.NoError(t, err, string(out))
	return src
}

func sdrSource(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "Plain (2020).mkv")
	gen := exec.Command("/usr/bin/ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", src)
	out, err := gen.CombinedOutput()
	require.NoError(t, err, string(out))
	return src
}

func fastProfile() transcode.ProfileSpec {
	p := defaultProfile()
	p.Video.Preset = "ultrafast"
	p.Policy.MinDuration = 0
	return p
}

// TestFromSummaryAndFromProbeRenderTheSameArgs is the property the gap-fix
// item "the HDR arguments [the TranscodeJob controller] renders into
// status.plan may differ from the worker's" asks for, on real files: the
// controller plans from the stored summary (FromSummary) and the worker from
// a live probe of the same bytes (FromProbe), and the two render the very
// same ffmpeg argv -- HDR arguments included -- so status.plan.argsHash is
// the hash of what the worker runs.
func TestFromSummaryAndFromProbeRenderTheSameArgs(t *testing.T) {
	requireLibx265(t)
	dir := t.TempDir()
	for name, src := range map[string]string{"hdr10": hdr10Source(t, dir), "sdr": sdrSource(t, dir)} {
		t.Run(name, func(t *testing.T) {
			mi, raw, err := mediainfo.Probe(context.Background(), src)
			require.NoError(t, err)
			if name == "hdr10" {
				require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr, "the fixture must exercise the HDR path")
				require.NotNil(t, raw.MasteringDisplay, "and carry mastering metadata the summary has no room for")
			}

			live, err := transcode.FromProbe(mi, raw)
			require.NoError(t, err)
			live.Path = src
			stored, err := transcode.FromSummary(src, mi)
			require.NoError(t, err)

			fromProbe, err := transcode.Plan(live, fastProfile(), testCaps, testMeta)
			require.NoError(t, err)
			fromSummary, err := transcode.Plan(stored, fastProfile(), testCaps, testMeta)
			require.NoError(t, err)
			assert.Equal(t, fromProbe.Decision, fromSummary.Decision)
			assert.Equal(t, transcode.Args(fromProbe), transcode.Args(fromSummary))
			assert.Equal(t, transcode.ArgsHash(fromProbe), transcode.ArgsHash(fromSummary))
		})
	}

	// The same for an HDR10 source whose video must be ENCODED (here, by
	// asking for it): the summary path renders the full HDR argument set.
	src := hdr10Source(t, dir)
	mi, raw, err := mediainfo.Probe(context.Background(), src)
	require.NoError(t, err)
	live, err := transcode.FromProbe(mi, raw)
	require.NoError(t, err)
	live.Path = src
	stored, err := transcode.FromSummary(src, mi)
	require.NoError(t, err)
	live.Video[0].Codec, stored.Video[0].Codec = "h264", "h264"
	fromProbe, err := transcode.Plan(live, fastProfile(), testCaps, testMeta)
	require.NoError(t, err)
	fromSummary, err := transcode.Plan(stored, fastProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, fromSummary.Decision)
	assert.Equal(t, transcode.Args(fromProbe), transcode.Args(fromSummary))
	joined := strings.Join(transcode.Args(fromSummary), " ")
	assert.Contains(t, joined, "hdr10=1")
	assert.Contains(t, joined, "setparams=")
}

// An HDR source whose video is already compliant is remuxed with
// -c:v copy, and ffmpeg refuses any -vf beside a stream copy: the plan must
// carry no video filter, and the real run must succeed.
func TestHDRRemuxOnlyRunsWithoutAVideoFilter(t *testing.T) {
	requireLibx265(t)
	src := hdr10Source(t, t.TempDir())
	mi, raw, err := mediainfo.Probe(context.Background(), src)
	require.NoError(t, err)
	require.Equal(t, commonv1.HdrFormatHDR10, mi.Hdr)
	info, err := transcode.FromProbe(mi, raw)
	require.NoError(t, err)
	info.Path = src

	plan, err := transcode.Plan(info, fastProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionRemuxOnly, plan.Decision, "hevc Main 10 with ac3 audio is a remux")
	assert.Empty(t, plan.Filters)
	assert.NotContains(t, transcode.Args(plan), "-vf")
	require.NoError(t, transcode.NewRunner("/usr/bin/ffmpeg").Run(context.Background(), plan, func(transcode.Progress) {}))
}

// TestHDR10StaticMetadataPassesThroughLibx265 is why the renderer can leave
// master-display and max-cll out of -x265-params: encoding an HDR10 source
// with the plan Args renders, which carries neither, keeps both in the
// output, value for value (FFmpeg's libx265 handle_side_data).
func TestHDR10StaticMetadataPassesThroughLibx265(t *testing.T) {
	requireLibx265(t)
	src := hdr10Source(t, t.TempDir())
	mi, raw, err := mediainfo.Probe(context.Background(), src)
	require.NoError(t, err)
	require.NotNil(t, raw.MasteringDisplay)
	require.NotNil(t, raw.ContentLight)

	info, err := transcode.FromProbe(mi, raw)
	require.NoError(t, err)
	info.Path = src
	info.Video[0].Codec = "h264" // force a video encode of the HEVC source
	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierCPUx265: true}}
	plan, err := transcode.Plan(info, fastProfile(), caps, transcode.PlanMeta{ProfileName: "t", ProfileHash: "h", Threads: 2})
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.NotContains(t, strings.Join(transcode.Args(plan), " "), "master-display")

	require.NoError(t, transcode.NewRunner("/usr/bin/ffmpeg").Run(context.Background(), plan, func(transcode.Progress) {}))
	outMI, outRaw, err := mediainfo.Probe(context.Background(), plan.Output)
	require.NoError(t, err)
	assert.Equal(t, commonv1.HdrFormatHDR10, outMI.Hdr)
	require.NotNil(t, outRaw.MasteringDisplay, "the output lost its mastering display metadata")
	require.NotNil(t, outRaw.ContentLight, "the output lost its content light levels")
	assert.Equal(t, *raw.MasteringDisplay, *outRaw.MasteringDisplay)
	assert.Equal(t, *raw.ContentLight, *outRaw.ContentLight)
}
