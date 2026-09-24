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

// Condition types reported on an Album.
const (
	// AlbumConditionReady is True when the album is fully reconciled.
	AlbumConditionReady = "Ready"
	// AlbumConditionMetadataReady is True once metadata has been fetched.
	AlbumConditionMetadataReady = "MetadataReady"
	// AlbumConditionInvalid is True when the album cannot be represented, for
	// example when it has more tracks than status.tracks can hold.
	AlbumConditionInvalid = "Invalid"
)

// AlbumPhase is the coarse lifecycle state of an album.
//
// +kubebuilder:validation:Enum=Wanted;Delayed;Downloading;Imported;CutoffUnmet;Unmonitored
type AlbumPhase string

// Album phases.
const (
	AlbumPhaseWanted      AlbumPhase = "Wanted"
	AlbumPhaseDelayed     AlbumPhase = "Delayed"
	AlbumPhaseDownloading AlbumPhase = "Downloading"
	AlbumPhaseImported    AlbumPhase = "Imported"
	AlbumPhaseCutoffUnmet AlbumPhase = "CutoffUnmet"
	AlbumPhaseUnmonitored AlbumPhase = "Unmonitored"
)

// Medium is one disc, tape or side of a MusicBrainz release.
type Medium struct {
	// Number is the 1-based medium number.
	// +required
	// +kubebuilder:validation:Minimum=1
	Number int32 `json:"number"`

	// Format is the medium format, e.g. CD, Vinyl or Digital Media.
	// +optional
	Format string `json:"format,omitempty"`
}

// ReleaseSummary is one MusicBrainz release of the album's release group.
type ReleaseSummary struct {
	// ID is the MusicBrainz release MBID.
	// +required
	ID string `json:"id"`

	// Status is the MusicBrainz release status, e.g. Official.
	// +optional
	Status string `json:"status,omitempty"`

	// Country is the ISO 3166-1 country the release was issued in.
	// +optional
	Country string `json:"country,omitempty"`

	// Label is the issuing record label.
	// +optional
	Label string `json:"label,omitempty"`

	// ReleaseDate is when this release was issued, MusicBrainz's release
	// date; a partial date is the first day of the period it names. A
	// remaster or reissue carries its own, later date here while the
	// release group's earliest stays in AlbumMetadata.ReleaseDate, which is
	// how a release named by its edition's year is recognised as this
	// album (Lidarr's AlbumYearMatcher checks each release's date).
	// +optional
	ReleaseDate *metav1.Time `json:"releaseDate,omitempty"`

	// TrackCount is the total number of tracks across all media.
	// +optional
	TrackCount int32 `json:"trackCount,omitempty"`

	// Media lists the discs or sides making up the release.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	Media []Medium `json:"media,omitempty"`
}

// Track is one recording on the selected release of an album.
type Track struct {
	// RecordingID is the MusicBrainz recording MBID.
	// +required
	RecordingID string `json:"recordingID"`

	// Medium is the 1-based medium the track sits on.
	// +optional
	Medium int32 `json:"medium,omitempty"`

	// Number is the track number within the medium.
	// +optional
	Number int32 `json:"number,omitempty"`

	// AbsoluteNumber is the track number across the whole release.
	// +optional
	AbsoluteNumber int32 `json:"absoluteNumber,omitempty"`

	// Title is the track title.
	// +optional
	Title string `json:"title,omitempty"`

	// DurationMs is the track duration in milliseconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DurationMs int32 `json:"durationMs,omitempty"`

	// Explicit marks a track with an explicit-content advisory.
	// +optional
	Explicit bool `json:"explicit,omitempty"`

	// FileRef is the name of the MediaFile holding this track.
	// +optional
	FileRef *string `json:"fileRef,omitempty"`
}

// AlbumMetadata is the provider metadata cached on the album.
type AlbumMetadata struct {
	// Title is the album title.
	// +optional
	Title string `json:"title,omitempty"`

	// Disambiguation distinguishes same-named release groups.
	// +optional
	Disambiguation string `json:"disambiguation,omitempty"`

	// Overview is the album description.
	// +optional
	Overview string `json:"overview,omitempty"`

	// AlbumType is the MusicBrainz primary release-group type.
	// +optional
	AlbumType string `json:"albumType,omitempty"`

	// SecondaryTypes are the MusicBrainz secondary release-group types.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	SecondaryTypes []string `json:"secondaryTypes,omitempty"`

	// ReleaseDate is the earliest release date of the release group.
	// +optional
	ReleaseDate *metav1.Time `json:"releaseDate,omitempty"`

	// Releases lists the releases in the release group.
	// +optional
	// +listType=map
	// +listMapKey=id
	// +kubebuilder:validation:MaxItems=50
	Releases []ReleaseSummary `json:"releases,omitempty"`

	// SelectedReleaseID is the release the tracks were taken from.
	// +optional
	SelectedReleaseID string `json:"selectedReleaseID,omitempty"`

	// Images lists the cover art published by the provider.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Images []Image `json:"images,omitempty"`

	// RefreshedAt is when the metadata was last fetched.
	// +optional
	RefreshedAt metav1.Time `json:"refreshedAt,omitempty"`
}

