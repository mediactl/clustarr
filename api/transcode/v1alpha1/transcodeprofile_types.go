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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Container is the output container format written by a transcode.
//
// +kubebuilder:validation:Enum=mkv;mp4
type Container string

// Output containers.
const (
	ContainerMKV Container = "mkv"
	ContainerMP4 Container = "mp4"
)

// Hardware selects the encoder backend a transcode runs on.
//
// +kubebuilder:validation:Enum=cpu;nvidia;intel;auto
type Hardware string

// Hardware backends.
const (
	HardwareCPU    Hardware = "cpu"
	HardwareNVIDIA Hardware = "nvidia"
	HardwareIntel  Hardware = "intel"
	// HardwareAuto prefers a GPU class with a labelled GPU node and a free slot,
	// else cpu, chosen per task at dispatch (spec §18.5).
	HardwareAuto Hardware = "auto"
)

// TranscodeProfile condition types.
const (
	// TranscodeProfileConditionReady is True once the profile has been
	// validated and hashed and jobs may be planned against it.
	TranscodeProfileConditionReady = "Ready"
	// TranscodeProfileConditionInvalid is True when the profile cannot be used,
	// for example because it is a second default profile.
	TranscodeProfileConditionInvalid = "Invalid"
)

// EncoderLimit is one GPU node's measured encoder device limits.
type EncoderLimit struct {
	// Class is the hardware class of the node's pool.
	// +required
	Class Hardware `json:"class"`

	// Node is the node the limits were measured on.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Node string `json:"node"`

	// NVDEC are the source formats, as codec:bitDepth ("h264:8",
	// "hevc:10"), the node's decoder was measured to decode; the nvidia tier
	// decodes those on the GPU. Absent is none measured or none decodable,
	// and squasharr decides an unmeasured format from a static list.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=32
	NVDEC []string `json:"nvdec,omitempty"`

	// Healthy is whether the node's pool pod could use its device when it
	// last measured it (spec §4); false sends the class no work while every
	// node reports so. Absent is a report from a worker that predates it.
	// +optional
	Healthy *bool `json:"healthy,omitempty"`

	// Message is why the device could not be used, when Healthy is false.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Message string `json:"message,omitempty"`
}

// AudioSpec is what a profile decides about audio: which languages to keep.
type AudioSpec struct {
	// Languages keeps only audio in these languages (commentary always, and
	// every track when none matches); empty keeps all. The kept tracks are
	// copied when Apple TV plays them directly (AAC, AC-3, E-AC-3), else
	// encoded to AAC: the standard decides, not the profile.
	// +optional
	Languages []string `json:"languages,omitempty"`
}

// PolicySpec decides which files are transcoded and what happens afterwards.
type PolicySpec struct {
	// NeverTranscodeModifiers lists quality modifiers (see common Modifier)
	// whose files are never transcoded.
	// +optional
	// +kubebuilder:default={"remux","brdisk"}
	NeverTranscodeModifiers []string `json:"neverTranscodeModifiers,omitempty"`

	// MinDuration is the shortest source runtime considered for transcoding;
	// "0s" considers every file. A pointer because zero is meaningful: a Go
	// client always sends a non-pointer Duration, so the 1m default would
	// never reach a profile created from Go. Unset means 1m.
	// +optional
	// +kubebuilder:default="1m"
	MinDuration *metav1.Duration `json:"minDuration,omitempty"`

	// MaxOutputToSourcePercent fails a job whose output is larger than this
	// percentage of the source size: 100 (the default) refuses any output
	// bigger than the file it replaces, 150 allows half as large again. It is
	// the design spec's MaxOutputToSourceRatio 1.0 as a scaled integer, since
	// the API carries no floats. 0 disables the check. A pointer so a Go
	// client can send that 0: with omitempty it would be dropped and
	// defaulted back to 100. Unset means 100.
	// +optional
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=0
	MaxOutputToSourcePercent *int32 `json:"maxOutputToSourcePercent,omitempty"`

	// ReplaceSource replaces the source file with the output on success;
	// false leaves the source file in place instead of renaming the output
	// over it. A pointer so a Go client can send an explicit false; unset
	// means true.
	// +optional
	// +kubebuilder:default=true
	ReplaceSource *bool `json:"replaceSource,omitempty"`

	// RecycleBin keeps the replaced source in the root folder's recycle bin.
	// false lets the swap drop the library's link to it outright (a seeding
	// hard link elsewhere under /data keeps its own copy either way). A
	// pointer so a Go client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	RecycleBin *bool `json:"recycleBin,omitempty"`
}

