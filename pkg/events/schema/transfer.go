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

// Bounds of what an engine reports (ADR-0019 §6.7). The writers clamp to
// them (A3.6); this package only declares them.
const (
	// MaxTransferMessage caps TransferRecord.Message; the entry keeps 1024.
	MaxTransferMessage = 2048
	// MaxTransferFilesBytes is the JSON budget of TransferRecord.Files;
	// past it the list is cut and FilesTruncated set.
	MaxTransferFilesBytes = 384 << 10
	// MaxUnidentified caps EngineRecord.Unidentified; past it only
	// UnidentifiedCount grows.
	MaxUnidentified = 64
)

// EngineCommand desired states (ADR-0019 §6.7).
const (
	// EngineDesiredPresent: the transfer exists in the state the command
	// describes.
	EngineDesiredPresent = "present"
	// EngineDesiredAbsent: the transfer is removed, its data too when
	// RemoveData.
	EngineDesiredAbsent = "absent"
	// EngineDesiredResync: rewrite every transfer record, then report
	// ResyncSeq in the engine record.
	EngineDesiredResync = "resync"
)

// TransferRecord states (RecordHeader.State on clustarr-transfers).
const (
	TransferStatePresent = "present"
	TransferStateRemoved = "removed"
	TransferStateFailed  = "failed"
)

// EngineRecord.ProxyUDP values.
const (
	ProxyUDPAvailable     = "available"
	ProxyUDPUnavailable   = "unavailable"
	ProxyUDPNotApplicable = "n/a"
)

// TransferClaim is what a transfer was added under (ADR-0019 §6.7,
// "Claims"): kept in the engine's journal and in its transfer record, and
// carried by every command, so the manager can converge a transfer no entry
// claims to its owner's state.
type TransferClaim struct {
	// Owner is the item that held the entry, UID included.
	Owner ItemRef `json:"owner"`
	// ProviderID is the provider id the owner had at grab time ("tmdb:603"),
	// so an etcd restore's new UID is still recognised as the same item.
	ProviderID string   `json:"providerID,omitempty"`
	Entry      EntryRef `json:"entry"`
	// The release's identity.
	InfoHash string                   `json:"infoHash,omitempty"`
	Indexer  string                   `json:"indexer,omitempty"`
	GUID     string                   `json:"guid,omitempty"`
	Title    string                   `json:"title,omitempty"`
	Purpose  commonv1.DownloadPurpose `json:"purpose,omitempty"`
	// RemoveDataOnDelete decides the data of a transfer whose owner is gone.
	RemoveDataOnDelete bool `json:"removeDataOnDelete"`
	// Imported is true once the entry's import finished.
	Imported bool `json:"imported,omitempty"`
}

// EpisodeNo is one episode, season and number, in the Series' stored order.
type EpisodeNo struct {
	Season int32 `json:"season"`
	Number int32 `json:"number"`
}

// TransferSelection is what of a pack the engine fetches: the covered
// episodes (or absolute numbers, or air dates) or issues. It replaces the
// engine's Episode read through the APIReader (ADR-0019 §6.7).
type TransferSelection struct {
	Episodes  []EpisodeNo `json:"episodes,omitempty"`
	Absolutes []int32     `json:"absolutes,omitempty"`
	AirDates  []string    `json:"airDates,omitempty"`
	Issues    []string    `json:"issues,omitempty"`
}

