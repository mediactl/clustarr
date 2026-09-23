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

package schema

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// SearchReason says why a search was enqueued. It drives both the priority
// lane and how aggressively the worker treats indexer rate limits.
type SearchReason string

// Search reasons.
const (
	SearchReasonAdd         SearchReason = "add"
	SearchReasonMissing     SearchReason = "missing"
	SearchReasonCutoffUnmet SearchReason = "cutoffUnmet"
	SearchReasonInteractive SearchReason = "interactive"
	SearchReasonRedownload  SearchReason = "redownload"
)

// MediaFileReason says why a media file event was emitted.
type MediaFileReason string

// Media file reasons.
const (
	MediaFileReasonManual          MediaFileReason = "manual"
	MediaFileReasonMissingFromDisk MediaFileReason = "missingFromDisk"
	MediaFileReasonUpgrade         MediaFileReason = "upgrade"
)

// ItemEvent reports that a catalog item was added, updated or deleted.
// Subject: clustarr.evt.catalog.<kind>.<added|updated|deleted>.<uid>.
type ItemEvent struct {
	// Media identifies the catalog item the event is about.
	Media commonv1.MediaRef `json:"media"`

	// Ref is the catalog object itself.
	Ref Ref `json:"ref"`

	// Action is one of added, updated or deleted.
	Action string `json:"action"`

	// Title is the item title at the time of the event.
	Title string `json:"title,omitempty"`

	// Year is the item year, when it has one.
	Year int32 `json:"year,omitempty"`

	// IDs maps external metadata providers to this item's ID.
	IDs map[string]string `json:"ids,omitempty"`

	// Monitored is whether the item was monitored at the time of the event.
	Monitored bool `json:"monitored"`

	// AddSource records the import list that added the item, if any.
	AddSource *commonv1.AddSource `json:"addSource,omitempty"`

	// At is when the change happened.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (ItemEvent) Schema() string { return "catalog.ItemEvent.v1" }

// ReleaseEvent reports that a release was grabbed or rejected for a catalog
// item. Subject: clustarr.evt.catalog.release.<grabbed|rejected>.<uid>.
type ReleaseEvent struct {
	// Media identifies the catalog item the release was evaluated against.
	Media commonv1.MediaRef `json:"media"`

	// Action is either grabbed or rejected.
	Action string `json:"action"`

	// GUID is the indexer-scoped release identifier.
	GUID string `json:"guid"`

	// Indexer is the display name of the indexer the release came from.
	Indexer string `json:"indexer,omitempty"`

	// IndexerRef is the Indexer object the release came from.
	IndexerRef *Ref `json:"indexerRef,omitempty"`

	// ReleaseGroup is the parsed release group.
	ReleaseGroup string `json:"releaseGroup,omitempty"`

	// DownloadRef is the Download created by a grab. It is nil for a
	// rejection.
	DownloadRef *Ref `json:"downloadRef,omitempty"`

	// Rejections lists why the release was turned down.
	Rejections []commonv1.Rejection `json:"rejections,omitempty"`

	// Quality is the quality parsed from the release title.
	Quality commonv1.Quality `json:"quality,omitempty"`

	// FormatScore is the total custom-format score of the release.
	FormatScore int32 `json:"formatScore,omitempty"`

	// At is when the decision was taken.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (ReleaseEvent) Schema() string { return "catalog.ReleaseEvent.v1" }

// MediaFileEvent reports an import, replacement or deletion of a file on
// disk. Subject:
// clustarr.evt.catalog.mediafile.<imported|replaced|deleted>.<uid>.
type MediaFileEvent struct {
	// Media identifies the catalog item the file belongs to.
	Media commonv1.MediaRef `json:"media"`

	// Action is one of imported, replaced or deleted.
	Action string `json:"action"`

	// DroppedPath is where the importer found the file.
	DroppedPath string `json:"droppedPath,omitempty"`

	// ImportedPath is where the file now lives in the library.
	ImportedPath string `json:"importedPath,omitempty"`

	// Reason explains a replacement or deletion.
	Reason MediaFileReason `json:"reason,omitempty"`

	// SizeBytes is the file size.
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Quality is the quality of the imported file.
	Quality commonv1.Quality `json:"quality,omitempty"`

	// DownloadRef is the Download the file came from, when it came from one.
	DownloadRef *Ref `json:"downloadRef,omitempty"`

	// At is when the file operation completed.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (MediaFileEvent) Schema() string { return "catalog.MediaFileEvent.v1" }

// ImportListSynced reports the outcome of one import list sync.
// Subject: clustarr.evt.catalog.importlist.synced.<uid>.
type ImportListSynced struct {
	// ListRef is the ImportList that was synced.
	ListRef Ref `json:"listRef"`

	// Fetched is how many items the remote list returned.
	Fetched int32 `json:"fetched"`

	// Added is how many catalog items were created.
	Added int32 `json:"added"`

	// Removed is how many catalog items were removed or unmonitored.
	Removed int32 `json:"removed"`

	// Skipped is how many items were already present.
	Skipped int32 `json:"skipped"`

	// Error is the failure message when the sync did not complete.
	Error string `json:"error,omitempty"`

	// At is when the sync finished.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (ImportListSynced) Schema() string { return "catalog.ImportListSynced.v1" }

// SearchTask asks a search worker to look for releases for a catalog item.
// Subject: clustarr.work.catalogarr.search.<priority>.<mediaKey>.
type SearchTask struct {
	// MediaRef identifies what to search for.
	MediaRef commonv1.MediaRef `json:"mediaRef"`

	// Keys narrows a pack search to specific episodes or issues.
	Keys []string `json:"keys,omitempty"`

	// Reason says why the search was enqueued.
	Reason SearchReason `json:"reason"`

	// SearchRef is the Search object to write results to, for an interactive
	// search.
	SearchRef *Ref `json:"searchRef,omitempty"`

	// UserInvoked marks a search a human asked for. It bypasses some rate
	// limiting and is never coalesced away.
	UserInvoked bool `json:"userInvoked,omitempty"`
}

// Schema implements Payload.
func (SearchTask) Schema() string { return "catalog.SearchTask.v1" }

// GrabTask asks a grab worker to turn the best pending candidate for a media
// key into a Download. It is published with a schedule so delay profiles can
// hold it. Subject: clustarr.work.catalogarr.grab.normal.<mediaKey>.
type GrabTask struct {
	// MediaRef identifies the catalog item to grab for.
	MediaRef commonv1.MediaRef `json:"mediaRef"`

	// Keys narrows a pack grab to specific episodes or issues.
	Keys []string `json:"keys,omitempty"`
}

// Schema implements Payload.
func (GrabTask) Schema() string { return "catalog.GrabTask.v1" }

// ImportTask asks an import worker to import a completed download.
// Subject: clustarr.work.importarr.fileimport.<download-uid>, built by
// events.WorkFileImportSubject and consumed by ConsumerImportFile
// ("importarr-fileimport", topology.go). This comment previously claimed
// clustarr.work.catalogarr.import.normal.<download-uid>, which no consumer
// ever listened on; amendment-1 moved the importer out of catalogarr into
// importarr/worker/fileimport (D2-7), and the subject documented here never
// followed.
type ImportTask struct {
	// DownloadRef is the completed Download to import.
	DownloadRef Ref `json:"downloadRef"`
}

// Schema implements Payload.
func (ImportTask) Schema() string { return "catalog.ImportTask.v1" }

// MetadataTask asks the metadata gateway to refresh a catalog item.
// Subject: clustarr.work.catalogarr.metadata.<high|normal>.<mediaKey>.
type MetadataTask struct {
	// MediaRef identifies the item to refresh.
	MediaRef commonv1.MediaRef `json:"mediaRef"`

	// RefreshEpoch increments whenever an operator forces a refresh, so a
	// forced refresh is not deduplicated against the scheduled one.
	RefreshEpoch int64 `json:"refreshEpoch,omitempty"`
}

// Schema implements Payload.
func (MetadataTask) Schema() string { return "catalog.MetadataTask.v1" }

// ImportListTask asks a worker to sync one import list.
// Subject: clustarr.work.catalogarr.importlist.normal.<uid>.
type ImportListTask struct {
	// ListRef is the ImportList to sync.
	ListRef Ref `json:"listRef"`

	// Full forces a full sync instead of an incremental one.
	Full bool `json:"full,omitempty"`
}

// Schema implements Payload.
func (ImportListTask) Schema() string { return "catalog.ImportListTask.v1" }

// WantedScan asks the search workers to sweep a namespace for missing and
// cutoff-unmet items. Subject:
// clustarr.work.catalogarr.wantedscan.low.<namespace>.
type WantedScan struct {
	// Namespace is the namespace to sweep.
	Namespace string `json:"namespace"`

	// Kinds limits the sweep to these media kinds. Empty means all kinds.
	Kinds []commonv1.MediaKind `json:"kinds,omitempty"`

	// CutoffUnmet includes items that have a file below the profile cutoff.
	CutoffUnmet bool `json:"cutoffUnmet,omitempty"`

	// Epoch identifies the sweep, so a cron re-fire inside the deduplication
	// window does not enqueue a second sweep.
	Epoch int64 `json:"epoch,omitempty"`
}

// Schema implements Payload.
func (WantedScan) Schema() string { return "catalog.WantedScan.v1" }

// MetadataRequest is the request half of the metadata gateway RPC.
// Subject: clustarr.rpc.catalogarr.metadata.<lookup|search|resolve>.
type MetadataRequest struct {
	// Kind is the media kind being asked about.
	Kind commonv1.MediaKind `json:"kind"`

	// IDs are the external IDs known to the caller, for lookup and resolve.
	IDs map[string]string `json:"ids,omitempty"`

	// Text is the free-text query, for search.
	Text string `json:"text,omitempty"`

	// Year narrows a search.
	Year int32 `json:"year,omitempty"`

	// Region is the release region used for certifications and dates.
	Region string `json:"region,omitempty"`

	// Language is the preferred metadata language.
	Language string `json:"language,omitempty"`
}

// Schema implements Payload.
func (MetadataRequest) Schema() string { return "catalog.MetadataRequest.v1" }

// MetadataResponse is the reply half of the metadata gateway RPC. Result
// holds the provider document as returned by pkg/metadata, left opaque here
// so the bus contract does not depend on the provider models.
type MetadataResponse struct {
	// Kind is the media kind of the result.
	Kind commonv1.MediaKind `json:"kind"`

	// Provider is the provider that answered.
	Provider string `json:"provider,omitempty"`

	// IDs are the external IDs the gateway resolved.
	IDs map[string]string `json:"ids,omitempty"`

	// Result is the provider document, JSON-encoded.
	Result []byte `json:"result,omitempty"`

	// Results holds the hits of a search request.
	Results [][]byte `json:"results,omitempty"`

	// CachedAt is when the gateway cached the document, if it was a hit.
	CachedAt *time.Time `json:"cachedAt,omitempty"`

	// Error is the failure message when the gateway could not answer.
	Error string `json:"error,omitempty"`
}

// Schema implements Payload.
func (MetadataResponse) Schema() string { return "catalog.MetadataResponse.v1" }
