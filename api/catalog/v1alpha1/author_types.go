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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types reported on an Author.
const (
	// AuthorConditionReady is True when the author is fully reconciled.
	AuthorConditionReady = "Ready"
	// AuthorConditionMetadataReady is True once metadata has been fetched.
	AuthorConditionMetadataReady = "MetadataReady"
	// AuthorConditionBooksSynced is True once the Book objects match the metadata.
	AuthorConditionBooksSynced = "BooksSynced"
)

// AuthorMonitorMode says which books are monitored when an author is added.
//
// +kubebuilder:validation:Enum=all;future;missing;existing;none
type AuthorMonitorMode string

// Author monitor modes.
const (
	AuthorMonitorAll      AuthorMonitorMode = "all"
	AuthorMonitorFuture   AuthorMonitorMode = "future"
	AuthorMonitorMissing  AuthorMonitorMode = "missing"
	AuthorMonitorExisting AuthorMonitorMode = "existing"
	AuthorMonitorNone     AuthorMonitorMode = "none"
)

// BookMetadataProfile filters which Open Library works become Books.
type BookMetadataProfile struct {
	// MinPopularity is the lowest provider popularity score a work may have,
	// written as a decimal quantity such as "0.5" rather than a float, which
	// CRD schemas do not allow.
	// +optional
	MinPopularity *resource.Quantity `json:"minPopularity,omitempty"`

	// SkipMissingDate drops works with no release date.
	// +optional
	SkipMissingDate bool `json:"skipMissingDate,omitempty"`

	// SkipMissingISBN drops works with no edition carrying an ISBN.
	// +optional
	SkipMissingISBN bool `json:"skipMissingISBN,omitempty"`

	// SkipPartsAndSets drops box sets and part-of-a-volume entries.
	// +optional
	SkipPartsAndSets bool `json:"skipPartsAndSets,omitempty"`

	// SkipSeriesSecondary drops works that are only secondary entries in a series.
	// +optional
	SkipSeriesSecondary bool `json:"skipSeriesSecondary,omitempty"`

	// AllowedLanguages restricts works to these BCP-47 languages.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	AllowedLanguages []string `json:"allowedLanguages,omitempty"`

	// MinPages is the lowest page count a work may have.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MinPages int32 `json:"minPages,omitempty"`
}

// AuthorAddOptions are applied exactly once, when the author is first reconciled.
type AuthorAddOptions struct {
	// Monitor selects which books start out monitored.
	// +optional
	// +kubebuilder:default=all
	Monitor AuthorMonitorMode `json:"monitor,omitempty"`

	// SearchForMissing searches for missing books on add.
	// +optional
	SearchForMissing bool `json:"searchForMissing,omitempty"`
}

// AuthorMetadata is the provider metadata cached on the author.
type AuthorMetadata struct {
	// Name is the author name.
	// +optional
	Name string `json:"name,omitempty"`

	// SortName is the name used for sorting.
	// +optional
	SortName string `json:"sortName,omitempty"`

	// Disambiguation distinguishes same-named authors.
	// +optional
	Disambiguation string `json:"disambiguation,omitempty"`

	// Overview is the author biography.
	// +optional
	Overview string `json:"overview,omitempty"`

	// Genres lists the genres the author writes in.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Genres []string `json:"genres,omitempty"`

	// ExternalIDs maps provider names (openlibrary, hardcover, ...) to their IDs.
	// +optional
	ExternalIDs map[string]string `json:"externalIDs,omitempty"`

	// Images lists the artwork published by the provider.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Images []Image `json:"images,omitempty"`

	// RefreshedAt is when the metadata was last fetched.
	// +optional
	RefreshedAt metav1.Time `json:"refreshedAt,omitempty"`
}

// AuthorSpec defines the desired state of Author.
type AuthorSpec struct {
	// OpenLibraryID is the Open Library author key, e.g. "OL23919A"; it
	// identifies the author and is immutable.
	// +required
	// +kubebuilder:validation:Pattern=`^OL[0-9]+A$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="openLibraryID is immutable"
	OpenLibraryID string `json:"openLibraryID"`

	// HardcoverID is the Hardcover author ID, when the author is crosswalked.
	// +optional
	HardcoverID *string `json:"hardcoverID,omitempty"`

	// Monitored enables automatic searching and importing for this author.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// MonitorNewItems says what happens to books discovered after the author
	// was added.
	// +optional
	// +kubebuilder:default=all
	MonitorNewItems MonitorNewChildrenMode `json:"monitorNewItems,omitempty"`

	// MetadataProfile filters which works become Books.
	// +optional
	MetadataProfile BookMetadataProfile `json:"metadataProfile,omitempty"`

	// AddOptions are applied once, when the author is first reconciled.
	// +optional
	AddOptions AuthorAddOptions `json:"addOptions,omitempty"`

	// QualityProfileRef is the QualityProfile books are ranked against.
	// +required
	QualityProfileRef string `json:"qualityProfileRef"`

	// RootFolderRef is the RootFolder the author is stored under.
	// +required
	RootFolderRef string `json:"rootFolderRef"`

	// DelayProfileRef pins a DelayProfile; unset falls back to tag matching.
	// +optional
	DelayProfileRef *string `json:"delayProfileRef,omitempty"`

	// Folder overrides the folder name under the root folder.
	// +optional
	Folder *string `json:"folder,omitempty"`

	// Tags select DelayProfiles and other tag-matched policy.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Tags []string `json:"tags,omitempty"`

	// Source records the ImportList that added the author, if any.
	// +optional
	Source *commonv1.AddSource `json:"source,omitempty"`
}

// AuthorStatus describes the observed state of Author.
type AuthorStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the author's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *AuthorMetadata `json:"metadata,omitempty"`

	// Path is the resolved folder on disk.
	// +optional
	Path string `json:"path,omitempty"`

	// BookCount is the number of Book objects owned by the author.
	// +optional
	BookCount int32 `json:"bookCount,omitempty"`

	// BookFileCount is the number of books with an imported file.
	// +optional
	BookFileCount int32 `json:"bookFileCount,omitempty"`

	// AddOptionsApplied is true once spec.addOptions has been acted on.
	// +optional
	AddOptionsApplied bool `json:"addOptionsApplied,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=auth,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.openLibraryID`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.status.metadata.name`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Books",type=integer,JSONPath=`.status.bookCount`
// +kubebuilder:printcolumn:name="Files",type=integer,JSONPath=`.status.bookFileCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Author is a monitored book author in the catalog.
type Author struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AuthorSpec   `json:"spec,omitempty"`
	Status AuthorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// AuthorList contains a list of Author.
type AuthorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Author `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Author{}, &AuthorList{})
}
