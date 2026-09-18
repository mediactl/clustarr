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
)

// Condition types reported on an ImportExclusion.
const (
	// ImportExclusionConditionReady is True when the exclusion is in effect.
	ImportExclusionConditionReady = "Ready"
)

// ExclusionKind is the catalog kind an exclusion suppresses.
//
// +kubebuilder:validation:Enum=movie;series;album;book;audiobook;comic
type ExclusionKind string

// Exclusion kinds.
const (
	ExclusionKindMovie     ExclusionKind = "movie"
	ExclusionKindSeries    ExclusionKind = "series"
	ExclusionKindAlbum     ExclusionKind = "album"
	ExclusionKindBook      ExclusionKind = "book"
	ExclusionKindAudiobook ExclusionKind = "audiobook"
	ExclusionKindComic     ExclusionKind = "comic"
)

// Well-known keys of ImportExclusionSpec.ExternalIDs.
const (
	ExclusionIDKeyTMDB        = "tmdb"
	ExclusionIDKeyTVDB        = "tvdb"
	ExclusionIDKeyIMDB        = "imdb"
	ExclusionIDKeyMusicBrainz = "musicbrainz"
	ExclusionIDKeyOpenLibrary = "openlibrary"
	ExclusionIDKeyASIN        = "asin"
	ExclusionIDKeyComicVine   = "comicvine"
	ExclusionIDKeyMangaDex    = "mangadex"
)

// ImportExclusionSpec defines the desired state of ImportExclusion.
type ImportExclusionSpec struct {
	// Kind is the catalog kind the exclusion suppresses.
	// +required
	Kind ExclusionKind `json:"kind"`

	// ExternalIDs identifies the excluded item by at least one provider ID.
	// Recognised keys are tmdb, tvdb, imdb, musicbrainz, openlibrary, asin,
	// comicvine and mangadex.
	// +required
	// +kubebuilder:validation:MaxProperties=16
	// +kubebuilder:validation:XValidation:rule="size(self) > 0",message="at least one external ID is required"
	ExternalIDs map[string]string `json:"externalIDs"`

	// Title is the excluded item's title, kept for display.
	// +optional
	Title string `json:"title,omitempty"`

	// Year is the excluded item's year, kept for display.
	// +optional
	Year int32 `json:"year,omitempty"`

	// Reason explains why the item is excluded.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ImportExclusionStatus describes the observed state of ImportExclusion.
type ImportExclusionStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the exclusion's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// MatchCount is how many list entries this exclusion has suppressed.
	// +optional
	MatchCount int32 `json:"matchCount,omitempty"`

	// LastMatchedAt is when the exclusion last suppressed a list entry.
	// +optional
	LastMatchedAt *metav1.Time `json:"lastMatchedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=iex,categories=clustarr;catalog
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
// +kubebuilder:printcolumn:name="Year",type=integer,JSONPath=`.spec.year`
// +kubebuilder:printcolumn:name="Matches",type=integer,JSONPath=`.status.matchCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ImportExclusion stops an item from ever being added by an ImportList.
type ImportExclusion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImportExclusionSpec   `json:"spec,omitempty"`
	Status ImportExclusionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// ImportExclusionList contains a list of ImportExclusion.
type ImportExclusionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImportExclusion `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImportExclusion{}, &ImportExclusionList{})
}
