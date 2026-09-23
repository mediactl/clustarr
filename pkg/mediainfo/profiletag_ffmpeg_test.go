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

package mediainfo_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// TestProbeReadsTheTagSquasharrWrites builds its input through the real
// producer: the argv pkg/transcode.Args renders for a plan carrying the
// CLUSTARR_PROFILE tag, run by a real ffmpeg, then read back by the real
// Probe. A fixture shaped like the answer -- a hand-written ffprobe JSON with
// the key already in it -- would pass whatever the muxer actually does with
// the tag.
//
// Both output containers a TranscodeProfile can name are covered, because
// the muxers differ: matroska writes any global tag, while the mp4 muxer
// silently drops a key it does not know unless -movflags carries
// +use_metadata_tags -- so without that flag in Args, every mp4 transcode
// would read back as an untouched original and stay upgradeable forever.
func TestProbeReadsTheTagSquasharrWrites(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}

	const tag = "hevc-main10@0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	src := "../../testdata/mediainfo/sample_hevc_10bit.mkv"
	for _, c := range []struct {
		container transcode.Container
		file      string
		videoArgs []string
	}{
		{container: transcode.ContainerMKV, file: "out.mkv", videoArgs: []string{"-c:v", "copy"}},
		// -tag:v hvc1 is what transcode.Plan adds for an mp4 output.
		{container: transcode.ContainerMP4, file: "out.mp4", videoArgs: []string{"-c:v", "copy", "-tag:v", "hvc1"}},
	} {
		t.Run(string(c.container), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), c.file)
			plan := &transcode.PlanResult{
				Decision:  transcode.DecisionRemuxOnly,
				Container: c.container,
				Input:     src,
				Output:    out,
				Maps:      []string{"-map", "0:v:0"},
				VideoArgs: c.videoArgs,
				Tags:      map[string]string{mediainfo.ProfileTagKey: tag},
			}
			args := transcode.Args(plan)
			require.Contains(t, args, mediainfo.ProfileTagKey+"="+tag, "the argv must carry the tag this test reads back")
			cmd := exec.CommandContext(t.Context(), ffmpeg, args...)
			outBytes, err := cmd.CombinedOutput()
			require.NoError(t, err, "ffmpeg: %s", outBytes)

			mi, _, err := mediainfo.Probe(context.Background(), out)
			require.NoError(t, err)
			assert.Equal(t, tag, mi.TranscodeProfile, "a file squasharr wrote must read back as transcoded")
		})
	}

	untagged, _, err := mediainfo.Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Empty(t, untagged.TranscodeProfile, "the untouched source carries no tag")
}