// CommandRelease is the release an engine command or an import task names.
type CommandRelease struct {
	Title      string `json:"title"`
	GUID       string `json:"guid,omitempty"`
	IndexerRef string `json:"indexerRef,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
}

// EngineCommand is a transfer's whole desired state at a seq, from the
// manager to the one engine instance holding it (ADR-0019 §6.7). Subject
// events.WorkEngineSubject (or the resync and unidentified-removal forms),
// Msg-Id events.MsgIDForEngineCommand. The engine applies it only above the
// seq its journal holds for the entry, and acks once its transfer record
// shows the new seq.
type EngineCommand struct {
	Seq   int64    `json:"seq"`
	Owner ItemRef  `json:"owner"`
	Entry EntryRef `json:"entry"`
	// Desired is EngineDesiredPresent, EngineDesiredAbsent or
	// EngineDesiredResync.
	Desired string `json:"desired"`
	// RemoveData removes the payload with an absent transfer.
	RemoveData       bool                     `json:"removeData,omitempty"`
	Protocol         commonv1.Protocol        `json:"protocol,omitempty"`
	Source           *commonv1.DownloadSource `json:"source,omitempty"`
	ExpectedInfoHash string                   `json:"expectedInfoHash,omitempty"`
	Release          CommandRelease           `json:"release"`
	// Category is the owner kind's download category.
	Category  string             `json:"category,omitempty"`
	Selection *TransferSelection `json:"selection,omitempty"`
	// Paused and Priority are the owner's standing intents in force.
	Paused   bool                      `json:"paused,omitempty"`
	Priority commonv1.DownloadPriority `json:"priority,omitempty"`
	// HealthOverride is the resume nonce that releases a health hold.
	HealthOverride string                 `json:"healthOverride,omitempty"`
	SeedCriteria   *commonv1.SeedCriteria `json:"seedCriteria,omitempty"`
	RemoveOnImport bool                   `json:"removeOnImport"`
	Imported       bool                   `json:"imported,omitempty"`
	ImportedAt     *time.Time             `json:"importedAt,omitempty"`
	Claim          TransferClaim          `json:"claim"`
	// ResyncSeq is the resync asked for, on a resync command.
	ResyncSeq int64 `json:"resyncSeq,omitempty"`
	// DownloadID addresses an unidentified transfer to remove.
	DownloadID string    `json:"downloadID,omitempty"`
	IssuedAt   time.Time `json:"issuedAt"`
}

// Schema implements Payload.
func (EngineCommand) Schema() string { return "download.EngineCommand.v1" }

// TransferFile is one file within a transfer, relative to its content root.
type TransferFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"`
}

// TransferHealth reports article availability for a usenet transfer, in
// whole percentages.
type TransferHealth struct {
	HealthPercent         int32 `json:"healthPercent,omitempty"`
	CriticalHealthPercent int32 `json:"criticalHealthPercent,omitempty"`
	FailedArticles        int32 `json:"failedArticles,omitempty"`
	TotalArticles         int32 `json:"totalArticles,omitempty"`
}

