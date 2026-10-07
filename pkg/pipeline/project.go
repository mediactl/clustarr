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
	"cmp"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s/conditions"
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
	entry := project(item, related)
	if entry.Stage.Settled() {
		entry.Since = settledAt(item, related)
	}
	return entry
}

// settledAt is when a settled item reached its result: the newest activity
// among its grab entries (grabbed, completed, imported), its file's
// transcode (finished) and subtitles (a subtitle written), the MediaFile and
// Search (created), or the item's own creation when it has none. The pipeline
// page orders its results by it (Trim), so an item added long ago and
// imported yesterday reads as yesterday's result.
func settledAt(item client.Object, related Related) time.Time {
	at := item.GetCreationTimestamp().Time
	seen := func(t *metav1.Time) {
		if t != nil && t.After(at) {
			at = t.Time
		}
	}
	for i := range related.Entries {
		e := &related.Entries[i]
		seen(&e.GrabbedAt)
		seen(e.CompletedAt)
		if e.Import != nil {
			seen(e.Import.ImportedAt)
		}
	}
	if t := transcodeOf(related); t != nil {
		seen(t.FinishedAt)
	}
	if st := subtitlesOf(related); st != nil {
		for i := range st.Items {
			seen(st.Items[i].DownloadedAt)
		}
	}
	if related.MediaFile != nil {
		seen(&related.MediaFile.CreationTimestamp)
	}
	if related.Search != nil {
		seen(&related.Search.CreationTimestamp)
	}
	return at
}

