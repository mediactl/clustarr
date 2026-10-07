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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GraftPhase is status.graft's phase (loop spec §2.6).
// +kubebuilder:validation:Enum=Waiting;Queued;Running;Swapping;Succeeded;Failed
type GraftPhase string

// Graft phases.
const (
	GraftPhaseWaiting   GraftPhase = "Waiting"
	GraftPhaseQueued    GraftPhase = "Queued"
	GraftPhaseRunning   GraftPhase = "Running"
	GraftPhaseSwapping  GraftPhase = "Swapping"
	GraftPhaseSucceeded GraftPhase = "Succeeded"
	GraftPhaseFailed    GraftPhase = "Failed"
)

// GraftState is the file's audio graft, written by the remediation loop's
// graft planner (loop spec §2.6; ADR-0016). It replaces AudioGraft's
// per-file half; the item-scoped half (the donor, the rejected releases) is
// the item's status.audio. It exists only on a single-item file whose item
// has a donor and is missing languages, or has a graft result for this
// probe.
type GraftState struct {
	// +required
	Phase GraftPhase `json:"phase"`
	// Reason: Waiting -- FileUnprobed, TranscodeRunning, WaitingForTranscode,
	// WaitingForSlot, Backoff; Running -- JoinedTranscode; Succeeded --
	// Grafted, Present; Failed -- the grafttask reasons and JobLost.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// DonorRelease and DonorImportedAt identify the item's donor this graft
	// is for (status.audio.donor); a Failed graft is retried only for another
	// donor or another ProbeHash.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	DonorRelease string `json:"donorRelease,omitempty"`
	// +optional
	DonorImportedAt *metav1.Time `json:"donorImportedAt,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=35
	Languages []string `json:"languages,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`
	// DonorFault marks a failure that rejects the donor's release
	// (AlignmentRejected, VerifyFailed, MuxFailed, DonorLacksLanguage).
	// +optional
	DonorFault bool `json:"donorFault,omitempty"`
	// JoinedTranscodeSeq is the transcode dispatch this graft rides.
	// +optional
	// +kubebuilder:validation:Minimum=0
	JoinedTranscodeSeq int64 `json:"joinedTranscodeSeq,omitempty"`
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
	// JobName is the standalone graft Job of the in-flight dispatch.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	JobName string `json:"jobName,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	RateName string `json:"rateName,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	RateMicros int64 `json:"rateMicros,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	RateMarginMilli int32 `json:"rateMarginMilli,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CoveragePercent int32 `json:"coveragePercent,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	Segments []GraftSegment `json:"segments,omitempty"`
	// +optional
	ResidualMillis int32 `json:"residualMillis,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Within80Percent int32 `json:"within80Percent,omitempty"`
	// Tag is the CLUSTARR_GRAFT tag the worker wrote; status.graftTag takes
	// it when the grafted bytes' probe is incorporated.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Tag string `json:"tag,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// GraftSegment is one aligned span: the donor's start, the target's start
// and its length, in milliseconds.
type GraftSegment struct {
	DonorStartMillis  int64 `json:"donorStartMillis"`
	TargetStartMillis int64 `json:"targetStartMillis"`
	LengthMillis      int64 `json:"lengthMillis"`
}
