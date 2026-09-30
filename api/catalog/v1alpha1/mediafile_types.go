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

// Condition types reported on a MediaFile.
const (
	// MediaFileConditionProbed is True once ffprobe results are in status.mediaInfo.
	MediaFileConditionProbed = "Probed"
	// MediaFileConditionReady is True when the file is present and usable.
	MediaFileConditionReady = "Ready"
)

// TranscodeResult is the outcome of the most recent transcode of a file.
//
// +kubebuilder:validation:Enum=none;succeeded;failed;skipped
type TranscodeResult string

// Transcode results.
const (
	TranscodeResultNone      TranscodeResult = "none"
	TranscodeResultSucceeded TranscodeResult = "succeeded"
	TranscodeResultFailed    TranscodeResult = "failed"
	TranscodeResultSkipped   TranscodeResult = "skipped"
)

// ImportSource records where an imported file came from.
type ImportSource struct {
	// DownloadRef is the name of the Download the file was imported from.
	// +optional
	DownloadRef string `json:"downloadRef,omitempty"`

	// ReleaseTitle is the raw title of the release the file came from.
	// +optional
	ReleaseTitle string `json:"releaseTitle,omitempty"`

	// IndexerName is the display name of the indexer the release came from.
	// +optional
	IndexerName string `json:"indexerName,omitempty"`

	// Protocol is the transfer protocol the release was fetched over.
	// +optional
	Protocol commonv1.Protocol `json:"protocol,omitempty"`

	// ImportedAt is when the file was imported.
	// +optional
	ImportedAt metav1.Time `json:"importedAt,omitempty"`

	// Manual is true when a user imported the file by hand.
	// +optional
	Manual bool `json:"manual,omitempty"`
}

// Sidecar is a subtitle or metadata file sitting next to the media file.
type Sidecar struct {
	// Path is the sidecar's absolute path.
	// +required
	Path string `json:"path"`

	// Language is the ISO 639 language tag of the sidecar.
	// +optional
	Language string `json:"language,omitempty"`

	// Forced is true when the sidecar is a forced subtitle track.
	// +optional
	Forced bool `json:"forced,omitempty"`

	// HI is true when the sidecar is a hearing-impaired (SDH) subtitle track.
	// +optional
	HI bool `json:"hi,omitempty"`
}

// TranscodeState is transcodarr's view of this file, mirrored back onto it.
type TranscodeState struct {
	// Compliant is true when the file already matches its TranscodeProfile.
	// +optional
	Compliant bool `json:"compliant,omitempty"`

	// ProfileTag identifies the TranscodeProfile revision compliance was judged against.
	// +optional
	ProfileTag string `json:"profileTag,omitempty"`

	// JobRef is the TranscodeJob currently working on the file.
	// +optional
	JobRef *string `json:"jobRef,omitempty"`

	// LastResult is the outcome of the most recent transcode.
	// +optional
	// +kubebuilder:default=none
	LastResult TranscodeResult `json:"lastResult,omitempty"`
}

