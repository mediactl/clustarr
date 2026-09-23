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

// KeepOriginalPolicy says when the original audio track is kept alongside
// (or instead of) the re-encoded one.
//
// +kubebuilder:validation:Enum=never;lossless;atmos;always
type KeepOriginalPolicy string

// Keep-original policies.
const (
	KeepOriginalNever    KeepOriginalPolicy = "never"
	KeepOriginalLossless KeepOriginalPolicy = "lossless"
	KeepOriginalAtmos    KeepOriginalPolicy = "atmos"
	KeepOriginalAlways   KeepOriginalPolicy = "always"
)

// HDR10PlusMode says what happens to HDR10+ dynamic metadata.
//
// +kubebuilder:validation:Enum=drop
type HDR10PlusMode string

// HDR10+ modes.
const (
	HDR10PlusDrop HDR10PlusMode = "drop"
)

// DolbyVisionMode says what happens to Dolby Vision sources.
//
// +kubebuilder:validation:Enum=passthrough;downgradeToHDR10;reject
type DolbyVisionMode string

// Dolby Vision modes.
const (
	DolbyVisionPassthrough      DolbyVisionMode = "passthrough"
	DolbyVisionDowngradeToHDR10 DolbyVisionMode = "downgradeToHDR10"
	DolbyVisionReject           DolbyVisionMode = "reject"
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

// CRFTable holds the x265 constant-rate-factor per source resolution class.
type CRFTable struct {
	// SD is the CRF used for standard-definition sources.
	// +optional
	// +kubebuilder:default=21
	SD int32 `json:"sd,omitempty"`

	// HD is the CRF used for 720p/1080p sources.
	// +optional
	// +kubebuilder:default=22
	HD int32 `json:"hd,omitempty"`

	// UHD is the CRF used for 2160p sources.
	// +optional
	// +kubebuilder:default=23
	UHD int32 `json:"uhd,omitempty"`

	// HDROffset is added to the resolution CRF when the source is HDR; 0
	// means no HDR offset. A pointer so a Go client can send that 0: with
	// omitempty it would be dropped and defaulted back to -1. Unset means -1;
	// read it through HDROffsetOrDefault.
	// +optional
	// +kubebuilder:default=-1
	HDROffset *int32 `json:"hdrOffset,omitempty"`
}

// NVENCSpec tunes the NVIDIA NVENC encoder (hardware=nvidia).
type NVENCSpec struct {
	// Preset is the NVENC preset (p1 fastest .. p7 slowest).
	// +optional
	// +kubebuilder:default="p6"
	Preset string `json:"preset,omitempty"`

	// Tune is the NVENC tuning target.
	// +optional
	// +kubebuilder:default="hq"
	Tune string `json:"tune,omitempty"`

	// CQ is the constant-quality level.
	// +optional
	// +kubebuilder:default=24
	CQ int32 `json:"cq,omitempty"`

	// Multipass is the NVENC multi-pass mode.
	// +optional
	// +kubebuilder:default="fullres"
	Multipass string `json:"multipass,omitempty"`

	// BRefMode is the NVENC B-frame reference mode.
	// +optional
	// +kubebuilder:default="middle"
	BRefMode string `json:"bRefMode,omitempty"`
}

// QSVSpec tunes the Intel Quick Sync encoder (hardware=intel).
type QSVSpec struct {
	// GlobalQuality is the QSV ICQ/global quality level.
	// +optional
	// +kubebuilder:default=22
	GlobalQuality int32 `json:"globalQuality,omitempty"`

	// Preset is the QSV preset.
	// +optional
	// +kubebuilder:default="veryslow"
	Preset string `json:"preset,omitempty"`

	// LookAheadDepth is the QSV look-ahead depth in frames.
	// +optional
	// +kubebuilder:default=40
	LookAheadDepth int32 `json:"lookAheadDepth,omitempty"`
}

// VideoSpec describes how the video stream is encoded.
type VideoSpec struct {
	// Codec is the target video codec.
	// +optional
	// +kubebuilder:default="hevc"
	Codec string `json:"codec,omitempty"`

	// PixelFormat is the target pixel format.
	// +optional
	// +kubebuilder:default="yuv420p10le"
	PixelFormat string `json:"pixelFormat,omitempty"`

	// Profile is the target codec profile.
	// +optional
	// +kubebuilder:default="main10"
	Profile string `json:"profile,omitempty"`

	// CRF is the constant-rate-factor table by resolution class.
	// +optional
	// +kubebuilder:default={}
	CRF CRFTable `json:"crf,omitempty"`

	// Preset is the x265 preset.
	// +optional
	// +kubebuilder:default="slow"
	Preset string `json:"preset,omitempty"`

	// Tune is the optional x265 tune.
	// +optional
	Tune *string `json:"tune,omitempty"`

	// KeyintFactor sets the keyframe interval as a multiple of the frame rate.
	// +optional
	// +kubebuilder:default=10
	KeyintFactor int32 `json:"keyintFactor,omitempty"`

	// BFrames is the number of consecutive B-frames.
	// +optional
	// +kubebuilder:default=8
	BFrames int32 `json:"bFrames,omitempty"`

	// Refs is the number of reference frames.
	// +optional
	// +kubebuilder:default=4
	Refs int32 `json:"refs,omitempty"`

	// RCLookahead is the rate-control look-ahead in frames.
	// +optional
	// +kubebuilder:default=40
	RCLookahead int32 `json:"rcLookahead,omitempty"`

	// AQMode is the adaptive-quantisation mode.
	// +optional
	// +kubebuilder:default=3
	AQMode int32 `json:"aqMode,omitempty"`

	// MaxRateKbps caps the video bitrate (VBV maxrate). Required for Dolby
	// Vision output; validated by the planner.
	// +optional
	MaxRateKbps *int32 `json:"maxRateKbps,omitempty"`

	// BufSizeKbps is the VBV buffer size. Required for Dolby Vision output;
	// validated by the planner.
	// +optional
	BufSizeKbps *int32 `json:"bufSizeKbps,omitempty"`

	// ExtraX265Params are additional key/value pairs appended to -x265-params.
	// +optional
	ExtraX265Params map[string]string `json:"extraX265Params,omitempty"`

	// NVENC tunes the NVIDIA encoder; used when hardware=nvidia.
	// +optional
	// +kubebuilder:default={}
	NVENC NVENCSpec `json:"nvenc,omitempty"`

	// QSV tunes the Intel Quick Sync encoder; used when hardware=intel.
	// +optional
	// +kubebuilder:default={}
	QSV QSVSpec `json:"qsv,omitempty"`
}

// AudioSpec describes how audio tracks are handled.
type AudioSpec struct {
	// Codec is the target audio codec for re-encoded tracks.
	// +optional
	// +kubebuilder:default="aac"
	Codec string `json:"codec,omitempty"`

	// BitratePerChannelKbps is the encoded bitrate per audio channel.
	// +optional
	// +kubebuilder:default=64
	BitratePerChannelKbps int32 `json:"bitratePerChannelKbps,omitempty"`

	// KeepOriginal says when the original track is kept instead of, or in
	// addition to, the re-encoded one.
	// +optional
	// +kubebuilder:default="atmos"
	KeepOriginal KeepOriginalPolicy `json:"keepOriginal,omitempty"`

	// Languages restricts which audio languages are kept; empty keeps all.
	// +optional
	Languages []string `json:"languages,omitempty"`

	// DropCommentary drops tracks flagged as commentary. A pointer so a Go
	// client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	DropCommentary *bool `json:"dropCommentary,omitempty"`

	// StereoCompatTrack adds a stereo downmix track for compatibility.
	// +optional
	// +kubebuilder:default=false
	StereoCompatTrack bool `json:"stereoCompatTrack,omitempty"`
}

// SubSpec describes how subtitle tracks and attachments are handled.
type SubSpec struct {
	// CopyText copies text-based subtitle tracks. A pointer so a Go client can
	// send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	CopyText *bool `json:"copyText,omitempty"`

	// CopyBitmap copies bitmap (PGS/VobSub) subtitle tracks. A pointer so a Go
	// client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	CopyBitmap *bool `json:"copyBitmap,omitempty"`

	// CopyAttachments copies container attachments such as fonts. A pointer so a
	// Go client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	CopyAttachments *bool `json:"copyAttachments,omitempty"`
}

// HDRSpec describes how high-dynamic-range metadata is handled.
type HDRSpec struct {
	// HDR10Plus says what happens to HDR10+ dynamic metadata.
	// +optional
	// +kubebuilder:default="drop"
	HDR10Plus HDR10PlusMode `json:"hdr10Plus,omitempty"`

	// DolbyVision says what happens to Dolby Vision sources.
	// +optional
	// +kubebuilder:default="passthrough"
	DolbyVision DolbyVisionMode `json:"dolbyVision,omitempty"`
}

// PolicySpec decides which files are transcoded and what happens afterwards.
type PolicySpec struct {
	// SkipIfCompliant skips files that already satisfy the profile. A pointer so
	// a Go client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	SkipIfCompliant *bool `json:"skipIfCompliant,omitempty"`

	// RemuxOnlyWhenVideoCompliant only remuxes (no video encode) when the video
	// stream already satisfies the profile. A pointer so a Go client can send an
	// explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	RemuxOnlyWhenVideoCompliant *bool `json:"remuxOnlyWhenVideoCompliant,omitempty"`

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

// VerifySpec describes post-encode verification.
type VerifySpec struct {
	// PacketCount compares packet counts between source and output. A pointer so
	// a Go client can send an explicit false; unset means true.
	// +optional
	// +kubebuilder:default=true
	PacketCount *bool `json:"packetCount,omitempty"`

	// FullDecode fully decodes the output to check for corruption.
	// +optional
	// +kubebuilder:default=false
	FullDecode bool `json:"fullDecode,omitempty"`

	// VMAFMin fails the job when the VMAF score falls below this value.
	// +optional
	VMAFMinCentis *int32 `json:"vmafMinCentis,omitempty"`
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

	// Hardware is the encoder backend: auto is chosen per task, with CPU
	// fallback; cpu, nvidia and intel are pinned and never fall back.
	// +optional
	// +kubebuilder:default="auto"
	Hardware Hardware `json:"hardware,omitempty"`

	// Video describes the video encode.
	// +optional
	// +kubebuilder:default={}
	Video VideoSpec `json:"video,omitempty"`

	// Audio describes audio handling.
	// +optional
	// +kubebuilder:default={}
	Audio AudioSpec `json:"audio,omitempty"`

	// Subtitles describes subtitle and attachment handling.
	// +optional
	// +kubebuilder:default={}
	Subtitles SubSpec `json:"subtitles,omitempty"`

	// HDR describes HDR metadata handling.
	// +optional
	// +kubebuilder:default={}
	HDR HDRSpec `json:"hdr,omitempty"`

	// Policy decides which files are transcoded and what happens afterwards.
	// +optional
	// +kubebuilder:default={}
	Policy PolicySpec `json:"policy,omitempty"`

	// Verify describes post-encode verification.
	// +optional
	// +kubebuilder:default={}
	Verify VerifySpec `json:"verify,omitempty"`

	// Resources are the encode container's resource requirements. The default
	// limits (cpu 8, memory 4Gi) suit 1080p; the CPU limit is fed to the x265
	// thread pools. The kubebuilder default fills only an ABSENT field, and a
	// Go client always sends this struct, so the controller also floors an
	// entirely empty value (no limits, no requests) to the same default when
	// it builds the Job: an encode with no resources at all is never meant.
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

	// Hash is the sha256 of the render-relevant spec. Encode pods receive it
	// as CLUSTARR_PROFILE=<name>@<hash>.
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
