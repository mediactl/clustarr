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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
)

// defaultProfile is the shared fixture the rest of this file's steps reuse.
func defaultProfile() transcode.ProfileSpec {
	return transcode.ProfileSpec{
		Container: transcode.ContainerMKV,
		Hardware:  transcode.HardwareCPU,
		Video: transcode.VideoSpec{
			Codec: "hevc", PixelFormat: "yuv420p10le", Profile: "main10",
			CRF:          transcode.CRFTable{SD: 21, HD: 22, UHD: 23, HDROffset: -1},
			Preset:       "slow",
			KeyintFactor: 10, BFrames: 8, Refs: 4, RCLookahead: 40, AQMode: 3,
		},
		Audio: transcode.AudioSpec{
			Codec: "aac", BitratePerChannelKbps: 64,
			KeepOriginal: transcode.KeepOriginalAtmos, DropCommentary: true,
		},
		Subtitles: transcode.SubSpec{CopyText: true, CopyBitmap: true, CopyAttachments: true},
		HDR:       transcode.HDRSpec{HDR10Plus: transcode.HDR10PlusDrop, DolbyVision: transcode.DolbyVisionPassthrough},
		Policy: transcode.PolicySpec{
			SkipIfCompliant: true, RemuxOnlyWhenVideoCompliant: true,
			NeverTranscodeModifiers:  []string{"remux", "brdisk"},
			MinDuration:              time.Minute,
			MaxOutputToSourcePercent: 100,
			ReplaceSource:            true, RecycleBin: true,
		},
		Verify: transcode.VerifySpec{PacketCount: true},
	}
}

func TestProfileHashIsDeterministicAndSensitiveToChange(t *testing.T) {
	a := transcode.ProfileHash(defaultProfile())
	b := transcode.ProfileHash(defaultProfile())
	require.Equal(t, a, b, "hashing the same spec twice must be stable")

	changed := defaultProfile()
	changed.Video.CRF.HD = 21
	require.NotEqual(t, a, transcode.ProfileHash(changed), "a changed field must change the hash")
}
