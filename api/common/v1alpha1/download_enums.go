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

// The download vocabulary shared by the Download kind (until release N+1)
// and the grab entries on catalog items (ADR-0019 §6.2): phases, stages,
// failure reasons, purposes, grab sources, import rejection classes,
// priorities and the payload source. api/download/v1alpha1 aliases every one
// until the Download CRD is removed.

// DownloadPhase is the lifecycle phase of a Download, owned by the grabarr
// controller.
//
// +kubebuilder:validation:Enum=Pending;Assigned;Queued;Downloading;Paused;Completed;Seeding;Imported;Failed;Blocklisted;Removing
type DownloadPhase string

// Download phases.
const (
	// DownloadPhasePending means the Download exists but has no client yet.
	DownloadPhasePending DownloadPhase = "Pending"
	// DownloadPhaseAssigned means a client and engine ordinal were chosen.
	DownloadPhaseAssigned DownloadPhase = "Assigned"
	// DownloadPhaseQueued means the engine accepted the work but has not started it.
	DownloadPhaseQueued DownloadPhase = "Queued"
	// DownloadPhaseDownloading means bytes are moving.
	DownloadPhaseDownloading DownloadPhase = "Downloading"
	// DownloadPhasePaused means transfer is suspended, by spec.paused or by a health action.
	DownloadPhasePaused DownloadPhase = "Paused"
	// DownloadPhaseCompleted means the content is on disk and ready to import.
	DownloadPhaseCompleted DownloadPhase = "Completed"
	// DownloadPhaseSeeding means the content is imported or importable and the torrent is seeding.
	DownloadPhaseSeeding DownloadPhase = "Seeding"
	// DownloadPhaseImported means catalogarr finished importing the content.
	DownloadPhaseImported DownloadPhase = "Imported"
	// DownloadPhaseFailed means the Download cannot make progress; see status.failureReason.
	DownloadPhaseFailed DownloadPhase = "Failed"
	// DownloadPhaseBlocklisted means the release is blocklisted until status.blocklistedUntil.
	DownloadPhaseBlocklisted DownloadPhase = "Blocklisted"
	// DownloadPhaseRemoving means the engine is tearing the transfer down.
	DownloadPhaseRemoving DownloadPhase = "Removing"
)

// DownloadStage is the fine-grained engine stage, reported by grabarr-engine.
//
// +kubebuilder:validation:Enum=fetchingMetadata;transferring;verifying;repairing;extracting;publishing;seeding;done
type DownloadStage string

// Download stages.
const (
	// DownloadStageFetchingMetadata means the engine is resolving the torrent metainfo or NZB.
	DownloadStageFetchingMetadata DownloadStage = "fetchingMetadata"
	// DownloadStageTransferring means pieces or articles are being fetched.
	DownloadStageTransferring DownloadStage = "transferring"
	// DownloadStageVerifying means hashes or article checksums are being checked.
	DownloadStageVerifying DownloadStage = "verifying"
	// DownloadStageRepairing means par2 repair is running.
	DownloadStageRepairing DownloadStage = "repairing"
	// DownloadStageExtracting means archives are being unpacked.
	DownloadStageExtracting DownloadStage = "extracting"
	// DownloadStagePublishing means the content is being moved to its output path.
	DownloadStagePublishing DownloadStage = "publishing"
	// DownloadStageSeeding means the torrent is seeding toward its goal.
	DownloadStageSeeding DownloadStage = "seeding"
	// DownloadStageDone means the engine has nothing left to do.
	DownloadStageDone DownloadStage = "done"
)

// DownloadFailureReason is the machine-readable cause of a failure.
//
// +kubebuilder:validation:Enum=none;missingArticles;diskFull;encrypted;stalled;writeError;timeout;importRejected;importExpired;manual;payloadMismatch;payloadUnavailable
type DownloadFailureReason string

