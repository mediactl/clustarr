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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
)

// TestOrphaned is final-review C1's predicate: a dispatched job whose
// profile is gone, or whose jobRef is not the current profile's pool for
// its class, is orphaned; a live one, or one not dispatched, is not.
func TestOrphaned(t *testing.T) {
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc", UID: "uid-new"}}
	recreated := pool.Name(pool.Key{Profile: "hevc", ProfileUID: "uid-new", Class: "cpu"})
	previous := pool.Name(pool.Key{Profile: "hevc", ProfileUID: "uid-old", Class: "cpu"})
	job := func(phase transcodev1alpha1.TranscodeJobPhase, hw transcodev1alpha1.Hardware, ref *string) *transcodev1alpha1.TranscodeJob {
		return &transcodev1alpha1.TranscodeJob{Status: transcodev1alpha1.TranscodeJobStatus{Phase: phase, Hardware: hw, JobRef: ref}}
	}
	queued, running, planned := transcodev1alpha1.TranscodeJobPhaseQueued, transcodev1alpha1.TranscodeJobPhaseRunning,
		transcodev1alpha1.TranscodeJobPhasePlanned

	assert.False(t, orphaned(job(queued, "cpu", &recreated), tp), "dispatched to its profile's pool")
	assert.False(t, orphaned(job(running, "cpu", &recreated), tp))
	assert.True(t, orphaned(job(queued, "cpu", &recreated), nil), "its profile is gone")
	assert.True(t, orphaned(job(running, "cpu", &previous), tp), "dispatched to the old UID's pool")
	assert.True(t, orphaned(job(queued, "nvidia", &recreated), tp), "jobRef is not the pool of its class")
	assert.True(t, orphaned(job(queued, "cpu", nil), tp), "adopted while its profile was gone")
	assert.True(t, orphaned(job(running, "cpu", ptr.To("heat-hevc-transcode")), tp), "a pre-pool job's own batch Job")
	assert.False(t, orphaned(job(planned, "cpu", &previous), nil), "not dispatched: holds nothing")
}
