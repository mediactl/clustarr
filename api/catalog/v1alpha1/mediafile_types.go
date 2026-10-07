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

	// MediaFileReasonProbePending is the Probed and Ready reason while a probe
	// of changed bytes, a new file or a transcode swap's target is queued
	// (spec 2026-10-06 §6.5.3): status.mediaInfo, if any, describes bytes the
	// file may no longer hold.
	MediaFileReasonProbePending = "ProbePending"
)

// Planner-error conditions (loop spec §2.8). Each is present only while its
// planner fails, with reason MediaFileReasonPlannerError (a returned error)
// or MediaFileReasonPlannerPanic (a recovered panic) and a message clamped
// to k8s.MaxConditionMessage; the planner's last block stays in place, and
// the condition is removed on its next success. With Probed, Ready,
// NamingCurrent and DeadLettered the set is closed at ten; Conditions'
// MaxItems is 12.
const (
	MediaFileConditionProbePlannerError     = "ProbePlannerError"
	MediaFileConditionNamingPlannerError    = "NamingPlannerError"
	MediaFileConditionTranscodePlannerError = "TranscodePlannerError"
	MediaFileConditionGraftPlannerError     = "GraftPlannerError"
	MediaFileConditionSubtitlesPlannerError = "SubtitlesPlannerError"
	MediaFileConditionMarkersPlannerError   = "MarkersPlannerError"

	MediaFileReasonPlannerError = "PlannerError"
	MediaFileReasonPlannerPanic = "PlannerPanic"
)

// Field selectors on MediaFile's selectable fields (loop spec §2.10).
// Transcode admission's rebuild lists through the APIReader with
// client.MatchingFields{FieldTranscodePhase: p}; a file with no block
// reads "".
const (
	FieldTranscodePhase = "status.transcode.phase"
	FieldGraftPhase     = "status.graft.phase"
)

// ImportSource records where an imported file came from.
type ImportSource struct {
	// DownloadRef is the name of the Download the file was imported from.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	DownloadRef string `json:"downloadRef,omitempty"`

	// ReleaseTitle is the raw title of the release the file came from.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	ReleaseTitle string `json:"releaseTitle,omitempty"`

	// IndexerName is the display name of the indexer the release came from.
	// +optional
	// +kubebuilder:validation:MaxLength=253
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

	// InfoHash is the release's info hash, frozen at import; the search's
	// current-file check reads it instead of the Download downloadRef named
	// (ADR-0019 §6.2). Release N backfills it from live Downloads (§10.2).
	// +optional
	// +kubebuilder:validation:MaxLength=64
	InfoHash string `json:"infoHash,omitempty"`
}

// Bounds of the MediaFile spec's paths and the release text importarr
// freezes into it from a Download (loop spec §2.11.2), mirrored by
// pkg/crdcheck.TestProbeAndImportBoundsMatchTheCRD. A Download's release
// fields are unbounded, so fileimport clamps to these where it freezes them;
// the item's audio donor (AudioDonor) uses the same path and release bounds.
const (
	MaxPathLength         = 4096
	MaxReleaseTitleLength = 512
	MaxIndexerNameLength  = 253
	MaxReleaseGroupLength = 256
	MaxEditionLength      = 256
)

