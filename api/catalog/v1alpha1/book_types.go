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

// The book's phase values, commonv1.LeafPhase since 2026-11-10 (design D4).
const (
	BookPhasePending           = commonv1.LeafPhasePending
	BookPhaseUnavailable       = commonv1.LeafPhaseUnavailable
	BookPhaseWanted            = commonv1.LeafPhaseWanted
	BookPhaseDelayed           = commonv1.LeafPhaseDelayed
	BookPhaseDownloading       = commonv1.LeafPhaseDownloading
	BookPhaseImported          = commonv1.LeafPhaseImported
	BookPhaseCutoffUnmet       = commonv1.LeafPhaseCutoffUnmet
	BookPhaseCutoffUnevaluated = commonv1.LeafPhaseCutoffUnevaluated
	BookPhaseUnmonitored       = commonv1.LeafPhaseUnmonitored
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

	// SchemaVersion is the metadata gateway's pkg/metadata.SchemaVersion at
	// the time the document was built; a document older than the current
	// version is refreshed once (2026-11-10 design D8).
	// +optional
	SchemaVersion int32 `json:"schemaVersion,omitempty"`

	// Language is the BCP-47 language the metadata was fetched in.
	// +optional
	Language string `json:"language,omitempty"`

	// Ratings are the scores the sources report.
	// +optional
	// +listType=map
	// +listMapKey=source
	// +kubebuilder:validation:MaxItems=8
	Ratings []Rating `json:"ratings,omitempty"`

	// Languages lists the BCP-47 languages the work is published in.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Languages []string `json:"languages,omitempty"`

	// Subjects are the work's subject keywords.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Subjects []string `json:"subjects,omitempty"`

	// PageCount is the page count of the work's representative edition.
	// +optional
	PageCount int32 `json:"pageCount,omitempty"`

	// Publisher is the representative edition's publisher.
	// +optional
	Publisher string `json:"publisher,omitempty"`

	// Links are the work's external pages.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=16
	Links []Link `json:"links,omitempty"`

	// RefreshedAt is when the metadata was last fetched.
	// +optional
	RefreshedAt metav1.Time `json:"refreshedAt,omitempty"`
}

// BookSpec defines the desired state of Book. A standalone book (no
// authorRef) names its own quality profile and root folder (2026-11-10
// design §4.5).
// +kubebuilder:validation:XValidation:rule="has(self.authorRef) || (has(self.qualityProfileRef) && has(self.rootFolderRef))",message="a book without an author names its qualityProfileRef and rootFolderRef"
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

	// Artwork overrides the provider's image for a type. One entry per type.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkOverride `json:"artwork,omitempty"`
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

	// Phase is the coarse lifecycle state of the book (commonv1.LeafPhase,
	// 2026-11-10 design D4).
	// +optional
	Phase commonv1.LeafPhase `json:"phase,omitempty"`

	// CutoffReason says why CutoffMet reads as it does: Pending (no file),
	// ProfileUnresolved, CutoffMet, CutoffUnmet (2026-11-10 design D4).
	// +optional
	// +kubebuilder:validation:MaxLength=64
	CutoffReason string `json:"cutoffReason,omitempty"`

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

	// FileQuality is the quality frozen on the imported file at import, and
	// FileFormatScore its custom-format score (2026-11-10 design D5).
	// +optional
	FileQuality *commonv1.Quality `json:"fileQuality,omitempty"`
	// +optional
	FileFormatScore int32 `json:"fileFormatScore,omitempty"`

	// CutoffMet is true when the imported file meets the profile cutoff.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the id of the grab entry currently working on this book.
	// +optional
	ActiveDownloadRef *string `json:"activeDownloadRef,omitempty"`

	// Downloads is every grab of this item the system is responsible for, live
	// entries only (ADR-0019 §6.4); written by the remediation loop's item key
	// under catalog.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	Downloads []DownloadEntry `json:"downloads,omitempty"`

	// DownloadPhase is the active entry's phase, "" when none: a print column
	// and a selectable field (ADR-0019 §6.2).
	// +optional
	DownloadPhase commonv1.DownloadPhase `json:"downloadPhase,omitempty"`

	// DownloadNonces records the one-shot download intents last handled
	// (ADR-0019 §6.10).
	// +optional
	DownloadNonces *DownloadNonces `json:"downloadNonces,omitempty"`

	// SearchDispatch is the outstanding search task (ADR-0019 §7.3), written
	// under catalog-grab.
	// +optional
	SearchDispatch *Dispatch `json:"searchDispatch,omitempty"`

	// PendingGrab is a chosen release waiting out a DelayProfile delay.
	// +optional
	PendingGrab *PendingGrab `json:"pendingGrab,omitempty"`

	// LastSearchedAt is when the book was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// LastLiveSearchAt is when a search that asked the indexers was last
	// dispatched for the book -- its first search, a redownload, a backlog
	// search, or its season's search folded in -- where lastSearchedAt
	// moves for the searches the release index answers too. A
	// BacklogSearch's cooldown runs from it.
	// +optional
	LastLiveSearchAt *metav1.Time `json:"lastLiveSearchAt,omitempty"`

	// SearchAttempts counts the searches made for this book.
	// +optional
	SearchAttempts commonv1.Attempts `json:"searchAttempts,omitempty"`

	// Artwork lists the images fetched into the artwork store, one per type.
	// Written by the metadata gateway.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkEntry `json:"artwork,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=bk,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.workID`
// +kubebuilder:selectablefield:JSONPath=`.status.downloadPhase`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.metadata.title`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="File",type=boolean,JSONPath=`.status.hasFile`
// +kubebuilder:printcolumn:name="Format",type=string,JSONPath=`.status.fileFormat`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Download",type=string,JSONPath=`.status.downloadPhase`
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