// project derives the entry; Project sets a settled entry's Since after it.
func project(item client.Object, related Related) Entry {
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
	blockedDL := blockedDownload(related)
	importedDL := importedDownload(related)
	importingDL := importingDownload(related)
	downloadedDL := downloadedDownload(related)
	downloadingDL := downloadingDownload(related)
	imported := related.MediaFile != nil || importedDL != nil
	subsDone := subtitlesIs(related, catalogv1.SubtitlesPhaseSatisfied, catalogv1.SubtitlesPhaseNotWanted)
	// complete requires imported, plus -- for a kind that goes through
	// transcode and subtitles -- the file's subtitles satisfied or not
	// wanted and its transcode settled (Succeeded, Skipped, or a file
	// already final). Complete no longer needs a job or a request object
	// (ADR-0016): a Succeeded block stays on the file, so retention no
	// longer drops an item back to SubtitleDone. A video file with no
	// blocks yet (unprobed, or waiting beyond the transcode window) is not
	// complete. A non-video kind (Album, Artist, Author, Book, Audiobook,
	// Comic, Issue) has neither block, so for those Complete is reached as
	// soon as the item is imported. Failure and blocked states are already
	// ruled out above.
	complete := imported && (!postProcessed || (subsDone && transcodeSettled(related)))

	switch {
	case failedDL != nil:
		entry.Stage = StageFailed
		entry.Failure = string(failedDL.FailureReason)
		if entry.Failure == "" {
			entry.Failure = failedDL.Message
		}
		return entry

	case postProcessed && transcodeIs(related, catalogv1.TranscodePhaseFailed):
		t := transcodeOf(related)
		entry.Stage = StageFailed
		entry.Failure = cmp.Or(t.Message, t.Reason)
		return entry

	case blockedDL != nil:
		entry.Stage = StageBlocked
		entry.Failure = "release blocklisted"
		if imp := blockedDL.Import; imp != nil && blockedDL.Phase != commonv1.DownloadPhaseBlocklisted &&
			(imp.Phase == catalogv1.ImportPhaseHeld || imp.Phase == catalogv1.ImportPhaseBlocked) {
			entry.Failure = imp.Message
			if entry.Failure == "" {
				entry.Failure = "import held"
			}
		}
		return entry

	case postProcessed && subtitlesIs(related, catalogv1.SubtitlesPhaseBlocked):
		entry.Stage = StageBlocked
		entry.Failure = "subtitles: " + subtitlesOf(related).Reason
		return entry

	case complete:
		entry.Stage = StageComplete
		entry.Percent = 100
		return entry

	case postProcessed && transcodeIs(related, catalogv1.TranscodePhaseQueued, catalogv1.TranscodePhaseRunning):
		t := transcodeOf(related)
		entry.Stage = StageTranscoding
		if related.TranscodePercent != nil {
			entry.Percent = *related.TranscodePercent
		}
		if t.Plan != nil {
			entry.Detail = t.Plan.Encoder
		}
		if t.StartedAt != nil {
			entry.Since = t.StartedAt.Time
		}
		return entry

	case postProcessed && transcodeDone(related):
		entry.Stage = StageTranscodeDone
		entry.Percent = 100
		return entry

	case postProcessed && subsDone:
		entry.Stage = StageSubtitleDone
		entry.Percent = 100
		return entry

	case postProcessed && subtitleOnDisk(related):
		entry.Stage = StageSubtitleFound
		return entry

	case postProcessed && subtitlesIs(related, catalogv1.SubtitlesPhaseWanted, catalogv1.SubtitlesPhaseSearching):
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
		// The entry carries no byte counters (they are clustarr-progress'):
		// the percent stays unknown here and the ui reads the 1 Hz record.
		entry.Stage = StageDownloading
		entry.Detail = string(downloadingDL.Stage)
		if downloadingDL.StartedAt != nil {
			entry.Since = downloadingDL.StartedAt.Time
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

// requiresPostProcessing reports whether kind goes through transcode
// (transcode) and caption (subtitles) after import. Only video kinds do:
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

func failedDownload(related Related) *catalogv1.DownloadEntry {
	for i := range related.Entries {
		if related.Entries[i].Phase == commonv1.DownloadPhaseFailed {
			return &related.Entries[i]
		}
	}
	return nil
}

// blockedDownload is a blocklisted grab, or one whose import waits for a
// person (held) or was refused as the release's fault (blocked).
func blockedDownload(related Related) *catalogv1.DownloadEntry {
	for i := range related.Entries {
		e := &related.Entries[i]
		if e.Phase == commonv1.DownloadPhaseBlocklisted {
			return e
		}
		if imp := e.Import; imp != nil && (imp.Phase == catalogv1.ImportPhaseHeld || imp.Phase == catalogv1.ImportPhaseBlocked) {
			return e
		}
	}
	return nil
}

// --- the file's transcode and subtitles (MediaFile status, ADR-0016) ------------

func transcodeOf(r Related) *catalogv1.TranscodeState {
	if r.MediaFile == nil {
		return nil
	}
	return r.MediaFile.Status.Transcode
}

func subtitlesOf(r Related) *catalogv1.SubtitlesStatus {
	if r.MediaFile == nil {
		return nil
	}
	return r.MediaFile.Status.Subtitles
}

func transcodeIs(r Related, phases ...catalogv1.TranscodePhase) bool {
	t := transcodeOf(r)
	return t != nil && slices.Contains(phases, t.Phase)
}

// incorporated reports a phase-less block carrying a profileTag: the file
// was transcoded before the loop wrote phases, which reads as Succeeded
// (loop spec §2.5).
func incorporated(r Related) bool {
	t := transcodeOf(r)
	return t != nil && t.Phase == "" && t.ProfileTag != ""
}

// transcodeDone: swapped, incorporated, or a file that is final.
func transcodeDone(r Related) bool {
	if transcodeIs(r, catalogv1.TranscodePhaseSwapping, catalogv1.TranscodePhaseSucceeded) || incorporated(r) {
		return true
	}
	return r.MediaFile != nil && r.MediaFile.Transcoded()
}

// transcodeSettled: the file's transcode is over -- Succeeded (or an
// incorporated phase-less block), Skipped, or the file is already final.
// A video file with no block yet (no profile won it, or it waits in the
// backlog beyond the window) is not settled.
func transcodeSettled(r Related) bool {
	if transcodeIs(r, catalogv1.TranscodePhaseSucceeded, catalogv1.TranscodePhaseSkipped) || incorporated(r) {
		return true
	}
	return r.MediaFile != nil && r.MediaFile.Transcoded()
}

func subtitlesIs(r Related, phases ...catalogv1.SubtitlesPhase) bool {
	s := subtitlesOf(r)
	return s != nil && slices.Contains(phases, s.Phase)
}

// subtitleOnDisk: a language is on disk (downloaded, or downloaded but below
// the cutoff) while the file's subtitles are not yet satisfied.
func subtitleOnDisk(r Related) bool {
	if !subtitlesIs(r, catalogv1.SubtitlesPhaseWanted, catalogv1.SubtitlesPhaseSearching) {
		return false
	}
	for _, it := range subtitlesOf(r).Items {
		if it.State == catalogv1.SubtitleStateDownloaded || it.State == catalogv1.SubtitleStateUpgradable {
			return true
		}
	}
	return false
}

// --- download and import --------------------------------------------------------

func importedDownload(related Related) *catalogv1.DownloadEntry {
	for i := range related.Entries {
		e := &related.Entries[i]
		if e.Phase == commonv1.DownloadPhaseImported ||
			(e.Import != nil && e.Import.Phase == catalogv1.ImportPhaseImported && e.Import.ImportedAt != nil) {
			return e
		}
	}
	return nil
}

func importingDownload(related Related) *catalogv1.DownloadEntry {
	for i := range related.Entries {
		e := &related.Entries[i]
		if e.Phase != commonv1.DownloadPhaseCompleted || e.Import == nil {
			continue
		}
		switch e.Import.Phase {
		case catalogv1.ImportPhasePending, catalogv1.ImportPhaseInspected, catalogv1.ImportPhaseApproved,
			catalogv1.ImportPhaseImported:
			return e
		}
	}
	return nil
}

func downloadedDownload(related Related) *catalogv1.DownloadEntry {
	for i := range related.Entries {
		switch related.Entries[i].Phase {
		case commonv1.DownloadPhaseCompleted, commonv1.DownloadPhaseSeeding:
			return &related.Entries[i]
		}
	}
	return nil
}

// downloadingDownload is the item's in-flight grab: every phase before
// Completed, and the empty phase of an entry just created -- the grab has
// happened, so the item is downloading, not still at ReleaseSelected. This
// is rollup.EntryNonTerminal's reading; the pipeline only splits the
// finished half finer (Downloaded, Importing, Imported). Removing, and any
// phase added later, show no download stage until someone decides one.
func downloadingDownload(related Related) *catalogv1.DownloadEntry {
	for i := range related.Entries {
		switch related.Entries[i].Phase {
		case "",
			commonv1.DownloadPhasePending,
			commonv1.DownloadPhaseAssigned,
			commonv1.DownloadPhaseQueued,
			commonv1.DownloadPhaseDownloading,
			commonv1.DownloadPhasePaused:
			return &related.Entries[i]
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
// current generation (pkg/k8s/conditions.StatusUpToDate), so a
// MetadataReady=True left over from a previous generation shows as "found"
// rather than "synced" until the next reconcile catches up.
func metadataDescription(obj client.Object, conds []metav1.Condition, condType string, kind commonv1.MediaKind, title string) itemDescription {
	ready := conditions.IsConditionTrue(conds, condType)
	synced := ready && conditions.StatusUpToDate(obj, conds, condType)
	return itemDescription{kind: kind, title: title, metadataReady: ready, metadataSynced: synced}
}
