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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// The controller plans with what the class's pool workers measured, so the
// plan it records -- plan hash and Planned message -- is the one the worker
// runs: a published NVDEC measurement decides the decode path, and with
// none published the static list does.
func TestTheControllerPlansWithThePublishedDeviceLimits(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	r := &Reconciler{Bus: bus}

	tp := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hevc-mkv"},
		Spec:       transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareNVIDIA},
		Status:     transcodev1alpha1.TranscodeProfileStatus{Hash: "abcdef0123"},
	}
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "m-abcdef01", Namespace: "media"},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: "m", ProfileRef: "hevc-mkv", SourcePath: "/data/movies/M/M.mkv"},
	}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "media"},
		Spec:       catalogv1alpha1.MediaFileSpec{Path: "/data/movies/M/M.mkv", SizeBytes: 4 << 30},
		Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
			Container: "mkv", VideoCodec: "h264", PixelFormat: "yuv420p", VideoBitDepth: 8,
			Width: 1920, Height: 1080, FpsMilli: 24000, RuntimeMillis: 2 * 60 * 60 * 1000,
			Audio: []commonv1.AudioStream{{Index: 1, Codec: "aac", Channels: 2, Language: "eng", Default: true}},
		}},
	}

	p, fail := planFor(tj, tp, mf, nil, r.encoderLimits(ctx, tj, tp, nil))
	require.Nil(t, fail)
	assert.Equal(t, "nvdec", p.plan.Video.Decode, "nothing published: the static list decodes 8-bit H.264")

	require.NoError(t, task.PublishEncoderLimits(ctx, bus.KV(events.BucketProgress), "nvidia", "laptop",
		transcode.Limits{NVDEC: &transcode.Decoders{Formats: map[string]bool{"h264:8": false}}}, time.Now()))
	p, fail = planFor(tj, tp, mf, nil, r.encoderLimits(ctx, tj, tp, nil))
	require.Nil(t, fail)
	assert.Equal(t, "upload", p.plan.Video.Decode, "the device measured no NVDEC for it")

	cpu := transcodev1alpha1.HardwareCPU
	assert.Nil(t, r.encoderLimits(ctx, tj, tp, &cpu), "a cpu plan reads no GPU class's limits")
}
