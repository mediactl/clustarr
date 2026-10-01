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

package transcodejob

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// The controller plans with the device limits the class's pool workers
// published, so the plan it records -- argv hash and Planned message --
// is the one the worker runs; with none published it plans with the
// profile's values.
func TestTheControllerPlansWithThePublishedDeviceLimits(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	r := &Reconciler{Bus: bus}

	tp := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hevc-mkv"},
		Spec: transcodev1alpha1.TranscodeProfileSpec{
			Hardware: transcodev1alpha1.HardwareNVIDIA,
			Video: transcodev1alpha1.VideoSpec{
				Codec: "hevc", PixelFormat: "yuv420p10le", Profile: "main10", BFrames: 8, RCLookahead: 32,
				NVENC: transcodev1alpha1.NVENCSpec{Preset: "p6", Tune: "hq", CQ: 24, Multipass: "fullres", BRefMode: "middle"},
			},
		},
		Status: transcodev1alpha1.TranscodeProfileStatus{Hash: "abcdef0123"},
	}
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "m-abcdef01", Namespace: "media"},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: "m", ProfileRef: "hevc-mkv", SourcePath: "/data/movies/M/M.mkv"},
	}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "media"},
		Spec:       catalogv1alpha1.MediaFileSpec{Path: "/data/movies/M/M.mkv", SizeBytes: 4 << 30},
		Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
			Container: "matroska", VideoCodec: "h264", PixelFormat: "yuv420p", VideoBitDepth: 8,
			Width: 1920, Height: 1080, FpsMilli: 24000, RuntimeMillis: 2 * 60 * 60 * 1000,
			Audio: []commonv1.AudioStream{{Index: 1, Codec: "aac", Channels: 2, Language: "eng", Default: true}},
		}},
	}
	bf := func(p planning) string {
		for i, a := range p.result.VideoArgs {
			if a == "-bf" && i+1 < len(p.result.VideoArgs) {
				return p.result.VideoArgs[i+1]
			}
		}
		return ""
	}

	p, fail := planFor(tj, tp, mf, nil, r.encoderLimits(ctx, tj, tp, nil), "")
	require.Nil(t, fail)
	assert.Equal(t, "8", bf(p), "nothing published: the profile's value")

	require.NoError(t, task.PublishEncoderLimits(ctx, bus.KV(events.BucketProgress), "nvidia", "laptop",
		transcode.Limits{MaxBFrames: ptr.To[int32](5)}, time.Now()))
	p, fail = planFor(tj, tp, mf, nil, r.encoderLimits(ctx, tj, tp, nil), "")
	require.Nil(t, fail)
	assert.Equal(t, "5", bf(p))
	assert.Contains(t, p.result.Reason, "bFrames 8 → 5 (device limit)")

	cpu := transcodev1alpha1.HardwareCPU
	assert.Nil(t, r.encoderLimits(ctx, tj, tp, &cpu), "a cpu plan reads no GPU class's limits")
}