// MediaFileSpec defines the desired state of MediaFile.
//
// MediaFile is the one exception to the single-writer rule, and the split is
// spec versus status: importarr creates the resource and owns this spec --
// the observed path, size and fingerprint, plus the quality, revision,
// formatScore, matchedFormats and releaseType frozen at import -- while
// catalogarr owns all of MediaFileStatus, plus metadata.labels, and takes
// over sizeBytes, modTime and original once it incorporates a transcode
// swap -- and path as well when the transcode landed under a new name and
// the source is gone (a container change or an explicit outputPath). Both
// write under their own field manager, so the apiserver enforces the split
// rather than convention.
type MediaFileSpec struct {
	// MediaRef points at the catalog item this file backs.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mediaRef is immutable"
	MediaRef commonv1.MediaRef `json:"mediaRef"`

	// Path is the file's absolute path. importarr sets it at import; it
	// changes only when catalogarr swaps in a transcode written under a new
	// name, and catalogarr then owns it.
	// +required
	Path string `json:"path"`

	// SizeBytes is the file size in bytes.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// ModTime is the file's modification time.
	// +optional
	ModTime metav1.Time `json:"modTime,omitempty"`

	// Quality is the quality frozen at import; transcoding never rewrites it.
	// +optional
	Quality commonv1.Quality `json:"quality,omitempty"`

	// Revision is the proper/repack revision frozen at import.
	// +optional
	Revision commonv1.Revision `json:"revision,omitempty"`

	// ReleaseType classifies how many catalog items the source release covered.
	// +optional
	ReleaseType commonv1.ReleaseType `json:"releaseType,omitempty"`

	// ReleaseGroup is the release group parsed from the source release.
	// +optional
	ReleaseGroup string `json:"releaseGroup,omitempty"`

	// Edition is the edition parsed from the source release.
	// +optional
	Edition string `json:"edition,omitempty"`

	// Languages lists the languages parsed from the source release.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Languages []string `json:"languages,omitempty"`

	// FormatScore is the custom-format score of the source release at import.
	// +optional
	FormatScore int32 `json:"formatScore,omitempty"`

	// MatchedFormats lists the custom-format slugs that matched at import.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	MatchedFormats []string `json:"matchedFormats,omitempty"`

	// ProfileHash is the QualityProfile status.hash the file was scored against.
	// +optional
	ProfileHash string `json:"profileHash,omitempty"`

	// ImportedFrom records where the file came from.
	// +optional
	ImportedFrom *ImportSource `json:"importedFrom,omitempty"`

	// Original is true while this is the file as imported; it goes false once a
	// transcode replaces it.
	// +optional
	// +kubebuilder:default=true
	Original *bool `json:"original,omitempty"`
}

// MediaFileStatus describes the observed state of MediaFile.
type MediaFileStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the file's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ProbeHash is sha1(path|size|mtime); a change makes downstream services replan.
	// +optional
	ProbeHash string `json:"probeHash,omitempty"`

	// ProbedAt is when the file was last probed.
	// +optional
	ProbedAt *metav1.Time `json:"probedAt,omitempty"`

	// ProbeVersion is the version of the probe that last described the
	// file (pkg/mediainfo.ProbeVersion). A file probed by an older version
	// is probed again, with probeHash unchanged: the hash is the file's
	// identity to captionarr and squasharr, and its bytes did not change.
	// Zero is a file probed before versions existed.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ProbeVersion int32 `json:"probeVersion,omitempty"`

	// MediaInfo is the technical description produced by the probe. Its
	// transcodeProfile is the file's CLUSTARR_PROFILE tag: with it, or with
	// spec.original false, the file is transcoded and final
	// (app/catalog/controller/rollup.Transcoded) -- the tag is what recognises
	// a file an earlier install transcoded, found by a rescan.
	// +optional
	MediaInfo *commonv1.MediaInfo `json:"mediaInfo,omitempty"`

	// Sidecars lists the subtitle and metadata files found next to this one.
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=50
	Sidecars []Sidecar `json:"sidecars,omitempty"`

	// Transcode is transcodarr's view of this file.
	// +optional
	Transcode *TranscodeState `json:"transcode,omitempty"`

	// Naming is the file's canonical path under its RootFolder's naming
	// preset, rendered by catalogarr from the item's metadata, the
	// release-time spec and the probe; importarr performs the rename.
	// +optional
	Naming *NamingStatus `json:"naming,omitempty"`

	// Markers are the skip segments TheIntroDB publishes for this file,
	// fetched by catalogarr's marker worker (field manager
	// catalogarr-markers) with the probe's duration, and seeded into Plex
	// by cluster-plex (spec 2026-09-30 plex-analyze-bypass).
	// +optional
	Markers *FileMarkers `json:"markers,omitempty"`
}

// MarkersResult is how a file's last marker fetch ended.
// +kubebuilder:validation:Enum=Found;NotFound;Error
type MarkersResult string

// Marker fetch results.
const (
	MarkersFound    MarkersResult = "Found"
	MarkersNotFound MarkersResult = "NotFound"
	MarkersError    MarkersResult = "Error"
)

// MarkerKind is a skip segment's kind, as TheIntroDB names it.
// +kubebuilder:validation:Enum=intro;recap;credits;preview
type MarkerKind string

