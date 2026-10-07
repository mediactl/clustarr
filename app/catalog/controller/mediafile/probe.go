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

package mediafile

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	clustarrevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// probeState is what a reconcile learns about the file on disk before
// deciding whether to re-run ffprobe. Pure and cluster-free so the
// staleness rule is unit-testable without envtest.
type probeState struct {
	SizeBytes int64
	ModTime   metav1.Time
	Hash      string
	Stale     bool
}

// evaluateProbe hashes the file's current stat against currentHash
// (MediaFile.status.probeHash as last observed). An empty currentHash --
// never probed -- is always stale.
func evaluateProbe(path string, statSize int64, statMod time.Time, currentHash string) probeState {
	h := mediainfo.ProbeHash(path, statSize, statMod)
	return probeState{
		SizeBytes: statSize,
		ModTime:   metav1.NewTime(statMod),
		Hash:      h,
		Stale:     h != currentHash,
	}
}

// probeDue reports whether the file must be probed: it never was, its
// bytes changed (ps.Stale), or an older probe version described it and
// missed a field this one records. The last leaves ps.Hash as it was, so
// nothing that keys on the hash -- captionarr's subtitle history,
// squasharr's source identity -- sees a different file.
func probeDue(currentHash string, version int32, ps probeState) bool {
	return currentHash == "" || ps.Stale || version < probeVersion
}

// bytesChanged reports whether the file's bytes changed since mf recorded
// them: a stale probe whose size or mtime (to the second, as a stat round
// trips through metav1.Time) differs from spec.sizeBytes and spec.modTime.
// A probe hash names the path, so a rename or move alone is stale too, but
// it moves the same bytes, keeping both -- not a change.
func bytesChanged(mf *catalogv1alpha1.MediaFile, ps probeState) bool {
	if !ps.Stale {
		return false
	}
	return ps.SizeBytes != mf.Spec.SizeBytes ||
		!ps.ModTime.UTC().Truncate(time.Second).Equal(mf.Spec.ModTime.UTC().Truncate(time.Second))
}

// The probe queue's timing (spec 2026-10-06 §6.5.3).
const (
	// probeRequestTimeoutHigh and probeRequestTimeoutLow are how long a
	// request waits for its answer before it is asked again, by lane.
	probeRequestTimeoutHigh = 6 * time.Hour
	probeRequestTimeoutLow  = 72 * time.Hour
	// probeRepublishWindow is how long a pending request republishes its
	// task under the same Msg-Id, which the stream's one-hour dedup window
	// absorbs: it covers a publish that failed after the record was written.
	probeRepublishWindow = 50 * time.Minute
	// probeTransientRetry and probeFailedRetry are how long a failed probe
	// waits before it is asked again (OD47; it was 30 s, forever).
	probeTransientRetry = 5 * time.Minute
	probeFailedRetry    = time.Hour
	// probeSkewRetry is how long an answer from an agent older than this
	// manager stands before the file is asked again: re-asking sooner would
	// loop while the old agent still answers (a rollout, a draining pod).
	probeSkewRetry = 30 * time.Minute
	// probeAbandonLimit is how many consecutive abandoned probes of the same
	// bytes are tried before the reconciler gives up on them.
	probeAbandonLimit = 3
	// probeConflictRetry requeues a reconcile whose request lost a
	// compare-and-swap.
	probeConflictRetry = time.Second
)

// probeVersion is the probe version a reconcile wants: mediainfo.ProbeVersion,
// a variable only so a test can raise it (export_test.go's SetProbeVersion).
var probeVersion = mediainfo.ProbeVersion

// verdict is what a reconcile does about a file's probe record.
type verdict int

const (
	verdictRequest     verdict = iota // write a request and publish its task
	verdictIncorporate                // fold the record's summary into status
	verdictPending                    // a request is in flight
	verdictWait                       // an older agent's answer stands for now
	verdictFailed                     // report the failure; ask again later
	verdictGiveUp                     // report the failure; never ask again for these bytes
)

// judgement is judgeProbe's answer.
type judgement struct {
	verdict   verdict
	lane      clustarrevents.Priority // verdictRequest: the lane to ask on
	requeueAt time.Time               // when to look again; zero for never
	republish bool                    // verdictPending: publish the same Msg-Id again
	failure   string                  // verdictFailed, verdictGiveUp: the condition's message
}

// requestTimeout is how long a request on lane waits for its answer.
func requestTimeout(lane clustarrevents.Priority) time.Duration {
	if lane == clustarrevents.PriorityLow {
		return probeRequestTimeoutLow
	}
	return probeRequestTimeoutHigh
}

