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

// Condition types reported on an Audiobook.
const (
	// AudiobookConditionReady is True when the audiobook is fully reconciled.
	AudiobookConditionReady = "Ready"
	// AudiobookConditionMetadataReady is True once metadata has been fetched.
	AudiobookConditionMetadataReady = "MetadataReady"
	// AudiobookConditionHasFile is True while MediaFiles back the audiobook.
	AudiobookConditionHasFile = "HasFile"
	// AudiobookConditionCutoffMet is True when the imported files meet the profile cutoff.
	AudiobookConditionCutoffMet = "CutoffMet"
	// AudiobookConditionQueueFull is True while metadata-refresh publishes are
	// being throttled. Controller-introduced, not spec-listed -- mirrors
	// MovieConditionQueueFull (movie_types.go), added by a later task there
	// for the same reason: it lets a full work queue be told apart from an
	// ordinary "refreshing" MetadataReady=false.
	AudiobookConditionQueueFull = "QueueFull"
	// AudiobookConditionBookRefResolved is True when spec.bookRef is unset or
	// names a Book that exists, False when it names one that does not. A
	// dangling bookRef never fails reconciliation (§8.1's Want flow does not
	// depend on it); this condition is the only place the problem surfaces.
	AudiobookConditionBookRefResolved = "BookRefResolved"
)

// AudiobookRegion is the Audible marketplace an ASIN belongs to.
//
// +kubebuilder:validation:Enum=us;uk;ca;au;de;fr;es;in;it;jp
type AudiobookRegion string

// Audible marketplaces.
const (
	AudiobookRegionUS AudiobookRegion = "us"
	AudiobookRegionUK AudiobookRegion = "uk"
	AudiobookRegionCA AudiobookRegion = "ca"
	AudiobookRegionAU AudiobookRegion = "au"
	AudiobookRegionDE AudiobookRegion = "de"
	AudiobookRegionFR AudiobookRegion = "fr"
	AudiobookRegionES AudiobookRegion = "es"
	AudiobookRegionIN AudiobookRegion = "in"
	AudiobookRegionIT AudiobookRegion = "it"
	AudiobookRegionJP AudiobookRegion = "jp"
)

// The audiobook's phase values, commonv1.LeafPhase since 2026-11-10 (design D4).
const (
	AudiobookPhasePending           = commonv1.LeafPhasePending
	AudiobookPhaseUnavailable       = commonv1.LeafPhaseUnavailable
	AudiobookPhaseWanted            = commonv1.LeafPhaseWanted
	AudiobookPhaseDelayed           = commonv1.LeafPhaseDelayed
	AudiobookPhaseDownloading       = commonv1.LeafPhaseDownloading
	AudiobookPhaseImported          = commonv1.LeafPhaseImported
	AudiobookPhaseCutoffUnmet       = commonv1.LeafPhaseCutoffUnmet
	AudiobookPhaseCutoffUnevaluated = commonv1.LeafPhaseCutoffUnevaluated
	AudiobookPhaseUnmonitored       = commonv1.LeafPhaseUnmonitored
)

// Chapter is one chapter marker of an audiobook.
type Chapter struct {
	// Title is the chapter title.
	// +required
	Title string `json:"title"`

	// StartMs is the chapter start offset in milliseconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	StartMs int64 `json:"startMs,omitempty"`
}

// AudiobookMetadata is the provider metadata cached on the audiobook.
type AudiobookMetadata struct {
	// Title is the audiobook title.
	// +optional
	Title string `json:"title,omitempty"`

	// Subtitle is the audiobook subtitle.
	// +optional
	Subtitle string `json:"subtitle,omitempty"`

	// Authors lists the credited authors.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Authors []NamedRef `json:"authors,omitempty"`

	// Narrators lists the credited narrators.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Narrators []string `json:"narrators,omitempty"`

	// Series places the audiobook inside a reading order.
	// +optional
	Series *SeriesLink `json:"series,omitempty"`

	// Publisher is the publishing house.
	// +optional
	Publisher string `json:"publisher,omitempty"`

	// ReleaseDate is the publication date.
	// +optional
	ReleaseDate *metav1.Time `json:"releaseDate,omitempty"`

	// RuntimeMinutes is the total runtime in minutes.
	// +optional
	RuntimeMinutes int32 `json:"runtimeMinutes,omitempty"`

	// Abridged is true for an abridged recording.
	// +optional
	Abridged bool `json:"abridged,omitempty"`

	// Language is the BCP-47 language tag of the recording.
	// +optional
	Language string `json:"language,omitempty"`

	// ISBN is the ISBN of the recording, when published with one.
	// +optional
	ISBN string `json:"isbn,omitempty"`

	// Overview is the synopsis.
	// +optional
	Overview string `json:"overview,omitempty"`

	// Genres lists the genres.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Genres []string `json:"genres,omitempty"`

	// Chapters lists the chapter markers.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Chapters []Chapter `json:"chapters,omitempty"`

	// ExternalIDs maps provider names (audnexus, asin, isbn, ...) to their IDs.
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

	// FetchLanguage is the BCP-47 language the metadata was fetched in
	// (language above is the recording's own).
	// +optional
	FetchLanguage string `json:"fetchLanguage,omitempty"`

	// Ratings are the scores the sources report.
	// +optional
	// +listType=map
	// +listMapKey=source
	// +kubebuilder:validation:MaxItems=8
	Ratings []Rating `json:"ratings,omitempty"`

	// Tags are Audible's tags.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Tags []string `json:"tags,omitempty"`

	// IsAdult is Audible's adult flag.
	// +optional
	IsAdult bool `json:"isAdult,omitempty"`

	// RefreshedAt is when the metadata was last fetched.
	// +optional
	RefreshedAt metav1.Time `json:"refreshedAt,omitempty"`
}

