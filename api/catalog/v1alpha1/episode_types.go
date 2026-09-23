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

// Condition types reported on an Episode.
const (
	// EpisodeConditionAired is True once the episode's air date has passed.
	EpisodeConditionAired = "Aired"
	// EpisodeConditionHasFile is True while a MediaFile backs the episode.
	EpisodeConditionHasFile = "HasFile"
	// EpisodeConditionCutoffMet is True when the imported file meets the profile cutoff.
	EpisodeConditionCutoffMet = "CutoffMet"
	// EpisodeConditionReady is True when the episode is fully reconciled.
	EpisodeConditionReady = "Ready"
)

// EpisodePhase is the coarse lifecycle state of an episode.
//
// CutoffUnevaluated means a file is imported but the owning Series'
// QualityProfile could not be resolved, so the file was never ranked against
// a cutoff; see MoviePhase for why CutoffUnmet was the wrong answer there.
//
// +kubebuilder:validation:Enum=Unaired;Wanted;Delayed;Downloading;Imported;CutoffUnmet;CutoffUnevaluated;Unmonitored
type EpisodePhase string

// Episode phases.
const (
	EpisodePhaseUnaired     EpisodePhase = "Unaired"
	EpisodePhaseWanted      EpisodePhase = "Wanted"
	EpisodePhaseDelayed     EpisodePhase = "Delayed"
	EpisodePhaseDownloading EpisodePhase = "Downloading"
	EpisodePhaseImported    EpisodePhase = "Imported"
	EpisodePhaseCutoffUnmet EpisodePhase = "CutoffUnmet"
	// EpisodePhaseCutoffUnevaluated: a file is imported but the quality
	// profile could not be resolved, so its cutoff was never evaluated.
	EpisodePhaseCutoffUnevaluated EpisodePhase = "CutoffUnevaluated"
	EpisodePhaseUnmonitored       EpisodePhase = "Unmonitored"
)

// SceneNumbering is the numbering scene releases use for an episode when it
// differs from the official numbering.
type SceneNumbering struct {
	// Season is the scene season number.
	// +optional
	Season *int32 `json:"season,omitempty"`

	// Episode is the scene episode number.
	// +optional
	Episode *int32 `json:"episode,omitempty"`

	// Absolute is the scene absolute episode number.
	// +optional
	Absolute *int32 `json:"absolute,omitempty"`

	// Unverified is true when the mapping has not been confirmed upstream.
	// +optional
	Unverified bool `json:"unverified,omitempty"`
}

// EpisodeSpec defines the desired state of Episode. Episodes are created and
// owned by their Series; only Monitored is meant to be edited by users.
type EpisodeSpec struct {
	// SeriesRef is the name of the owning Series in the same namespace.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="seriesRef is immutable"
	SeriesRef string `json:"seriesRef"`

	// SeasonNumber is the season the episode belongs to; 0 is the specials season.
	// +required
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="seasonNumber is immutable"
	SeasonNumber int32 `json:"seasonNumber"`

	// EpisodeNumber is the episode number within the season.
	// +required
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="episodeNumber is immutable"
	EpisodeNumber int32 `json:"episodeNumber"`

	// Monitored enables automatic searching for this episode. The Series
	// controller sets it at creation and per the series' monitorNewItems; after
	// that it belongs to the user.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`
}

// EpisodeStatus describes the observed state of Episode.
type EpisodeStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the episode's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// TvdbID is the TheTVDB episode ID.
	// +optional
	TvdbID int64 `json:"tvdbID,omitempty"`

	// Title is the episode title.
	// +optional
	Title string `json:"title,omitempty"`

	// Overview is the episode synopsis.
	// +optional
	Overview string `json:"overview,omitempty"`

	// AirDate is when the episode first aired.
	// +optional
	AirDate *metav1.Time `json:"airDate,omitempty"`

	// RuntimeMinutes is the episode runtime in minutes.
	// +optional
	RuntimeMinutes int32 `json:"runtimeMinutes,omitempty"`

	// AbsoluteNumber is the absolute episode number, used for anime.
	// +optional
	AbsoluteNumber *int32 `json:"absoluteNumber,omitempty"`

	// SceneNumbering is the numbering scene releases use for this episode.
	// +optional
	SceneNumbering *SceneNumbering `json:"sceneNumbering,omitempty"`

	// FinaleType marks a season or series finale, e.g. "season" or "series".
	// +optional
	FinaleType string `json:"finaleType,omitempty"`

	// Phase is the coarse lifecycle state of the episode.
	// +optional
	Phase EpisodePhase `json:"phase,omitempty"`

	// HasFile is true while a MediaFile backs the episode.
	// +optional
	HasFile bool `json:"hasFile,omitempty"`

	// FileRef is the name of the MediaFile backing the episode.
	// +optional
	FileRef *string `json:"fileRef,omitempty"`

	// FileQuality is the quality of the imported file.
	// +optional
	FileQuality *commonv1.Quality `json:"fileQuality,omitempty"`

	// FileFormatScore is the custom-format score of the imported file.
	// +optional
	FileFormatScore int32 `json:"fileFormatScore,omitempty"`

	// CutoffMet is true when the imported file meets the profile cutoff.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the Download currently working on this episode.
	// +optional
	ActiveDownloadRef *string `json:"activeDownloadRef,omitempty"`

	// PendingGrab is a chosen release waiting out a DelayProfile delay.
	// +optional
	PendingGrab *PendingGrab `json:"pendingGrab,omitempty"`

	// LastSearchedAt is when the episode was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// SearchAttempts counts the searches made for this episode.
	// +optional
	SearchAttempts commonv1.Attempts `json:"searchAttempts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=ep,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.seriesRef`
// +kubebuilder:printcolumn:name="Series",type=string,JSONPath=`.spec.seriesRef`
// +kubebuilder:printcolumn:name="Season",type=integer,JSONPath=`.spec.seasonNumber`
// +kubebuilder:printcolumn:name="Episode",type=integer,JSONPath=`.spec.episodeNumber`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.title`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="File",type=boolean,JSONPath=`.status.hasFile`
// +kubebuilder:printcolumn:name="Aired",type=string,JSONPath=`.status.conditions[?(@.type=="Aired")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Episode is one episode of a Series. Episodes are created and owned by the
// Series controller and named <series>-s<NN>e<NN>, or <series>-<yyyy-mm-dd>
// for daily series.
type Episode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EpisodeSpec   `json:"spec,omitempty"`
	Status EpisodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// EpisodeList contains a list of Episode.
type EpisodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Episode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Episode{}, &EpisodeList{})
}
