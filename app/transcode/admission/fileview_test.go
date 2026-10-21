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

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// A Failed or Skipped block records the priority of the attempt that ended,
// and the backlog is ordered by priority: a file whose verdict a new hash
// made stale, annotated transcode.clustarr.io/priority=100, kept the
// profile's 50 from its old block and waited in name order behind the H.264
// backlog (6 Mister Rogers files and 11 stream-cap files on talos,
// 2026-10-20). An ended block's priority is the eligibility's; a block still
// in the window keeps the priority it was admitted with, and with no
// eligibility this pass the block's is all there is.
func TestAnEndedBlockTakesTheEligibilitysPriority(t *testing.T) {
	mf := &metav1.ObjectMeta{Namespace: "media", Name: "f", UID: "f-uid"}
	view := func(phase catalogv1alpha1.TranscodePhase, e *transcodeplan.Eligibility) FileView {
		return FileViewOf(mf, &catalogv1alpha1.MediaFileStatus{ProbeHash: "p1", Transcode: &catalogv1alpha1.TranscodeState{
			Phase: phase, Priority: 50, ProfileHash: "old",
		}}, e)
	}
	eligible := &transcodeplan.Eligibility{Eligible: true, Hash: "new", Priority: 100}

	assert.Equal(t, int32(100), view(catalogv1alpha1.TranscodePhaseFailed, eligible).Priority)
	assert.Equal(t, int32(100), view(catalogv1alpha1.TranscodePhaseSkipped, eligible).Priority)
	assert.Equal(t, int32(50), view(catalogv1alpha1.TranscodePhaseQueued, eligible).Priority, "a window block keeps its own")
	assert.Equal(t, int32(50), view(catalogv1alpha1.TranscodePhaseFailed, nil).Priority, "no eligibility this pass")
}