// Sidecar is a subtitle sidecar beside the media file (loop spec §2.7):
// every attributable one the subtitles planner's directory read finds,
// downloaded, written by the MP4 standard or placed by hand.
type Sidecar struct {
	// Path is the sidecar's absolute path: today's field and the list's map
	// key, written by the loop on every entry in release N (dir(spec.path)
	// + "/" + Name) so that the previous release can read and re-send the
	// list after a rollback (§2.16). The loop fills each half from the
	// other on an entry it carries forward. Deleted in N+1.
	// +required
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path"`

	// Name is the sidecar's file name in the media file's directory.
	// +optional in release N (an entry the previous release wrote after a
	// rollback has none); +required with MinLength=1 from N+1.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`

	// Language is the sidecar's language tag.
	// +optional
	// +kubebuilder:validation:MaxLength=35
	Language string `json:"language,omitempty"`

	// Forced is true when the sidecar is a forced subtitle track.
	// +optional
	Forced bool `json:"forced,omitempty"`

	// HI is true when the sidecar is a hearing-impaired (SDH) subtitle track.
	// +optional
	HI bool `json:"hi,omitempty"`
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
	// +kubebuilder:validation:MaxLength=4096
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
	// +kubebuilder:validation:MaxLength=256
	ReleaseGroup string `json:"releaseGroup,omitempty"`

	// Edition is the edition parsed from the source release.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Edition string `json:"edition,omitempty"`

	// Languages lists the languages parsed from the source release.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=35
	Languages []string `json:"languages,omitempty"`

	// FormatScore is the custom-format score of the source release at import.
	// +optional
	FormatScore int32 `json:"formatScore,omitempty"`

	// MatchedFormats lists the custom-format slugs that matched at import.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	// +kubebuilder:validation:items:MaxLength=128
	MatchedFormats []string `json:"matchedFormats,omitempty"`

	// ProfileHash is the QualityProfile status.hash the file was scored against.
	// +optional
	// +kubebuilder:validation:MaxLength=64
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

// MediaFileStatus is written by one remediation loop in the manager, under
// field manager catalogarr (ADR-0016). Until F3.1 the MediaFile reconciler
// still writes it through knownStatus.
type MediaFileStatus struct {
	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the file's
	// state: a set closed at ten types (loop spec §2.8).
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=12
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastSeq is the last sequence the loop issued to any remediation task
	// for this file (subtitles, transcode, graft). It never decreases while
	// the MediaFile exists; the probe and markers keep their record-local
	// Seq (split §6.5.2).
	// +optional
	// +kubebuilder:validation:Minimum=0
	LastSeq int64 `json:"lastSeq,omitempty"`

	// ProbeHash is sha1(path|size|mtime); a change makes downstream services replan.
	// +optional
	// +kubebuilder:validation:MaxLength=128
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

	// Sidecars are the subtitle sidecars beside the file, found on disk by
	// the subtitles planner's directory read, sorted by name. Release N keeps
	// today's map list keyed by path (loop spec §2.16); N+1 makes it atomic.
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=32
	Sidecars []Sidecar `json:"sidecars,omitempty"`

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

	// Subtitles is the remediation loop's subtitles block (loop spec §2.4);
	// it replaces SubtitleRequest.
	// +optional
	Subtitles *SubtitlesStatus `json:"subtitles,omitempty"`

	// Transcode is the remediation loop's transcode block (loop spec §2.5);
	// it replaces TranscodeJob.
	// +optional
	Transcode *TranscodeState `json:"transcode,omitempty"`

	// Graft is the remediation loop's graft block (loop spec §2.6); it
	// replaces AudioGraft's per-file half.
	// +optional
	Graft *GraftState `json:"graft,omitempty"`

	// GraftTag is the CLUSTARR_GRAFT tag of the last audio graft catalogarr
	// incorporated into this file (anime dual-audio spec §7.2), and
	// GraftedAt when. A graft is not a transcode: spec.original stays as it
	// was, but from the first graft catalogarr owns spec.path, sizeBytes and
	// modTime, as after a transcode swap.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	GraftTag string `json:"graftTag,omitempty"`
	// +optional
	GraftedAt *metav1.Time `json:"graftedAt,omitempty"`

	// HandledNonces records the last one-shot intent nonce handled for each
	// one-shot intent annotation (loop spec §2.9).
	// +optional
	HandledNonces *HandledNonces `json:"handledNonces,omitempty"`
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
	// Result is TheIntroDB's: Found, NotFound or Error. Empty while only
	// clustarr's own analysis has a result (Analysis).
	// +optional
	Result MarkersResult `json:"result,omitempty"`

	// FetchedAt is when TheIntroDB was last asked.
	// +optional
	FetchedAt metav1.Time `json:"fetchedAt,omitzero"`

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

	// Analysis is clustarr's own analysis of the file; segments it found
	// are merged into Segments under TheIntroDB's and the chapters'.
	// +optional
	Analysis *SegmentAnalysis `json:"analysis,omitempty"`
}

// MarkerSegment is one skip segment, in milliseconds from the file's start.
type MarkerSegment struct {
	Kind MarkerKind `json:"kind"`
	// +kubebuilder:validation:Minimum=0
	StartMs int64 `json:"startMs"`
	// +kubebuilder:validation:Minimum=0
	EndMs int64 `json:"endMs"`
	// Source is where the segment came from; empty is theintrodb, which
	// wrote segments before sources existed.
	// +optional
	Source SegmentSource `json:"source,omitempty"`
	// Confidence is a whole percent: 100 for TheIntroDB and chapters,
	// 70-90 for local analysis (pkg/segments).
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Confidence int32 `json:"confidence,omitempty"`
}

// SegmentSource is where a skip segment came from. Precedence, per kind:
// theintrodb, then chapters, then analysis (pkg/segments.Merge).
// +kubebuilder:validation:Enum=theintrodb;chapters;analysis
type SegmentSource string

// Segment sources.
const (
	SegmentSourceTheIntroDB SegmentSource = "theintrodb"
	SegmentSourceChapters   SegmentSource = "chapters"
	SegmentSourceAnalysis   SegmentSource = "analysis"
)

// SegmentAnalysis records clustarr's own analysis of a file
// (segmentarr-worker), beside TheIntroDB's fetch bookkeeping.
type SegmentAnalysis struct {
	// Result is Found, NotFound or Error.
	Result MarkersResult `json:"result"`
	// AnalyzedAt is when the worker's result was recorded.
	AnalyzedAt metav1.Time `json:"analyzedAt"`
	// ForProbeHash is status.probeHash of the file analyzed.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ForProbeHash string `json:"forProbeHash,omitempty"`
	// Version is pkg/segments.AnalyzerVersion when analyzed; an older one
	// is analyzed again.
	// +optional
	Version int32 `json:"version,omitempty"`
	// Message says why the analysis ended NotFound or Error.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`
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
// +kubebuilder:selectablefield:JSONPath=`.spec.mediaRef.kind`
// +kubebuilder:selectablefield:JSONPath=`.status.transcode.phase`
// +kubebuilder:selectablefield:JSONPath=`.status.subtitles.phase`
// +kubebuilder:selectablefield:JSONPath=`.status.graft.phase`
// +kubebuilder:selectablefield:JSONPath=`.status.naming.current`
// +kubebuilder:printcolumn:name="Media",type=string,JSONPath=`.spec.mediaRef.name`
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.mediaRef.kind`
// +kubebuilder:printcolumn:name="Quality",type=string,JSONPath=`.spec.quality.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Transcode",type=string,JSONPath=`.status.transcode.phase`
// +kubebuilder:printcolumn:name="Subtitles",type=string,JSONPath=`.status.subtitles.phase`
// +kubebuilder:printcolumn:name="Graft",type=string,JSONPath=`.status.graft.phase`,priority=1
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.status.transcode.class`,priority=1
// +kubebuilder:printcolumn:name="Worker",type=string,JSONPath=`.status.transcode.workerPod`,priority=1
// +kubebuilder:printcolumn:name="Probed",type=string,JSONPath=`.status.conditions[?(@.type=="Probed")].status`,priority=1
// +kubebuilder:printcolumn:name="Score",type=integer,JSONPath=`.spec.formatScore`,priority=1
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.spec.sizeBytes`,priority=1
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