// GPUSpec schedules the encode pod onto a GPU.
type GPUSpec struct {
	// Count is the number of GPUs requested.
	// +optional
	// +kubebuilder:default=1
	Count int32 `json:"count,omitempty"`

	// RuntimeClassName is the RuntimeClass used for the encode pod.
	// +optional
	// +kubebuilder:default="nvidia"
	RuntimeClassName string `json:"runtimeClassName,omitempty"`

	// NodeSelector is applied to the encode pod.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations are applied to the encode pod.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// ChunkSpec configures chunked (parallel) encoding. Chunking is deferred: it
// is accepted by the API but Enabled must be false in v1alpha1.
type ChunkSpec struct {
	// Enabled turns on chunked encoding. Must be false in v1alpha1.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// TranscodeProfileSpec defines the desired state of TranscodeProfile.
type TranscodeProfileSpec struct {
	// Default marks this profile as the cluster default. Exactly one profile
	// may be the default; the controller sets Invalid on the newer one.
	// +optional
	Default bool `json:"default,omitempty"`

	// Selector matches MediaFile labels this profile applies to. Only video
	// kinds (movie, episode) are eligible; enforced by the controller.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Container is the output container.
	// +optional
	// +kubebuilder:default="mkv"
	Container Container `json:"container,omitempty"`

	// Quality is the standard's one quality setting (0 best, 51 smallest),
	// mapped to each encoder's own control by a code table: libx265 crf,
	// hevc_nvenc qp (quality - 1, under constqp), hevc_qsv global_quality,
	// hevc_vaapi qp. Unset means 24 (QualityOrDefault), and hashes as 24.
	// Everything else about the encode is the standard's (ffgo spec §1):
	// the video, HDR, subtitle and verification settings profiles had
	// until 2026-10-01 were removed, and the apiserver prunes them from a
	// stored profile.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=51
	Quality *int32 `json:"quality,omitempty"`

	// Hardware is the encoder backend: auto is chosen per task, with CPU
	// fallback; cpu, nvidia and intel are pinned and never fall back.
	// +optional
	// +kubebuilder:default="auto"
	Hardware Hardware `json:"hardware,omitempty"`

	// Audio decides which audio languages are kept.
	// +optional
	// +kubebuilder:default={}
	Audio AudioSpec `json:"audio,omitempty"`

	// Policy decides which files are transcoded and what happens afterwards.
	// +optional
	// +kubebuilder:default={}
	Policy PolicySpec `json:"policy,omitempty"`

	// Resources are the encode container's resource requirements. The default
	// limits (cpu 8, memory 4Gi) suit 1080p; the CPU limit is fed to the x265
	// thread pools. The kubebuilder default fills only an ABSENT field, and a
	// Go client always sends this struct, so the controller also floors an
	// entirely empty value (no limits, no requests) to the same default when
	// it builds the Job: an encode with no resources at all is never meant.
	// A GPU pool (nvidia, intel) keeps these limits but requests only cpu
	// 500m and memory 512Mi (each capped at its limit), since its encode
	// runs on the GPU; the cpu pool requests these resources as they are.
	// +optional
	// +kubebuilder:default={limits:{cpu:"8",memory:"4Gi"}}
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// GPU schedules the encode pod onto a GPU.
	// +optional
	GPU *GPUSpec `json:"gpu,omitempty"`

	// Scratch is the emptyDir sizeLimit for the encode pod's scratch space.
	// A Go client always sends a Quantity, so the controller floors a zero
	// (or negative) one to the 20Gi default when it builds the Job: a
	// zero-byte scratch volume has no coherent meaning.
	// +optional
	// +kubebuilder:default="20Gi"
	Scratch resource.Quantity `json:"scratch,omitempty"`

	// Priority orders jobs created from this profile; higher runs first.
	// +optional
	// +kubebuilder:default=50
	Priority int32 `json:"priority,omitempty"`

	// MaxConcurrent caps how many TranscodeJobs created from this profile may
	// run at once, across every hardware class -- the "per-profile counts"
	// admission applies alongside the --slots budgets. Zero or absent means no
	// per-profile cap: only the hardware slot budget applies.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`

	// ActiveDeadline is each task's deadline, enforced by the worker; a task
	// past it is blocked as DeadlineExceeded. A Go client always sends a
	// Duration, so the controller floors a zero (or negative) one to the 48h
	// default when it builds the Job, rather than running the encode with no
	// deadline at all.
	// +optional
	// +kubebuilder:default="48h"
	ActiveDeadline metav1.Duration `json:"activeDeadline,omitempty"`

	// TTLSecondsAfterFinished: Deprecated: ignored. Transcode pools never
	// finish; this is removed at the next API version.
	// +optional
	// +kubebuilder:default=86400
	TTLSecondsAfterFinished int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// Chunking configures chunked encoding. Deferred: accepted but
	// chunking.enabled must be false in v1alpha1.
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled",message="chunking is not supported in v1alpha1: chunking.enabled must be false"
	Chunking *ChunkSpec `json:"chunking,omitempty"`
}

// TranscodeProfileStatus defines the observed state of TranscodeProfile.
type TranscodeProfileStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Hash is the sha256 of the standard's inputs -- quality, container,
	// audio.languages, policy.neverTranscodeModifiers -- and the standard's
	// version. Encode pods tag their output CLUSTARR_PROFILE=<name>@<hash>.
	// A new hash plans new jobs only for files not yet transcoded: a
	// transcoded file is final, whatever hash it carries.
	// +optional
	Hash string `json:"hash,omitempty"`

	// MatchingFiles is the number of MediaFiles selected by this profile.
	// +optional
	MatchingFiles int32 `json:"matchingFiles,omitempty"`

	// PendingJobs is the number of TranscodeJobs for this profile not yet running.
	// +optional
	PendingJobs int32 `json:"pendingJobs,omitempty"`

	// RunningJobs is the number of TranscodeJobs for this profile currently running.
	// +optional
	RunningJobs int32 `json:"runningJobs,omitempty"`

	// Conditions holds Ready and Invalid.
	// EncoderLimits are the encoder device limits each GPU node measured by
	// trial encodes and published within the last ten minutes, per class and
	// node. Plans for a class render min(spec, the tightest limit here), and
	// each clamp is named in the job's Planned message. Empty while no GPU
	// pool of this cluster has published.
	// +optional
	// +listType=map
	// +listMapKey=class
	// +listMapKey=node
	// +kubebuilder:validation:MaxItems=16
	EncoderLimits []EncoderLimit `json:"encoderLimits,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// TranscodeProfile describes how matching MediaFiles are transcoded.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Cluster,categories=clustarr
// +kubebuilder:printcolumn:name="Default",type="boolean",JSONPath=".spec.default"
// +kubebuilder:printcolumn:name="Hardware",type="string",JSONPath=".spec.hardware"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Matching",type="integer",JSONPath=".status.matchingFiles"
// +kubebuilder:printcolumn:name="Pending",type="integer",JSONPath=".status.pendingJobs"
// +kubebuilder:printcolumn:name="Running",type="integer",JSONPath=".status.runningJobs"
// +kubebuilder:printcolumn:name="Hash",type="string",JSONPath=".status.hash",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type TranscodeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec TranscodeProfileSpec `json:"spec,omitempty"`
	// +optional
	Status TranscodeProfileStatus `json:"status,omitempty"`
}

// TranscodeProfileList contains a list of TranscodeProfile.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type TranscodeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TranscodeProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TranscodeProfile{}, &TranscodeProfileList{})
}
