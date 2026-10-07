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

// Bounds of the grab entries on an owning item (ADR-0019 §6.2, §6.3),
// mirrored by pkg/crdcheck.TestDownloadEntryBoundsMatchTheCRD. A release
// title takes MaxReleaseTitleLength.
const (
	// MaxEntryMessage caps entry.message and entry.import.message; the
	// transfer record keeps the engine's 2048.
	MaxEntryMessage = 1024
	// MaxOwnerEntries caps status.downloads on Movie, Album, Book and
	// Audiobook.
	MaxOwnerEntries = 8
	// MaxContainerEntries caps status.downloads and status.pendingGrabs on
	// Series and Comic, which hold every episode's or issue's grab.
	MaxContainerEntries = 24
)

// DownloadEntry is one grab of its owner, from the manager's decision to
// grab through seeding and removal. Written only by the owner's item key
// in the remediation loop, under field manager catalogarr (ADR-0019 §6.2).
type DownloadEntry struct {
	// ID is the grab's stable name: k8s.ChildName(target, guid), or
	// k8s.ChildName(target, purpose, guid) for a donor, the name the
	// Download had (adopted entries keep it, so MediaFile
	// spec.importedFrom.downloadRef, history and ui links still resolve).
	// +kubebuilder:validation:MaxLength=253
	ID string `json:"id"`
	// The UIDs here are strings, not types.UID: controller-gen's
	// apply-configuration generator cannot apply MaxLength to types.UID.

	// UID is issued by the manager when it admits the grab (an adopted
	// entry keeps its Download's UID). It keys clustarr-transfers,
	// clustarr-imports, the engine command subject and the clustarr-progress
	// key download.<uid>, so a re-grab under the same ID never reads an old
	// transfer's records.
	// +kubebuilder:validation:MaxLength=36
	UID string `json:"uid"`
	// +optional
	Purpose commonv1.DownloadPurpose `json:"purpose,omitempty"`
	// Episodes names the covered episodes on a Series entry (season and
	// number in the Series' stored order), Issues the covered issue numbers
	// on a Comic entry. Empty on every other owner.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	Episodes []EpisodeNumber `json:"episodes,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	// +kubebuilder:validation:items:MaxLength=16
	Issues []string `json:"issues,omitempty"`

	Release DownloadRelease         `json:"release"`
	Source  commonv1.DownloadSource `json:"source"`
	// Client is the DownloadClient, Engine the instance holding the
	// transfer (<client>-<ordinal>); both pinned once, at admission.
	// +kubebuilder:validation:MaxLength=253
	Client string `json:"client"`
	// +optional
	// +kubebuilder:validation:MaxLength=263
	Engine string `json:"engine,omitempty"`

	GrabbedBy commonv1.GrabSource `json:"grabbedBy"`
	GrabbedAt metav1.Time         `json:"grabbedAt"`
	// +optional
	Manual bool `json:"manual,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`
	// SeedCriteria are the release's (from its Indexer) when it has any;
	// the engine applies its DownloadClient's defaults to what is unset.
	// +optional
	SeedCriteria *commonv1.SeedCriteria `json:"seedCriteria,omitempty"`
	// RemoveOnImport and RemoveDataOnDelete are the effective values,
	// resolved at admission from DownloadClient and RootFolder settings and
	// always written (no default can hide a false).
	RemoveOnImport     bool `json:"removeOnImport"`
	RemoveDataOnDelete bool `json:"removeDataOnDelete"`

	Phase commonv1.DownloadPhase `json:"phase"`
	// +optional
	Stage commonv1.DownloadStage `json:"stage,omitempty"`
	// +optional
	FailureReason commonv1.DownloadFailureReason `json:"failureReason,omitempty"`
	// OutputPath is where the engine put the payload; the manager removes
	// it (fsops.SafeRemove) when the entry's removal asks for data, so it is
	// kept in etcd, not only in the transfer record.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	OutputPath string `json:"outputPath,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	SeedGoalMetAt *metav1.Time `json:"seedGoalMetAt,omitempty"`
	// +optional
	Import *DownloadImportSummary `json:"import,omitempty"`
	// Dispatch fences the engine command: Seq is the last desired state
	// published, AnsweredSeq the transfer record's seq last incorporated
	// (loop spec §2.3, with ADR-0019 §8.3's Delivery). A Pending entry
	// carries seq 1 from creation; nothing is published until Assigned.
	Dispatch Dispatch `json:"dispatch"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// Block is the blocklist write this entry owes the release index
	// (§6.14): set on the Blocklisted transition; ConfirmedAt when the
	// index answered. A Blocklisted entry is dropped only after its transfer
	// is removed and its block confirmed, and then after BlockQuarantine.
	// +optional
	Block *EntryBlock `json:"block,omitempty"`
}

// BlockScope is where a release is blocked (ADR-0019 §6.14).
// +kubebuilder:validation:Enum=item;global
type BlockScope string

// Block scopes.
const (
	// BlockScopeItem blocks the release for this item only.
	BlockScopeItem BlockScope = "item"
	// BlockScopeGlobal blocks the release for every item.
	BlockScopeGlobal BlockScope = "global"
)

// EntryBlock is the blocklist row a Blocklisted entry owes the release
// index (ADR-0019 §6.14).
type EntryBlock struct {
	Scope  BlockScope                     `json:"scope"`
	Reason commonv1.DownloadFailureReason `json:"reason"`
	// Seq fences block and unblock writes on the release index row.
	// +kubebuilder:validation:Minimum=1
	Seq int64 `json:"seq"`
	// +optional
	ConfirmedAt *metav1.Time `json:"confirmedAt,omitempty"`
}

// EpisodeNumber is one episode a Series entry or pending grab covers, in
// the Series' stored order.
type EpisodeNumber struct {
	// +kubebuilder:validation:Minimum=0
	Season int32 `json:"season"`
	// +kubebuilder:validation:Minimum=0
	Number int32 `json:"number"`
}

// DownloadRelease is the capped subset of ReleaseInfo a grab keeps.
// ReleaseInfo itself has no string caps, so it cannot go into status.
type DownloadRelease struct {
	// Title is clamped on a rune boundary; it is for display and the
	// blocklist's title match.
	// +kubebuilder:validation:MaxLength=512
	Title string `json:"title"`
	// GUID is identity: a longer one is refused at admission, never clamped.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	GUID string `json:"guid,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	IndexerRef string `json:"indexerRef,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	IndexerName string `json:"indexerName,omitempty"`
	// +optional
	// +kubebuilder:validation:Pattern=`^([0-9a-f]{40}|[0-9a-f]{64})$`
	InfoHash string            `json:"infoHash,omitempty"`
	Protocol commonv1.Protocol `json:"protocol"`
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// +optional
	Quality commonv1.Quality `json:"quality,omitempty"`
	// +optional
	Revision commonv1.Revision `json:"revision,omitempty"`
	// +optional
	FormatScore int32 `json:"formatScore,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=35
	Languages []string `json:"languages,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	ReleaseGroup string `json:"releaseGroup,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Edition string `json:"edition,omitempty"`
}

