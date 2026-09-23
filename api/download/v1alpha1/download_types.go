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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Condition types reported on a Download.
const (
	// DownloadConditionAssigned is True once the Download has a client and engine.
	DownloadConditionAssigned = "Assigned"
	// DownloadConditionDownloaded is True once every wanted byte is on disk and verified.
	DownloadConditionDownloaded = "Downloaded"
	// DownloadConditionSeedGoalMet is True once the torrent satisfied its seed criteria.
	DownloadConditionSeedGoalMet = "SeedGoalMet"
	// DownloadConditionImported is True once catalogarr finished importing the content.
	DownloadConditionImported = "Imported"
	// DownloadConditionFailed is True when the Download cannot make progress.
	DownloadConditionFailed = "Failed"
)

// Labels applied to Download objects.
const (
	// LabelEngine mirrors status.engine, "<client>-<ordinal>", so an engine
	// replica can watch only the Downloads it owns with a label selector.
	LabelEngine = "download.clustarr.io/engine"

	// LabelClient mirrors status.clientRef so a DownloadClient can select its
	// own Downloads.
	LabelClient = "download.clustarr.io/client"

	// LabelBlocklisted is set to "true" on a Download that represents a
	// blocklisted release. Blocklisting has no status list of its own: the
	// blocklist IS the set of Downloads carrying this label whose
	// status.blocklistedUntil is still in the future. grabarr sweeps expired
	// entries, and the decision engine reads the live set through a catalogarr
	// informer indexed by infohash and normalized title.
	LabelBlocklisted = "download.clustarr.io/blocklisted"

	// LabelBlocklistedValue is the only meaningful value of LabelBlocklisted.
	LabelBlocklistedValue = "true"
)

// DefaultBlocklistTTL is how long a blocklisted Download is kept before
// grabarr sweeps it, when the blocklisting party sets no explicit deadline.
const DefaultBlocklistTTL = 90 * 24 * time.Hour

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
// +kubebuilder:validation:Enum=none;missingArticles;diskFull;encrypted;stalled;writeError;timeout;importRejected;manual
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
	// DownloadFailureManual means an operator failed the Download by hand.
	DownloadFailureManual DownloadFailureReason = "manual"
)

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

// ImportPhase is the state of the import handoff, written by catalogarr.
//
// +kubebuilder:validation:Enum=pending;blocked;importing;imported;ignored
type ImportPhase string

