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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

var updateGolden = os.Getenv("UPDATE_GOLDEN") == "1"

// goldenPath resolves name to its golden fixture, relative to the package
// directory (Go tests run with the package directory as CWD), so the
// repo-root testdata/transcode/ tree is reached via ../../.
func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "transcode", "golden", name+".golden")
}

func assertGolden(t *testing.T, name string, got []string) {
	t.Helper()
	joined := strings.Join(got, "\n") + "\n"
	path := goldenPath(name)
	if updateGolden {
		require.NoError(t, os.WriteFile(path, []byte(joined), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file %s (run with UPDATE_GOLDEN=1 once, then hand-verify against the cited note section)", path)
	require.Equal(t, string(want), joined)
}

func fps24() transcode.Rational { return transcode.Rational{Num: 24, Den: 1} }

var testCaps = transcode.Capabilities{Encoders: map[transcode.Tier]bool{
	transcode.TierCPUx265: true, transcode.TierNVENC: true, transcode.TierQSV: true, transcode.TierVAAPI: true,
}}
var testMeta = transcode.PlanMeta{ProfileName: "hevc10-aac-space", ProfileHash: "abc12345", Threads: 8}

func TestArgsGoldenSDR1080pH264CPU(t *testing.T) {
	info := transcode.MediaInfo{
		Path:   "/media/movies/Example (2019)/Example (2019).mkv",
		Format: transcode.FormatInfo{Duration: 2 * time.Hour},
		Video: []transcode.VideoStream{{
			Codec: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080, FrameRate: fps24(),
			Disposition: transcode.Disposition{Default: true},
		}},
		Audio: []transcode.AudioStream{{
			Codec: "ac3", Channels: 6, ChannelLayout: "5.1", Language: "eng",
			Disposition: transcode.Disposition{Default: true},
		}},
	}
	plan, err := transcode.Plan(info, defaultProfile(), testCaps, testMeta)
	require.NoError(t, err)
	require.Equal(t, transcode.DecisionEncode, plan.Decision)
	require.Equal(t, transcode.TierCPUx265, plan.Tier)

	assertGolden(t, "sdr_1080p_h264_cpu", transcode.Args(plan))
}
