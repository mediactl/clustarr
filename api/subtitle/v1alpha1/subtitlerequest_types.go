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

// SubtitleRequestPhase is the coarse lifecycle phase of a SubtitleRequest.
//
// +kubebuilder:validation:Enum=Satisfied;Wanted;Searching;Blocked
type SubtitleRequestPhase string

// SubtitleRequest phases.
const (
	// SubtitleRequestPhaseSatisfied means every wanted language is present and
	// the profile cutoff is met.
	SubtitleRequestPhaseSatisfied SubtitleRequestPhase = "Satisfied"
	// SubtitleRequestPhaseWanted means at least one language is missing and is
	// waiting for its next search window.
	SubtitleRequestPhaseWanted SubtitleRequestPhase = "Wanted"
	// SubtitleRequestPhaseSearching means a worker is currently searching
	// providers for at least one language.
	SubtitleRequestPhaseSearching SubtitleRequestPhase = "Searching"
	// SubtitleRequestPhaseBlocked means no progress is possible, for example
	// because the media file is missing, the profile is invalid or every
	// provider is throttled.
	SubtitleRequestPhaseBlocked SubtitleRequestPhase = "Blocked"
)

// SubtitleItemState is the per-language state of a SubtitleRequest.
//
// +kubebuilder:validation:Enum=pending;searching;downloaded;upgradable;unavailable;failed
type SubtitleItemState string

// Per-language states.
const (
	// SubtitleItemPending means the language is wanted and queued.
	SubtitleItemPending SubtitleItemState = "pending"
	// SubtitleItemSearching means a worker is searching providers right now.
	SubtitleItemSearching SubtitleItemState = "searching"
	// SubtitleItemDownloaded means a subtitle at or above the cutoff is on disk.
	SubtitleItemDownloaded SubtitleItemState = "downloaded"
	// SubtitleItemUpgradable means a subtitle is on disk but scores below the
	// cutoff, so upgrade searches continue.
	SubtitleItemUpgradable SubtitleItemState = "upgradable"
	// SubtitleItemUnavailable means no candidate met the minimum score and the
	// adaptive search schedule has backed off.
	SubtitleItemUnavailable SubtitleItemState = "unavailable"
	// SubtitleItemFailed means the last attempt errored; see lastError.
	SubtitleItemFailed SubtitleItemState = "failed"
)

// SubtitleSource says where an already present subtitle lives.
//
// +kubebuilder:validation:Enum=embedded;sidecar
type SubtitleSource string

// Existing subtitle sources.
const (
	// SubtitleSourceEmbedded is a subtitle track inside the media container.
	SubtitleSourceEmbedded SubtitleSource = "embedded"
	// SubtitleSourceSidecar is a subtitle file next to the media file.
	SubtitleSourceSidecar SubtitleSource = "sidecar"
)

// SubtitleRequest condition types.
const (
	// SubtitleRequestConditionPlanned is True once the controller has resolved
	// the profile and written status.items for every wanted language.
	SubtitleRequestConditionPlanned = "Planned"
	// SubtitleRequestConditionSatisfied is True when every wanted language has
	// a subtitle on disk.
	SubtitleRequestConditionSatisfied = "Satisfied"
	// SubtitleRequestConditionCutoffMet is True when the profile cutoff
	// language is present at or above the minimum score.
	SubtitleRequestConditionCutoffMet = "CutoffMet"
)

// ExistingSub is a subtitle already present for the media file, found by
// probing the container and scanning the media file's directory.
type ExistingSub struct {
	// LangKey is the profile language key this subtitle satisfies.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	LangKey string `json:"langKey"`

	// Source says whether the subtitle is embedded in the container or a
	// sidecar file.
	// +required
	Source SubtitleSource `json:"source"`

	// Path is the sidecar path relative to the media file's directory, or the
	// media file itself for an embedded track.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path,omitempty"`

	// StreamIndex is the zero-based container stream index of an embedded
	// track. Unset for sidecars.
	// +optional
	// +kubebuilder:validation:Minimum=0
	StreamIndex *int32 `json:"streamIndex,omitempty"`
}