// AudiobookSpec defines the desired state of Audiobook.
type AudiobookSpec struct {
	// ASIN is the Audible ASIN; it identifies the audiobook and is immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="asin is immutable"
	ASIN string `json:"asin"`

	// Region is the Audible marketplace the ASIN belongs to.
	// +optional
	// +kubebuilder:default=us
	Region AudiobookRegion `json:"region,omitempty"`

	// BookRef links the audiobook to the Book it is a recording of.
	// +optional
	BookRef *string `json:"bookRef,omitempty"`

	// AuthorRef links the audiobook to its Author: set from the Book's
	// author when bookRef resolves, else by Add New when an Author's name
	// matches the first credited author (2026-11-10 design D11). The Plex
	// audiobooks root's artist level and the ui's author link read it.
	// +optional
	AuthorRef *string `json:"authorRef,omitempty"`

	// AddOptions are applied once, when the audiobook is first reconciled.
	// +optional
	AddOptions AudiobookAddOptions `json:"addOptions,omitempty"`

	// Monitored enables automatic searching and importing for this audiobook.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// QualityProfileRef is the QualityProfile this audiobook is ranked against;
	// it must be a profile whose mediaKind is audiobook.
	// +required
	QualityProfileRef string `json:"qualityProfileRef"`

	// RootFolderRef is the RootFolder the audiobook is stored under.
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

	// Source records the ImportList that added the audiobook, if any.
	// +optional
	Source *commonv1.AddSource `json:"source,omitempty"`

	// Artwork overrides the provider's image for a type. One entry per type.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkOverride `json:"artwork,omitempty"`
}

// AudiobookStatus describes the observed state of Audiobook.
type AudiobookStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the audiobook's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *AudiobookMetadata `json:"metadata,omitempty"`

	// Phase is the coarse lifecycle state of the audiobook (commonv1.LeafPhase,
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

	// HasFile is true while at least one MediaFile backs the audiobook.
	// +optional
	HasFile bool `json:"hasFile,omitempty"`

	// FileRefs lists the MediaFiles holding the audio parts, in play order
	// (each part's spec.partIndex, 2026-11-10 design D11).
	// +optional
	// +kubebuilder:validation:MaxItems=200
	FileRefs []string `json:"fileRefs,omitempty"`

	// PartCount is the number of parts on disk.
	// +optional
	PartCount int32 `json:"partCount,omitempty"`

	// FileQuality is the quality of the imported recording, the
	// lowest-ranked part's (quality until 2026-11-10, design D5), and
	// FileFormatScore its custom-format score.
	// +optional
	FileQuality *commonv1.Quality `json:"fileQuality,omitempty"`
	// +optional
	FileFormatScore int32 `json:"fileFormatScore,omitempty"`

	// AddOptionsApplied is true once spec.addOptions has been acted on.
	// +optional
	AddOptionsApplied bool `json:"addOptionsApplied,omitempty"`

	// CutoffMet is true when the imported files meet the profile cutoff.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the id of the grab entry currently working on this audiobook.
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

	// LastSearchedAt is when the audiobook was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// LastLiveSearchAt is when a search that asked the indexers was last
	// dispatched for the audiobook -- its first search, a redownload, a backlog
	// search, or its season's search folded in -- where lastSearchedAt
	// moves for the searches the release index answers too. A
	// BacklogSearch's cooldown runs from it.
	// +optional
	LastLiveSearchAt *metav1.Time `json:"lastLiveSearchAt,omitempty"`

	// SearchAttempts counts the searches made for this audiobook.
	// +optional
	SearchAttempts commonv1.Attempts `json:"searchAttempts,omitempty"`

	// NFO records the artist.nfo/album.nfo the import agent last wrote for
	// this item (2026-11-10 design §8).
	// +optional
	NFO *NFOStatus `json:"nfo,omitempty"`

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
// +kubebuilder:resource:scope=Namespaced,shortName=abk,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.asin`
// +kubebuilder:selectablefield:JSONPath=`.status.downloadPhase`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.metadata.title`
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.spec.region`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="File",type=boolean,JSONPath=`.status.hasFile`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Download",type=string,JSONPath=`.status.downloadPhase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Audiobook is a monitored Audible recording in the catalog.
type Audiobook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AudiobookSpec   `json:"spec,omitempty"`
	Status AudiobookStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// AudiobookList contains a list of Audiobook.
type AudiobookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Audiobook `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Audiobook{}, &AudiobookList{})
}
