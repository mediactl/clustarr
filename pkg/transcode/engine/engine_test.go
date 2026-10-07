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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// remuxPlan copies everything: the copyVideo decision with every audio
// track copied (as the standard plans a compliant file in another
// container), so Task 4's engine is exercised before any stage encodes.
func remuxPlan() standard.Result {
	return standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMKV,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"},
		Audio: []standard.AudioPlan{
			{SourceIndex: 0, Action: "copy", Language: "eng"},
			{SourceIndex: 1, Action: "copy", Language: "fre", Title: "Commentary", Comment: true},
		},
		Subtitles: []standard.SubtitlePlan{{SourceIndex: 0, Action: standard.SubtitleCopy, Codec: "subrip"}}, Attachments: true, Chapters: true,
		Tags: map[string]string{"CLUSTARR_PROFILE": "p@h"},
	}
}

func TestRunCopiesEveryStreamTagAndChapter(t *testing.T) {
	src := everythingClip(t, 3)
	out := filepath.Join(t.TempDir(), "out.part.mkv")
	var last transcode.Progress
	res, err := Run(context.Background(), remuxPlan(), src, out, Options{Progress: func(p transcode.Progress) { last = p }})
	require.NoError(t, err)
	assert.Greater(t, res.Frames, int64(0))

	p := ffprobeJSON(t, out)
	require.Len(t, p.Streams, 5)
	assert.Equal(t, "h264", p.Streams[0].CodecName)
	assert.Equal(t, "ac3", p.Streams[1].CodecName)
	assert.Equal(t, "eng", p.Streams[1].Tags["language"])
	assert.Equal(t, 1, p.Streams[1].Disposition["default"])
	assert.Equal(t, "aac", p.Streams[2].CodecName)
	assert.Equal(t, "Commentary", p.Streams[2].Tags["title"])
	assert.Equal(t, 1, p.Streams[2].Disposition["comment"])
	assert.Equal(t, "subrip", p.Streams[3].CodecName)
	assert.Equal(t, 1, p.Streams[3].Disposition["forced"])
	assert.Equal(t, "attachment", p.Streams[4].CodecType)
	require.Len(t, p.Chapters, 2)
	assert.Equal(t, "Clip", p.Format.Tags["title"])
	assert.Equal(t, "p@h", p.Format.Tags["CLUSTARR_PROFILE"])
	assert.InDelta(t, ffprobeJSON(t, src).seconds(t), p.seconds(t), 0.1)
	assert.Equal(t, int32(100), last.Percent, "the last progress report is complete")
}

func TestCancelStopsEveryStageAndRemovesTheOutput(t *testing.T) {
	src := everythingClip(t, 20)
	out := filepath.Join(t.TempDir(), "out.part.mkv")
	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Run(ctx, remuxPlan(), src, out, Options{Progress: func(transcode.Progress) { cancel() }, ProgressEvery: time.Millisecond})
	require.ErrorIs(t, err, context.Canceled)
	_, statErr := os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "the partial output is removed")
	// Polled here, not with assert.Eventually, whose own goroutine counts.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), base, "every stage goroutine exits")
}

func TestASlowMuxerHoldsTheDemuxerBack(t *testing.T) {
	// 250 MB of lossless noise: a demuxer not held back by its bounded
	// channels would read most of it into FFmpeg's (C) heap, which Go's
	// HeapInuse cannot see -- so the process's RSS is measured.
	ffmpeg9OrSkip(t)
	src := filepath.Join(t.TempDir(), "big.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=6,noise=alls=60:allf=t",
		"-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", src)
	plan := standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMKV,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"},
	}
	base := rssBytes(t)
	var peak int64
	_, err := Run(context.Background(), plan, src, filepath.Join(t.TempDir(), "out.part.mkv"),
		Options{QueueDepth: 4, ProgressEvery: time.Nanosecond, Progress: func(transcode.Progress) {
			time.Sleep(3 * time.Millisecond) // a slow consumer on the mux goroutine
			peak = max(peak, rssBytes(t)-base)
		}})
	require.NoError(t, err)
	assert.Less(t, peak, int64(100<<20), "bounded channels keep the demuxer from reading the file into memory")
}

// rssBytes is this process's resident set size.
func rssBytes(t *testing.T) int64 {
	t.Helper()
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Skip("no /proc/self/statm")
	}
	f := strings.Fields(string(b))
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize())
}

func TestAnUnreadableInputNamesTheStage(t *testing.T) {
	ffmpeg9OrSkip(t)
	src := filepath.Join(t.TempDir(), "junk.mkv")
	require.NoError(t, os.WriteFile(src, []byte("this is not a matroska file"), 0o644))
	_, err := Run(context.Background(), remuxPlan(), src, filepath.Join(t.TempDir(), "o.mkv"), Options{})
	var e *Error
	require.True(t, errors.As(err, &e), "%v", err)
	assert.Equal(t, "demux", e.Stage)
}

// Progress carries what the worker's telemetry and status report: bytes
// written so far and the output's bit rate, as ffmpeg's -progress did.
func TestProgressReportsTheOutputsSizeAndBitRate(t *testing.T) {
	src := everythingClip(t, 3)
	out := filepath.Join(t.TempDir(), "out.part.mkv")
	var last transcode.Progress
	_, err := Run(context.Background(), remuxPlan(), src, out, Options{Progress: func(p transcode.Progress) { last = p }})
	require.NoError(t, err)
	st, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, st.Size(), last.OutputBytes, "the last report is the finished file")
	assert.Positive(t, last.BitrateKbps)
}

// A stage that encodes a tiny input to the end must not close its encoder
// before the muxer has read its parameters to write the header: the
// muxer's AddEncoderStream then read a freed codec context and the process
// died with SIGSEGV (seen under load, in the worker's measurement trials).
func TestATinyInputNeverRacesTheMuxer(t *testing.T) {
	ffmpeg9OrSkip(t)
	src := filepath.Join(t.TempDir(), "tiny.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=25", "-frames:v", "5", "-c:v", "libx264", src)
	plan := encodePlan(standard.VideoPlan{Encoder: "libx265", Options: fastX265, Decode: "cpu", Filter: "format=yuv420p10le", HDR: "sdr"})
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Run(context.Background(), plan, src, filepath.Join(dir, fmt.Sprintf("o%d.mkv", i)), Options{})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}
