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

// TranscodeResult is the outcome of the most recent transcode of a file:
// today's lastResult, kept in release N's schema only so the previous
// release's apply stays valid after a rollback (loop spec §2.16). The loop
// never writes it; F9.2 deletes it.
//
// +kubebuilder:validation:Enum=none;succeeded;failed;skipped
type TranscodeResult string

// Transcode results.
const (
	TranscodeResultNone      TranscodeResult = "none"
	TranscodeResultSucceeded TranscodeResult = "succeeded"
	TranscodeResultFailed    TranscodeResult = "failed"
	TranscodeResultSkipped   TranscodeResult = "skipped"
)

// TranscodePhase is status.transcode's phase (loop spec §2.5).
// +kubebuilder:validation:Enum=Pending;Planned;Queued;Running;Swapping;Succeeded;Failed;Skipped
type TranscodePhase string

// Transcode phases.
const (
	// TranscodePhasePending: in the window but not plannable yet (Unprobed,
	// ProbePending, Grafting).
	TranscodePhasePending TranscodePhase = "Pending"
	// TranscodePhasePlanned: waiting for admission.
	TranscodePhasePlanned TranscodePhase = "Planned"
	// TranscodePhaseQueued and TranscodePhaseRunning: in flight
	// (dispatch.InFlight()).
	TranscodePhaseQueued  TranscodePhase = "Queued"
	TranscodePhaseRunning TranscodePhase = "Running"
	// TranscodePhaseSwapping: the worker reported success; the output's
	// probe is awaited before the spec takeover.
	TranscodePhaseSwapping TranscodePhase = "Swapping"
	// TranscodePhaseSucceeded: incorporated.
	TranscodePhaseSucceeded TranscodePhase = "Succeeded"
	// TranscodePhaseFailed and TranscodePhaseSkipped are verdicts for one
	// (profileHash, probeHash).
	TranscodePhaseFailed  TranscodePhase = "Failed"
	TranscodePhaseSkipped TranscodePhase = "Skipped"
)

// TranscodeHardware mirrors transcodev1alpha1.Hardware, which this package
// cannot import (pkg/crdcheck.TestTranscodeHardwareMirrorsTheProfileEnum).
// +kubebuilder:validation:Enum=cpu;nvidia;intel;auto;gpu
type TranscodeHardware string

// Transcode hardware.
const (
	TranscodeHardwareCPU    TranscodeHardware = "cpu"
	TranscodeHardwareNVIDIA TranscodeHardware = "nvidia"
	TranscodeHardwareIntel  TranscodeHardware = "intel"
	TranscodeHardwareAuto   TranscodeHardware = "auto"
	TranscodeHardwareGPU    TranscodeHardware = "gpu"
)

// TranscodeClass is the concrete class a task was dispatched to
// (pkg/crdcheck.TestTranscodeClassIsConcreteHardware).
// +kubebuilder:validation:Enum=cpu;nvidia;intel
type TranscodeClass string

// Transcode classes.
const (
	TranscodeClassCPU    TranscodeClass = "cpu"
	TranscodeClassNVIDIA TranscodeClass = "nvidia"
	TranscodeClassIntel  TranscodeClass = "intel"
)

// TranscodeMode is a plan's mode.
// +kubebuilder:validation:Enum=transcode;remuxOnly
type TranscodeMode string

// Transcode modes.
const (
	TranscodeModeTranscode TranscodeMode = "transcode"
	TranscodeModeRemuxOnly TranscodeMode = "remuxOnly"
)