// ImportPhase is a grab's import, summarised (ADR-0019 §6.9). Not
// downloadv1alpha1.ImportPhase, which release N+1 removes with Download.
// +kubebuilder:validation:Enum=pending;inspected;approved;imported;blocked;held;expired
type ImportPhase string

// Import phases of a grab entry.
const (
	// ImportPhasePending means the payload awaits its inspect task.
	ImportPhasePending ImportPhase = "pending"
	// ImportPhaseInspected means the inspect record is in and the manager
	// is deciding the plan.
	ImportPhaseInspected ImportPhase = "inspected"
	// ImportPhaseApproved means the plan is decided and the execute task
	// dispatched.
	ImportPhaseApproved ImportPhase = "approved"
	// ImportPhaseImported means every planned file has its MediaFile.
	ImportPhaseImported ImportPhase = "imported"
	// ImportPhaseBlocked means the import cannot proceed: a release fault
	// awaiting its confirming re-inspect, or refused.
	ImportPhaseBlocked ImportPhase = "blocked"
	// ImportPhaseHeld means the import waits for a person, keeping its
	// files for ImportHoldRetention.
	ImportPhaseHeld ImportPhase = "held"
	// ImportPhaseExpired means a held import passed ImportHoldRetention.
	ImportPhaseExpired ImportPhase = "expired"
)

