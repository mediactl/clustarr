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

// Condition types reported on an Artist.
const (
	// ArtistConditionReady is True when the artist is fully reconciled.
	ArtistConditionReady = "Ready"
	// ArtistConditionMetadataReady is True once metadata has been fetched.
	ArtistConditionMetadataReady = "MetadataReady"
	// ArtistConditionAlbumsSynced is True once the Album objects match the metadata.
	ArtistConditionAlbumsSynced = "AlbumsSynced"
)

// ArtistRunStatus is the upstream activity status of an artist.
//
// +kubebuilder:validation:Enum=continuing;ended
type ArtistRunStatus string

// Artist run statuses.
const (
	ArtistRunStatusContinuing ArtistRunStatus = "continuing"
	ArtistRunStatusEnded      ArtistRunStatus = "ended"
)

// ArtistMonitorMode says which albums are monitored when an artist is added.
//
// +kubebuilder:validation:Enum=all;future;missing;existing;latest;first;none
type ArtistMonitorMode string

// Artist monitor modes.
const (
	ArtistMonitorAll      ArtistMonitorMode = "all"
	ArtistMonitorFuture   ArtistMonitorMode = "future"
	ArtistMonitorMissing  ArtistMonitorMode = "missing"
	ArtistMonitorExisting ArtistMonitorMode = "existing"
	ArtistMonitorLatest   ArtistMonitorMode = "latest"
	ArtistMonitorFirst    ArtistMonitorMode = "first"
	ArtistMonitorNone     ArtistMonitorMode = "none"
)

// MusicMetadataProfile filters which MusicBrainz release groups become Albums.
type MusicMetadataProfile struct {
	// PrimaryTypes are the MusicBrainz primary release-group types to accept.
	// +optional
	// +kubebuilder:default={album,ep}
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:Enum=album;ep;single;broadcast;other
	PrimaryTypes []string `json:"primaryTypes,omitempty"`

	// SecondaryTypes are the MusicBrainz secondary release-group types to accept.
	// Each token folds one MusicBrainz spelling (https://musicbrainz.org/doc/Release_Group/Type):
	// "Audio drama" is audioDrama, "DJ-mix" djMix, "Mixtape/Street" mixtape
	// and "Field recording" fieldRecording; "studio" is this project's token
	// for a release group with no secondary type at all.
	// +optional
	// +kubebuilder:default={studio}
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:Enum=studio;compilation;soundtrack;spokenword;interview;audiobook;live;remix;djMix;mixtape;demo;audioDrama;fieldRecording
	SecondaryTypes []string `json:"secondaryTypes,omitempty"`

	// ReleaseStatuses are the MusicBrainz release statuses to accept.
	// +optional
	// +kubebuilder:default={official}
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:Enum=official;promotion;bootleg;pseudoRelease
	ReleaseStatuses []string `json:"releaseStatuses,omitempty"`
}

// ArtistAddOptions are applied exactly once, when the artist is first reconciled.
type ArtistAddOptions struct {
	// Monitor selects which albums start out monitored.
	// +optional
	// +kubebuilder:default=all
	Monitor ArtistMonitorMode `json:"monitor,omitempty"`

	// SearchForMissing searches for missing albums on add.
	// +optional
	SearchForMissing bool `json:"searchForMissing,omitempty"`
}

// ArtistMetadata is the provider metadata cached on the artist.
type ArtistMetadata struct {
	// Name is the artist name.
	// +optional
	Name string `json:"name,omitempty"`

	// SortName is the name used for sorting.
	// +optional
	SortName string `json:"sortName,omitempty"`

	// Disambiguation distinguishes same-named artists.
	// +optional
	Disambiguation string `json:"disambiguation,omitempty"`

	// Type is the MusicBrainz artist type, e.g. Person or Group.
	// +optional
	Type string `json:"type,omitempty"`

	// Overview is the artist biography.
	// +optional
	Overview string `json:"overview,omitempty"`

	// Status is the upstream activity status.
	// +optional
	Status ArtistRunStatus `json:"status,omitempty"`

	// Genres lists the genres.
	// +optional
	// +kubebuilder:validation:MaxItems=30
	Genres []string `json:"genres,omitempty"`

	// ExternalIDs maps provider names (musicbrainz, fanart, ...) to their IDs.
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

// ArtistSpec defines the desired state of Artist.
type ArtistSpec struct {
	// MusicBrainzID is the MusicBrainz artist MBID; it identifies the artist
	// and is immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="musicBrainzID is immutable"
	MusicBrainzID string `json:"musicBrainzID"`

	// Monitored enables automatic searching and importing for this artist.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// MonitorNewItems says what happens to albums discovered after the artist
	// was added.
	// +optional
	// +kubebuilder:default=all
	MonitorNewItems MonitorNewItemsMode `json:"monitorNewItems,omitempty"`

	// MetadataProfile filters which release groups become Albums.
	// +optional
	MetadataProfile MusicMetadataProfile `json:"metadataProfile,omitempty"`

	// AddOptions are applied once, when the artist is first reconciled.
	// +optional
	AddOptions ArtistAddOptions `json:"addOptions,omitempty"`

	// QualityProfileRef is the QualityProfile albums are ranked against.
	// +required
	QualityProfileRef string `json:"qualityProfileRef"`

	// RootFolderRef is the RootFolder the artist is stored under.
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

	// Source records the ImportList that added the artist, if any.
	// +optional
	Source *commonv1.AddSource `json:"source,omitempty"`
}

// ArtistStatus describes the observed state of Artist.
type ArtistStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the artist's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *ArtistMetadata `json:"metadata,omitempty"`

	// Path is the resolved folder on disk.
	// +optional
	Path string `json:"path,omitempty"`

	// AlbumCount is the number of Album objects owned by the artist.
	// +optional
	AlbumCount int32 `json:"albumCount,omitempty"`

	// AlbumFileCount is the number of albums with at least one imported file.
	// +optional
	AlbumFileCount int32 `json:"albumFileCount,omitempty"`

	// AddOptionsApplied is true once spec.addOptions has been acted on.
	// +optional
	AddOptionsApplied bool `json:"addOptionsApplied,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=art,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.musicBrainzID`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.status.metadata.name`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Albums",type=integer,JSONPath=`.status.albumCount`
// +kubebuilder:printcolumn:name="Files",type=integer,JSONPath=`.status.albumFileCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Artist is a monitored recording artist in the catalog.
type Artist struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ArtistSpec   `json:"spec,omitempty"`
	Status ArtistStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// ArtistList contains a list of Artist.
type ArtistList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Artist `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Artist{}, &ArtistList{})
}
