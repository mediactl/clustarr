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

package admission

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/transcode"
)

const intel = transcodev1alpha1.HardwareIntel

func TestVideoStreamsReadsTheLease(t *testing.T) {
	lease := func(n string, renewed time.Duration) *coordinationv1.Lease {
		rt := metav1.NewMicroTime(t0.Add(-renewed))
		return &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{VideoTranscodesAnnotation: n}},
			Spec:       coordinationv1.LeaseSpec{RenewTime: &rt},
		}
	}
	assert.Equal(t, 2, videoStreams(lease("2", 29*time.Second), t0))
	assert.Equal(t, 0, videoStreams(lease("2", 31*time.Second), t0), "a Lease not renewed for 30 s counts as none")
	assert.Equal(t, 0, videoStreams(lease("two", time.Second), t0))
	assert.Equal(t, 0, videoStreams(&coordinationv1.Lease{}, t0))
	assert.Equal(t, 0, videoStreams(nil, t0))
}

// intelIn is a pass on a cluster with an Intel node while Plex converts
// streams video conversions on the GPU pool.
func intelIn(streams int) PassInputs {
	in := passIn()
	in.GPUNodes = map[transcodev1alpha1.Hardware]bool{intel: true}
	in.VideoStreams = streams
	return in
}

func TestAFreshVideoCountHoldsNewIntelAdmissions(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	gpu := fv("g", 1, planned)
	gpu.Hardware = transcodev1alpha1.HardwareGPU
	l.Observe(gpu)
	l.Observe(fv("a", 2, planned)) // auto
	l.Pass(intelIn(1))

	assert.Equal(t, 0, l.dispatchGrants(intel), "no new file is admitted to intel while Plex converts")
	assert.Equal(t, YieldingToPlex, l.Snapshot("g").WaitReason)
}

func TestAPinnedIntelFileWaitsForPlex(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	v := fv("i", 1, planned)
	v.Hardware = intel
	l.Observe(v)
	l.Pass(intelIn(2))

	assert.Equal(t, 0, l.dispatchGrants(intel))
	assert.Equal(t, YieldingToPlex, l.Snapshot("i").WaitReason)
}

func TestRunningIntelEncodesKeepRunningBesideAStream(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	r := fv("r", 1, running)
	r.Class = intel
	l.Observe(r)
	l.Pass(intelIn(3))

	assert.Equal(t, running, l.phaseOf("r"))
	assert.Equal(t, 1, l.inFlight(intel), "a running encode keeps its slot")
	assert.Empty(t, l.Snapshot("r").WaitReason)
}

func TestAdmissionResumesOnceTheCountIsZero(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	gpu := fv("g", 1, planned)
	gpu.Hardware = transcodev1alpha1.HardwareGPU
	l.Observe(gpu)
	l.Pass(intelIn(1))
	assert.Equal(t, 0, l.dispatchGrants(intel))

	l.Pass(intelIn(0))
	assert.Equal(t, 1, l.dispatchGrants(intel), "the held file is admitted once no stream is live")
}

func TestVideoAllowanceReservesTheStreamsEnginesWhole(t *testing.T) {
	for _, c := range []struct{ engines, streams, want int }{
		{2, 1, 1}, {2, 4, 1}, {2, 5, 0}, {2, 8, 0}, {2, 9, 0},
		{1, 1, 0}, {0, 1, 0}, {3, 1, 2}, {3, 5, 1}, {2, 0, 2}, {0, 0, 1},
	} {
		assert.Equal(t, c.want, VideoAllowance(c.engines, c.streams), "engines %d streams %d", c.engines, c.streams)
	}
}

// twoEngines is a pass whose intel pods measured talos-88's Arc 140T.
func twoEngines(streams int) PassInputs {
	in := intelIn(streams)
	in.Devices = map[transcodev1alpha1.Hardware]transcodeplan.Device{intel: {
		Tier:   transcode.TierQSV,
		Limits: map[transcode.Tier]transcode.Limits{transcode.TierQSV: {VideoEngines: 2}, transcode.TierVAAPI: {VideoEngines: 2}},
	}}
	return in
}

func TestOneEncodeRunsBesideAStreamOnATwoEngineGPU(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	for _, uid := range []string{"a", "b"} {
		v := fv(uid, 1, planned)
		v.Hardware = intel
		l.Observe(v)
	}
	l.Pass(twoEngines(1))
	assert.Equal(t, 1, l.dispatchGrants(intel), "the stream's engine is reserved; the other takes one encode")
	held := 0
	for _, uid := range []string{"a", "b"} {
		if r := l.Snapshot(types.UID(uid)).WaitReason; r != "" {
			held++
			assert.Equal(t, YieldingToPlex+" (1 stream(s); 1 encode(s) run beside them)", r)
		}
	}
	assert.Equal(t, 1, held)
}

func TestFiveStreamsTakeBothEngines(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	v := fv("a", 1, planned)
	v.Hardware = intel
	l.Observe(v)
	l.Pass(twoEngines(5))
	assert.Equal(t, 0, l.dispatchGrants(intel))
	assert.Equal(t, YieldingToPlex, l.Snapshot("a").WaitReason)
}

func TestARunningEncodeFillsTheAllowanceBesideAStream(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	r := fv("r", 1, running)
	r.Class = intel
	l.Observe(r)
	p := fv("p", 2, planned)
	p.Hardware = intel
	l.Observe(p)
	l.Pass(twoEngines(1))
	assert.Equal(t, 0, l.dispatchGrants(intel), "the running encode holds the one engine the stream leaves")
	assert.Equal(t, running, l.phaseOf("r"))
	l.Pass(twoEngines(0))
	assert.Equal(t, 1, l.dispatchGrants(intel), "the second engine opens once no stream runs")
}
