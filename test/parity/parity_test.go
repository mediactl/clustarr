//go:build parity

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

// Package parity is the ffgo parity harness (spec §5 Rollout): the
// in-process standard on NVENC over a clip of every file class from the
// owner's library (hack/parity-clips.sh), each output probed and checked.
// The output must keep what spec §1 keeps -- every subtitle, attachment and
// chapter, HDR10's mastering metadata, Dolby Vision 7 and 8.1 as HDR10,
// every kept audio track direct-play or AAC, the duration -- and each
// class's time is printed. (Until the argv engine was deleted it ran beside
// the standard and every difference was printed; the spec's Rollout section
// records that comparison.)
//
//	CLUSTARR_PARITY_DIR=~/parity go test -tags parity -v ./test/parity/
package parity

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/app/squash/worker/inprocess"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/ffprobeexec"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

func TestParity(t *testing.T) {
	ctx := context.Background()
	dir := os.Getenv("CLUSTARR_PARITY_DIR")
	if dir == "" {
		t.Skip("CLUSTARR_PARITY_DIR names no clips (hack/parity-clips.sh)")
	}
	if _, err := inprocess.New(); err != nil {
		t.Skipf("no FFmpeg 9: %v", err)
	}
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	if err != nil {
		t.Skipf("no CUDA device: %v", err)
	}
	defer func() { _ = dev.Close() }()

	raw, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	require.NoError(t, err)
	var spec transcodev1alpha1.TranscodeProfileSpec
	require.NoError(t, json.Unmarshal(raw, &spec))
	measured, err := inprocess.Engine{}.Measure(ctx, transcode.HardwareNVIDIA)
	require.NoError(t, err)

	clips, err := filepath.Glob(filepath.Join(dir, "*.mkv"))
	require.NoError(t, err)
	out := t.TempDir()
	clips = append(clips, synthesizeHLG(t, out))
	var rows []string
	for _, clip := range clips {
		class := strings.TrimSuffix(filepath.Base(clip), ".mkv")
		if strings.Contains(class, ".") {
			continue // an output of an earlier run
		}
		t.Run(class, func(t *testing.T) {
			srcMI, srcRaw, err := ffprobeexec.Probe(ctx, clip)
			require.NoError(t, err)
			info, err := transcode.FromProbe(srcMI, srcRaw)
			require.NoError(t, err)
			info.Path = clip

			sp := standard.Plan(info, worker.StandardProfile("hevc-mkv", "parity", spec),
				standard.Hardware{Tier: transcode.TierNVENC, Limits: measured.Limits})
			if strings.HasSuffix(class, "-synthetic") {
				require.NotEqual(t, standard.DecisionSkip, sp.Decision, "a synthetic clip exists to be encoded: %s", sp.Reason)
			}
			stdOut, stdSecs := filepath.Join(out, class+".standard.mkv"), 0.0
			if sp.Decision != standard.DecisionSkip {
				start := time.Now()
				if _, err := engine.Run(ctx, sp, clip, stdOut, engine.Options{HWDevice: dev}); err != nil {
					t.Fatalf("standard: %v", err)
				}
				stdSecs = time.Since(start).Seconds()
				checkStandard(t, srcMI, srcRaw, sp, stdOut)
			}
			rows = append(rows, strings.Join([]string{
				class, string(sp.Decision), secs(stdSecs), sp.Video.Encoder + "/" + sp.Video.Decode,
			}, "\t"))
		})
	}
	t.Logf("\nclass\tdecision\tseconds\tencoder/decode\n%s", strings.Join(rows, "\n"))
}

// TestTheInProcessProbeAgreesOnTheLibrary holds the worker's in-process
// probe to ffprobe's on every real clip -- Dolby Vision 5, 7 and 8.1, HDR10+,
// PGS, TrueHD, fonts -- which the generated fixtures of
// app/squash/worker/inprocess cannot make: catalogarr plans from ffprobe's
// summary and the worker from its own probe, so a field they disagree on is
// a different decision. Codec profile names are ffprobe's alone.
func TestTheInProcessProbeAgreesOnTheLibrary(t *testing.T) {
	ctx := context.Background()
	dir := os.Getenv("CLUSTARR_PARITY_DIR")
	if dir == "" {
		t.Skip("CLUSTARR_PARITY_DIR names no clips (hack/parity-clips.sh)")
	}
	eng, err := inprocess.New()
	if err != nil {
		t.Skipf("no FFmpeg 9: %v", err)
	}
	clips, err := filepath.Glob(filepath.Join(dir, "*.mkv"))
	require.NoError(t, err)
	for _, clip := range clips {
		class := strings.TrimSuffix(filepath.Base(clip), ".mkv")
		if strings.Contains(class, ".") {
			continue // an output of an earlier run
		}
		t.Run(class, func(t *testing.T) {
			want, wantRaw, err := ffprobeexec.Probe(ctx, clip)
			require.NoError(t, err)
			got, gotRaw, err := eng.Probe(ctx, clip)
			require.NoError(t, err)
			want.VideoProfile = ""
			for i := range want.Audio {
				want.Audio[i].Profile = ""
			}
			require.Equal(t, want, got)
			require.Equal(t, wantRaw.Dovi, gotRaw.Dovi, "the Dolby Vision record")
			require.Equal(t, wantRaw.MasteringDisplay, gotRaw.MasteringDisplay)
			require.Equal(t, wantRaw.ContentLight, gotRaw.ContentLight)
			plan := func(mi *commonv1.MediaInfo, raw *mediainfo.Raw) string {
				info, err := transcode.FromProbe(mi, raw)
				require.NoError(t, err)
				return standard.Plan(info, standard.Profile{Name: "p", Hash: "h", Quality: 24},
					standard.Hardware{Tier: transcode.TierNVENC}).Hash()
			}
			require.Equal(t, plan(want, wantRaw), plan(got, gotRaw), "the same standard plan")
		})
	}
}

