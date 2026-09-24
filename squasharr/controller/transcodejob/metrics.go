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

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// Ruling R9: the controller owns the transcode metrics. The worker runs in a
// pool pod nothing scrapes for them, so anything it sets is lost.
//
// Every "tier" label here is the SLOT class -- cpu, nvidia or intel -- the
// same three values --slots budgets, never an encoder or a title. The
// resolution label is sd/hd/uhd, the same classes squasharr/worker uses.

// setActive sets clustarr_transcode_jobs_active{tier} to the TranscodeJobs
// holding a slot after this admission pass: those already dispatched
// (Queued or Running) plus those just admitted. Every budgeted class is set,
// so a class that drained to zero reads 0 rather than keeping its last
// value.
func setActive(slots map[string]int32, running, admitted []Slot) {
	count := map[string]int{}
	for hw := range slots {
		count[hw] = 0
	}
	for _, s := range running {
		count[s.Hardware]++
	}
	for _, s := range admitted {
		count[s.Hardware]++
	}
	for hw, n := range count {
		if hw == "" {
			continue
		}
		metrics.TranscodeJobsActive.WithLabelValues(hw).Set(float64(n))
	}
}

// ranToCompletion reports whether st is a terminal phase reached by a job
// that actually ran -- Succeeded, or Failed after a worker claimed it.
// Skipped jobs and jobs that failed before ever running (source changed,
// plan error, blocked at dispatch) encoded nothing, so they are not
// transcode observations.
func ranToCompletion(st *transcodev1alpha1.TranscodeJobStatus) bool {
	switch st.Phase {
	case transcodev1alpha1.TranscodeJobPhaseSucceeded, transcodev1alpha1.TranscodeJobPhaseFailed:
		return st.StartedAt != nil && st.FinishedAt != nil && st.Plan != nil
	}
	return false
}

// observeFinished records duration, speed ratio and size ratio for a job
// that just finished. fresh is the TranscodeJob as the terminal write left
// it -- its status.result is the worker's report; st is that status.
func (r *Reconciler) observeFinished(ctx context.Context, fresh *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) {
	// The class the last attempt ran on: after a CPU fallback that is not
	// the plan's GPU encoder's.
	tier := string(st.Hardware)
	if tier == "" {
		tier = string(hardwareForEncoder(st.Plan.Encoder))
	}
	// Both timestamps are whole seconds once they have round-tripped the
	// apiserver, so a very short job can read as zero; clamp rather than
	// drop the observation.
	wall := st.FinishedAt.Sub(st.StartedAt.Time)
	if wall < 0 {
		wall = 0
	}

	var height int32
	var runtimeMillis int64
	res := fresh.Status.Result
	if res != nil && res.MediaInfo != nil {
		height, runtimeMillis = res.MediaInfo.Height, res.MediaInfo.RuntimeMillis
	}
	if height == 0 || runtimeMillis == 0 {
		var mf catalogv1alpha1.MediaFile
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: fresh.Namespace, Name: fresh.Spec.MediaFileRef}, &mf); err == nil &&
			mf.Status.MediaInfo != nil {
			if height == 0 {
				height = mf.Status.MediaInfo.Height
			}
			if runtimeMillis == 0 {
				runtimeMillis = mf.Status.MediaInfo.RuntimeMillis
			}
		}
	}
	resolution := resolutionClass(height)

	outcome := "failed"
	if st.Phase == transcodev1alpha1.TranscodeJobPhaseSucceeded {
		outcome = "succeeded"
	}
	metrics.TranscodeDuration.WithLabelValues(tier, resolution, outcome).Observe(wall.Seconds())

	if st.Phase != transcodev1alpha1.TranscodeJobPhaseSucceeded {
		return
	}
	if runtimeMillis > 0 && wall > 0 {
		metrics.TranscodeSpeedRatio.WithLabelValues(tier).Set(float64(runtimeMillis) / float64(wall.Milliseconds()))
	}
	if res != nil && res.OutputToSourcePercent > 0 {
		metrics.TranscodeSizeRatio.WithLabelValues(tier, resolution).Observe(float64(res.OutputToSourcePercent) / 100)
	}
}

// resolutionClass matches squasharr/worker's (unexported) classes, so the
// label values are the ones the worker would have used; "unknown" when no
// probe is available at all.
func resolutionClass(height int32) string {
	switch {
	case height <= 0:
		return "unknown"
	case height <= 576:
		return "sd"
	case height <= 1080:
		return "hd"
	default:
		return "uhd"
	}
}