// SubtitleItem is the work unit for one wanted language of one media file: its
// state, the chosen candidate and the search backoff.
//
// The controller owns nextSearchAt and attempts; captionarr-worker owns the
// remaining fields and applies them server-side, so the two writers stay
// disjoint within an item.
//
// An item is live exactly while the controller owns its nextSearchAt. The
// controller creates an item by applying langKey, nextSearchAt and attempts
// alone, and removes one by no longer sending it; the worker re-sends its
// leaves only for items that still carry nextSearchAt, so once both have
// stopped, nothing owns the entry and it is deleted.
type SubtitleItem struct {
	// LangKey is the profile language key this item covers.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	LangKey string `json:"langKey"`

	// State is the current per-language state, written by captionarr-worker.
	// Absent means planned, never searched: the controller has created the
	// item and no worker has reported on it yet. It has no default on
	// purpose -- a defaulted value would be owned by no field manager.
	// +optional
	State SubtitleItemState `json:"state,omitempty"`

	// Score is the score of the chosen candidate.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Score int32 `json:"score,omitempty"`

	// ScoreOutOf is the maximum score achievable for this media file, against
	// which score is compared to derive the percentage the profile requires.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ScoreOutOf int32 `json:"scoreOutOf,omitempty"`

	// Provider is the name of the SubtitleProvider the chosen candidate came from.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Provider string `json:"provider,omitempty"`

	// SubtitleID is the provider-scoped identifier of the chosen candidate,
	// used to avoid re-downloading it and to blacklist it on a bad match.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	SubtitleID string `json:"subtitleID,omitempty"`

	// Path is the written sidecar path relative to the media file's directory.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path,omitempty"`

	// Attempts tracks how often this language has been searched.
	// +optional
	Attempts commonv1alpha1.Attempts `json:"attempts,omitempty"`

	// NextSearchAt is when the next search for this language becomes due; it is
	// the backoff schedule the controller derives from the profile's search and
	// upgrade intervals and from attempts.
	// +optional
	NextSearchAt *metav1.Time `json:"nextSearchAt,omitempty"`

	// LastError is the error from the most recent failed attempt.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	LastError string `json:"lastError,omitempty"`

	// DownloadedAt is when the chosen candidate was written to disk.
	// +optional
	DownloadedAt *metav1.Time `json:"downloadedAt,omitempty"`
}

// SubtitleRequestSpec defines the desired state of SubtitleRequest.
type SubtitleRequestSpec struct {
	// MediaFileRef is the name of the video MediaFile this request covers, in
	// the same namespace. It is immutable and equals the object's own name.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mediaFileRef is immutable"
	MediaFileRef string `json:"mediaFileRef"`

	// ProfileRef is the name of the SubtitleProfile to apply. Empty lets the
	// controller select by label, falling back to the default profile.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ProfileRef string `json:"profileRef,omitempty"`

	// Languages overrides the profile's wanted langKeys for this media file.
	// Every entry must be one of the profile's language keys.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=64
	Languages []string `json:"languages,omitempty"`

	// MinScoreOverride overrides the profile's minimum score percentage for
	// this media file.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MinScoreOverride *int32 `json:"minScoreOverride,omitempty"`

	// ForceSearch requests one immediate search of every unsatisfied language,
	// ignoring nextSearchAt. It is one-shot: the controller resets it to false
	// once the search has been dispatched.
	// +optional
	// +kubebuilder:default=false
	ForceSearch bool `json:"forceSearch,omitempty"`
}

// SubtitleRequestStatus defines the observed state of SubtitleRequest.
type SubtitleRequestStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the coarse lifecycle phase.
	// +optional
	Phase SubtitleRequestPhase `json:"phase,omitempty"`

	// ProfileGeneration is the metadata.generation of the SubtitleProfile the
	// current plan was computed from; a newer profile forces a replan.
	// +optional
	ProfileGeneration int64 `json:"profileGeneration,omitempty"`

	// ProbeHash identifies the container probe the existing list was derived
	// from, so the probe is not repeated while the file is unchanged.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`

	// FileFingerprint is the size and modification time of the target media
	// file at the time of the last probe. It is the dedup key: an unchanged
	// fingerprint means existing, probeHash and the downloaded items still
	// describe the file on disk.
	// +optional
	FileFingerprint *commonv1alpha1.FileFingerprint `json:"fileFingerprint,omitempty"`

	// Existing lists the subtitles already present for the media file.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=64
	Existing []ExistingSub `json:"existing,omitempty"`

	// Items is the per-language work unit list, keyed by langKey.
	// +optional
	// +listType=map
	// +listMapKey=langKey
	// +kubebuilder:validation:MaxItems=20
	Items []SubtitleItem `json:"items,omitempty"`

	// Conditions holds Planned, Satisfied and CutoffMet.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// SubtitleRequest is the subtitle work for one video MediaFile: one item per
// wanted language. It is owned by the MediaFile and shares its name.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,categories=clustarr
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="MediaFile",type="string",JSONPath=".spec.mediaFileRef"
// +kubebuilder:printcolumn:name="Profile",type="string",JSONPath=".spec.profileRef"
// +kubebuilder:printcolumn:name="Satisfied",type="string",JSONPath=".status.conditions[?(@.type==\"Satisfied\")].status"
// +kubebuilder:printcolumn:name="Cutoff",type="string",JSONPath=".status.conditions[?(@.type==\"CutoffMet\")].status",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type SubtitleRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec SubtitleRequestSpec `json:"spec,omitempty"`
	// +optional
	Status SubtitleRequestStatus `json:"status,omitempty"`
}

// SubtitleRequestList contains a list of SubtitleRequest.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type SubtitleRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubtitleRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubtitleRequest{}, &SubtitleRequestList{})
}