// Import phases.
const (
	// ImportPhasePending means the content is waiting to be imported.
	ImportPhasePending ImportPhase = "pending"
	// ImportPhaseBlocked means the import cannot proceed; see message and rejections.
	ImportPhaseBlocked ImportPhase = "blocked"
	// ImportPhaseImporting means files are being linked or moved into the library.
	ImportPhaseImporting ImportPhase = "importing"
	// ImportPhaseImported means the import finished.
	ImportPhaseImported ImportPhase = "imported"
	// ImportPhaseIgnored means the content was deliberately not imported.
	ImportPhaseIgnored ImportPhase = "ignored"
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

	// URL is the indexer's download link for the release. grabarr fetches it
	// through indexarr, which applies the indexer's auth and proxy settings.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url,omitempty"`
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

// DownloadSpec defines the desired state of Download.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.clientRef) || oldSelf.clientRef.size() == 0 || (has(self.clientRef) && self.clientRef == oldSelf.clientRef)",message="clientRef is immutable once set"
type DownloadSpec struct {
	// Protocol is the transfer protocol of the release; it selects the class of
	// DownloadClient that may take the work. Immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="protocol is immutable"
	Protocol commonv1alpha1.Protocol `json:"protocol"`

	// ClientRef is the DownloadClient handling this Download, in the same
	// namespace. Left empty it is resolved once by grabarr at admission from
	// the enabled clients of the matching protocol; once set it is immutable.
	// +optional
	ClientRef string `json:"clientRef,omitempty"`

	// Source is where the payload comes from. Immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="source is immutable"
	Source DownloadSource `json:"source"`

	// Release is the indexer release snapshot this grab was decided from. It is
	// immutable: re-deciding means creating a new Download.
	//
	// Every comparison below is guarded by has() on BOTH sides, and must stay
	// that way. All five fields are +optional with omitempty in
	// commonv1alpha1.ReleaseInfo, so an absent one is simply not in the object
	// map -- an unguarded self.infoHash == oldSelf.infoHash does not compare
	// unequal, it raises "no such key: infoHash" and the apiserver REJECTS the
	// write. Because a status-subresource apply re-evaluates every spec CEL
	// rule against the stored object, that rejection hits status-only writes
	// too, which is the only way grabarr reports progress at all.
	//
	// infoHash is what made this fatal rather than theoretical: usenet
	// releases do not have one, by protocol. So the unguarded form made every
	// usenet Download unwritable after creation, while every torrent Download
	// passed -- and every fixture in the tree was torrent-shaped, so two
	// separate tasks hit it, worked around it in their own fixtures and moved
	// on. pkg/crdcheck now owns the regression test, against a usenet-shaped
	// Download with no infoHash.
	// +required
	// +kubebuilder:validation:XValidation:rule="(!has(oldSelf.guid) || (has(self.guid) && self.guid == oldSelf.guid)) && (!has(oldSelf.indexerRef) || (has(self.indexerRef) && self.indexerRef == oldSelf.indexerRef)) && (!has(oldSelf.title) || (has(self.title) && self.title == oldSelf.title)) && (!has(oldSelf.protocol) || (has(self.protocol) && self.protocol == oldSelf.protocol)) && (!has(oldSelf.infoHash) || (has(self.infoHash) && self.infoHash == oldSelf.infoHash))",message="release identity is immutable"
	Release commonv1alpha1.ReleaseInfo `json:"release"`

	// Target is the catalog item the content is destined for. It is also the
	// ownerReference of this Download. Immutable.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="target is immutable"
	Target commonv1alpha1.MediaRef `json:"target"`

	// QualityProfileRef is the QualityProfile the grab decision used; the
	// importer re-checks the finished files against it.
	// +optional
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`

	// Priority orders this Download within its engine's queue.
	// +optional
	// +kubebuilder:default=normal
	Priority DownloadPriority `json:"priority,omitempty"`

	// Paused suspends the transfer without removing it from the engine.
	// +optional
	Paused bool `json:"paused,omitempty"`

	// SeedCriteria overrides the client's default seed goal for this Download.
	// Torrent only.
	// +optional
	SeedCriteria *commonv1alpha1.SeedCriteria `json:"seedCriteria,omitempty"`

	// RemoveOnImport removes the transfer from the engine once the import
	// succeeds and, for torrents, the seed goal is met.
	// +optional
	// +kubebuilder:default=true
	RemoveOnImport *bool `json:"removeOnImport,omitempty"`

	// RemoveDataOnDelete deletes the downloaded data when the Download object
	// is deleted. Imported files already linked into the library are untouched.
	// +optional
	// +kubebuilder:default=true
	RemoveDataOnDelete *bool `json:"removeDataOnDelete,omitempty"`

	// GrabbedBy records what caused the grab.
	// +optional
	GrabbedBy GrabSource `json:"grabbedBy,omitempty"`

	// Manual marks an operator-forced grab: a release a person picked, as a
	// Search's grab does. The completed-download importer then treats the
	// import as that person's decision -- it skips the upgrade comparison
	// against an item's existing file, accepts a non-video file whose quality
	// its extension does not determine, imports a video file only the sample
	// size floor suspects is a sample, and records importedFrom.manual on the
	// MediaFile. A quality the item's profile does not allow is still
	// rejected. The importer checks neither monitoring nor availability, so
	// there is no such check to skip. The annotation
	// catalog.clustarr.io/import-override=true has the same effect.
	// +optional
	Manual bool `json:"manual,omitempty"`
}

// DownloadFile is one file within a transfer.
type DownloadFile struct {
	// Path is the file path relative to status.contentRoot. It keys the list.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path"`

	// SizeBytes is the file's size in bytes.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Skipped is true when the engine was told not to fetch this file, e.g. a
	// sample or an unwanted extra in a pack.
	// +optional
	Skipped bool `json:"skipped,omitempty"`
}

// UsenetHealth reports article availability for a usenet transfer.
type UsenetHealth struct {
	// HealthPercent is the share of articles successfully fetched so far, as a
	// whole percentage.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	HealthPercent int32 `json:"healthPercent,omitempty"`

	// CriticalHealthPercent is the floor below which par2 recovery can no
	// longer repair the download, as a whole percentage.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CriticalHealthPercent int32 `json:"criticalHealthPercent,omitempty"`

	// FailedArticles is the number of articles that could not be fetched.
	// +optional
	FailedArticles int32 `json:"failedArticles,omitempty"`

	// TotalArticles is the number of articles in the NZB.
	// +optional
	TotalArticles int32 `json:"totalArticles,omitempty"`
}

// Both ImportedFile paths are capped at 4096, Linux PATH_MAX, rather than
// anything shorter: nothing truncates them, a real path over a tighter cap
// gets the whole status.import apply rejected, and truncating a path to fit
// would record a path that does not exist.

// ImportedFile records one file that made it into the library.
type ImportedFile struct {
	// SourcePath is the path the file had inside the download.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	SourcePath string `json:"sourcePath,omitempty"`

	// DestPath is the path the file now has in the library.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	DestPath string `json:"destPath,omitempty"`

	// MediaFileRef is the name of the MediaFile created for the file.
	// +optional
	MediaFileRef string `json:"mediaFileRef,omitempty"`
}

// ImportState is the outcome of the import handoff.
type ImportState struct {
	// State is the import phase.
	// +optional
	State ImportPhase `json:"state,omitempty"`

	// Message explains the current state in human-readable terms.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message,omitempty"`

	// Imported lists the files that were placed in the library.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	Imported []ImportedFile `json:"imported,omitempty"`

	// Rejections lists why individual files were not imported.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	// +kubebuilder:validation:items:MaxLength=1024
	Rejections []string `json:"rejections,omitempty"`

	// ImportedAt is when the import finished.
	// +optional
	ImportedAt *metav1.Time `json:"importedAt,omitempty"`
}

// DownloadStatus defines the observed state of Download. Three writers share
// it through server-side apply on disjoint field sets: the grabarr controller
// owns the lifecycle fields, the grabarr-engine replica owns the telemetry
// fields, and importarr's file-import worker owns status.import. (Design
// spec §8.4 assigned this field to catalogarr; amendment-1 moved the
// importer out of catalogarr into importarr/worker/fileimport, and this
// field's ownership moved with it. Settled at task D2-7.)
type DownloadStatus struct {
	// ObservedGeneration is the spec generation this status was computed from.
	// Written by the grabarr controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the lifecycle phase. Written by the grabarr controller.
	// +optional
	Phase DownloadPhase `json:"phase,omitempty"`

	// Engine is the engine replica that owns the transfer, "<client>-<ordinal>".
	// It is set once, when the Download is assigned, and the label
	// download.clustarr.io/engine mirrors it so replicas can select their work.
	// +optional
	Engine string `json:"engine,omitempty"`

	// FailureReason is the machine-readable cause of a failure.
	// +optional
	FailureReason DownloadFailureReason `json:"failureReason,omitempty"`

	// BlocklistedUntil is when this release stops being blocklisted. It is
	// meaningful only together with the download.clustarr.io/blocklisted label;
	// grabarr deletes the Download once the deadline passes.
	// +optional
	BlocklistedUntil *metav1.Time `json:"blocklistedUntil,omitempty"`

	// StartedAt is when the engine began transferring.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the content finished downloading.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// SeedGoalMetAt is when the torrent satisfied its seed criteria.
	// +optional
	SeedGoalMetAt *metav1.Time `json:"seedGoalMetAt,omitempty"`

	// Stage is the fine-grained engine stage. Written by grabarr-engine.
	// +optional
	Stage DownloadStage `json:"stage,omitempty"`

	// DownloadID is the engine's own handle for the transfer, e.g. the info
	// hash or NZB id.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	DownloadID string `json:"downloadID,omitempty"`

	// OutputPath is where the finished content was published.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	OutputPath string `json:"outputPath,omitempty"`

	// ContentRoot is the directory the file paths in status.files are relative to.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	ContentRoot string `json:"contentRoot,omitempty"`

	// Files lists the files in the transfer.
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=200
	Files []DownloadFile `json:"files,omitempty"`

	// TotalBytes is the size of the wanted content.
	// +optional
	TotalBytes int64 `json:"totalBytes,omitempty"`

	// RemainingBytes is how many wanted bytes are still missing.
	// +optional
	RemainingBytes int64 `json:"remainingBytes,omitempty"`

	// DownloadedBytes is how many bytes have been fetched.
	// +optional
	DownloadedBytes int64 `json:"downloadedBytes,omitempty"`

	// UploadedBytes is how many bytes have been uploaded. Torrent only.
	// +optional
	UploadedBytes int64 `json:"uploadedBytes,omitempty"`

	// DownloadRateBps is the current download rate in bytes per second.
	// +optional
	DownloadRateBps int64 `json:"downloadRateBps,omitempty"`

	// UploadRateBps is the current upload rate in bytes per second.
	// +optional
	UploadRateBps int64 `json:"uploadRateBps,omitempty"`

	// ETASeconds is the estimated number of seconds until the transfer
	// completes. Unset when the engine cannot estimate one.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ETASeconds *int32 `json:"etaSeconds,omitempty"`

	// ProgressPercent is the share of wanted bytes fetched so far, as a whole
	// percentage. It is an integer on purpose: status telemetry uses scaled
	// integers, never floating point.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ProgressPercent int32 `json:"progressPercent,omitempty"`

	// Seeders is the number of seeders the engine currently sees. Torrent only.
	// +optional
	Seeders int32 `json:"seeders,omitempty"`

	// Peers is the number of connected peers. Torrent only.
	// +optional
	Peers int32 `json:"peers,omitempty"`

	// RatioMilli is uploadedBytes/downloadedBytes in thousandths, so a ratio of
	// 1.5 is 1500. Compare it against spec.seedCriteria.ratio, which is the
	// same number written as a decimal Quantity. Torrent only.
	// +optional
	// +kubebuilder:validation:Minimum=0
	RatioMilli int32 `json:"ratioMilli,omitempty"`

	// SeedTimeSeconds is how long the torrent has been seeding.
	// +optional
	SeedTimeSeconds int64 `json:"seedTimeSeconds,omitempty"`

	// Health reports article availability. Usenet only.
	// +optional
	Health *UsenetHealth `json:"health,omitempty"`

	// IsEncrypted is true when the content turned out to be password protected.
	// +optional
	IsEncrypted bool `json:"isEncrypted,omitempty"`

	// CanMoveFiles is true when the importer may hardlink or move the files out
	// of the download directory right now.
	// +optional
	CanMoveFiles bool `json:"canMoveFiles,omitempty"`

	// CanBeRemoved is true when the engine has no reason to keep the transfer,
	// e.g. the seed goal is met and the import finished.
	// +optional
	CanBeRemoved bool `json:"canBeRemoved,omitempty"`

	// Message is the engine's latest human-readable note.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message,omitempty"`

	// LastProgressAt is when downloadedBytes last increased; the stall detector
	// works from it.
	// +optional
	LastProgressAt *metav1.Time `json:"lastProgressAt,omitempty"`

	// Import is the import outcome. It is the one cross-service field on this
	// object: importarr's file-import worker, not grabarr, writes it,
	// through server-side apply with its own field manager (k8s.ManagerImportarr),
	// and grabarr never touches it. grabarr reads it to decide when a
	// Download may be removed or blocklisted.
	// +optional
	Import *ImportState `json:"import,omitempty"`

	// Conditions holds Assigned, Downloaded, SeedGoalMet, Imported and Failed.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// Download is one release being transferred by a DownloadClient. It carries an
// ownerReference to the catalog object in spec.target, so deleting the media
// deletes its in-flight downloads, and it is named deterministically as
// "<target>-<sha1(release guid)[:10]>" so a repeated grab of the same release
// for the same target is an idempotent create rather than a duplicate.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=dl,categories=clustarr
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Protocol",type="string",JSONPath=".spec.protocol"
// +kubebuilder:printcolumn:name="Client",type="string",JSONPath=".spec.clientRef"
// +kubebuilder:printcolumn:name="Progress",type="integer",JSONPath=".status.progressPercent"
// +kubebuilder:printcolumn:name="ETA",type="integer",JSONPath=".status.etaSeconds",priority=1
// +kubebuilder:printcolumn:name="Import",type="string",JSONPath=".status.import.state",priority=1
// +kubebuilder:printcolumn:name="Title",type="string",JSONPath=".spec.release.title",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Download struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DownloadSpec   `json:"spec,omitempty"`
	Status DownloadStatus `json:"status,omitempty"`
}

// DownloadList contains a list of Download.
//
// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true
type DownloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Download `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Download{}, &DownloadList{})
}
