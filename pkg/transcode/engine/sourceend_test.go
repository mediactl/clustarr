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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// Two sources whose container outlasts their picture, as two of the owner's
// failed transcodes did (2026-10-23 and -25): RoboCop's Turkish DTS track
// ran 3.4 s past the video and was dropped as not a kept language; The
// Mitchells vs. the Machines' SubRip tracks' last cues sat 265 s past it.
// Both outputs were as long as their video and failed verification against
// the container's duration, which neither source carried per-stream
// durations to correct. The run now measures where the kept streams end as
// it demuxes them, and that is the length the output is held to.

// outlastedClip is a 3 s clip (video, an English AAC track) with a Turkish
// AAC track of 6 s, and a SubRip track whose one cue ends at 9 s, so the
// Matroska duration is 9 s.
func outlastedClip(t *testing.T) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	srt := filepath.Join(dir, "late.srt")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:08,000 --> 00:00:09,000\nLate\n"), 0o644))
	out := filepath.Join(dir, "outlasted.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=500:sample_rate=48000:duration=6",
		"-i", srt,
		"-map", "0", "-map", "1", "-map", "2", "-map", "3",
		"-c:v", "libx264", "-preset", "veryfast", "-c:a", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=tur", "-metadata:s:s:0", "language=eng",
		out)
	return out
}

func TestTheRunMeasuresWhereTheKeptStreamsEnd(t *testing.T) {
	src := outlastedClip(t)
	out := filepath.Join(t.TempDir(), "o.mkv")
	plan := standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMKV,
		Video:    standard.VideoPlan{SourceIndex: 0, Action: "copy"},
		Audio:    []standard.AudioPlan{{SourceIndex: 0, Action: standard.AudioCopy, Language: "eng"}},
		Sidecars: []standard.SidecarPlan{{SourceIndex: 0, Format: standard.SidecarSRT, Codec: "subrip", Suffix: "en.srt"}},
		Expect: standard.Expectation{
			VideoStreams: 1, AudioStreams: 1, VideoCodec: "h264", PixelFormat: "yuv420p",
			DurationMillis: 9000, AudioSources: []int32{0}, // the container's 9 s, as the summary has it
		},
	}
	res, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err)
	assert.InDelta(t, 3000, res.SourceEndMillis, 150, "the video and the English track end at 3 s; the Turkish track and the subtitle do not count")

	// As the worker verifies: the measured end, not the container's duration.
	exp := plan.Expect
	exp.DurationMillis = res.SourceEndMillis
	rep, err := Verify(context.Background(), src, out, exp)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%v", rep.Problems)
	assert.FileExists(t, filepath.Join(filepath.Dir(out), "o.en.srt"))
}

// Verify's own fallback, for a run that measured nothing (a graft's): the
// source's per-stream durations, over the video and the kept audio alone,
// where a dropped track's would hold the output to a length it cannot have.
func TestVerifyMeasuresTheSourceOverTheKeptAudioAlone(t *testing.T) {
	src := outlastedClip(t)
	// A Matroska statistics tag per stream, as mkvmerge writes them and as
	// RoboCop's source carried: the fallback reads these.
	tagged := filepath.Join(t.TempDir(), "tagged.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", src, "-map", "0", "-c", "copy",
		"-metadata:s:v:0", "DURATION=00:00:03.000000000", "-metadata:s:a:0", "DURATION=00:00:03.000000000",
		"-metadata:s:a:1", "DURATION=00:00:06.000000000", "-metadata:s:s:0", "DURATION=00:00:09.000000000", tagged)
	out := filepath.Join(t.TempDir(), "o.mkv")
	plan := standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMKV,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"},
		Audio: []standard.AudioPlan{{SourceIndex: 0, Action: standard.AudioCopy, Language: "eng"}},
	}
	_, err := Run(context.Background(), plan, tagged, out, Options{})
	require.NoError(t, err)
	exp := standard.Expectation{VideoStreams: 1, AudioStreams: 1, VideoCodec: "h264", PixelFormat: "yuv420p", DurationMillis: 9000}
	rep, err := Verify(context.Background(), tagged, out, exp)
	require.NoError(t, err)
	assert.False(t, rep.OK, "every audio track counted: the Turkish one's 6 s is not what the output can be")
	exp.AudioSources = []int32{0}
	rep, err = Verify(context.Background(), tagged, out, exp)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%v", rep.Problems)
}
