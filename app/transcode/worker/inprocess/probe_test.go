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

package inprocess

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
	"github.com/mediactl/clustarr/test/goldens"
)

// TestTheInProcessProbeAgreesWithFFprobe: the worker reads a file through
// its engine (Engine.Probe, pkg/mediainfo/native) as ffprobe read it -- the
// probe goldens W6.23 recorded from ffprobeexec before its deletion are the
// oracle (W8.4's U2 amendment), never a live ffprobe. What the worker plans
// from must agree: the summary, the first frame's HDR leaves, and the
// standard's plan, so a worker never plans a file differently from the
// catalog probe the manager planned it from.
func TestTheInProcessProbeAgreesWithFFprobe(t *testing.T) {
	e := newEngine(t, transcode.HardwareCPU)
	clips := goldens.Clips(t, goldens.KindProbe)
	require.NotEmpty(t, clips)
	for name, path := range clips {
		t.Run(name, func(t *testing.T) {
			want := goldens.Probe(t, name)
			got, gotRaw, err := e.Probe(context.Background(), path)
			if want.ProbeError != "" {
				require.Error(t, err, "ffprobe failed on it: %s", want.ProbeError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, want.MediaInfo, got)
			require.NotNil(t, gotRaw)
			assert.Equal(t, want.Raw.MasteringDisplay, gotRaw.MasteringDisplay)
			assert.Equal(t, want.Raw.ContentLight, gotRaw.ContentLight)
			assert.Equal(t, want.Raw.ColorPrimaries, gotRaw.ColorPrimaries)
			assert.Equal(t, want.Raw.ColorTransfer, gotRaw.ColorTransfer)
			assert.Equal(t, want.Raw.ColorSpace, gotRaw.ColorSpace)
			assert.Equal(t, want.Raw.Dovi, gotRaw.Dovi)
			assert.Equal(t, want.Raw.HasHDR10Plus, gotRaw.HasHDR10Plus)
			assert.Equal(t, want.FrameError != "", gotRaw.FrameErr != nil, "the first frame read, or not, alike")
			if want.MediaInfo == nil || want.MediaInfo.VideoCodec == "" {
				return // nothing the standard plans
			}

			// The tag is compared above; planned with it, both sides would
			// skip ("already transcoded") and agree about nothing else.
			planOf := func(mi *commonv1.MediaInfo, raw *mediainfo.Raw) standard.Result {
				info, err := transcode.FromProbe(mi, raw)
				require.NoError(t, err)
				delete(info.Tags, "CLUSTARR_PROFILE")
				delete(info.Tags, "clustarr_profile")
				return standard.Plan(info, standard.Profile{Name: "p", Hash: "h", Quality: 24}, standard.Hardware{Tier: transcode.TierCPUx265})
			}
			wantPlan, gotPlan := planOf(want.MediaInfo, want.Raw), planOf(got, gotRaw)
			if name == "everything.mkv" {
				require.Equal(t, standard.DecisionEncode, wantPlan.Decision, wantPlan.Reason)
				require.Len(t, wantPlan.Audio, 5, "every audio track is planned: two surround tracks with their AAC companions, and the commentary")
			}
			assert.Equal(t, wantPlan.Hash(), gotPlan.Hash(), "the same standard plan")
		})
	}
}