// TranscodeState is the file's transcode, written by the remediation loop's
// transcode planner (loop spec §2.5; ADR-0016). It replaces TranscodeJob.
type TranscodeState struct {
	// Phase is always written by the loop. It is +optional, and omitted
	// when empty, in release N only: the previous release applies this
	// block with no phase under the same field manager, and a typed
	// phase-less block must not send phase:"" (§2.16). F9.2 makes it
	// +required.
	// +optional
	Phase TranscodePhase `json:"phase,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// Profile is the TranscodeProfile that won the file, and ProfileHash the
	// hash the block is planned under (jobspec.ProfileHash over the
	// standard's inputs and standard.Version, the function the profile's
	// status.hash uses). Pending, Failed and Skipped are verdicts for this
	// (ProfileHash, ProbeHash) only.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Profile string `json:"profile,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	ProfileHash string `json:"profileHash,omitempty"`
	// ProbeHash is the status.probeHash the plan or verdict was made for.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`
	// +optional
	Plan *TranscodePlan `json:"plan,omitempty"`
	// Hardware is the hardware in force: the transcode.clustarr.io/hardware
	// annotation when valid, else the profile's.
	// +optional
	Hardware TranscodeHardware `json:"hardware,omitempty"`
	// Priority is the priority admission used: the annotation's, else the
	// profile's spec.priority.
	// +optional
	Priority int32 `json:"priority,omitempty"`
	// Suspended is the transcode.clustarr.io/suspend annotation in force.
	// +optional
	Suspended bool `json:"suspended,omitempty"`
	// PlannedAt is when this (ProfileHash, ProbeHash) first reached Planned;
	// admission orders by priority, then PlannedAt, then name.
	// +optional
	PlannedAt *metav1.Time `json:"plannedAt,omitempty"`
	// +optional
	Class TranscodeClass `json:"class,omitempty"`
	// Pool is the pool Job of the in-flight or last dispatch.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Pool string `json:"pool,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	FallbackReason string `json:"fallbackReason,omitempty"`
	// Attempts counts dispatches in the current cycle; a retry, new bytes or
	// a new ProfileHash reset it. Dispatch.Seq never resets.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	NextAttemptAt *metav1.Time `json:"nextAttemptAt,omitempty"`
	// Blocked is a Failed verdict that is not retried without
	// transcode.clustarr.io/retry, new bytes or a new ProfileHash.
	// +optional
	Blocked bool `json:"blocked,omitempty"`
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	WorkerPod string `json:"workerPod,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Result stays until the swap is incorporated, and for good for a
	// replaceSource=false copy (the rescan protects result.outputPath).
	// +optional
	Result *TranscodeOutput `json:"result,omitempty"`
	// StderrTail is the last 1 KiB of the encoder's stderr on Failed; the
	// record keeps 4 KiB.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	StderrTail string `json:"stderrTail,omitempty"`
	// Compliant is true when the file already matches its TranscodeProfile.
	// +optional
	Compliant bool `json:"compliant,omitempty"`
	// ProfileTag is <Profile>@<ProfileHash>, set when a swap is incorporated,
	// from this block, never from a TranscodeProfile Get.
	// +optional
	// +kubebuilder:validation:MaxLength=320
	ProfileTag string `json:"profileTag,omitempty"`
	// JoinedGraft is the graft the dispatch at Dispatch.Seq carries, written
	// by the transcode planner in the apply that dispatches and kept until
	// its next dispatch (§5.13).
	// +optional
	JoinedGraft *TranscodeGraftJoin `json:"joinedGraft,omitempty"`

	// JobRef and LastResult are today's fields, kept in release N's schema
	// only so the previous release's apply is still valid after a rollback
	// (§2.16). The loop never writes them; LastResult has no default. F9.2
	// deletes both.
	// +optional
	JobRef *string `json:"jobRef,omitempty"`
	// +optional
	LastResult TranscodeResult `json:"lastResult,omitempty"`
}

// TranscodeGraftJoin is the graft a transcode dispatch carries.
type TranscodeGraftJoin struct {
	// +kubebuilder:validation:MaxLength=512
	DonorRelease string `json:"donorRelease"`
	// +optional
	DonorImportedAt *metav1.Time `json:"donorImportedAt,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=35
	Languages []string `json:"languages,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash"`
}

// TranscodePlan is what the transcode planner decided for the file.
type TranscodePlan struct {
	// +required
	Mode TranscodeMode `json:"mode"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Encoder string `json:"encoder,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=copy;encode
	VideoAction string `json:"videoAction,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=cpu;nvdec;upload;vaapi;qsv
	Decode string `json:"decode,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	HDRMode string `json:"hdrMode,omitempty"`
	// +required
	// +kubebuilder:validation:MaxLength=64
	PlanHash string `json:"planHash"`
	// Dropped names each source subtitle the output carries neither embedded
	// nor as a sidecar (standard.Result.Dropped: a codec the MP4 standard does
	// not carry, or a second track for a sidecar name already planned), each
	// clamped on a rune boundary; past 8, the eighth reads "and N more". It
	// replaces the "; dropped …" suffix main's c0fb39b7 adds to TranscodeJob's
	// Planned condition, which the fold removes (loop spec §2.5, D44).
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=320
	Dropped []string `json:"dropped,omitempty"`
}

// TranscodeOutput is a successful transcode's output, until the swap is
// incorporated.
type TranscodeOutput struct {
	// +required
	// +kubebuilder:validation:MaxLength=4096
	OutputPath string `json:"outputPath"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	OutputSizeBytes int64 `json:"outputSizeBytes,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	OutputToSourcePercent int32 `json:"outputToSourcePercent,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10000
	VMAFCentis *int32 `json:"vmafCentis,omitempty"`
}
