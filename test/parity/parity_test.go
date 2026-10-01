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
	var rows []string
	for _, clip := range clips {
		class := strings.TrimSuffix(filepath.Base(clip), ".mkv")
		if strings.Contains(class, ".") {
			continue // an output of an earlier run
		}
		t.Run(class, func(t *testing.T) {
			srcMI, srcRaw, err := mediainfo.Probe(ctx, clip)
			require.NoError(t, err)
			info, err := transcode.FromProbe(srcMI, srcRaw)
			require.NoError(t, err)
			info.Path = clip

			sp := standard.Plan(info, worker.StandardProfile("hevc-mkv", "parity", spec),
				standard.Hardware{Tier: transcode.TierNVENC, Limits: measured.Limits})
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

// checkStandard holds the standard's output to spec §1 against its source.
func checkStandard(t *testing.T, src *commonv1.MediaInfo, srcRaw *mediainfo.Raw, plan standard.Result, out string) {
	t.Helper()
	got, gotRaw, err := mediainfo.Probe(context.Background(), out)
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
	for _, a := range got.Audio {
		if !slices.Contains([]string{"aac", "ac3", "eac3"}, a.Codec) || (a.Codec == "aac" && a.Channels > 6) {
			t.Errorf("audio track %s %d channels is not Apple TV direct play", a.Codec, a.Channels)
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
	}
	if d := got.RuntimeMillis - src.RuntimeMillis; d > 1000 || d < -1000 {
		t.Errorf("runtime %d ms, source %d ms", got.RuntimeMillis, src.RuntimeMillis)
	}
}

func secs(s float64) string {
	if s == 0 {
		return "-"
	}
	return time.Duration(s * float64(time.Second)).Round(100 * time.Millisecond).String()
}
