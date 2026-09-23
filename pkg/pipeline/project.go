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

package pipeline

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Project derives the current stage of one catalog item from its resources.
// It is pure -- item and related are read, nothing is mutated and nothing is
// fetched -- so the trickiest part of the pipeline page is testable without a
// cluster or a browser.
//
// The design amendment gives Entry.Ref the type commonv1.ObjectRef, but that
// type does not exist anywhere in api/common/v1alpha1 (confirmed by grep: the
// only reference types the API defines are commonv1.MediaRef, which lacks a
// namespace, and Kubernetes' own corev1.LocalObjectReference, which lacks a
// kind). Project's own path ownership does not include api/, so rather than
// invent a type in a package this task does not own, Entry.Ref uses
// types.NamespacedName -- the standard controller-runtime identity for "the
// object kubectl would need --namespace and a name to find" -- which is
// exactly what a pipeline row needs to link back to its object.
//
// Stages are evaluated in reverse-completion order: the checks run from the
// most-advanced observable state down to the least, and the first match
// wins, so the furthest-along signal always determines the entry's stage.
// Failure and blocked states are checked before anything else, per the
// brief.
func Project(item client.Object, related Related) Entry {
	desc := describeItem(item)

	entry := Entry{
		Ref:     types.NamespacedName{Namespace: item.GetNamespace(), Name: item.GetName()},
		Kind:    desc.kind,
		Title:   desc.title,
		Percent: -1,
		// Project has no access to stage-transition history, only current
		// state, so Since is the item's creation time rather than the time
		// it entered its current stage. A future task with a stage-change
		// log or a status timestamp per stage could refine this.
		Since: item.GetCreationTimestamp().Time,
	}

	// Every signal Project might act on is looked up once, up front, so the
	// priority chain below is a flat sequence of nil/bool checks rather than
	// a mess of repeated slice scans.
	postProcessed := requiresPostProcessing(desc.kind)
	failedDL := failedDownload(related)
	failedTJ := failedJob(related)
	blockedDL := blockedDownload(related)
	blockedSR := blockedSubtitle(related)
	runningTJ := activeJob(related)
	fetchingSR := fetchingSubtitle(related)
	foundSR := foundSubtitle(related)
	searchingSR := searchingSubtitle(related)
	importedDL := importedDownload(related)
	importingDL := importingDownload(related)
	downloadedDL := downloadedDownload(related)
	downloadingDL := downloadingDownload(related)
	imported := related.MediaFile != nil || importedDL != nil
	satisfied := subtitlesSatisfied(related)
	// complete requires imported, plus -- for a kind that goes through
	// squasharr and captionarr -- at least one SubtitleRequest fully
	// satisfied and at least one TranscodeJob with none of them still doing
	// work. Both branches require their resource to actually exist:
	// captionarr creates a SubtitleRequest and squasharr a TranscodeJob for
	// every imported video MediaFile (even a TranscodeJob that ends up
	// Skipped), so their absence means those controllers have not caught up
	// yet, not that the work does not apply. Treating "no resource yet" as
	// "nothing to do" would show Complete before those controllers have even
	// run. A non-video kind (Album, Artist, Author, Book, Audiobook, Comic,
	// Issue) never gets a SubtitleRequest or a TranscodeJob at all, so for
	// those Complete is reached as soon as the item is imported. Failure and
	// blocked states are already ruled out above.
	complete := imported && (!postProcessed || (satisfied && len(related.Jobs) > 0 && runningTJ == nil))

	switch {
	case failedDL != nil:
		entry.Stage = StageFailed
		entry.Failure = string(failedDL.Status.FailureReason)
		if entry.Failure == "" {
			entry.Failure = failedDL.Status.Message
		}
		return entry

	case postProcessed && failedTJ != nil:
		entry.Stage = StageFailed
		entry.Failure = failedTJ.Status.Message
		return entry

	case blockedDL != nil:
		entry.Stage = StageBlocked
		entry.Failure = "release blocklisted"
		if blockedDL.Status.Import != nil && blockedDL.Status.Import.State == downloadv1.ImportPhaseBlocked {
			entry.Failure = blockedDL.Status.Import.Message
			if entry.Failure == "" {
				entry.Failure = "import blocked"
			}
		}
		return entry

	case postProcessed && blockedSR != nil:
		entry.Stage = StageBlocked
		entry.Failure = "subtitle search blocked"
		return entry

	case complete:
		entry.Stage = StageComplete
		entry.Percent = 100
		return entry

	case postProcessed && runningTJ != nil:
		entry.Stage = StageTranscoding
		if runningTJ.Status.Progress != nil {
			entry.Percent = runningTJ.Status.Progress.Percent
		}
		if runningTJ.Status.Plan != nil {
			entry.Detail = runningTJ.Status.Plan.Encoder
		}
		if runningTJ.Status.StartedAt != nil {
			entry.Since = runningTJ.Status.StartedAt.Time
		}
		return entry

	case postProcessed && len(related.Jobs) > 0:
		entry.Stage = StageTranscodeDone
		entry.Percent = 100
		return entry

	case postProcessed && satisfied:
		entry.Stage = StageSubtitleDone
		entry.Percent = 100
		return entry

	case postProcessed && fetchingSR != nil:
		entry.Stage = StageSubtitleFetching
		return entry

	case postProcessed && foundSR != nil:
		entry.Stage = StageSubtitleFound
		return entry

	case postProcessed && searchingSR != nil:
		entry.Stage = StageSubtitleSearching
		return entry

	case related.MediaFile != nil || importedDL != nil:
		entry.Stage = StageImported
		entry.Percent = 100
		return entry

	case importingDL != nil:
		entry.Stage = StageImporting
		return entry

	case downloadedDL != nil:
		entry.Stage = StageDownloaded
		entry.Percent = 100
		return entry

	case downloadingDL != nil:
		entry.Stage = StageDownloading
		entry.Percent = downloadingDL.Status.ProgressPercent
		entry.Detail = string(downloadingDL.Status.Stage)
		if downloadingDL.Status.ETASeconds != nil {
			eta := time.Duration(*downloadingDL.Status.ETASeconds) * time.Second
			entry.ETA = &eta
		}
		if downloadingDL.Status.StartedAt != nil {
			entry.Since = downloadingDL.Status.StartedAt.Time
		}
		return entry

	case related.Search != nil && related.Search.Status.Phase == catalogv1.SearchPhaseCompleted:
		entry.Stage = StageReleaseSelected
		return entry

	case related.Search != nil && releaseSearchActive(related.Search.Status.Phase):
		entry.Stage = StageReleaseSearching
		if related.Search.Status.StartedAt != nil {
			entry.Since = related.Search.Status.StartedAt.Time
		}
		return entry

	case desc.metadataSynced:
		entry.Stage = StageMetadataSynced
		return entry

	case desc.metadataReady:
		entry.Stage = StageMetadataFound
		return entry

	default:
		entry.Stage = StageMetadataSearching
		return entry
	}
}

