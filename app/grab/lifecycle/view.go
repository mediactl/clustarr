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

package lifecycle

import (
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Client is a DownloadClient, as the stage reads it.
type Client struct {
	Name     string
	UID      types.UID
	Protocol commonv1.Protocol
	Enabled  bool
	Priority int32
	Replicas int32
	// RemoveCompleted is torrent spec.torrent.removeCompleted (default true);
	// usenet clients read true.
	RemoveCompleted bool
	// StallTimeout is torrent's (default 24 h) or usenet's (0: never).
	StallTimeout time.Duration
	// DownloadTimeout is usenet's opt-in total time (R21); 0 is none.
	DownloadTimeout time.Duration
	HealthAction    downloadv1alpha1.HealthAction
	// Seed is the client's default seed criteria.
	Seed       *commonv1.SeedCriteria
	Categories map[string]string
	// ResyncSeq is status.resyncSeq: the resync the controller last asked.
	ResyncSeq int64
}

// Engine is an engine instance, from its record.
type Engine struct {
	// Name is "<client>-<ordinal>".
	Name              string
	Client            string
	Ordinal           int32
	Ready, Reattached bool
	BootID            string
	ResyncSeq         int64
	Active, Queued    int32
	// At is the record's write time.
	At time.Time
	// ReattachedAt is when this bootID first reported reattached.
	ReattachedAt time.Time
}

// Transfer is one transfer record and its KV revision.
type Transfer struct {
	Record   schema.TransferRecord
	Revision uint64
}

// Intents is what the owner's download annotations ask (§6.10).
type Intents struct {
	// Paused names the entries a person paused (standing).
	Paused map[string]bool
	// Priority is each named entry's priority (standing).
	Priority map[string]string
	// The one-shots, parsed; nil when the annotation is absent or invalid.
	Remove  *RemoveIntent
	Resume  *ResumeIntent
	Import  *ImportIntent
	Unblock *UnblockIntent
	// Invalid lists the values that did not parse: each is recorded through
	// HandledNonce with a Warning InvalidAnnotation.
	Invalid []InvalidIntent
	// raw is each one-shot annotation's value, for the nonce bookkeeping.
	raw map[string]string
}

// ImportIntent is download.clustarr.io/import.
type ImportIntent struct {
	Nonce, ID string
	Target    *schema.ItemRef
	// TargetKey is the target's optional key (an episode of a pack).
	TargetKey string
	Override  bool
}

// RemoveIntent is download.clustarr.io/remove.
type RemoveIntent struct {
	Nonce, ID       string
	Data, Blocklist bool
}

// ResumeIntent is download.clustarr.io/resume.
type ResumeIntent struct{ Nonce, ID string }

// UnblockIntent is download.clustarr.io/unblock.
type UnblockIntent struct {
	Nonce, InfoHash, Indexer, GUID string
	Global                         bool
}

// InvalidIntent is an annotation value that did not parse.
type InvalidIntent struct{ Annotation, Value, Reason string }

// IssuedCommand is the desired state a seq carried, by hash: the stage's
// leader-local book of what it last commanded each entry, which tells a
// changed standing intent (a priority) from an unchanged one. A missing or
// other-seq entry reads as unknown, and the next live pass issues a new seq
// (one extra command per entry after a leader change).
type IssuedCommand struct {
	Seq  int64
	Hash string
}

// View is everything Decide reads for one owner.
type View struct {
	Owner schema.ItemRef
	// ProviderID is the owner's provider id ("tmdb:603"); ProviderVerified
	// reports that the stage re-read it through the APIReader this pass,
	// which an unclaimed transfer whose claim names another provider id
	// needs before it is removed (§6.7).
	ProviderID       string
	ProviderVerified bool
	Kind             commonv1.MediaKind
	Now              time.Time
	Deleting         bool
	Monitored        bool
	// HasFile is the owner's (A3's default StillWants reads it).
	HasFile  bool
	EntryCap int
	Stored   []catalogv1alpha1.DownloadEntry
	Nonces   catalogv1alpha1.DownloadNonces
	Intents  Intents
	// NewEntries are Pending entries from the grab stage (A4) or a manual
	// pick.
	NewEntries []catalogv1alpha1.DownloadEntry
	// Transfers are the stored entries' records, by entry uid.
	Transfers map[types.UID]Transfer
	// Unclaimed are records naming this owner whose entry is not in Stored
	// and that read claimed: false.
	Unclaimed []Transfer
	Clients   []Client
	Engines   map[string]Engine
	// Files counts the MediaFiles naming each entry id in
	// spec.importedFrom.downloadRef.
	Files map[string]int
	// Import is importplan's decision per Completed entry (A3.8).
	Import map[types.UID]ImportDecision
	// Blocks are the block book's replies by entry uid (R9).
	Blocks map[types.UID]BlockReply
	// Unblocked are the book's confirmed unblock nonces.
	Unblocked map[string]bool
	// Searched is each covered item's searchDispatch.dispatchedAt (R25).
	Searched map[schema.ItemRef]time.Time
	// Covered is each entry's covered items (Episodes, Issues), by entry
	// uid; absent means the owner itself.
	Covered map[types.UID][]schema.ItemRef
	// Removed are the output paths the stage's SafeRemove removed.
	Removed map[string]bool
	// Issued is the stage's command book.
	Issued map[types.UID]IssuedCommand
	// StillWants is the grab planner's (A4); nil is A3's default.
	StillWants func(schema.TransferClaim) (bool, string)
	// IndexerClient is each Indexer's spec.downloadClientRef.
	IndexerClient map[string]string
}

// Command is one engine command the plan owes. Subject and MsgID are the
// stage's to render (events.WorkEngineSubject and MsgIDForEngineCommand, or
// MsgIDForEngineCommandAfterBoot with BootID when it is set).
type Command struct {
	Engine  string
	Client  string
	Ordinal int32
	// BootID is set when the Msg-Id names the engine's boot (a resync or a
	// re-add after a restart).
	BootID  string
	Subject string
	MsgID   string
	Cmd     schema.EngineCommand
	// Retry marks a republish past RepublishWindow (Event CommandRetrying).
	Retry bool
}

// BlockCall is one owed blocklist RPC: a block for an entry, or an unblock
// for a nonce.
type BlockCall struct {
	EntryUID types.UID
	Nonce    string
	Req      schema.BlocklistRequest
}

// Plan is Decide's answer.
type Plan struct {
	Entries  []catalogv1alpha1.DownloadEntry
	Phase    commonv1.DownloadPhase
	Nonces   catalogv1alpha1.DownloadNonces
	Commands []Command
	Blocks   []BlockCall
	// Removals are output paths to remove before their entries are dropped.
	Removals []string
	// Imports are the inspect and execute tasks owed (A3.8).
	Imports     []ImportDispatch
	Materialise []Materialise
	// History are published on clustarr.evt.download.download.<action>.<uid>.
	History []schema.DownloadEvent
	Events  []Event
	// Redownloads are the items a Failed or Blocklisted entry asks to
	// search (R25).
	Redownloads []schema.ItemRef
	// DirectGrabs are the Assigned edges of direct-source grabs (§6.11).
	DirectGrabs []DirectGrab
	// Finalizer: true ensures FinalizerTransfers, false removes it, nil
	// leaves it.
	Finalizer *bool
	// Issued is the command book's update.
	Issued map[types.UID]IssuedCommand
	Due    time.Time
}

// ImportVerdict is importplan's outcome for one entry.
type ImportVerdict string

// The import verdicts.
const (
	ImportNone         ImportVerdict = "none"
	ImportImported     ImportVerdict = "imported"
	ImportReleaseFault ImportVerdict = "releaseFault"
	ImportHeld         ImportVerdict = "held"
	ImportExpired      ImportVerdict = "expired"
	ImportRetry        ImportVerdict = "retry"
)

// ImportDispatch is one inspect or execute task owed; Subject and MsgID are
// the stage's to render.
type ImportDispatch struct {
	EntryUID types.UID
	Phase    string // schema.ImportSubInspect | schema.ImportSubExecute
	Seq      int64
	Subject  string
	MsgID    string
	Task     any // *schema.ImportInspectTask or *schema.ImportExecuteTask
	// Republish marks a republish of an unanswered task under its Msg-Id.
	Republish bool
}

// ApplyMediaFile materialises one placed file's MediaFile spec.
type ApplyMediaFile struct {
	Name      string
	RV        string // "" to create
	Ref       commonv1.MediaRef
	Path      string
	SizeBytes int64
	ModTime   time.Time
	Frozen    schema.FrozenFields
	From      catalogv1alpha1.ImportSource
}

// AudioGraftApply applies a donor's AudioGraft (R26, until F7.1).
type AudioGraftApply struct {
	Owner                      schema.ItemRef
	DonorPath, Anchor, Default string
}

// Materialise is one MediaFile apply, one replaced MediaFile's delete, or
// one AudioGraft apply.
type Materialise struct {
	Apply      *ApplyMediaFile
	Delete     *schema.MediaFileBasis
	AudioGraft *AudioGraftApply
}

// ImportDecision is importplan's decision for one Completed entry.
type ImportDecision struct {
	Summary     catalogv1alpha1.DownloadImportSummary
	Dispatch    *ImportDispatch
	Materialise []Materialise
	Verdict     ImportVerdict
	Class       commonv1.ImportRejectionClass
	// Due is when the import next needs a pass (a retry, a hold's expiry).
	Due time.Time
}

// BlockReply is the release index's answer to a block or unblock call.
type BlockReply struct {
	Seq            int64
	Applied, Stale bool
	At             time.Time
}

// DirectGrab is a direct-source grab to count into its Indexer's ring.
type DirectGrab struct {
	IndexerRef string
	EntryUID   types.UID
	At         time.Time
}

// Event is one Kubernetes Event the plan owes.
type Event struct {
	Recorder, Type, Reason, Message string
	// On is nil for the owner; a DownloadClient ref for TransferOwnerGone.
	On *schema.ItemRef
}
