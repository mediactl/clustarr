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

// Condition types reported on a Book.
const (
	// BookConditionReady is True when the book is fully reconciled.
	BookConditionReady = "Ready"
	// BookConditionMetadataReady is True once metadata has been fetched.
	BookConditionMetadataReady = "MetadataReady"
	// BookConditionHasFile is True while a MediaFile backs the book.
	BookConditionHasFile = "HasFile"
	// BookConditionCutoffMet is True when the imported file meets the profile cutoff.
	BookConditionCutoffMet = "CutoffMet"
)

// BookPhase is the coarse lifecycle state of a book.
//
// +kubebuilder:validation:Enum=Wanted;Delayed;Downloading;Imported;CutoffUnmet;Unmonitored
type BookPhase string

// Book phases.
const (
	BookPhaseWanted      BookPhase = "Wanted"
	BookPhaseDelayed     BookPhase = "Delayed"
	BookPhaseDownloading BookPhase = "Downloading"
	BookPhaseImported    BookPhase = "Imported"
	BookPhaseCutoffUnmet BookPhase = "CutoffUnmet"
	BookPhaseUnmonitored BookPhase = "Unmonitored"
)

// EditionSpec overrides monitoring for one Open Library edition.
type EditionSpec struct {
	// ID is the Open Library edition key, e.g. "OL7353617M".
	// +required
	ID string `json:"id"`

	// Monitored enables automatic searching for this edition.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`
}

// Edition is one published edition of a work.
type Edition struct {
	// ID is the Open Library edition key.
	// +required
	ID string `json:"id"`

	// ISBN13 is the 13-digit ISBN of the edition.
	// +optional
	ISBN13 string `json:"isbn13,omitempty"`

	// ASIN is the Amazon ASIN of the edition.
	// +optional
	ASIN string `json:"asin,omitempty"`

	// Title is the edition title.
	// +optional
	Title string `json:"title,omitempty"`

	// Language is the BCP-47 language tag of the edition.
	// +optional
	Language string `json:"language,omitempty"`

	// Format is the physical or digital format, e.g. Hardcover or EPUB.
	// +optional
	Format string `json:"format,omitempty"`

	// Publisher is the publishing house.
	// +optional
	Publisher string `json:"publisher,omitempty"`

	// IsEbook is true when the edition is a digital edition.
	// +optional
	IsEbook bool `json:"isEbook,omitempty"`

	// PageCount is the number of pages.
	// +optional
	PageCount int32 `json:"pageCount,omitempty"`

	// ReleaseDate is the edition's publication date.
	// +optional
	ReleaseDate *metav1.Time `json:"releaseDate,omitempty"`
}

// BookMetadata is the provider metadata cached on the book.
type BookMetadata struct {
	// Title is the work title.
	// +optional
	Title string `json:"title,omitempty"`

	// Subtitle is the work subtitle.
	// +optional
	Subtitle string `json:"subtitle,omitempty"`

	// Overview is the synopsis.
	// +optional
	Overview string `json:"overview,omitempty"`

	// SeriesLinks places the work inside one or more reading orders.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	SeriesLinks []SeriesLink `json:"seriesLinks,omitempty"`

	// ReleaseDate is the first publication date of the work.
	// +optional
	ReleaseDate *metav1.Time `json:"releaseDate,omitempty"`

	// Genres lists the genres.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Genres []string `json:"genres,omitempty"`

	// Editions lists the known editions of the work.
	// +optional
	// +listType=map
	// +listMapKey=id
	// +kubebuilder:validation:MaxItems=100
	Editions []Edition `json:"editions,omitempty"`

	// ExternalIDs maps provider names (openlibrary, hardcover, isbn, ...) to their IDs.
	// +optional
	ExternalIDs map[string]string `json:"externalIDs,omitempty"`

	// Images lists the cover art published by the provider.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Images []Image `json:"images,omitempty"`

	// RefreshedAt is when the metadata was last fetched.
	// +optional
	RefreshedAt metav1.Time `json:"refreshedAt,omitempty"`
}

// BookSpec defines the desired state of Book.
type BookSpec struct {
	// AuthorRef is the name of the owning Author; unset makes the book standalone.
	// +optional
	AuthorRef *string `json:"authorRef,omitempty"`

	// WorkID is the Open Library work key, e.g. "OL45883W"; it identifies the
	// book and is immutable.
	// +required
	// +kubebuilder:validation:Pattern=`^OL[0-9]+W$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="workID is immutable"
	WorkID string `json:"workID"`

	// Monitored enables automatic searching and importing for this book.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// AnyEditionOk accepts an edition other than the pinned one when it is the
	// best match.
	// +optional
	// +kubebuilder:default=true
	AnyEditionOk *bool `json:"anyEditionOk,omitempty"`

	// EditionID pins a specific Open Library edition.
	// +optional
	EditionID *string `json:"editionID,omitempty"`

	// Editions overrides monitoring per edition.
	// +optional
	// +listType=map
	// +listMapKey=id
	// +kubebuilder:validation:MaxItems=100
	Editions []EditionSpec `json:"editions,omitempty"`

	// QualityProfileRef overrides the Author's QualityProfile.
	// +optional
	QualityProfileRef *string `json:"qualityProfileRef,omitempty"`

	// RootFolderRef overrides the Author's RootFolder.
	// +optional
	RootFolderRef *string `json:"rootFolderRef,omitempty"`
}

// BookStatus describes the observed state of Book.
type BookStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the book's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *BookMetadata `json:"metadata,omitempty"`

	// Phase is the coarse lifecycle state of the book.
	// +optional
	Phase BookPhase `json:"phase,omitempty"`

	// Path is the resolved folder on disk.
	// +optional
	Path string `json:"path,omitempty"`

	// HasFile is true while a MediaFile backs the book.
	// +optional
	HasFile bool `json:"hasFile,omitempty"`

	// FileRef is the name of the MediaFile backing the book.
	// +optional
	FileRef *string `json:"fileRef,omitempty"`

	// FileFormat is the format of the imported file, e.g. EPUB or AZW3.
	// +optional
	FileFormat string `json:"fileFormat,omitempty"`

	// CutoffMet is true when the imported file meets the profile cutoff.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the Download currently working on this book.
	// +optional
	ActiveDownloadRef *string `json:"activeDownloadRef,omitempty"`

	// PendingGrab is a chosen release waiting out a DelayProfile delay.
	// +optional
	PendingGrab *PendingGrab `json:"pendingGrab,omitempty"`

	// LastSearchedAt is when the book was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// SearchAttempts counts the searches made for this book.
	// +optional
	SearchAttempts commonv1.Attempts `json:"searchAttempts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=bk,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.workID`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.metadata.title`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="File",type=boolean,JSONPath=`.status.hasFile`
// +kubebuilder:printcolumn:name="Format",type=string,JSONPath=`.status.fileFormat`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Book is one Open Library work, owned by an Author or standalone.
type Book struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BookSpec   `json:"spec,omitempty"`
	Status BookStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// BookList contains a list of Book.
type BookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Book `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Book{}, &BookList{})
}