// releaseSearchActive reports whether phase means a search is still running
// or waiting to run.
func releaseSearchActive(phase catalogv1.SearchPhase) bool {
	return phase == catalogv1.SearchPhasePending || phase == catalogv1.SearchPhaseRunning
}

// requiresPostProcessing reports whether kind goes through squasharr
// (transcode) and captionarr (subtitles) after import. Only video kinds do:
// Movie, Series and Episode. Music, book and comic kinds (Artist, Album,
// Author, Book, Audiobook, Comic, Issue) have no transcode or subtitle
// pipeline at all, so the transcode and subtitle stages -- and StageComplete
// requiring them -- must never be produced for those kinds; for them,
// StageComplete is reached as soon as the item is imported.
func requiresPostProcessing(kind commonv1.MediaKind) bool {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		return true
	default:
		return false
	}
}

// --- failure and blocked signals -------------------------------------------------

func failedDownload(related Related) *downloadv1.Download {
	for i := range related.Downloads {
		if related.Downloads[i].Status.Phase == downloadv1.DownloadPhaseFailed {
			return &related.Downloads[i]
		}
	}
	return nil
}

func failedJob(related Related) *transcodev1.TranscodeJob {
	for i := range related.Jobs {
		if related.Jobs[i].Status.Phase == transcodev1.TranscodeJobPhaseFailed {
			return &related.Jobs[i]
		}
	}
	return nil
}

func blockedDownload(related Related) *downloadv1.Download {
	for i := range related.Downloads {
		d := &related.Downloads[i]
		if d.Status.Phase == downloadv1.DownloadPhaseBlocklisted {
			return d
		}
		if d.Status.Import != nil && d.Status.Import.State == downloadv1.ImportPhaseBlocked {
			return d
		}
	}
	return nil
}

func blockedSubtitle(related Related) *subtitlev1.SubtitleRequest {
	for i := range related.Subtitles {
		if related.Subtitles[i].Status.Phase == subtitlev1.SubtitleRequestPhaseBlocked {
			return &related.Subtitles[i]
		}
	}
	return nil
}

// --- transcode -----------------------------------------------------------------

