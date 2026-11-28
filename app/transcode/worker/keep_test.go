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

package worker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// sizingEngine writes an encode's output encodeBytes long and a remux's
// copyBytes long, and records each plan it ran.
type sizingEngine struct {
	*scriptedEngine
	encodeBytes, copyBytes int

	plansMu sync.Mutex
	plans   []standard.Result
}

func (e *sizingEngine) Encode(_ context.Context, plan standard.Result, _ transcode.Tier, _, output string,
	_ func(transcode.Progress),
) (transcode.EncodeResult, error) {
	e.plansMu.Lock()
	e.plans = append(e.plans, plan)
	e.plansMu.Unlock()
	n := e.copyBytes
	if plan.Decision == standard.DecisionEncode {
		n = e.encodeBytes
	}
	return transcode.EncodeResult{}, os.WriteFile(output, bytes.Repeat([]byte("x"), n), 0o644)
}

// sdr1080Reading is a probe of a 1080p 8-bit SDR H.264 WEB-DL, the source
// the standard encodes only to save space.
func sdr1080Reading(path string) (*commonv1alpha1.MediaInfo, *mediainfo.Raw) {
	raw := &mediainfo.Raw{
		Format: &ffprobe.Format{Filename: path, FormatName: "matroska,webm", DurationSeconds: 1554},
		Streams: []*ffprobe.Stream{{
			Index: 0, CodecType: string(ffprobe.StreamVideo), CodecName: "h264", PixFmt: "yuv420p",
			Width: 1920, Height: 1080, RFrameRate: "24/1",
		}},
	}
	return mediainfo.FromRaw(raw), raw
}

// keepCase is a safetyCase over a source read by reading, whose encode
// comes out encodeBytes long and whose remux copyBytes long.
func keepCase(t *testing.T, reading func(string) (*commonv1alpha1.MediaInfo, *mediainfo.Raw), encodeBytes, copyBytes int,
) (*safetyCase, *sizingEngine) {
	t.Helper()
	c := newSafetyCase(t, "sdr", "sdr")
	mi, raw := reading(c.local)
	c.tk.PlanHash = planHashOf(t, mi, raw, c.tk)
	eng := &sizingEngine{
		scriptedEngine: &scriptedEngine{source: c.local, mi: mi, raw: raw},
		encodeBytes:    encodeBytes, copyBytes: copyBytes,
	}
	c.eng = eng.scriptedEngine
	return c, eng
}

func (c *safetyCase) processWith(t *testing.T, eng Engine) Outcome {
	t.Helper()
	dataDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(c.local))))
	return Process(context.Background(), c.tk, Options{
		DataDir: dataDir, Engine: eng, Measurement: &transcode.Measurement{Tier: transcode.TierCPUx265},
	})
}

// An 8-bit SDR H.264 encode no smaller than its source keeps the source's
// video (owner, 2026-11-15; Spare Me, Great Lord! S01E11 at 110%): the
// refused encode is removed, the remux runs in the same attempt and
// replaces the source, and the result names the remux and the encode's
// size, so the loop records the file as final instead of Failed.
func TestAnH264EncodeNoSmallerThanItsSourceIsReplacedByARemux(t *testing.T) {
	src := len("the original, byte for byte")
	enc := src*110/100 + 1
	c, eng := keepCase(t, sdr1080Reading, enc, src-5)
	out := c.processWith(t, eng)
	require.Equal(t, ExitOK, out.Code, "%+v", out.Err)

	require.Len(t, eng.plans, 2, "the encode, then the remux")
	assert.Equal(t, standard.DecisionEncode, eng.plans[0].Decision)
	keep := eng.plans[1]
	assert.Equal(t, standard.DecisionCopyVideo, keep.Decision)
	assert.Equal(t, standard.VideoKept, keep.Tags[standard.VideoTagKey])

	require.NotNil(t, out.Result)
	require.NotNil(t, out.Result.KeptVideo)
	assert.Equal(t, keep.Hash(), out.Result.KeptVideo.PlanHash)
	assert.Equal(t, sizePercent(int64(enc), int64(src)), out.Result.KeptVideo.EncodePercent)
	assert.Greater(t, out.Result.KeptVideo.EncodePercent, int32(100))
	got, err := os.ReadFile(c.local)
	require.NoError(t, err)
	assert.Len(t, got, src-5, "the remux replaced the source")
	for _, name := range listDir(c.dir) {
		_, isPart := fsopsPart(name)
		assert.Falsef(t, isPart, "no part file is left: %s", name)
	}
}

// An encode that fits is the output, with no remux; and an encode the file
// needs to play -- here 10-bit video, which has no kept form -- that comes
// out larger than its source is the output too, up to
// RequiredEncodeLimitPercent (owner, 2026-11-27: "If it's another compressed
// codec, we can still go through with the transcode but can be more liberal
// about the output size"; 131 lean 10-bit HEVC files had failed at 100-291%
// of their sources). Past that it is refused as a runaway, as it was.
func TestOnlyAnOversizedSpaceSavingEncodeIsKept(t *testing.T) {
	src := len("the original, byte for byte")
	c, eng := keepCase(t, sdr1080Reading, src-10, src-5)
	out := c.processWith(t, eng)
	require.Equal(t, ExitOK, out.Code, "%+v", out.Err)
	assert.Len(t, eng.plans, 1, "the encode fit")
	assert.Nil(t, out.Result.KeptVideo)

	hi10p := func(path string) (*commonv1alpha1.MediaInfo, *mediainfo.Raw) { return uhdReading(path, "sdr") }
	c, eng = keepCase(t, hi10p, src*2, src-5)
	out = c.processWith(t, eng)
	require.Equal(t, ExitOK, out.Code, "%+v", out.Err)
	assert.Len(t, eng.plans, 1, "the encode is the output: there is no remux of video that does not play")
	assert.Nil(t, out.Result.KeptVideo)
	got, err := os.ReadFile(c.local)
	require.NoError(t, err)
	assert.Len(t, got, src*2, "the encode replaced the source at twice its size")

	c, eng = keepCase(t, hi10p, src*4, src-5)
	out = c.processWith(t, eng)
	assert.Equal(t, ExitVerifyFailed, out.Code, "%+v", out.Err)
	assert.Len(t, eng.plans, 1, "no remux")
	require.Error(t, out.Err)
	assert.Contains(t, out.Err.Error(), "output is 400% of the source, above the 300% an encode the file needs to play may reach")
	c.requireSourceUntouched(t)
}