// Download failure reasons.
const (
	// DownloadFailureNone means no failure has been recorded.
	DownloadFailureNone DownloadFailureReason = "none"
	// DownloadFailureMissingArticles means usenet articles could not be fetched or repaired.
	DownloadFailureMissingArticles DownloadFailureReason = "missingArticles"
	// DownloadFailureDiskFull means the client ran out of space.
	DownloadFailureDiskFull DownloadFailureReason = "diskFull"
	// DownloadFailureEncrypted means the content is password protected.
	DownloadFailureEncrypted DownloadFailureReason = "encrypted"
	// DownloadFailureStalled means no progress was made within the inactivity window.
	DownloadFailureStalled DownloadFailureReason = "stalled"
	// DownloadFailureWriteError means the engine could not write to its volume.
	DownloadFailureWriteError DownloadFailureReason = "writeError"
	// DownloadFailureTimeout means the Download exceeded its overall deadline.
	DownloadFailureTimeout DownloadFailureReason = "timeout"
	// DownloadFailureImportRejected means catalogarr refused every file.
	DownloadFailureImportRejected DownloadFailureReason = "importRejected"
	// DownloadFailureImportExpired means importarr held the import for a
	// person -- the item would not take the files, they could not be
	// attributed, or the import kept failing transiently -- and nobody
	// imported them within ImportHoldRetention. The files are removed; the
	// release is not blocklisted and no redownload is searched, since
	// nothing showed the release was at fault.
	DownloadFailureImportExpired DownloadFailureReason = "importExpired"
	// DownloadFailureManual means an operator failed the Download by hand.
	DownloadFailureManual DownloadFailureReason = "manual"
	// DownloadFailurePayloadMismatch means the resolved torrent's info hash
	// is not spec.source.expectedInfoHash: the indexer served different
	// content than the grab decision was made from, so nothing was added.
	DownloadFailurePayloadMismatch DownloadFailureReason = "payloadMismatch"
	// DownloadFailurePayloadUnavailable is an engine's answer when it cannot
	// re-add an entry's transfer because the payload can no longer be fetched
	// from its indexer (ADR-0019 §6.7): before import a release fault, after
	// import a lost seed.
	DownloadFailurePayloadUnavailable DownloadFailureReason = "payloadUnavailable"
)

// IsReleaseFault reports whether a failure for reason r is the RELEASE's
// fault, which is what decides whether grabarr blocklists it (design spec
// §8.3, "Failed -> Blocklisted").
//
// The ruling (gap fix Y2): missingArticles, encrypted, stalled, timeout,
// importRejected and manual blocklist; diskFull and writeError do not, and
// neither do none or an empty or unknown reason. payloadMismatch (Z1)
// blocklists too: an info hash that differs from the one the grab decided on
// is the indexer serving other content, which no retry of the same release
// can fix. payloadUnavailable (ADR-0019 §6.7) blocklists too: the indexer no
// longer serves the release, so no re-add of it can succeed. A local fault is not
// evidence against the release -- the same release grabbed onto a disk with
// room would have succeeded -- so blocklisting it would discard a good
// release and send the redownload search to a worse one. Sonarr draws the
// same line: its SABnzbd client reports "Unpacking failed, write error or
// disk is full?" as a Warning rather than a Failed item, and NZBGet's
// UnpackStatus=SPACE likewise, while every other client-reported failure is
// Failed, which Sonarr's FailedDownloadService blocklists.
//
// manual counts as the release's fault because it is an operator saying so:
// it is recorded when someone labels a Download blocklisted by hand.
func (r DownloadFailureReason) IsReleaseFault() bool {
	switch r {
	case DownloadFailureMissingArticles, DownloadFailureEncrypted, DownloadFailureStalled,
		DownloadFailureTimeout, DownloadFailureImportRejected, DownloadFailureManual,
		DownloadFailurePayloadMismatch, DownloadFailurePayloadUnavailable:
		return true
	default:
		return false
	}
}

// IsFailure reports whether r names an actual failure: set, and not
// DownloadFailureNone.
func (r DownloadFailureReason) IsFailure() bool {
	return r != "" && r != DownloadFailureNone
}

// IsLocalFault is a failure of this host, not of the release: never
// blocklisted and never searched again (Radarr raises no DownloadFailedEvent
// for one; ADR-0019 §6.4).
func (r DownloadFailureReason) IsLocalFault() bool {
	return r == DownloadFailureDiskFull || r == DownloadFailureWriteError
}

// DownloadPriority orders Downloads within an engine's queue.
//
// +kubebuilder:validation:Enum=high;normal;low
type DownloadPriority string

// Download priorities.
const (
	// DownloadPriorityHigh jumps the queue.
	DownloadPriorityHigh DownloadPriority = "high"
	// DownloadPriorityNormal is the default.
	DownloadPriorityNormal DownloadPriority = "normal"
	// DownloadPriorityLow runs only when the engine is otherwise idle.
	DownloadPriorityLow DownloadPriority = "low"
)

// GrabSource records what caused a release to be grabbed.
//
// +kubebuilder:validation:Enum=rss;search;interactive;push;redownload
type GrabSource string

// Grab sources.
const (
	// GrabSourceRSS means an indexer RSS poll matched a wanted item.
	GrabSourceRSS GrabSource = "rss"
	// GrabSourceSearch means an automatic search grabbed the release.
	GrabSourceSearch GrabSource = "search"
	// GrabSourceInteractive means an operator picked the release by hand.
	GrabSourceInteractive GrabSource = "interactive"
	// GrabSourcePush means an external system pushed the release in.
	GrabSourcePush GrabSource = "push"
	// GrabSourceRedownload means an earlier grab failed and was replaced.
	GrabSourceRedownload GrabSource = "redownload"
)

