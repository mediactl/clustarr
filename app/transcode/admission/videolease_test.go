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

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
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
