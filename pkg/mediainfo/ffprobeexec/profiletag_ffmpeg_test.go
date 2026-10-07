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

package ffprobeexec_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/ffprobeexec"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// TestProbeReadsTheTagSquasharrWrites builds its input through the real
// producer: the in-process engine (pkg/transcode/engine) running a plan
// that carries the CLUSTARR_PROFILE tag, read back by the real Probe. A
// fixture shaped like the answer -- a hand-written ffprobe JSON with the key
// already in it -- would pass whatever the muxer actually does with the tag.
//
// Both output containers a TranscodeProfile can name are covered, because
// the muxers differ: matroska writes any global tag, while the mp4 muxer
// silently drops a key it does not know unless movflags carries
// +use_metadata_tags -- so without that flag in the engine, every mp4
// transcode would read back as an untouched original and stay upgradeable
// forever.
func TestProbeReadsTheTagSquasharrWrites(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg libraries: %v", err)
	}
	if _, avc, _ := ffgo.Version(); avc>>16 != 63 {
		t.Skip("not FFmpeg 9")
	}

	const tag = "hevc-main10@0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	src := "../../../test/data/mediainfo/sample_hevc_10bit.mkv"
	for _, c := range []struct {
		container transcode.Container
		file      string
	}{
		{container: transcode.ContainerMKV, file: "out.mkv"},
		{container: transcode.ContainerMP4, file: "out.mp4"},
	} {
		t.Run(string(c.container), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), c.file)
			plan := standard.Result{
				Decision:  standard.DecisionCopyVideo,
				Container: c.container,
				Video:     standard.VideoPlan{Action: "copy"},
				Audio:     []standard.AudioPlan{{Action: "copy"}},
				Tags:      map[string]string{mediainfo.ProfileTagKey: tag},
			}
			_, err := engine.Run(t.Context(), plan, src, out, engine.Options{})
			require.NoError(t, err)

			mi, _, err := ffprobeexec.Probe(context.Background(), out)
			require.NoError(t, err)
			assert.Equal(t, tag, mi.TranscodeProfile, "a file squasharr wrote must read back as transcoded")
		})
	}

	untagged, _, err := ffprobeexec.Probe(context.Background(), src)
	require.NoError(t, err)
	assert.Empty(t, untagged.TranscodeProfile, "the untouched source carries no tag")
}
