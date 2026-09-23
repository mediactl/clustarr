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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TranscodeJobPhase is the coarse lifecycle phase of a TranscodeJob.
//
// +kubebuilder:validation:Enum=Pending;Planned;Queued;Running;Verifying;Succeeded;Failed;Skipped
type TranscodeJobPhase string

// TranscodeJob phases.
const (
	TranscodeJobPhasePending   TranscodeJobPhase = "Pending"
	TranscodeJobPhasePlanned   TranscodeJobPhase = "Planned"
	TranscodeJobPhaseQueued    TranscodeJobPhase = "Queued"
	TranscodeJobPhaseRunning   TranscodeJobPhase = "Running"
	TranscodeJobPhaseVerifying TranscodeJobPhase = "Verifying"
	TranscodeJobPhaseSucceeded TranscodeJobPhase = "Succeeded"
	TranscodeJobPhaseFailed    TranscodeJobPhase = "Failed"
	TranscodeJobPhaseSkipped   TranscodeJobPhase = "Skipped"
)

// PlanMode says what kind of work the planner decided on.
//
// +kubebuilder:validation:Enum=transcode;remuxOnly;skip
type PlanMode string

// Plan modes.
const (
	PlanModeTranscode PlanMode = "transcode"
	PlanModeRemuxOnly PlanMode = "remuxOnly"
	PlanModeSkip      PlanMode = "skip"
)

// AudioAction says what happens to one source audio track.
//
// +kubebuilder:validation:Enum=encode;copy;drop
type AudioAction string

// Audio actions.
const (
	AudioActionEncode AudioAction = "encode"
	AudioActionCopy   AudioAction = "copy"
	AudioActionDrop   AudioAction = "drop"
)

// TranscodeJob condition types.
const (
	// TranscodeJobConditionPlanned is True once the planner has produced status.plan.
	TranscodeJobConditionPlanned = "Planned"
	// TranscodeJobConditionJobCreated is True once the batch Job exists.
	TranscodeJobConditionJobCreated = "JobCreated"
	// TranscodeJobConditionVerified is True once the output passed verification.
	TranscodeJobConditionVerified = "Verified"
	// TranscodeJobConditionSucceeded is True once the job finished successfully.
	TranscodeJobConditionSucceeded = "Succeeded"
	// TranscodeJobConditionFailed is True once the job failed permanently.
	TranscodeJobConditionFailed = "Failed"
)

// TranscodeJobSpec defines the desired state of TranscodeJob.
type TranscodeJobSpec struct {
	// MediaFileRef is the name of the MediaFile (same namespace) being transcoded.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mediaFileRef is immutable"
	MediaFileRef string `json:"mediaFileRef"`

	// ProfileRef is the name of the cluster-scoped TranscodeProfile to apply.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="profileRef is immutable"
	ProfileRef string `json:"profileRef"`

	// SourcePath is the path of the source file.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="sourcePath is immutable"
	SourcePath string `json:"sourcePath"`

	// SourceProbeHash is the hash of the source probe the job was planned from.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="sourceProbeHash is immutable"
	SourceProbeHash string `json:"sourceProbeHash,omitempty"`

	// OutputPath is where the output is written. Defaults to <stem>.mkv
	// beside the source.
	// +optional
	OutputPath *string `json:"outputPath,omitempty"`

	// Priority orders jobs; higher runs first.
	// +optional
	Priority int32 `json:"priority,omitempty"`

	// Hardware overrides the profile's encoder backend.
	// +optional
	Hardware *Hardware `json:"hardware,omitempty"`

	// Suspend pauses the job (user pause).
	// +optional
	Suspend *bool `json:"suspend,omitempty"`
}

// AudioPlan is the planner's decision for one source audio track.
type AudioPlan struct {
	// SourceIndex is the source stream index.
	// +optional
	SourceIndex int32 `json:"sourceIndex,omitempty"`

	// Action says whether the track is encoded, copied or dropped.
	// +optional
	Action AudioAction `json:"action,omitempty"`

	// Codec is the output codec for encoded tracks.
	// +optional
	Codec string `json:"codec,omitempty"`

	// BitrateKbps is the output bitrate for encoded tracks.
	// +optional
	BitrateKbps int32 `json:"bitrateKbps,omitempty"`

	// Default marks the output default audio track.
	// +optional
	Default bool `json:"default,omitempty"`
}

// Plan is the planner's rendered decision for a TranscodeJob.
type Plan struct {
	// Encoder is the ffmpeg encoder selected (for example libx265, hevc_nvenc).
	// +optional
	Encoder string `json:"encoder,omitempty"`

	// Mode says whether the job transcodes, only remuxes, or is skipped.
	// +optional
	Mode PlanMode `json:"mode,omitempty"`

	// SkipReason explains a skip decision.
	// +optional
	SkipReason string `json:"skipReason,omitempty"`

	// HDRMode is the HDR handling chosen for this source.
	// +optional
	HDRMode string `json:"hdrMode,omitempty"`

	// VideoArgs are the rendered ffmpeg video arguments.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	VideoArgs []string `json:"videoArgs,omitempty"`

	// AudioTracks is the per-track audio plan.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	AudioTracks []AudioPlan `json:"audioTracks,omitempty"`

	// SubtitleTracks lists the source subtitle stream indexes to copy.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	SubtitleTracks []int32 `json:"subtitleTracks,omitempty"`

	// ArgsHash is a hash of the rendered arguments.
	// +optional
	ArgsHash string `json:"argsHash,omitempty"`
}