// activeJob returns the first TranscodeJob still doing work, if any.
func activeJob(related Related) *transcodev1.TranscodeJob {
	for i := range related.Jobs {
		switch related.Jobs[i].Status.Phase {
		case transcodev1.TranscodeJobPhasePending,
			transcodev1.TranscodeJobPhasePlanned,
			transcodev1.TranscodeJobPhaseQueued,
			transcodev1.TranscodeJobPhaseRunning,
			transcodev1.TranscodeJobPhaseVerifying:
			return &related.Jobs[i]
		}
	}
	return nil
}

// --- subtitles -----------------------------------------------------------------

// subtitlesSatisfied reports whether every related SubtitleRequest has
// reached SubtitleRequestPhaseSatisfied. An item with no SubtitleRequest at
// all is not considered satisfied by this helper -- callers that want to
// treat "no subtitles wanted" as done check len(related.Subtitles) too.
func subtitlesSatisfied(related Related) bool {
	if len(related.Subtitles) == 0 {
		return false
	}
	for i := range related.Subtitles {
		if related.Subtitles[i].Status.Phase != subtitlev1.SubtitleRequestPhaseSatisfied {
			return false
		}
	}
	return true
}

// fetchingSubtitle returns a SubtitleRequest that is actively downloading a
// chosen candidate.
//
// SubtitleRequestPhase has no distinct "fetching" value (only Wanted,
// Searching, Satisfied and Blocked), so this is derived from the per-language
// SubtitleItem: phase Searching plus an item that has already picked a
// candidate (Provider or SubtitleID set) but has not written DownloadedAt
// yet. That is the one moment the API can distinguish "still probing
// providers" from "grabbing the file it already chose".
func fetchingSubtitle(related Related) *subtitlev1.SubtitleRequest {
	for i := range related.Subtitles {
		s := &related.Subtitles[i]
		if s.Status.Phase != subtitlev1.SubtitleRequestPhaseSearching {
			continue
		}
		for _, it := range s.Status.Items {
			if (it.Provider != "" || it.SubtitleID != "") && it.DownloadedAt == nil {
				return s
			}
		}
	}
	return nil
}

// foundSubtitle returns a SubtitleRequest that already has a subtitle on
// disk for at least one language (downloaded, or downloaded-but-below-cutoff)
// while the request as a whole is not yet Satisfied. There is no explicit
// "found" phase on SubtitleRequest either; this reuses the per-language item
// state the same way fetchingSubtitle does.
func foundSubtitle(related Related) *subtitlev1.SubtitleRequest {
	for i := range related.Subtitles {
		s := &related.Subtitles[i]
		if s.Status.Phase == subtitlev1.SubtitleRequestPhaseSatisfied {
			continue
		}
		for _, it := range s.Status.Items {
			if it.State == subtitlev1.SubtitleItemDownloaded || it.State == subtitlev1.SubtitleItemUpgradable {
				return s
			}
		}
	}
	return nil
}

// searchingSubtitle returns a SubtitleRequest that is wanted or actively
// searching with nothing found yet -- the fallback subtitle state once
// fetchingSubtitle and foundSubtitle have both ruled themselves out.
func searchingSubtitle(related Related) *subtitlev1.SubtitleRequest {
	for i := range related.Subtitles {
		s := &related.Subtitles[i]
		if s.Status.Phase == subtitlev1.SubtitleRequestPhaseWanted || s.Status.Phase == subtitlev1.SubtitleRequestPhaseSearching {
			return s
		}
	}
	return nil
}

// --- download and import --------------------------------------------------------

func importedDownload(related Related) *downloadv1.Download {
	for i := range related.Downloads {
		d := &related.Downloads[i]
		if d.Status.Import != nil &&
			(d.Status.Import.State == downloadv1.ImportPhaseImported || d.Status.Import.State == downloadv1.ImportPhaseIgnored) {
			return d
		}
	}
	return nil
}

func importingDownload(related Related) *downloadv1.Download {
	for i := range related.Downloads {
		d := &related.Downloads[i]
		if d.Status.Import != nil &&
			(d.Status.Import.State == downloadv1.ImportPhasePending || d.Status.Import.State == downloadv1.ImportPhaseImporting) {
			return d
		}
	}
	return nil
}

func downloadedDownload(related Related) *downloadv1.Download {
	for i := range related.Downloads {
		switch related.Downloads[i].Status.Phase {
		case downloadv1.DownloadPhaseCompleted, downloadv1.DownloadPhaseSeeding:
			return &related.Downloads[i]
		}
	}
	return nil
}