// checkStandard holds the standard's output to spec §1 against its source.
func checkStandard(t *testing.T, src *commonv1.MediaInfo, srcRaw *mediainfo.Raw, plan standard.Result, out string) {
	t.Helper()
	got, gotRaw, err := ffprobeexec.Probe(context.Background(), out)
	require.NoError(t, err)
	if got.VideoCodec != "hevc" {
		t.Errorf("video %s, want hevc", got.VideoCodec)
	}
	if plan.Decision == standard.DecisionEncode {
		want := int32(10)
		if plan.Expect.PixelFormat == "yuv420p" {
			want = 8
		}
		if got.VideoBitDepth != want {
			t.Errorf("bit depth %d, want %d", got.VideoBitDepth, want)
		}
	}
	if len(got.Audio) != len(plan.Audio) {
		t.Errorf("%d audio tracks, the plan keeps %d", len(got.Audio), len(plan.Audio))
	}
	for i, a := range got.Audio {
		if !slices.Contains([]string{"aac", "ac3", "eac3"}, a.Codec) || (a.Codec == "aac" && a.Channels > 6) {
			t.Errorf("audio track %s %d channels is not Apple TV direct play", a.Codec, a.Channels)
		}
		if i < len(plan.Audio) && plan.Audio[i].Language != "" && a.Language != plan.Audio[i].Language {
			t.Errorf("audio track %d is %q, the plan keeps %q", i, a.Language, plan.Audio[i].Language)
		}
	}
	if len(got.Subtitles) != len(src.Subtitles) || got.Attachments != src.Attachments || got.Chapters != src.Chapters {
		t.Errorf("subtitles %d/%d, attachments %d/%d, chapters %d/%d (output/source)",
			len(got.Subtitles), len(src.Subtitles), got.Attachments, src.Attachments, got.Chapters, src.Chapters)
	}
	switch src.Hdr {
	case commonv1.HdrFormatHDR10, commonv1.HdrFormatHDR10Plus, commonv1.HdrFormatDolbyVisionHDR10:
		if got.Hdr != commonv1.HdrFormatHDR10 {
			t.Errorf("HDR %s out of %s, want hdr10", got.Hdr, src.Hdr)
		}
		if srcRaw.MasteringDisplay != nil && (gotRaw.MasteringDisplay == nil || *gotRaw.MasteringDisplay != *srcRaw.MasteringDisplay) {
			t.Errorf("mastering display %+v, source %+v", gotRaw.MasteringDisplay, srcRaw.MasteringDisplay)
		}
		if srcRaw.ContentLight != nil && (gotRaw.ContentLight == nil || *gotRaw.ContentLight != *srcRaw.ContentLight) {
			t.Errorf("content light (MaxCLL/MaxFALL) %+v, source %+v", gotRaw.ContentLight, srcRaw.ContentLight)
		}
		if gotRaw.ColorTransfer != "smpte2084" {
			t.Errorf("transfer %q out of %s, want smpte2084 (PQ)", gotRaw.ColorTransfer, src.Hdr)
		}
	case commonv1.HdrFormatHLG10:
		if got.Hdr != commonv1.HdrFormatHLG10 || gotRaw.ColorTransfer != "arib-std-b67" {
			t.Errorf("HDR %s transfer %q out of HLG, want hlg / arib-std-b67", got.Hdr, gotRaw.ColorTransfer)
		}
	}
	if d := got.RuntimeMillis - src.RuntimeMillis; d > 1000 || d < -1000 {
		t.Errorf("runtime %d ms, source %d ms", got.RuntimeMillis, src.RuntimeMillis)
	}
}

// synthesizeHLG writes a clip of the one class the owner's library has none
// of (2026-10-01): HLG, as 10-bit H.264 so the standard encodes it, with a
// FLAC track in Japanese, which it must encode to AAC and keep the language
// of. It runs 70 s, past the profile's policy.minDuration (1m).
func synthesizeHLG(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "hlg-synthetic.mkv")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=24:duration=70",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=70,aformat=channel_layouts=stereo",
		"-map", "0", "-map", "1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-color_primaries", "bt2020", "-color_trc", "arib-std-b67", "-colorspace", "bt2020nc", "-color_range", "tv",
		"-c:a", "flac", "-metadata:s:a:0", "language=jpn", path)
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, string(b))
	return path
}

func secs(s float64) string {
	if s == 0 {
		return "-"
	}
	return time.Duration(s * float64(time.Second)).Round(100 * time.Millisecond).String()
}
