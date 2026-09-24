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

// Condition types reported on a Comic.
const (
	// ComicConditionReady is True when the comic is fully reconciled.
	ComicConditionReady = "Ready"
	// ComicConditionMetadataReady is True once metadata has been fetched.
	ComicConditionMetadataReady = "MetadataReady"
	// ComicConditionIssuesSynced is True once the Issue objects match the metadata.
	ComicConditionIssuesSynced = "IssuesSynced"
)

// ComicSourceProvider is the upstream a comic's metadata comes from.
//
// +kubebuilder:validation:Enum=comicvine;mangadex
type ComicSourceProvider string

// Comic metadata sources.
const (
	ComicSourceComicVine ComicSourceProvider = "comicvine"
	ComicSourceMangaDex  ComicSourceProvider = "mangadex"
)

// ComicKind distinguishes western comics from manga.
//
// +kubebuilder:validation:Enum=comic;manga
type ComicKind string

// Comic kinds.
const (
	ComicKindComic ComicKind = "comic"
	ComicKindManga ComicKind = "manga"
)

// ComicSpecialVersion says how a volume is collected and numbered.
//
// +kubebuilder:validation:Enum=normal;tpb;oneShot;hardCover;omnibus;volumeAsIssue
type ComicSpecialVersion string

// Comic special versions.
const (
	ComicSpecialVersionNormal        ComicSpecialVersion = "normal"
	ComicSpecialVersionTPB           ComicSpecialVersion = "tpb"
	ComicSpecialVersionOneShot       ComicSpecialVersion = "oneShot"
	ComicSpecialVersionHardCover     ComicSpecialVersion = "hardCover"
	ComicSpecialVersionOmnibus       ComicSpecialVersion = "omnibus"
	ComicSpecialVersionVolumeAsIssue ComicSpecialVersion = "volumeAsIssue"
)

// MangaFlag says whether a volume is manga and, if so, how it reads.
//
// +kubebuilder:validation:Enum=unknown;no;yes;yesAndRightToLeft
type MangaFlag string

// Manga flags.
const (
	MangaFlagUnknown           MangaFlag = "unknown"
	MangaFlagNo                MangaFlag = "no"
	MangaFlagYes               MangaFlag = "yes"
	MangaFlagYesAndRightToLeft MangaFlag = "yesAndRightToLeft"
)

// ComicMetadata is the provider metadata cached on the comic.
type ComicMetadata struct {
	// Title is the volume title.
	// +optional
	Title string `json:"title,omitempty"`

	// Publisher is the publishing house.
	// +optional
	Publisher string `json:"publisher,omitempty"`

	// Overview is the synopsis.
	// +optional
	Overview string `json:"overview,omitempty"`

	// Year is the year the volume started.
	// +optional
	Year int32 `json:"year,omitempty"`

	// VolumeNumber is the volume number within the title, when numbered.
	// +optional
	VolumeNumber int32 `json:"volumeNumber,omitempty"`

	// IssueCount is the number of issues the provider lists.
	// +optional
	IssueCount int32 `json:"issueCount,omitempty"`

	// Manga says whether the volume is manga and how it reads.
	// +optional
	Manga MangaFlag `json:"manga,omitempty"`

	// AgeRating is the provider's age rating.
	// +optional
	AgeRating string `json:"ageRating,omitempty"`

	// ExternalIDs maps provider names (comicvine, metron, mangadex, anilist,
	// mal) to their IDs.
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

// ComicSpec defines the desired state of Comic.
type ComicSpec struct {
	// Source is the upstream the comic's metadata comes from.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="source is immutable"
	Source ComicSourceProvider `json:"source"`

	// SourceID is the ComicVine volume ID or MangaDex manga UUID.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="sourceID is immutable"
	SourceID string `json:"sourceID"`

	// Kind distinguishes western comics from manga.
	// +optional
	// +kubebuilder:default=comic
	Kind ComicKind `json:"kind,omitempty"`

	// Monitored enables automatic searching and importing for this comic.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// MonitorNewIssues monitors issues discovered after the comic was added.
	// +optional
	// +kubebuilder:default=true
	MonitorNewIssues *bool `json:"monitorNewIssues,omitempty"`

	// SpecialVersion says how the volume is collected and numbered.
	// +optional
	// +kubebuilder:default=normal
	SpecialVersion ComicSpecialVersion `json:"specialVersion,omitempty"`

	// UnmonitoredIssues lists the issue numbers the user unmonitored. It is
	// kept here so the choice survives Issue objects being recreated.
	// +optional
	// +kubebuilder:validation:MaxItems=1000
	UnmonitoredIssues []string `json:"unmonitoredIssues,omitempty"`

	// QualityProfileRef is the QualityProfile issues are ranked against; it
	// must be a profile whose mediaKind is comic.
	// +required
	QualityProfileRef string `json:"qualityProfileRef"`

	// RootFolderRef is the RootFolder the comic is stored under.
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

	// AddSource records the ImportList that added the comic, if any. It is
	// named addSource rather than source because spec.source already names the
	// metadata provider.
	// +optional
	AddSource *commonv1.AddSource `json:"addSource,omitempty"`

	// Artwork overrides the provider's image for a type. One entry per type.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkOverride `json:"artwork,omitempty"`
}

// ComicStatus describes the observed state of Comic.
type ComicStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the comic's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *ComicMetadata `json:"metadata,omitempty"`

	// Path is the resolved folder on disk.
	// +optional
	Path string `json:"path,omitempty"`

	// IssueFileCount is the number of issues with an imported file.
	// +optional
	IssueFileCount int32 `json:"issueFileCount,omitempty"`

	// NextPullDate is when the next issue is expected on the pull list.
	// +optional
	NextPullDate *metav1.Time `json:"nextPullDate,omitempty"`

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
// +kubebuilder:resource:scope=Namespaced,shortName=cmc,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.sourceID`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.metadata.title`
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Files",type=integer,JSONPath=`.status.issueFileCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Comic is a monitored comic or manga volume in the catalog. Its issues are
// separate Issue objects rather than an unbounded status list.
type Comic struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ComicSpec   `json:"spec,omitempty"`
	Status ComicStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// ComicList contains a list of Comic.
type ComicList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Comic `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Comic{}, &ComicList{})
}