// TransferRecord is clustarr-transfers' value for one entry, keyed
// events.RecordKey(entry uid), written only by the engine holding the
// transfer (ADR-0019 §6.7): Item is the owner, Seq the last command applied,
// State TransferState*. It carries every engine field of §6.5. The engine
// writes it on a state or stage change, on a command, at most once a minute
// for counters, and at least once a day while the transfer lives.
type TransferRecord struct {
	RecordHeader
	Entry EntryRef `json:"entry"`
	// Claim is the claim the transfer was added under; nil for a
	// pre-journal transfer, which holds only its Download name.
	Claim *TransferClaim `json:"claim,omitempty"`
	// Claimed is true once a command named the entry since the engine's
	// boot.
	Claimed bool `json:"claimed"`
	// Engine is "<client>-<ordinal>"; BootID the engine's boot.
	Engine     string                 `json:"engine"`
	BootID     string                 `json:"bootID"`
	DownloadID string                 `json:"downloadID,omitempty"`
	Stage      commonv1.DownloadStage `json:"stage,omitempty"`
	OutputPath string                 `json:"outputPath,omitempty"`
	// ContentRoot is the directory Files are relative to.
	ContentRoot string         `json:"contentRoot,omitempty"`
	Files       []TransferFile `json:"files,omitempty"`
	// FilesTruncated is true when Files was cut to MaxTransferFilesBytes.
	FilesTruncated  bool            `json:"filesTruncated,omitempty"`
	TotalBytes      int64           `json:"totalBytes,omitempty"`
	RemainingBytes  int64           `json:"remainingBytes,omitempty"`
	DownloadedBytes int64           `json:"downloadedBytes,omitempty"`
	UploadedBytes   int64           `json:"uploadedBytes,omitempty"`
	ProgressPercent int32           `json:"progressPercent,omitempty"`
	RatioMilli      int32           `json:"ratioMilli,omitempty"`
	SeedTimeSeconds int64           `json:"seedTimeSeconds,omitempty"`
	Seeders         int32           `json:"seeders,omitempty"`
	Peers           int32           `json:"peers,omitempty"`
	Health          *TransferHealth `json:"health,omitempty"`
	IsEncrypted     bool            `json:"isEncrypted,omitempty"`
	CanMoveFiles    bool            `json:"canMoveFiles,omitempty"`
	CanBeRemoved    bool            `json:"canBeRemoved,omitempty"`
	SeedGoalReached bool            `json:"seedGoalReached,omitempty"`
	HealthPaused    bool            `json:"healthPaused,omitempty"`
	// EngineFailureReason is the engine's report, not the entry's verdict.
	EngineFailureReason commonv1.DownloadFailureReason `json:"engineFailureReason,omitempty"`
	// Message is at most MaxTransferMessage bytes.
	Message        string     `json:"message,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	LastProgressAt *time.Time `json:"lastProgressAt,omitempty"`
	AddedAt        time.Time  `json:"addedAt"`
	// DataRemoved is true once an absent command's data removal finished.
	DataRemoved bool `json:"dataRemoved,omitempty"`
}

// Schema implements Payload and is RecordHeader.Schema on every value; it
// shadows the promoted field, which pkg/records reaches through Header().
func (TransferRecord) Schema() string { return "download.Transfer.v1" }

// UnidentifiedTransfer is a transfer an engine holds with no claim
// (ADR-0019 §6.7): added by hand, an unreadable journal entry, or a
// pre-journal transfer no Download names.
type UnidentifiedTransfer struct {
	DownloadID string    `json:"downloadID"`
	Name       string    `json:"name,omitempty"`
	AddedAt    time.Time `json:"addedAt"`
	SizeBytes  int64     `json:"sizeBytes,omitempty"`
}

// EngineRecord is clustarr-engines' value for one engine instance, keyed
// events.RecordSubKey(DownloadClient uid, "<ordinal>"), written only by that
// engine pod at start, on change and every 60 s (ADR-0019 §6.7): Item is the
// DownloadClient, Sub the ordinal, Seq monotone per write, State "present".
type EngineRecord struct {
	RecordHeader
	Client  string `json:"client"`
	Ordinal int32  `json:"ordinal"`
	Pod     string `json:"pod"`
	BootID  string `json:"bootID"`
	Version string `json:"version,omitempty"`
	Ready   bool   `json:"ready"`
	// Reattached is true once the engine re-attached every journalled
	// transfer and rewrote its record at BootID.
	Reattached bool `json:"reattached"`
	// ResyncSeq is the last resync the engine finished.
	ResyncSeq        int64 `json:"resyncSeq,omitempty"`
	Active           int32 `json:"active,omitempty"`
	Queued           int32 `json:"queued,omitempty"`
	Seeding          int32 `json:"seeding,omitempty"`
	ScratchFreeBytes int64 `json:"scratchFreeBytes,omitempty"`
	PublishFreeBytes int64 `json:"publishFreeBytes,omitempty"`
	// ProxyUDP is ProxyUDPAvailable, ProxyUDPUnavailable or
	// ProxyUDPNotApplicable.
	ProxyUDP string `json:"proxyUDP,omitempty"`
	// Unidentified lists at most MaxUnidentified transfers with no claim;
	// UnidentifiedCount counts them all.
	Unidentified      []UnidentifiedTransfer `json:"unidentified,omitempty"`
	UnidentifiedCount int32                  `json:"unidentifiedCount,omitempty"`
	At                time.Time              `json:"at"`
}

// Schema implements Payload and is RecordHeader.Schema on every value.
func (EngineRecord) Schema() string { return "download.Engine.v1" }
