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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// AudioGraftSpec is a donor's audio, kept beside the library, to be muxed
// into an item's file (anime dual-audio spec §6.2). importarr writes it when
// it imports a donor and rewrites it when a newer donor replaces it.
type AudioGraftSpec struct {
	// ItemRef is the Episode or Movie the donor is for.
	// +required
	ItemRef commonv1.MediaRef `json:"itemRef"`

	// DonorPath is the donor file as importarr placed it, under
	// <RootFolder>/.clustarr/donors/<item-uid>/. The first graft reduces a
	// non-Matroska-audio donor to <stem>.mka beside it and removes it.
	// +required
	// +kubebuilder:validation:MinLength=1
	DonorPath string `json:"donorPath"`

	// Languages are the tracks to graft, BCP-47.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +listType=set
	Languages []string `json:"languages"`

	// Anchor is the language both files carry, which the donor is aligned
	// on: the item's original language (ja).
	// +required
	// +kubebuilder:validation:MinLength=1
	Anchor string `json:"anchor"`

	// Default is the profile's default audio language: a grafted track in
	// it becomes the file's default track.
	// +optional
	Default string `json:"default,omitempty"`

	// Release is the donor's release title.
	// +required
	Release string `json:"release"`
}

// AudioGraftPhase is where a graft stands.
// +kubebuilder:validation:Enum=Waiting;Pending;Running;Succeeded;Failed
type AudioGraftPhase string

const (
	// AudioGraftWaiting: nothing to run yet -- no probed file, an open
	// transcode, or no free graft slot (reason says which).
	AudioGraftWaiting AudioGraftPhase = "Waiting"
	// AudioGraftPending: the graft Job is created and has not started.
	AudioGraftPending AudioGraftPhase = "Pending"
	// AudioGraftRunning: the graft Job's pod runs.
	AudioGraftRunning AudioGraftPhase = "Running"
	// AudioGraftSucceeded: the file carries the languages, grafted or
	// already (reason Present).
	AudioGraftSucceeded AudioGraftPhase = "Succeeded"
	// AudioGraftFailed: alignment, mux or verify failed; the file is
	// untouched and the donor's release is rejected for this item.
	AudioGraftFailed AudioGraftPhase = "Failed"
)

// AudioGraftSegment is one run of the donor placed on the target, in
// milliseconds: DonorStart on the donor's timeline stretched by the rate.
type AudioGraftSegment struct {
	DonorStartMillis  int64 `json:"donorStartMillis"`
	TargetStartMillis int64 `json:"targetStartMillis"`
	LengthMillis      int64 `json:"lengthMillis"`
}

// AudioGraftStatus is squasharr's (ManagerSquasharr).
type AudioGraftStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase AudioGraftPhase `json:"phase,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message,omitempty"`

	// MediaFileRef is the MediaFile the graft was judged against: the
	// item's file when this status was written.
	// +optional
	MediaFileRef string `json:"mediaFileRef,omitempty"`
	// TargetProbeHash is that file's probe hash then; a Failed graft is
	// retried only for another hash or another generation.
	// +optional
	TargetProbeHash string `json:"targetProbeHash,omitempty"`
	// JobName is the Job of the current attempt: a graft or reduce Job, or
	// "transcodejob/<name>" for a graft riding along with that transcode.
	// +optional
	JobName string `json:"jobName,omitempty"`
	// DonorAudioPath is the donor reduced to its audio (<stem>.mka), which
	// every graft reads; empty until the reduce Job has run.
	// +optional
	DonorAudioPath string `json:"donorAudioPath,omitempty"`

	// RateName is the donor's speed against the target ("1", "25/23.976");
	// RateMicros the refined rate in millionths.
	// +optional
	RateName string `json:"rateName,omitempty"`
	// +optional
	RateMicros int64 `json:"rateMicros,omitempty"`
	// RateMarginMilli is the chosen rate's margin over the runner-up, in
	// thousandths (2030 = 2.03).
	// +optional
	RateMarginMilli int32 `json:"rateMarginMilli,omitempty"`
	// CoveragePercent is the share of the target the confident windows
	// cover.
	// +optional
	CoveragePercent int32 `json:"coveragePercent,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	Segments []AudioGraftSegment `json:"segments,omitempty"`
	// ResidualMillis is the verify pass's median residual offset, and
	// Within80Percent its share of windows within 80 ms.
	// +optional
	ResidualMillis int32 `json:"residualMillis,omitempty"`
	// +optional
	Within80Percent int32 `json:"within80Percent,omitempty"`
	// GraftTag is the CLUSTARR_GRAFT tag the grafted file carries.
	// +optional
	GraftTag string `json:"graftTag,omitempty"`

	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// RejectedReleases are donor releases a graft failed with: the donor
	// search never takes them again for this item (spec §9).
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	RejectedReleases []string `json:"rejectedReleases,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GraftResultPhase is where a graft riding along with a transcode stands.
// +kubebuilder:validation:Enum=Joined;Succeeded;Failed
type GraftResultPhase string

const (
	GraftJoined    GraftResultPhase = "Joined"
	GraftSucceeded GraftResultPhase = "Succeeded"
	GraftFailed    GraftResultPhase = "Failed"
)

// GraftResult is a graft run inside a TranscodeJob (TranscodeJob
// status.graft): which AudioGraft joined it and, once the worker reported,
// what came of it. The AudioGraft controller copies it into the AudioGraft.
type GraftResult struct {
	Phase GraftResultPhase `json:"phase"`
	// AudioGraft names the AudioGraft that joined, and Release its donor.
	AudioGraft string `json:"audioGraft"`
	// +optional
	Release string `json:"release,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message,omitempty"`
	// +optional
	RateName string `json:"rateName,omitempty"`
	// +optional
	RateMicros int64 `json:"rateMicros,omitempty"`
	// +optional
	RateMarginMilli int32 `json:"rateMarginMilli,omitempty"`
	// +optional
	CoveragePercent int32 `json:"coveragePercent,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	Segments []AudioGraftSegment `json:"segments,omitempty"`
	// +optional
	ResidualMillis int32 `json:"residualMillis,omitempty"`
	// +optional
	Within80Percent int32 `json:"within80Percent,omitempty"`
	// +optional
	GraftTag string `json:"graftTag,omitempty"`
}

// AudioGraft is one item's donor audio and the graft of it into the item's
// file (anime dual-audio spec §6.2, §7.2). Spec is importarr's, status
// squasharr's.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories=clustarr
// +kubebuilder:printcolumn:name="Item",type="string",JSONPath=".spec.itemRef.name"
// +kubebuilder:printcolumn:name="Languages",type="string",JSONPath=".spec.languages"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=".status.reason"
// +kubebuilder:printcolumn:name="Rate",type="string",JSONPath=".status.rateName",priority=1
// +kubebuilder:printcolumn:name="Coverage",type="integer",JSONPath=".status.coveragePercent",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type AudioGraft struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AudioGraftSpec   `json:"spec"`
	Status AudioGraftStatus `json:"status,omitempty"`
}

// AudioGraftList contains a list of AudioGraft.
//
// +kubebuilder:object:root=true
type AudioGraftList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AudioGraft `json:"items"`
}

// Terminal reports whether the graft is finished for its current inputs.
func (p AudioGraftPhase) Terminal() bool {
	return p == AudioGraftSucceeded || p == AudioGraftFailed
}

func init() {
	SchemeBuilder.Register(&AudioGraft{}, &AudioGraftList{})
}
