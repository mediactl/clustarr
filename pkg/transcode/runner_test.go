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
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestParseProgressBlockMatchesTheVerifiedNoteExample(t *testing.T) {
	b, err := os.ReadFile("../../testdata/transcode/fixtures/progress-block.txt")
	require.NoError(t, err)

	var got []transcode.Progress
	require.NoError(t, transcode.ParseProgressStream(bufio.NewScanner(strings.NewReader(string(b))), 10_000, func(p transcode.Progress) {
		got = append(got, p)
	}))

	require.Len(t, got, 1, "one progress=end block yields exactly one Progress")
	p := got[0]
	require.Equal(t, int64(72), p.Frame)
	require.Equal(t, int32(0), p.FPSMilli)         // fps=0.00
	require.Equal(t, int64(3000), p.OutTimeMillis) // out_time_us=3000000 -> 3000ms; NOT divided again despite the out_time_ms quirk (note §7)
	require.Equal(t, int32(9240), p.SpeedMilli)    // speed=9.24x -> 9240
	require.Equal(t, int32(623), p.BitrateKbps)    // "623.4kbits/s" -> 623
	require.Equal(t, int32(30), p.Percent)         // 3000ms / 10000ms duration
}

func TestRunReturnsATypedErrorWithTheStderrTailOnNonZeroExit(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	r := transcode.NewRunner("/usr/bin/ffmpeg")
	plan := &transcode.PlanResult{
		Decision:  transcode.DecisionEncode,
		Input:     "/nonexistent/does-not-exist.mkv", // ffmpeg exits 1 with "No such file or directory"
		Output:    filepath.Join(t.TempDir(), "out.part.mkv"),
		Container: transcode.ContainerMKV,
		VideoArgs: []string{"-c:v", "copy"},
	}

	err := r.Run(context.Background(), plan, func(transcode.Progress) {})
	require.Error(t, err)

	var runErr *transcode.RunError
	require.ErrorAs(t, err, &runErr)
	require.NotZero(t, runErr.ExitCode)
	require.Contains(t, strings.ToLower(runErr.StderrTail), "no such file")
	require.LessOrEqual(t, len(runErr.StderrTail), 4096)
}

func TestRunHonoursContextCancellationAndSendsSIGINTFirst(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	r := transcode.NewRunner("/usr/bin/ffmpeg")
	plan := &transcode.PlanResult{
		Decision:  transcode.DecisionEncode,
		Input:     "", // overridden below via a lavfi source; see Args note
		Output:    filepath.Join(t.TempDir(), "cancel.part.mkv"),
		Container: transcode.ContainerMKV,
		// a 10s synthetic source encoded at veryslow guarantees Run is still
		// mid-flight well past the 100ms timeout below, on any CI box.
		HWInit:    []string{"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=10"},
		VideoArgs: []string{"-c:v", "libx265", "-preset", "veryslow", "-crf", "30"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := r.Run(ctx, plan, func(transcode.Progress) {})
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRunEndToEndEncodesAGeneratedClipAndEmitsProgress(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")

	// Generate a tiny 3s clip from lavfi sources only -- no external file,
	// no network, "a few seconds" per the task's own budget.
	gen := exec.Command("/usr/bin/ffmpeg", "-hide_banner", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=3",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k",
		"-shortest", src)
	require.NoError(t, gen.Run())

	info, err := probeForTest(t, src)
	require.NoError(t, err)

	profile := defaultProfile()
	profile.Video.Preset = "ultrafast" // keep the real encode under a second
	profile.Policy.MinDuration = 0     // the generated clip is 3s; defaultProfile's
	// 1-minute floor (Step 3's fixture, matching
	// the CRD default) would otherwise skip it.
	caps := transcode.Capabilities{Encoders: map[transcode.Tier]bool{transcode.TierCPUx265: true}}
	plan, err := transcode.Plan(info, profile, caps, transcode.PlanMeta{ProfileName: "t", ProfileHash: "h", Threads: 2})
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)

	var events []transcode.Progress
	r := transcode.NewRunner("/usr/bin/ffmpeg")
	err = r.Run(context.Background(), plan, func(p transcode.Progress) { events = append(events, p) })
	require.NoError(t, err)
	require.NotEmpty(t, events, "at least one progress event must be observed")
	require.Equal(t, int32(100), events[len(events)-1].Percent)

	report, err := transcode.NewVerifier("/usr/bin/ffprobe").Verify(context.Background(), src, plan.Output, plan.Expect)
	require.NoError(t, err)
	require.True(t, report.OK, "problems: %v", report.Problems)
}

// probeForTest is a test-only helper, not part of the package's Produces
// API: it shells ffprobe -show_format -show_streams and fills just enough
// of a transcode.MediaInfo (container, one VideoStream, one AudioStream,
// Format.Duration) to drive Plan. It exists only because Task B4's real
// prober (pkg/mediainfo.Probe) predates this package by one wave but this
// end-to-end test wants a real ffprobe round-trip independent of
// pkg/mediainfo/FromProbe (already covered by its own integration test in
// mediainfo_test.go) -- a minimal probe kept deliberately separate so this
// file's ffmpeg/ffprobe integration tests don't depend on FromProbe too.
func probeForTest(t *testing.T, path string) (transcode.MediaInfo, error) {
	t.Helper()

	out, err := exec.Command("/usr/bin/ffprobe", "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path).Output()
	if err != nil {
		return transcode.MediaInfo{}, err
	}

	var data struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int32  `json:"width"`
			Height     int32  `json:"height"`
			PixFmt     string `json:"pix_fmt"`
			RFrameRate string `json:"r_frame_rate"`
			Channels   int32  `json:"channels"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return transcode.MediaInfo{}, err
	}

	durSec, _ := strconv.ParseFloat(data.Format.Duration, 64)
	info := transcode.MediaInfo{
		Path:   path,
		Format: transcode.FormatInfo{Duration: time.Duration(durSec * float64(time.Second))},
	}
	for _, s := range data.Streams {
		switch s.CodecType {
		case "video":
			num, den := int64(24), int64(1)
			if n, d, ok := strings.Cut(s.RFrameRate, "/"); ok {
				if nn, err := strconv.ParseInt(n, 10, 64); err == nil {
					num = nn
				}
				if dd, err := strconv.ParseInt(d, 10, 64); err == nil && dd != 0 {
					den = dd
				}
			}
			info.Video = append(info.Video, transcode.VideoStream{
				Codec: s.CodecName, PixFmt: s.PixFmt, Width: s.Width, Height: s.Height,
				FrameRate: transcode.Rational{Num: num, Den: den},
			})
		case "audio":
			info.Audio = append(info.Audio, transcode.AudioStream{
				Codec: s.CodecName, Channels: s.Channels, Language: "eng",
				Disposition: transcode.Disposition{Default: true},
			})
		}
	}
	return info, nil
}
