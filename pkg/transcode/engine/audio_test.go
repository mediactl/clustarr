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
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

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
