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

package standard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// UPDATE_GOLDEN=1 rewrites them, as pkg/transcode's argv goldens; hand-check
// each against spec §1 before committing.
var updateGolden = os.Getenv("UPDATE_GOLDEN") == "1"

func TestPlanGoldens(t *testing.T) {
	for name, c := range map[string]struct {
		info transcode.MediaInfo
		hw   Hardware
	}{
		"cpu_sdr_h264":         {info(h264, eac3), cpu},
		"nvenc_nvdec_sdr_h264": {info(h264, eac3), nvenc},
		"nvenc_upload_hi10p":   {info(video("h264", "yuv420p10le", 10, commonv1.HdrFormatNone), eac3), nvenc},
		"cpu_hdr10":            {info(video("hevc", "yuv420p", 8, commonv1.HdrFormatHDR10), eac3), cpu},
		"copyvideo_truehd":     {info(hevc10, audio(0, "truehd", 8, "7.1", "eng")), cpu},
		"skip_dovi5":           {info(video("hevc", "yuv420p10le", 10, commonv1.HdrFormatDolbyVision), eac3), nvenc},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(Plan(c.info, profile, c.hw), "", "  ")
			require.NoError(t, err)
			path := filepath.Join("..", "..", "..", "test", "data", "transcode", "standard", name+".json")
			if updateGolden {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "missing golden %s (run with UPDATE_GOLDEN=1 once, then check it against spec §1)", path)
			require.JSONEq(t, string(want), string(got))
		})
	}
}