// judgeProbe decides what a reconcile does with rec (ok false: there is
// none) for want, given the hash status.probeHash names: spec §6.5.3's table,
// the first matching row winning. A record matches when its UID, path and
// hash are want's. Pure, so every row is a unit test.
//
// The third row (an older agent answered a current request) waits only until
// ProbedAt + probeSkewRetry, so it never shadows the fourth (ask again then);
// incorporating a new file's answer as is applies at any time.
func judgeProbe(rec schema.ProbeRecord, ok bool, want probestore.Want, statusProbeHash string, now time.Time) judgement {
	request := func(lane clustarrevents.Priority) judgement {
		return judgement{verdict: verdictRequest, lane: lane, requeueAt: now.Add(requestTimeout(lane))}
	}
	// A record for the bytes status already describes is a version-only
	// re-probe: low lane. Anything else is high.
	versionLane := clustarrevents.PriorityHigh
	if statusProbeHash == want.ProbeHash {
		versionLane = clustarrevents.PriorityLow
	}
	if !ok || rec.MediaFile.UID != want.MediaFile.UID || rec.Path != want.Path || rec.ProbeHash != want.ProbeHash {
		return request(want.Lane)
	}
	answered := rec.State == schema.ProbeProbed || rec.State == schema.ProbeFailed
	skew := answered && rec.ProbeVersion < want.ProbeVersion && rec.RequestedVersion >= want.ProbeVersion
	skewUntil := rec.ProbedAt.Add(probeSkewRetry)
	switch {
	case rec.State == schema.ProbeProbed && rec.ProbeVersion >= want.ProbeVersion:
		return judgement{verdict: verdictIncorporate}
	case answered && rec.ProbeVersion < want.ProbeVersion && rec.RequestedVersion < want.ProbeVersion:
		// A seed, or an answer to a request made before a ProbeVersion raise.
		return request(versionLane)
	case skew && rec.State == schema.ProbeProbed && statusProbeHash != rec.ProbeHash:
		// An older agent answered a current request for bytes status does not
		// describe: use the answer now, ask again once the skew window passes.
		return judgement{verdict: verdictIncorporate, requeueAt: later(skewUntil, now)}
	case skew && rec.State == schema.ProbeProbed && now.Before(skewUntil):
		return judgement{verdict: verdictWait, requeueAt: skewUntil}
	case skew && !now.Before(skewUntil):
		return request(versionLane)
	case rec.State == schema.ProbeFailed && rec.AbandonedCount >= probeAbandonLimit:
		return judgement{verdict: verdictGiveUp, failure: fmt.Sprintf("probe abandoned %d times: %s", rec.AbandonedCount, rec.Failure)}
	case rec.State == schema.ProbeFailed:
		retryAt := rec.ProbedAt.Add(probeFailedRetry)
		if rec.Transient {
			retryAt = rec.ProbedAt.Add(probeTransientRetry)
		}
		if now.Before(retryAt) {
			return judgement{verdict: verdictFailed, requeueAt: retryAt, failure: rec.Failure}
		}
		return request(want.Lane)
	case rec.State == schema.ProbeRequested:
		timeout := requestTimeout(clustarrevents.Priority(rec.Lane))
		if now.Sub(rec.RequestedAt) < timeout {
			return judgement{
				verdict: verdictPending, requeueAt: rec.RequestedAt.Add(timeout),
				republish: now.Sub(rec.RequestedAt) < probeRepublishWindow,
			}
		}
	}
	return request(want.Lane)
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// probeLane is the lane a probe of ps is asked on: high for a file never
// probed, changed bytes, a transcode swap, a kept job or an audio graft's
// swap (swapOrKept carries all three); low for a
// version-only re-probe, which never queues ahead of a new import.
func probeLane(mf *catalogv1alpha1.MediaFile, ps probeState, swapOrKept bool) clustarrevents.Priority {
	if mf.Status.ProbeHash == "" || bytesChanged(mf, ps) || swapOrKept {
		return clustarrevents.PriorityHigh
	}
	return clustarrevents.PriorityLow
}

// pathOnly reports a rename: the probe hash names the path, so a moved file
// is stale, but its bytes are the ones status.mediaInfo describes. It is
// incorporated without a probe (spec §6.5.3, "Path-only staleness").
func pathOnly(mf *catalogv1alpha1.MediaFile, ps probeState, swapOrKept bool) bool {
	return ps.Stale && !bytesChanged(mf, ps) && !swapOrKept && mf.Status.ProbeHash != "" && mf.Status.MediaInfo != nil
}

// versionOnly reports a re-probe of the bytes status already describes, for
// a newer probe version: it changes no condition and no naming input.
func versionOnly(mf *catalogv1alpha1.MediaFile, ps probeState, swapOrKept bool) bool {
	return !ps.Stale && mf.Status.ProbeHash != "" && !swapOrKept
}

// keptNeedsNoProbe reports a kept job (replaceSource=false) whose source the
// current summary still describes: it is recorded from that summary.
func keptNeedsNoProbe(mf *catalogv1alpha1.MediaFile, ps probeState, kept bool) bool {
	return kept && !ps.Stale && mf.Status.ProbeHash != "" && mf.Status.MediaInfo != nil &&
		mf.Status.ProbeVersion >= probeVersion
}

// sooner is res, requeued after d if that is sooner than res asks.
func sooner(res ctrl.Result, d time.Duration) ctrl.Result {
	if d > 0 && (res.RequeueAfter == 0 || d < res.RequeueAfter) {
		res.RequeueAfter = d
	}
	return res
}

// requeueAt is res, requeued at at (at least a second from now); a zero at
// leaves res as it is.
func requeueAt(res ctrl.Result, at, now time.Time) ctrl.Result {
	if at.IsZero() {
		return res
	}
	return sooner(res, max(at.Sub(now), time.Second))
}
