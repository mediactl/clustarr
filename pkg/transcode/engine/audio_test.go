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
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// withAudio makes a 3 s H.264 clip with one audio track made by the lavfi
// filter spec af (a 1 kHz tone panned by it), encoded with codec, the audio
// input delayed by offset seconds.
func withAudio(t *testing.T, af string, codec []string, offset string) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	out := filepath.Join(t.TempDir(), "a.mkv")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=3",
		"-itsoffset", offset, "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=3",
	}
	args = append(args, "-map", "0", "-map", "1", "-af", af, "-c:v", "libx264", "-preset", "veryfast")
	args = append(args, codec...)
	run(t, "ffmpeg", append(args, out)...)
	return out
}

func audioPlan(a standard.AudioPlan) standard.Result {
	return standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMKV,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"}, Audio: []standard.AudioPlan{a},
	}
}

var (
	astatsChannelRE = regexp.MustCompile(`Channel: (\d+)`)
	astatsRMSRE     = regexp.MustCompile(`RMS level dB: (\S+)`)
)

// rmsByChannel is each 5.1 channel's RMS level, by position name.
func rmsByChannel(t *testing.T, path string) map[string]float64 {
	t.Helper()
	names := []string{"FL", "FR", "FC", "LFE", "BL", "BR"}
	out := run(t, "ffmpeg", "-hide_banner", "-nostats", "-i", path, "-map", "0:a:0", "-af", "astats=measure_overall=none", "-f", "null", "-")
	levels, ch := map[string]float64{}, 0
	for _, line := range strings.Split(out, "\n") {
		if m := astatsChannelRE.FindStringSubmatch(line); m != nil {
			ch, _ = strconv.Atoi(m[1])
		}
		if m := astatsRMSRE.FindStringSubmatch(line); m != nil && ch >= 1 && ch <= len(names) {
			v := math.Inf(-1)
			if m[1] != "-inf" {
				v, _ = strconv.ParseFloat(m[1], 64)
			}
			levels[names[ch-1]] = v
		}
	}
	return levels
}

func TestTrueHD71BecomesAAC51(t *testing.T) {
	src := withAudio(t, "pan=7.1|SL=c0", []string{"-c:a", "truehd", "-strict", "-2"}, "0")
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), audioPlan(standard.AudioPlan{SourceIndex: 0, Action: "aac", Channels: 6, Layout: "5.1", BitRate: 384000}), src, out, Options{})
	require.NoError(t, err)
	p := ffprobeJSON(t, out)
	require.Len(t, p.Streams, 2)
	assert.Equal(t, "aac", p.Streams[1].CodecName)
	assert.Equal(t, 6, p.Streams[1].Channels)
	assert.Equal(t, "5.1", p.Streams[1].Layout)
	l := rmsByChannel(t, out)
	assert.Greater(t, l["BL"], -30.0, "SL folds into BL")
	assert.Less(t, l["FL"], -60.0)
	assert.InDelta(t, ffprobeJSON(t, src).seconds(t), p.seconds(t), 0.1)
}

func TestFLACStereoBecomesAAC(t *testing.T) {
	src := withAudio(t, "aformat=channel_layouts=stereo", []string{"-c:a", "flac"}, "0")
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), audioPlan(standard.AudioPlan{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 128000}), src, out, Options{})
	require.NoError(t, err)
	p := ffprobeJSON(t, out)
	assert.Equal(t, "aac", p.Streams[1].CodecName)
	assert.Equal(t, 2, p.Streams[1].Channels)
}

func TestSideSurroundsLandInTheBackSurrounds(t *testing.T) {
	src := withAudio(t, "pan=5.1(side)|SR=c0", []string{"-c:a", "flac"}, "0")
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), audioPlan(standard.AudioPlan{SourceIndex: 0, Action: "aac", Channels: 6, Layout: "5.1", BitRate: 384000}), src, out, Options{})
	require.NoError(t, err)
	l := rmsByChannel(t, out)
	assert.Greater(t, l["BR"], -30.0)
	assert.Less(t, l["FR"], -60.0)
}

func TestAudioThatStartsLateKeepsItsStart(t *testing.T) {
	src := withAudio(t, "aformat=channel_layouts=stereo", []string{"-c:a", "flac"}, "0.5")
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), audioPlan(standard.AudioPlan{SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 128000}), src, out, Options{})
	require.NoError(t, err)
	srcStart, _ := strconv.ParseFloat(ffprobeJSON(t, src).Streams[1].StartTime, 64)
	outStart, _ := strconv.ParseFloat(ffprobeJSON(t, out).Streams[1].StartTime, 64)
	assert.InDelta(t, srcStart, outStart, 0.03, "within about one AAC frame (21 ms) of the source's start")
}

// A FLAC 5.1 source becomes AC-3 5.1 and AAC 2.0 from one decode; an
// E-AC-3 5.1 source is copied and gains AAC 2.0; titles and the default
// flag are the plan's (MP4 standard spec §3), the title as the track's
// handler name, the one name an MP4 track has.
func TestTheEngineWritesTheMP4AudioLayout(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3,aformat=channel_layouts=5.1",
		"-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=3,aformat=channel_layouts=5.1",
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error",
		"-c:a:0", "flac", "-c:a:1", "eac3",
		"-metadata:s:a:0", "language=jpn", "-metadata:s:a:0", "title=FLAC 5.1",
		"-metadata:s:a:1", "language=eng", "-disposition:a:0", "default", "-disposition:a:1", "0", src)
	plan := standard.Plan(probeInfo(t, src), standard.Profile{Name: "p", Hash: "h"}, standard.Hardware{Tier: transcode.TierCPUx265})
	require.Equal(t, standard.DecisionCopyVideo, plan.Decision, plan.Reason)
	out := filepath.Join(dir, "out.mp4")
	_, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err)
	var auds []string
	for _, s := range ffprobeJSON(t, out).Streams {
		if s.CodecType == "audio" {
			auds = append(auds, fmt.Sprintf("%s/%d/%s/%s/%d", s.CodecName, s.Channels, s.Tags["language"], s.Tags["handler_name"], s.Disposition["default"]))
		}
	}
	assert.Equal(t, []string{
		"aac/2/jpn/Stereo/1", "ac3/6/jpn/Dolby Digital 5.1/0", // FLAC 5.1 encoded twice from one decode, AAC first
		"aac/2/eng/Stereo/0", "eac3/6/eng/SoundHandler/0", // the companion, then E-AC-3 copied (no title: the muxer's default handler)
	}, auds)
	rep, err := Verify(context.Background(), src, out, plan.Expect)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%v", rep.Problems)
}

// A video stream that starts after the audio must not stall the run: an
// encoded audio stage reads on while the video stage has yet to open its
// encoder (final review I1: setup waited for the header before reading,
// filled its channel, blocked the demuxer, and the video never set up).
func TestALateVideoStartDoesNotStallAnEncodedAudioTrack(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "late.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-itsoffset", "2", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=6,aformat=channel_layouts=5.1",
		"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "veryfast", "-c:a", "eac3", src)
	plan := standard.Plan(probeInfo(t, src), standard.Profile{Name: "p", Hash: "h"}, standard.Hardware{Tier: transcode.TierCPUx265})
	require.Len(t, plan.Audio, 2, "E-AC-3 copied, AAC companion encoded")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := Run(ctx, plan, src, filepath.Join(dir, "out.mp4"), Options{VideoOptions: map[string]string{"preset": "ultrafast", "crf": "30"}})
	require.NoError(t, err)
}
