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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// dtsSync is the DTS core frame sync word.
var dtsSync = []byte{0x7f, 0xfe, 0x80, 0x01}

// damagedDTS is withAudio's 3 s clip with DTS 5.1 audio, the nth frame's
// header damaged so that it names channel arrangement 63, which no DTS has:
// FFmpeg's dca decoder refuses such a frame as "not yet implemented"
// (AVERROR_PATCHWELCOME), not as invalid data. Mandy's English track
// (2026-10-22) had three such frames among the 10,426 of its 31,000 that
// would not decode, and one failed the whole transcode.
func damagedDTS(t *testing.T, nth int) string {
	t.Helper()
	path := withAudio(t, "aformat=channel_layouts=5.1", []string{"-c:a", "dca", "-strict", "-2"}, "0")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	off, seen := 0, 0
	for {
		i := bytes.Index(b[off:], dtsSync)
		require.GreaterOrEqual(t, i, 0, "fewer than %d DTS frames in the clip", nth)
		off += i
		seen++
		if seen == nth {
			break
		}
		off += len(dtsSync)
	}
	// AMODE is bits 60-65 of the frame: the low nibble of byte 7 and the top
	// two bits of byte 8.
	b[off+7] |= 0x0f
	b[off+8] |= 0xc0
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

func TestAPacketTheDecoderRefusesAsUnimplementedIsSkippedAsDamaged(t *testing.T) {
	src := damagedDTS(t, 12)
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), audioPlan(standard.AudioPlan{
		SourceIndex: 0, Action: "aac", Channels: 2, Layout: "stereo", BitRate: 128000,
	}), src, out, Options{})
	require.NoError(t, err)
	// 3 s at 48 kHz; the one skipped frame's samples are silence, not lost.
	assert.InDelta(t, 3*48000, decodedSamples(t, out), 4096)
}