// IndexerDownload points at a release on an indexer. grabarr resolves it
// through indexarr at grab time to obtain the actual .torrent or .nzb bytes;
// those bytes are deliberately never stored on the Download object, which
// keeps the object small and keeps indexer credentials out of the API.
type IndexerDownload struct {
	// IndexerRef is the name of the Indexer that published the release, in the
	// same namespace as the Download.
	// +required
	// +kubebuilder:validation:MinLength=1
	IndexerRef string `json:"indexerRef"`

	// GUID is the indexer-scoped unique identifier of the release. It is the
	// input to the Download's deterministic name.
	// +required
	// +kubebuilder:validation:MinLength=1
	GUID string `json:"guid"`

	// url has no omitempty, so the plain struct always sends it: since the
	// type moved to api/common (ADR-0019 §6.2) it has no apply configuration,
	// and every Download ever applied sent url even when empty. source is
	// `self == oldSelf`, and an absent key does not equal an empty one, so
	// omitting it would make a re-apply onto such a Download fail as "source
	// is immutable". The optional marker keeps the schema's field optional.

	// URL is the indexer's download link for the release. grabarr fetches it
	// through indexarr, which applies the indexer's auth and proxy settings.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url"`
}

// DownloadSource says where the transfer payload comes from. Exactly one of
// the four members is set. There is intentionally no inline bytes field: a
// .torrent or .nzb body is never embedded in the object. Either the payload is
// addressable by URL, or indexerDownload names the release and grabarr
// resolves it through indexarr.
//
// +kubebuilder:validation:XValidation:rule="(has(self.magnetURL) ? 1 : 0) + (has(self.torrentURL) ? 1 : 0) + (has(self.nzbURL) ? 1 : 0) + (has(self.indexerDownload) ? 1 : 0) == 1",message="exactly one of magnetURL, torrentURL, nzbURL or indexerDownload must be set"
type DownloadSource struct {
	// MagnetURL is a magnet link the torrent engine can add directly.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:Pattern=`^magnet:\?.+$`
	MagnetURL *string `json:"magnetURL,omitempty"`

	// TorrentURL is a directly fetchable .torrent URL that needs no indexer
	// credentials.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	TorrentURL *string `json:"torrentURL,omitempty"`

	// NZBURL is a directly fetchable .nzb URL that needs no indexer credentials.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	NZBURL *string `json:"nzbURL,omitempty"`

	// IndexerDownload names a release to resolve through indexarr. This is the
	// normal path: the indexer applies auth, rate limits and proxying, and
	// returns the payload to the engine.
	// +optional
	IndexerDownload *IndexerDownload `json:"indexerDownload,omitempty"`

	// ExpectedInfoHash is the info hash the resolved torrent must have. The
	// engine refuses a mismatch, which stops an indexer from swapping content
	// out from under a grab decision. Torrent only.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{40}([0-9a-f]{24})?$`
	ExpectedInfoHash *string `json:"expectedInfoHash,omitempty"`
}

// DownloadPurpose is what a Download is for.
//
// +kubebuilder:validation:Enum=audioDonor
type DownloadPurpose string

// DownloadPurposeAudioDonor is a release grabbed only for its audio: the
// importer keeps its missing-language tracks as an item's donor and makes
// no library file of it (anime dual-audio spec §6).
const DownloadPurposeAudioDonor DownloadPurpose = "audioDonor"

// ImportRejectionClass is why an import that imported nothing did so, and
// with it the remediation (2026-10-07: refusing every file used to blocklist
// the release at once, which threw away a good 20 GB download that the item
// merely would not take).
//
// +kubebuilder:validation:Enum=transient;itemState;needsPerson;releaseFault
type ImportRejectionClass string

// Import rejection classes, in the order importarr lets one outweigh the
// next when the files of one download were refused for several reasons:
// any doubt holds the download rather than condemning the release.
const (
	// ImportClassTransient means the walk could not judge the files: one
	// could not be read or probed, or placing it failed (a full disk, a
	// permission, the NAS). importarr retries on the import consumer's
	// backoff and holds the download after the last attempt.
	ImportClassTransient ImportRejectionClass = "transient"
	// ImportClassItemState means the item does not take the files now: its
	// file is transcoded and final, or already as good, or it holds several
	// files. Not the release's fault: held for a person.
	ImportClassItemState ImportRejectionClass = "itemState"
	// ImportClassNeedsPerson means the files cannot be attributed without
	// guessing (a name that does not parse, an ambiguous episode), or an
	// annotation or the target is wrong. Held for a person.
	ImportClassNeedsPerson ImportRejectionClass = "needsPerson"
	// ImportClassReleaseFault means the release is not what it claimed:
	// only samples or junk, another item, a quality the profile does not
	// allow. importarr walks it once more to confirm, then blocks it, and
	// grabarr blocklists the release and searches again.
	ImportClassReleaseFault ImportRejectionClass = "releaseFault"
)
