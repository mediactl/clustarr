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

package native

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// The committed fixtures, read by the in-process probe as ffprobe read them
// (moved from pkg/mediainfo/ffprobeexec with its probe, spec 2026-10-06 §6.8;
// "Constrained Baseline" and "Main 10" prove F11a).

func TestProbeH264MP4(t *testing.T) {
	mi, raw, err := testProber(t).Probe(context.Background(), "../../../test/data/mediainfo/sample_h264_8bit.mp4")
	require.NoError(t, err)

	assert.Equal(t, "mp4", mi.Container)
	assert.Equal(t, "h264", mi.VideoCodec)
	assert.Equal(t, "Constrained Baseline", mi.VideoProfile)
	assert.Equal(t, int32(8), mi.VideoBitDepth)
	assert.Equal(t, int32(320), mi.Width)
	assert.Equal(t, int32(240), mi.Height)
	assert.Equal(t, int32(24000), mi.FpsMilli)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	require.Len(t, mi.Audio, 1)
	assert.Equal(t, "aac", mi.Audio[0].Codec)
	assert.Empty(t, mi.Subtitles)
	assert.Equal(t, int32(0), mi.Chapters)
	require.NotNil(t, raw.Format)
	assert.Contains(t, raw.Format.FormatName, "mp4")
	assert.Nil(t, raw.Dovi)
}

func TestProbeHEVC10bitMKV(t *testing.T) {
	mi, raw, err := testProber(t).Probe(context.Background(), "../../../test/data/mediainfo/sample_hevc_10bit.mkv")
	require.NoError(t, err)

	assert.Equal(t, "mkv", mi.Container)
	assert.Equal(t, "hevc", mi.VideoCodec)
	assert.Equal(t, "Main 10", mi.VideoProfile)
	assert.Equal(t, "yuv420p10le", mi.PixelFormat)
	assert.Equal(t, int32(10), mi.VideoBitDepth)
	assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
	require.Len(t, mi.Audio, 1)
	assert.Equal(t, "ac3", mi.Audio[0].Codec)
	assert.Equal(t, int32(128), mi.Audio[0].BitrateKbps)
	require.Len(t, mi.Subtitles, 1)
	assert.Equal(t, "subrip", mi.Subtitles[0].Codec)
	assert.False(t, mi.Subtitles[0].Forced)
	assert.Contains(t, raw.Format.FormatName, "matroska")
	assert.Nil(t, raw.Dovi)
}

// A file that is not media -- empty, or bytes no demuxer reads, as a fake
// release's encrypted "remux" on talos was (2026-10-15) -- is
// mediainfo.ErrNotMedia, which the import refuses as the release's fault. A
// file that is not there is not one: nothing is known about its bytes.
func TestProbeOfAFileThatIsNotMediaIsErrNotMedia(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.mkv")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))
	junk := filepath.Join(dir, "junk.mkv")
	b := make([]byte, 1<<20)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	require.NoError(t, os.WriteFile(junk, b, 0o644))
	for _, p := range []string{empty, junk} {
		_, _, err := testProber(t).Probe(context.Background(), p)
		require.ErrorIs(t, err, mediainfo.ErrNotMedia, filepath.Base(p))
	}
	_, _, err := testProber(t).Probe(context.Background(), filepath.Join(dir, "missing.mkv"))
	require.Error(t, err)
	require.NotErrorIs(t, err, mediainfo.ErrNotMedia)
}

func TestProbeMissingFileReturnsWrappedError(t *testing.T) {
	_, _, err := testProber(t).Probe(context.Background(), "../../../test/data/mediainfo/does-not-exist.mkv")
	require.Error(t, err)
}
