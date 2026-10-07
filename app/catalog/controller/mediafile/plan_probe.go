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
	"hash/fnv"
	"io/fs"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	clustarrevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// fileMissingRetry is how soon a file the stat did not find is looked at again.
const fileMissingRetry = time.Minute

// ProbeInput is what PlanProbe decides from: everything the probe adapter's
// Gather read. It holds no client.
type ProbeInput struct {
	File             *catalogv1alpha1.MediaFile
	Prev             *catalogv1alpha1.MediaFileStatus
	Path             string      // spec.path, or a transcode swap's target (SwapTarget)
	Info             fs.FileInfo // Path's stat; nil when Missing
	Missing          error       // the stat found nothing at Path
	Swap, Kept       *transcodev1alpha1.TranscodeJob
	SwapTag, KeptTag string
	Graft            *transcodev1alpha1.AudioGraft
	Record           probestore.Current
	Read             bool      // Record was read
	PacedUntil       time.Time // a low-lane request waits for this pacer slot
	Version          int32     // the wanted probe version; 0 is mediainfo.ProbeVersion
	Now              metav1.Time
}

// Takeover is the spec this pass's incorporation hands catalogarr.
type Takeover struct {
	Path      string
	SizeBytes int64
	ModTime   metav1.Time
	Swap      bool
}

// ProbeEvent is one Event PlanProbe asks for on the MediaFile.
type ProbeEvent struct{ Type, Reason, Note string }

// ProbeOutcome is what PlanProbe decided beyond out.
type ProbeOutcome struct {
	Takeover  *Takeover
	Request   *probestore.Want    // write this request and publish its task, after the apply
	Republish *schema.ProbeRecord // publish this outstanding request again under its Msg-Id
	Due       time.Time
	Again     bool
	Unpaced   bool
	Records   []string // record states incorporated
	Events    []ProbeEvent
}

func (in ProbeInput) version() int32 {
	if in.Version == 0 {
		return mediainfo.ProbeVersion
	}
	return in.Version
}

// NeedsProbeRecord reports whether in's file needs its probe record read
// (the probe is due, or a swap, kept job or graft waits) and whether a new
// request for it would ride the low lane, the one the pacer meters.
func NeedsProbeRecord(in ProbeInput) (need, low bool) {
	if in.Missing != nil || in.Info == nil {
		return false, false
	}
	ps := evaluateProbe(in.Path, in.Info.Size(), in.Info.ModTime(), in.Prev.ProbeHash)
	swapOrKept := in.Swap != nil || in.Kept != nil || in.Graft != nil
	switch {
	case pathOnly(in.File, ps, swapOrKept), keptNeedsNoProbe(in.File, ps, in.Kept != nil, in.version()):
		return false, false
	case swapOrKept || probeDue(in.Prev.ProbeHash, in.Prev.ProbeVersion, ps, in.version()):
		return true, probeLane(in.File, ps, swapOrKept) == clustarrevents.PriorityLow
	}
	return false, false
}

// PacerJitter spreads a paced key's pass over five seconds, from its key.
func PacerJitter(key string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return time.Duration(h.Sum32()%5000) * time.Millisecond
}

// PlanProbe is the MediaFile reconciler's probe path (split §6.5.3, plan
// W4.13) as a pure planner: it writes only the probe's fields of out (out
// starts as a copy of the draft) and returns the request, the takeover, the
// timer and the Events. The request is an effect: the status says
// ProbePending first (loop spec §3.8).
func PlanProbe(in ProbeInput, out *catalogv1alpha1.MediaFileStatus) ProbeOutcome {
	var o ProbeOutcome
	mf, now := in.File, in.Now
	transcoded := mf.Spec.Original != nil && !*mf.Spec.Original
	if in.Missing != nil {
		k8s.MarkFalse(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionReady, "FileMissing", "stat %s: %s", in.Path, in.Missing)
		o.Due = now.Add(fileMissingRetry)
		return o
	}
	if ready := k8s.FindCondition(out.Conditions, catalogv1alpha1.MediaFileConditionReady); ready != nil &&
		ready.Status == metav1.ConditionFalse && ready.Reason == "FileMissing" && in.Prev.ProbeHash != "" {
		k8s.MarkTrue(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionReady, "Ready", "file present and probed")
	}
	ps := evaluateProbe(in.Path, in.Info.Size(), in.Info.ModTime(), in.Prev.ProbeHash)
	swapOrKept := in.Swap != nil || in.Kept != nil || in.Graft != nil
	switch {
	case pathOnly(mf, ps, swapOrKept):
		// The bytes status.mediaInfo describes, under a new name: record the
		// new hash and the container the name says, and probe nothing.
		out.ProbeHash = ps.Hash
		out.MediaInfo = mediainfo.AtPath(in.Prev.MediaInfo, in.Path)
		out.ProbedAt = &now
		o.Again = in.Prev.ProbeVersion < in.version()
	case keptNeedsNoProbe(mf, ps, in.Kept != nil, in.version()):
		incorporateProbe(&o, in, ps, out, in.Prev.MediaInfo, in.Prev.ProbeVersion, transcoded)
	case swapOrKept || probeDue(in.Prev.ProbeHash, in.Prev.ProbeVersion, ps, in.version()):
		want := probestore.Want{
			MediaFile: schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)},
			Path:      in.Path, ProbeHash: ps.Hash, ProbeVersion: in.version(), Lane: probeLane(mf, ps, swapOrKept),
		}
		j := judgeProbe(in.Record.Record, in.Record.OK, want, in.Prev.ProbeHash, now.Time)
		switch j.verdict {
		case verdictIncorporate:
			incorporateProbe(&o, in, ps, out, in.Record.Record.MediaInfo, in.Record.Record.ProbeVersion, transcoded)
			o.Due, o.Unpaced = earliest(o.Due, j.requeueAt), true
			o.Records = append(o.Records, in.Record.Record.State)
		case verdictWait:
			o.Due = j.requeueAt
		case verdictFailed, verdictGiveUp:
			k8s.MarkFalse(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionProbed, "ProbeFailed", "%s", j.failure)
			k8s.MarkFalse(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionReady, "ProbeFailed", "probe failed: %s", j.failure)
			o.Due, o.Unpaced = j.requeueAt, true
			o.Records = append(o.Records, in.Record.Record.State)
		case verdictRequest:
			want.Lane = j.lane
			if want.Lane == clustarrevents.PriorityLow && in.PacedUntil.After(now.Time) {
				o.Due = in.PacedUntil
				break
			}
			o.Request, o.Due = &want, j.requeueAt
			if !versionOnly(mf, ps, swapOrKept) {
				markProbePending(mf, out, now.Time, laneName(j.lane))
			}
			o.Unpaced = in.Prev.ProbeHash == ""
		case verdictPending:
			if j.republish {
				rec := in.Record.Record
				o.Republish = &rec
			}
			o.Due = j.requeueAt
			if p := k8s.FindCondition(in.Prev.Conditions, catalogv1alpha1.MediaFileConditionProbed); !versionOnly(mf, ps, swapOrKept) &&
				(p == nil || p.Reason != catalogv1alpha1.MediaFileReasonProbePending) {
				markProbePending(mf, out, in.Record.Record.RequestedAt, in.Record.Record.Lane)
			}
		}
	}
	if transcoded || in.Swap != nil {
		o.Due = earliest(o.Due, now.Add(TranscodedRecheckInterval))
	}
	return o
}