// AlbumSpec defines the desired state of Album.
type AlbumSpec struct {
	// ArtistRef is the name of the owning Artist in the same namespace.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="artistRef is immutable"
	ArtistRef string `json:"artistRef"`

	// ReleaseGroupID is the MusicBrainz release-group MBID; it identifies the
	// album and is immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="releaseGroupID is immutable"
	ReleaseGroupID string `json:"releaseGroupID"`

	// Monitored enables automatic searching and importing for this album.
	// +optional
	// +kubebuilder:default=true
	Monitored *bool `json:"monitored,omitempty"`

	// AnyReleaseOk accepts a release of the group other than the selected one
	// when it is the best match.
	// +optional
	// +kubebuilder:default=true
	AnyReleaseOk *bool `json:"anyReleaseOk,omitempty"`

	// ReleaseID pins a specific MusicBrainz release of the group.
	// +optional
	ReleaseID *string `json:"releaseID,omitempty"`

	// QualityProfileRef overrides the Artist's QualityProfile.
	// +optional
	QualityProfileRef *string `json:"qualityProfileRef,omitempty"`

	// Artwork overrides the provider's image for a type. One entry per type.
	// +optional
	// +kubebuilder:validation:MaxItems=9
	// +listType=map
	// +listMapKey=type
	Artwork []ArtworkOverride `json:"artwork,omitempty"`
}

// AlbumStatus describes the observed state of Album.
type AlbumStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the album's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Metadata is the cached provider metadata.
	// +optional
	Metadata *AlbumMetadata `json:"metadata,omitempty"`

	// Tracks is the track listing of the selected release. Releases with more
	// than 200 tracks raise the Invalid condition instead of being truncated
	// silently.
	// +optional
	// +listType=map
	// +listMapKey=recordingID
	// +kubebuilder:validation:MaxItems=200
	Tracks []Track `json:"tracks,omitempty"`

	// Phase is the coarse lifecycle state of the album.
	// +optional
	Phase AlbumPhase `json:"phase,omitempty"`

	// Path is the resolved folder on disk.
	// +optional
	Path string `json:"path,omitempty"`

	// TrackFileCount is the number of tracks with an imported file.
	// +optional
	TrackFileCount int32 `json:"trackFileCount,omitempty"`

	// Quality is the lowest quality across the album's imported tracks.
	// +optional
	Quality *commonv1.Quality `json:"quality,omitempty"`

	// FormatScore is the custom-format score of the imported release.
	// +optional
	FormatScore int32 `json:"formatScore,omitempty"`

	// CutoffMet is true when the imported tracks meet the profile cutoff.
	// +optional
	CutoffMet bool `json:"cutoffMet,omitempty"`

	// ActiveDownloadRef is the Download currently working on this album.
	// +optional
	ActiveDownloadRef *string `json:"activeDownloadRef,omitempty"`

	// PendingGrab is a chosen release waiting out a DelayProfile delay.
	// +optional
	PendingGrab *PendingGrab `json:"pendingGrab,omitempty"`

	// LastSearchedAt is when the album was last searched for.
	// +optional
	LastSearchedAt *metav1.Time `json:"lastSearchedAt,omitempty"`

	// SearchAttempts counts the searches made for this album.
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
// +kubebuilder:resource:scope=Namespaced,shortName=alb,categories=clustarr;catalog;media
// +kubebuilder:selectablefield:JSONPath=`.spec.artistRef`
// +kubebuilder:printcolumn:name="Artist",type=string,JSONPath=`.spec.artistRef`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.metadata.title`
// +kubebuilder:printcolumn:name="Monitored",type=boolean,JSONPath=`.spec.monitored`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Files",type=integer,JSONPath=`.status.trackFileCount`
// +kubebuilder:printcolumn:name="Quality",type=string,JSONPath=`.status.quality.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Album is one MusicBrainz release group owned by an Artist. Albums are named
// <artist>-<releasegroup-uid8>.
type Album struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AlbumSpec   `json:"spec,omitempty"`
	Status AlbumStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// AlbumList contains a list of Album.
type AlbumList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Album `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Album{}, &AlbumList{})
}
