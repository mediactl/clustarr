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

// Package pipeline computes the end-to-end pipeline projection the UI's
// Pipeline page renders: one [Stage] per catalog item, derived from the
// resources owned by the five services that move it from "wanted" to "on
// disk, transcoded and subtitled".
//
// [Project] is pure: resources in, an [Entry] out. It touches no client and
// no context, which is what makes stage derivation -- the part of a page like
// this that usually rots first -- testable without a cluster or a browser.
package pipeline

import (
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// Stage is a coarse, ordered position in the end-to-end flow from wanting an
// item through to it being fully collected: metadata, then release, then
// download, then import, then (in parallel) subtitles and transcoding, then
// done -- or failed, or blocked, at any point along the way.
//
// Transcribed verbatim from the amendment (docs/superpowers/specs/2026-09-18-
// clustarr-design-amendment-1.md, §A3.3, lines 312-330).
type Stage string

// The 18 pipeline stages.
const (
	StageMetadataSearching Stage = "MetadataSearching"
	StageMetadataFound     Stage = "MetadataFound"
	StageMetadataSynced    Stage = "MetadataSynced"
	StageReleaseSearching  Stage = "ReleaseSearching"
	StageReleaseSelected   Stage = "ReleaseSelected"
	StageDownloading       Stage = "Downloading"
	StageDownloaded        Stage = "Downloaded"
	StageImporting         Stage = "Importing"
	StageImported          Stage = "Imported"
	StageSubtitleSearching Stage = "SubtitleSearching"
	StageSubtitleFound     Stage = "SubtitleFound"
	StageSubtitleFetching  Stage = "SubtitleFetching"
	StageSubtitleDone      Stage = "SubtitleDone"
	StageTranscoding       Stage = "Transcoding"
	StageTranscodeDone     Stage = "TranscodeDone"
	StageComplete          Stage = "Complete"
	StageFailed            Stage = "Failed"
	StageBlocked           Stage = "Blocked"
)

// Entry is one row on the pipeline page.
type Entry struct {
	// Ref identifies the catalog item this entry describes. The design
	// amendment specifies commonv1.ObjectRef, but no such type exists in
	// api/common/v1alpha1 (see the package doc on [Project] for why a
	// types.NamespacedName is used instead).
	Ref types.NamespacedName

	// Kind is the media kind of the item.
	Kind commonv1.MediaKind

	// Title is the item's display title.
	Title string

	// Stage is the item's current coarse position in the pipeline.
	Stage Stage

	// Percent is the completion percentage of the current stage, 0-100, or
	// -1 when the stage has no meaningful percentage.
	Percent int32

	// ETA is the estimated time remaining in the current stage, or nil when
	// no estimate is available.
	ETA *time.Duration

	// Detail is a short human-readable note: the indexer being queried, the
	// encoder tier in use, and so on.
	Detail string

	// Since is when the item entered its current stage.
	Since time.Time

	// Failure explains a StageFailed or StageBlocked entry. Empty otherwise.
	Failure string
}

// Related is every resource [Project] needs beyond the catalog item itself to
// derive its stage. All of it is read-only input; Project never mutates it
// and never fetches more of it.
type Related struct {
	// Downloads are the Downloads targeting this item, active or historical.
	Downloads []downloadv1.Download

	// Jobs are the TranscodeJobs for this item's MediaFile.
	Jobs []transcodev1.TranscodeJob

	// Subtitles are the SubtitleRequests for this item's MediaFile.
	Subtitles []subtitlev1.SubtitleRequest

	// Search is the active or most recent interactive Search for this item,
	// if any.
	Search *catalogv1.Search

	// MediaFile is the file backing this item once it has been imported. It
	// is nil until catalogarr's importer creates it; the import and
	// transcode stages both read it.
	MediaFile *catalogv1.MediaFile
}