// DownloadImportSummary is the grab's import, summarised from its
// clustarr-imports record (ADR-0019 §6.9); the per-file detail stays in the
// record.
type DownloadImportSummary struct {
	Phase ImportPhase `json:"phase"`
	// +optional
	Class commonv1.ImportRejectionClass `json:"class,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	NextAttemptAt *metav1.Time `json:"nextAttemptAt,omitempty"`
	// +optional
	HeldSince *metav1.Time `json:"heldSince,omitempty"`
	// +optional
	ImportedAt *metav1.Time `json:"importedAt,omitempty"`
	// Files is how many files the approved plan places; the entry reads
	// Imported only when that many MediaFiles name this entry's ID in
	// spec.importedFrom.downloadRef.
	// +optional
	Files int32 `json:"files,omitempty"`
	// Target is the download.clustarr.io/import intent's target,
	// "<kind>/<name>[/<key>]": every later inspect of this import keeps it.
	// Empty for the entry's own target.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Target string `json:"target,omitempty"`
	// Override is the intent's override: the import is a person's, so a
	// suspected sample, an unknown quality and a transcoded file are taken
	// as a manual import takes them (§6.10).
	// +optional
	Override bool `json:"override,omitempty"`
	// Dispatch fences the inspect and execute tasks (§6.9).
	Dispatch Dispatch `json:"dispatch"`
}

// DownloadNonces records the one-shot download intents last handled on
// this owner (ADR-0019 §6.10; loop spec §2.9's rules).
type DownloadNonces struct {
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Remove string `json:"remove,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Resume string `json:"resume,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Import string `json:"import,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Unblock string `json:"unblock,omitempty"`
}

// GrabCandidate is the release a delayed grab will be made from, so it can
// be made from status alone (ADR-0019 §6.2, §6.6).
type GrabCandidate struct {
	Release DownloadRelease         `json:"release"`
	Source  commonv1.DownloadSource `json:"source"`
	// +optional
	Score     int32               `json:"score,omitempty"`
	GrabbedBy commonv1.GrabSource `json:"grabbedBy"`
	// +optional
	Purpose commonv1.DownloadPurpose `json:"purpose,omitempty"`
	// +optional
	Manual bool `json:"manual,omitempty"`
}

// LegacyDownloads is release N's record of the Downloads this owner
// adopted (ADR-0019 §10.2). Release N+1 removes it.
type LegacyDownloads struct {
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=64
	Adopted []LegacyDownload `json:"adopted,omitempty"`
	// Held is "TooMany" while the owner has more live Downloads than its
	// entry cap; nothing is truncated.
	// +optional
	// +kubebuilder:validation:Enum=TooMany
	Held string `json:"held,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
}

// LegacyDownload is one Download release N adopted, blocklisted, dismissed
// or is removing for this owner.
type LegacyDownload struct {
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=36
	UID     string        `json:"uid"`
	Outcome LegacyOutcome `json:"outcome"`
	At      metav1.Time   `json:"at"`
}

// LegacyOutcome is what release N did with an adopted Download.
// +kubebuilder:validation:Enum=adopted;blocklisted;dismissed;removing
type LegacyOutcome string

// Legacy outcomes.
const (
	// LegacyOutcomeAdopted means the Download became an entry.
	LegacyOutcomeAdopted LegacyOutcome = "adopted"
	// LegacyOutcomeBlocklisted means a blocklisted Download became a
	// release-index block.
	LegacyOutcomeBlocklisted LegacyOutcome = "blocklisted"
	// LegacyOutcomeDismissed means a terminal Download was dropped.
	LegacyOutcomeDismissed LegacyOutcome = "dismissed"
	// LegacyOutcomeRemoving means the Download is being torn down.
	LegacyOutcomeRemoving LegacyOutcome = "removing"
)