// incorporateProbe folds mi -- a summary of in.Path's current bytes by probe
// version version -- into out: W4.13's incorporate without its apply, which
// the loop's main-resource apply makes from o.Takeover (loop spec §3.10).
// The swap's TranscodeState carries no LastResult: the loop never writes it
// (loop spec §2.1 item 5).
func incorporateProbe(o *ProbeOutcome, in ProbeInput, ps probeState, out *catalogv1alpha1.MediaFileStatus,
	mi *commonv1.MediaInfo, version int32, transcoded bool,
) {
	mf, now := in.File, in.Now
	original := (mf.Spec.Original == nil || *mf.Spec.Original) && in.Swap == nil
	if in.Graft != nil {
		// An audio graft rewrote the file in place (anime dual-audio spec
		// §7.2): not a transcode -- spec.original stays -- but its bytes are
		// new, and this summary is of them.
		out.GraftTag, out.GraftedAt = in.Graft.Status.GraftTag, &now
	}
	if !original || out.GraftTag != "" {
		o.Takeover = &Takeover{Path: in.Path, SizeBytes: ps.SizeBytes, ModTime: ps.ModTime, Swap: in.Swap != nil}
	}
	k8s.MarkTrue(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionProbed, "Probed", "probed at %s", now.UTC().Format(time.RFC3339))
	k8s.MarkTrue(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionReady, "Ready", "file present and probed")
	out.ProbeHash, out.ProbedAt, out.ProbeVersion, out.MediaInfo = ps.Hash, &now, version, mi
	o.Events = append(o.Events, ProbeEvent{Type: "Normal", Reason: "Probed", Note: "probed " + mf.Spec.Path})
	if in.Swap == nil && in.Kept == nil && in.Graft == nil && transcoded && bytesChanged(mf, ps) && in.Prev.ProbeHash != "" {
		// The bytes of a file catalogarr already took over changed with no
		// transcode to explain them: the verdict the old bytes earned goes.
		out.Transcode = staleTranscodeState(out.Transcode)
		o.Events = append(o.Events, ProbeEvent{Type: "Warning", Reason: "TranscodedFileChanged", Note: fmt.Sprintf(
			"the transcoded file at %s changed on disk with no transcode to explain it; re-probed, compliance cleared", mf.Spec.Path)})
	}
	if in.Swap != nil {
		out.Transcode = &catalogv1alpha1.TranscodeState{Compliant: true, ProfileTag: in.SwapTag}
	}
	if in.Kept != nil {
		out.Transcode = &catalogv1alpha1.TranscodeState{ProfileTag: in.KeptTag}
		o.Events = append(o.Events, ProbeEvent{Type: "Normal", Reason: "TranscodeKept", Note: fmt.Sprintf(
			"the transcode was written to %s beside the kept source; this MediaFile still names %s", in.Kept.Status.Result.OutputPath, in.Path)})
	}
}

func markProbePending(mf *catalogv1alpha1.MediaFile, out *catalogv1alpha1.MediaFileStatus, at time.Time, lane string) {
	msg := fmt.Sprintf("probe requested %s (%s)", at.UTC().Format(time.RFC3339), lane)
	k8s.MarkFalse(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionProbed, catalogv1alpha1.MediaFileReasonProbePending, "%s", msg)
	k8s.MarkFalse(mf, &out.Conditions, catalogv1alpha1.MediaFileConditionReady, catalogv1alpha1.MediaFileReasonProbePending, "%s", msg)
}

func laneName(p clustarrevents.Priority) string {
	if p == clustarrevents.PriorityLow {
		return "low"
	}
	return "high"
}

func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	}
	return b
}
