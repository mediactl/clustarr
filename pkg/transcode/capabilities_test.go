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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestParseCapabilitiesFindsOurFourTiersInARealFfmpegEncodersDump(t *testing.T) {
	// Go tests run with the package directory as CWD, so the repo-root
	// testdata/transcode/ fixture tree is reached via ../../.
	b, err := os.ReadFile("../../test/data/transcode/fixtures/ffmpeg-encoders.txt")
	require.NoError(t, err)

	caps := transcode.ParseCapabilities(string(b))

	require.True(t, caps.Encoders[transcode.TierCPUx265], "libx265 line must map to cpu-x265")
	require.True(t, caps.Encoders[transcode.TierNVENC], "hevc_nvenc line must map to nvenc")
	require.True(t, caps.Encoders[transcode.TierQSV], "hevc_qsv line must map to qsv")
	require.True(t, caps.Encoders[transcode.TierVAAPI], "hevc_vaapi line must map to vaapi")
}

func TestParseCapabilitiesReportsAbsenceHonestly(t *testing.T) {
	caps := transcode.ParseCapabilities(" V....D libx265              libx265 H.265 / HEVC (codec hevc)\n")
	require.True(t, caps.Encoders[transcode.TierCPUx265])
	require.False(t, caps.Encoders[transcode.TierNVENC])
	require.False(t, caps.Encoders[transcode.TierQSV])
	require.False(t, caps.Encoders[transcode.TierVAAPI])
}

func TestProbeCapabilitiesAgainstTheRealBinary(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not present on this box")
	}
	caps, err := transcode.ProbeCapabilities(context.Background(), "/usr/bin/ffmpeg")
	require.NoError(t, err)
	require.True(t, caps.Encoders[transcode.TierCPUx265], "the dev box's ffmpeg build always has libx265")
}