// Marker kinds.
const (
	MarkerIntro   MarkerKind = "intro"
	MarkerRecap   MarkerKind = "recap"
	MarkerCredits MarkerKind = "credits"
	MarkerPreview MarkerKind = "preview"
)

// FileMarkers is a file's skip segments and how they were fetched.
type FileMarkers struct {
	// Result is Found, NotFound or Error.
	Result MarkersResult `json:"result"`

	// FetchedAt is when TheIntroDB was last asked.
	FetchedAt metav1.Time `json:"fetchedAt"`

	// ForProbeHash is status.probeHash when these were fetched: a new file
	// at the same path fetches again.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ForProbeHash string `json:"forProbeHash,omitempty"`

	// DurationMs is the duration_ms sent: the probe's runtime.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DurationMs int64 `json:"durationMs,omitempty"`

	// Segments are ordered by start.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Segments []MarkerSegment `json:"segments,omitempty"`

	// Message says why the fetch ended NotFound or Error.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`

	// NotFoundSince is when TheIntroDB first had nothing for this probe,
	// on a NotFound result: the longer it has had nothing, the less often
	// it is asked again (markers.Due).
	// +optional
	NotFoundSince *metav1.Time `json:"notFoundSince,omitempty"`
}

// MarkerSegment is one skip segment, in milliseconds from the file's start.
type MarkerSegment struct {
	Kind MarkerKind `json:"kind"`
	// +kubebuilder:validation:Minimum=0
	StartMs int64 `json:"startMs"`
	// +kubebuilder:validation:Minimum=0
	EndMs int64 `json:"endMs"`
}

// NamingStatus is catalogarr's proposal for a MediaFile's canonical path.
type NamingStatus struct {
	// ExpectedPath is the absolute path the preset renders; empty until
	// the item's metadata and the probe are both present.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	ExpectedPath string `json:"expectedPath,omitempty"`

	// Current is true when spec.path equals expectedPath.
	Current bool `json:"current"`

	// Reason says why expectedPath is empty or the file is not renameable.
	// +optional
	// +kubebuilder:validation:Enum=MetadataPending;ProbePending;TranscodePending;Recycling;Unrenderable
	Reason NamingReason `json:"reason,omitempty"`

	// Quality is the probe-corrected quality importarr re-applies into
	// spec.quality when it renames.
	// +optional
	Quality *commonv1.Quality `json:"quality,omitempty"`
}

// NamingReason says why a MediaFile's expectedPath is empty or the file is
// not renameable.
//
// +kubebuilder:validation:Enum=MetadataPending;ProbePending;TranscodePending;Recycling;Unrenderable
type NamingReason string

// Naming reasons.
const (
	NamingReasonMetadataPending  NamingReason = "MetadataPending"
	NamingReasonProbePending     NamingReason = "ProbePending"
	NamingReasonTranscodePending NamingReason = "TranscodePending"
	NamingReasonRecycling        NamingReason = "Recycling"
	NamingReasonUnrenderable     NamingReason = "Unrenderable"
	// ConditionNamingCurrent mirrors status.naming.current.
	ConditionNamingCurrent = "NamingCurrent"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=mf,categories=clustarr;catalog
// +kubebuilder:selectablefield:JSONPath=`.spec.mediaRef.name`
// +kubebuilder:printcolumn:name="Media",type=string,JSONPath=`.spec.mediaRef.name`
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.mediaRef.kind`
// +kubebuilder:printcolumn:name="Quality",type=string,JSONPath=`.spec.quality.name`
// +kubebuilder:printcolumn:name="Score",type=integer,JSONPath=`.spec.formatScore`
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.spec.sizeBytes`
// +kubebuilder:printcolumn:name="Probed",type=string,JSONPath=`.status.conditions[?(@.type=="Probed")].status`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MediaFile is one file on disk backing a catalog item. It is the pivot every
// other service hangs off: downloadarr creates it at import, transcodarr and
// subtitlarr read its probe and write back through status.
type MediaFile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MediaFileSpec   `json:"spec,omitempty"`
	Status MediaFileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// MediaFileList contains a list of MediaFile.
type MediaFileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MediaFile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MediaFile{}, &MediaFileList{})
}