// Progress is the worker's encode progress, patched at most every 10 s.
type Progress struct {
	// Percent is the completed percentage of the encode, 0-100.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Percent int32 `json:"percent,omitempty"`

	// Frame is the last encoded frame number.
	// +optional
	Frame int64 `json:"frame,omitempty"`

	// FPSMilli is the current encode rate in thousandths of a frame per second.
	// +optional
	// +kubebuilder:validation:Minimum=0
	FPSMilli int32 `json:"fpsMilli,omitempty"`

	// SpeedMilli is the encode speed relative to real time, in thousandths,
	// so 1.5x real time is 1500.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SpeedMilli int32 `json:"speedMilli,omitempty"`

	// OutTimeMillis is the output timestamp reached so far, in milliseconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	OutTimeMillis int64 `json:"outTimeMillis,omitempty"`

	// BitrateKbps is the current output bitrate in whole kilobits per second.
	// +optional
	// +kubebuilder:validation:Minimum=0
	BitrateKbps int32 `json:"bitrateKbps,omitempty"`

	// UpdatedAt is when this progress was reported.
	// +optional
	UpdatedAt metav1.Time `json:"updatedAt,omitempty"`
}

// Result is the worker's final outcome of a TranscodeJob.
type Result struct {
	// OutputPath is where the output was written.
	// +optional
	OutputPath string `json:"outputPath,omitempty"`

	// OutputSizeBytes is the output file size.
	// +optional
	OutputSizeBytes int64 `json:"outputSizeBytes,omitempty"`

	// OutputToSourcePercent is the output size as a percentage of the source size,
	// so an output 45% the size of its source is 45.
	// +optional
	// +kubebuilder:validation:Minimum=0
	OutputToSourcePercent int32 `json:"outputToSourcePercent,omitempty"`

	// VMAFCentis is the measured VMAF score in hundredths, so 95.42 is 9542.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10000
	VMAFCentis *int32 `json:"vmafCentis,omitempty"`

	// MediaInfo is the probe of the output file.
	// +optional
	MediaInfo *commonv1alpha1.MediaInfo `json:"mediaInfo,omitempty"`
}

// TranscodeJobStatus defines the observed state of TranscodeJob. The
// controller (squasharr) owns phase, plan, jobRef, attempts, timestamps,
// message and conditions; the worker (squasharr-worker) owns progress, result
// and stderrTail via server-side apply with a disjoint field set.
type TranscodeJobStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the coarse lifecycle phase.
	// +optional
	Phase TranscodeJobPhase `json:"phase,omitempty"`

	// Plan is the planner's rendered decision.
	// +optional
	Plan *Plan `json:"plan,omitempty"`

	// JobRef is the name of the batch Job running the encode.
	// +optional
	JobRef *string `json:"jobRef,omitempty"`

	// Attempts is the number of encode attempts so far.
	// +optional
	Attempts int32 `json:"attempts,omitempty"`

	// StartedAt is when the encode started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is when the encode finished.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Message is a human-readable summary of the current state.
	// +optional
	Message string `json:"message,omitempty"`

	// Progress is the worker-reported encode progress.
	// +optional
	Progress *Progress `json:"progress,omitempty"`

	// Result is the worker-reported final outcome.
	// +optional
	Result *Result `json:"result,omitempty"`

	// StderrTail is the tail of the encoder's stderr, at most 4 KiB.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	StderrTail string `json:"stderrTail,omitempty"`

	// Conditions holds Planned, JobCreated, Verified, Succeeded and Failed.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// TranscodeJob is one transcode of a MediaFile against a TranscodeProfile.
// It is owned by the MediaFile and named <mediafile>-<profileHash[:8]>.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,categories=clustarr
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="MediaFile",type="string",JSONPath=".spec.mediaFileRef"
// +kubebuilder:printcolumn:name="Profile",type="string",JSONPath=".spec.profileRef"
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".status.plan.mode",priority=1
// +kubebuilder:printcolumn:name="Progress",type="integer",JSONPath=".status.progress.percent"
// +kubebuilder:printcolumn:name="Attempts",type="integer",JSONPath=".status.attempts",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type TranscodeJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec TranscodeJobSpec `json:"spec,omitempty"`
	// +optional
	Status TranscodeJobStatus `json:"status,omitempty"`
}

// TranscodeJobList contains a list of TranscodeJob.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type TranscodeJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TranscodeJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TranscodeJob{}, &TranscodeJobList{})
}