// downloadingDownload is the item's in-flight Download: every phase before
// Completed, and the empty phase of a Download the grab has just created and
// grabarr has not yet reconciled -- the grab has happened, so the item is
// downloading, not still at ReleaseSelected. This is the same reading as
// catalogarr's rollup.DownloadNonTerminal (gap-fix ruling R-12, re-read
// against every DownloadPhase by task X14); the pipeline only splits the
// finished half finer (Downloaded, Importing, Imported). Removing, and any
// phase added later, show no Download stage until someone decides one:
// TestProjectMapsEveryDownloadPhase fails for a phase with no row.
func downloadingDownload(related Related) *downloadv1.Download {
	for i := range related.Downloads {
		switch related.Downloads[i].Status.Phase {
		case "",
			downloadv1.DownloadPhasePending,
			downloadv1.DownloadPhaseAssigned,
			downloadv1.DownloadPhaseQueued,
			downloadv1.DownloadPhaseDownloading,
			downloadv1.DownloadPhasePaused:
			return &related.Downloads[i]
		}
	}
	return nil
}

// --- item metadata extraction ----------------------------------------------------

// itemDescription is what Project needs from the catalog item itself, beyond
// its related resources: its kind, its display title and how far along its
// metadata sync is.
type itemDescription struct {
	kind           commonv1.MediaKind
	title          string
	metadataReady  bool
	metadataSynced bool
}

// describeItem type-switches on the concrete catalog kind to read its title
// and MetadataReady condition. Every top-level catalog kind (Movie, Series,
// Album, Artist, Author, Book, Audiobook, Comic) declares its own
// <Kind>ConditionMetadataReady constant and a status.metadata pointer with a
// Title (or, for Artist and Author, Name) field, following the same shape;
// Episode and Issue are children of Series and Comic, carry their own
// status.title directly and have no MetadataReady condition of their own, so
// they are treated as always metadata-synced -- an episode or issue is only
// created once its parent's metadata sync has already run.
func describeItem(item client.Object) itemDescription {
	switch v := item.(type) {
	case *catalogv1.Movie:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Title != "" {
			title = v.Status.Metadata.Title
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.MovieConditionMetadataReady, commonv1.MediaKindMovie, title)

	case *catalogv1.Series:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Title != "" {
			title = v.Status.Metadata.Title
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.SeriesConditionMetadataReady, commonv1.MediaKindSeries, title)

	case *catalogv1.Album:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Title != "" {
			title = v.Status.Metadata.Title
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.AlbumConditionMetadataReady, commonv1.MediaKindAlbum, title)

	case *catalogv1.Artist:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Name != "" {
			title = v.Status.Metadata.Name
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.ArtistConditionMetadataReady, commonv1.MediaKindArtist, title)

	case *catalogv1.Author:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Name != "" {
			title = v.Status.Metadata.Name
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.AuthorConditionMetadataReady, commonv1.MediaKindAuthor, title)

	case *catalogv1.Book:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Title != "" {
			title = v.Status.Metadata.Title
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.BookConditionMetadataReady, commonv1.MediaKindBook, title)

	case *catalogv1.Audiobook:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Title != "" {
			title = v.Status.Metadata.Title
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.AudiobookConditionMetadataReady, commonv1.MediaKindAudiobook, title)

	case *catalogv1.Comic:
		title := v.GetName()
		if v.Status.Metadata != nil && v.Status.Metadata.Title != "" {
			title = v.Status.Metadata.Title
		}
		return metadataDescription(v, v.Status.Conditions, catalogv1.ComicConditionMetadataReady, commonv1.MediaKindComic, title)

	case *catalogv1.Episode:
		title := v.Status.Title
		if title == "" {
			title = v.GetName()
		}
		return itemDescription{kind: commonv1.MediaKindEpisode, title: title, metadataReady: true, metadataSynced: true}

	case *catalogv1.Issue:
		title := v.Status.Title
		if title == "" {
			title = v.GetName()
		}
		return itemDescription{kind: commonv1.MediaKindIssue, title: title, metadataReady: true, metadataSynced: true}

	default:
		return itemDescription{title: item.GetName(), metadataReady: true, metadataSynced: true}
	}
}

// metadataDescription reads the shared MetadataReady + status.metadata shape
// that every top-level catalog kind follows: ready comes from the condition,
// synced additionally requires that condition to be reporting on the item's
// current generation (pkg/k8s.StatusUpToDate), so a MetadataReady=True left
// over from a previous generation shows as "found" rather than "synced"
// until the next reconcile catches up.
func metadataDescription(obj client.Object, conditions []metav1.Condition, condType string, kind commonv1.MediaKind, title string) itemDescription {
	ready := k8s.IsConditionTrue(conditions, condType)
	synced := ready && k8s.StatusUpToDate(obj, conditions, condType)
	return itemDescription{kind: kind, title: title, metadataReady: ready, metadataSynced: synced}
}
