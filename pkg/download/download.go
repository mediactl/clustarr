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

// Package download is the seam between grabarr's controllers and the two
// transfer engines: BitTorrent (anacrolix, D2-1) and usenet (NNTP + yEnc +
// PAR2, D2-2). [Client] is the whole contract, and [ApplyStatus] is the one
// mapping from an engine's observation onto the fields
// k8s.ManagerGrabarrEngine owns on Download.status.
//
// # The interface is deliberately total
//
// Spec §7 sketches Client as ten methods and every one of them is here, with
// one added since (SetPriority, for a priority edited after the Add). The
// rule that kept it that size is that no method may exist which only one
// protocol can honour: the Download controller calls Client without knowing
// which engine is behind it, so a method a usenet client had to refuse would
// turn a protocol difference into a runtime error path in the controller.
//
// The three places the protocols genuinely differ are handled by saying what
// the other side does, not by splitting the interface:
//
//   - Seed criteria. [Client.SetSeedCriteria] is a no-op returning nil on a
//     usenet client. A usenet transfer has no seeding phase, so "stop when
//     this goal is met" is vacuously already met; returning an error instead
//     would make the controller branch on protocol for a call whose outcome
//     it does not use.
//   - Article health. [Item.Health] is nil for every torrent Item and
//     non-nil for a usenet one from the moment the NZB is parsed. It is a
//     pointer for exactly that reason: "this protocol has no such concept"
//     and "0% of articles available" must not be the same value.
//   - PAR2 repair and unpacking. These are stages inside the usenet client,
//     not methods: they surface as [Item.Stage] values
//     (DownloadStageRepairing, DownloadStageExtracting) and, on failure, as
//     [Item.FailureReason] = missingArticles or encrypted. A torrent client
//     simply never reports those stages.
//
// Two further asymmetries are values rather than methods, and are listed on
// [Item] where they are read: the torrent-only counters (UploadedBytes,
// UpRate, RatioMilli, SeedTime, Seeders, Peers) are zero for usenet.
//
// # What this package does not decide
//
// It does not decide Download.status.phase. [Item.Status] is the engine's own
// view of a transfer; the controller maps it onto DownloadPhase, because the
// phase enum is a pinned contract shared with catalogarr's rollup overlay
// (plan ruling R1) and an engine must not be able to move an object through
// it. [ApplyStatus] therefore never emits phase, conditions, or any other
// field in k8s.ManagerGrabarr's owned set -- see app/grab/status for the
// declaration of that split.
package download

import (
	"context"
	"errors"
	"time"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// ErrNotFound is returned by [Client.Get], [Client.Pause], [Client.Resume],
// [Client.SetPriority], [Client.SetSeedCriteria], [Client.MarkImported] and
// [Client.Remove] when the client has no transfer with that id.
//
// It is a sentinel because callers must distinguish it with errors.Is rather
// than by string. The Download controller reads it as "this engine restarted
// and has not re-attached this transfer", which is a retry, not a failure;
// the finalizer reads it as "already gone", which lets a Remove be idempotent.
var ErrNotFound = errors.New("download: no such transfer")

// ErrPayloadMismatch is returned by [Client.Add] when the resolved payload
// does not match [AddRequest.ExpectedInfoHash]. It is separated from a generic
// error because it means the indexer served different content than the grab
// decision was made from, which is a blocklist-worthy event rather than a
// transient one.
var ErrPayloadMismatch = errors.New("download: payload does not match the expected info hash")

// Status is the engine's own view of a transfer.
//
// It is deliberately NOT DownloadPhase. DownloadPhase is the object's
// lifecycle, owned by the grabarr controller and shared with catalogarr's
// rollup overlay; this is the six-way state a download client reports, which
// the controller combines with the object's own history (has it been
// assigned? has the import finished? is it blocklisted?) to compute a phase.
type Status string

// Transfer states, from spec §7.
const (
	// StatusQueued means the client accepted the transfer but is not moving
	// bytes for it yet.
	StatusQueued Status = "Queued"
	// StatusPaused means the transfer is suspended and will not resume
	// without a [Client.Resume].
	StatusPaused Status = "Paused"
	// StatusDownloading means bytes are moving, or the client is actively
	// trying to move them.
	StatusDownloading Status = "Downloading"
	// StatusCompleted means every wanted byte is on disk and verified. For a
	// torrent it says nothing about seeding, which continues afterwards.
	StatusCompleted Status = "Completed"
	// StatusFailed means the transfer cannot make progress; see
	// [Item.FailureReason].
	StatusFailed Status = "Failed"
	// StatusWarning means the transfer is still running but something needs
	// attention -- stalled peers, a provider refusing articles -- and
	// [Item.Message] says what.
	StatusWarning Status = "Warning"
)

// File is one file inside a transfer. It mirrors
// downloadv1alpha1.DownloadFile field for field, because [ApplyStatus] copies
// it straight across and a field that existed here but not there would be
// silently dropped on every write.
type File struct {
	// Path is the file's path relative to [Item.ContentRoot]. It is the list
	// key on status.files, so it must be non-empty and unique within an Item:
	// the CRD marks it required with MinLength=1, and an empty one fails the
	// whole status apply rather than just that entry.
	Path string

	// SizeBytes is the file's size.
	SizeBytes int64

	// Skipped is true when the engine was told not to fetch this file -- an
	// unwanted episode inside a season pack ([AddRequest.WantFile]).
	Skipped bool
}

// Item is one transfer as the client currently sees it. It is a snapshot: a
// client returns a copy and never a live view, so a caller may hold one across
// a slow status apply without racing the engine.
//
// Every field here maps onto a Download.status field that
// k8s.ManagerGrabarrEngine owns, with two exceptions. [Item.Status] is never
// persisted: the controller derives status.phase from the persisted
// telemetry instead, and an engine that wrote a phase would be a second
// writer of the controller's owned set. [Item.AddedAt] is never persisted
// either: it is the engine's own bookkeeping, read by its orphan reaper.
//
// Two fields are reports the controller turns into verdicts. [Item.FailureReason]
// is persisted as status.engineFailureReason and [Item.SeedGoalMet] as
// status.seedGoalReached; the controller reads those and writes
// status.failureReason, status.seedGoalMetAt, the phase and the conditions
// under its own manager (gap fix Y2). Before that the engine had no channel
// for either, so the controller could see no failure but encrypted and never
// set SeedGoalMet at all.
//
// # Torrent-only and usenet-only fields
//
// UploadedBytes, UpRate, RatioMilli, SeedTime, Seeders and Peers are zero on
// every usenet Item; a usenet transfer uploads nothing and has no swarm.
// Health is nil on every torrent Item. Nothing else is protocol-conditional.
type Item struct {
	// ID is the client's own handle for the transfer: the 40-hex info hash
	// for a torrent, the NZB id for usenet. It is what
	// [Client.Get]/[Client.Remove] take and what lands in status.downloadID.
	ID string

	// Status is the engine's view of the transfer.
	Status Status

	// Stage is the fine-grained step within that view. The enum is the CRD's,
	// not a free string: a value outside it is rejected by the apiserver on
	// every subsequent telemetry write, so typing it here turns a runtime
	// apply failure into a compile error.
	Stage downloadv1alpha1.DownloadStage

	// FailureReason is why the transfer failed, machine-readable. A client
	// sets it only together with Status == [StatusFailed], and keeps it
	// there: a failed transfer stays failed. [ApplyStatus] persists it as
	// status.engineFailureReason, the engine's report; the CONTROLLER turns
	// that into status.failureReason, which sits in its owned set alongside
	// the phase it justifies. Empty and DownloadFailureNone both mean "not
	// failed".
	FailureReason downloadv1alpha1.DownloadFailureReason

	// TotalBytes is the size of the wanted content -- the selected files, not
	// the whole torrent.
	TotalBytes int64

	// RemainingBytes is how many wanted bytes are still missing.
	RemainingBytes int64

	// DownloadedBytes is how many wanted bytes are on disk and verified. A
	// torrent re-attached after an engine restart re-verifies what it had,
	// so this survives the restart without being persisted.
	DownloadedBytes int64

	// UploadedBytes is how many bytes have been uploaded. Torrent only. It is
	// cumulative across engine restarts: a caller that persists it and hands
	// it back through [AddRequest.SeedHistory] gets a client that counts on
	// from it rather than from zero.
	UploadedBytes int64

	// DownRate is the current download rate in bytes per second.
	DownRate int64

	// UpRate is the current upload rate in bytes per second. Torrent only.
	UpRate int64

	// ETA is the estimated time until the transfer completes, or nil when the
	// engine cannot estimate one -- no rate yet, or a stalled transfer.
	//
	// It is truncated to whole seconds on the way to status.etaSeconds, which
	// is the CRD's own resolution.
	ETA *time.Duration

	// ProgressPercent is the share of wanted bytes fetched, 0-100.
	//
	// Spec §7 sketched this as a float64 fraction. It is a scaled integer
	// here because its only destination is status.progressPercent, which is
	// an int32 whose own comment says telemetry uses scaled integers and
	// never floating point. Carrying a float in between would add a rounding
	// step, and a rounding step is somewhere the two representations can
	// disagree.
	ProgressPercent int32

	// RatioMilli is uploadedBytes/downloadedBytes in thousandths, so 1.5 is
	// 1500. Torrent only. It is scaled for the same reason as
	// ProgressPercent, and to compare directly against
	// spec.seedCriteria.ratio without either side parsing a float.
	RatioMilli int32

	// SeedTime is how long the torrent has been seeding, cumulative across
	// engine restarts in the same way as UploadedBytes. Torrent only.
	SeedTime time.Duration

	// Seeders is the number of seeders the engine currently sees. Torrent only.
	Seeders int

	// Peers is the number of connected peers. Torrent only.
	Peers int

	// Health reports article availability. Usenet only; nil for a torrent.
	//
	// It is the CRD's own type rather than a copy: all four of its fields
	// reach status.health, server-side apply tracks ownership per leaf inside
	// that struct, and a local mirror that fell one field behind would
	// release the missing leaf on every write.
	Health *downloadv1alpha1.UsenetHealth

	// OutputPath is where the finished content was published -- the directory
	// or single file the importer is pointed at. It is the path the Download
	// controller's removeDataOnDelete finalizer removes, so it must be set
	// for as long as the transfer has anything on the shared data volume.
	//
	// A torrent downloads in place, so its OutputPath is its per-transfer
	// directory from the moment it is added. A usenet transfer assembles in
	// node-local scratch and publishes by atomic rename, so its OutputPath is
	// empty until that rename.
	OutputPath string

	// ContentRoot is the directory [File.Path] entries are relative to.
	ContentRoot string

	// Files lists the files in the transfer, including skipped ones.
	//
	// status.files is a listType=map keyed on path, so server-side apply
	// tracks ownership per ENTRY: an Item that stops listing a file releases
	// that entry and the apiserver removes it. That is the wanted behaviour
	// -- a file that left the transfer should leave status -- but it means an
	// Item must always carry the complete list, never a delta.
	Files []File

	// CanMoveFiles is true when the importer may hardlink or move the files
	// out of the download directory right now. A usenet transfer sets it as
	// soon as the atomic rename into the data directory is done; a torrent
	// sets it only when the engine no longer needs the files in place.
	CanMoveFiles bool

	// CanBeRemoved is true when the engine has no reason to keep the
	// transfer: the import finished and, for a torrent, the seed goal is met.
	CanBeRemoved bool

	// SeedGoalMet is true once a torrent has satisfied its seed criteria
	// (ratio, seed time or inactive seeding time -- the three limits Sonarr's
	// qBittorrent client checks), whether or not it has been imported. Unlike
	// CanBeRemoved it says nothing about the import, which is what lets the
	// controller record the goal on its own. Always false for usenet, which
	// never seeds.
	SeedGoalMet bool

	// IsEncrypted is true when the content turned out to be password
	// protected. Usenet sets it when extraction hits an encrypted archive.
	IsEncrypted bool

	// HealthPaused is true while a usenet client holds the transfer paused
	// because its article health crossed the floor under the pause health
	// action (DownloadClient spec.usenet.healthAction, NZBGet's
	// HealthCheck=pause). Status is [StatusPaused] with it, and Message says
	// why. It is distinct from a [Client.Pause] so a level-driven caller can
	// tell the two apart: [Client.Resume] leaves a health pause in place,
	// and [Client.Pause] turns it into an ordinary pause whose Resume carries
	// on without the health check -- the operator's "continue anyway".
	// Always false for a torrent.
	HealthPaused bool

	// Message is the engine's latest human-readable note.
	Message string

	// LastProgressAt is when DownloadedBytes last increased.
	//
	// Spec §7 did not sketch it, and it is here because status.lastProgressAt
	// is in the engine's owned set and nothing else can supply it: it is not
	// a property of a point-in-time observation but of the sequence, so only
	// the engine -- which already keeps per-transfer bookkeeping to compute
	// rates -- knows it. If [ApplyStatus] could not emit it, every telemetry
	// write would RELEASE status.lastProgressAt and the stall detector would
	// lose its reference on the first tick after the value was set.
	LastProgressAt *time.Time

	// AddedAt is when this transfer was first added to the client, carried
	// across engine restarts: a client that persists its own state (usenet)
	// restores it from there, and one its caller re-attaches (torrent) takes
	// it back from [AddRequest.AddedAt]. Zero means the client does not know.
	//
	// It is a true transfer age, which is what an engine's orphan reaper
	// measures its grace period against. A first-seen time kept in the
	// reaper's own memory is not: every restart would re-extend every
	// orphan's window, so a crash-looping engine could defer reaping
	// indefinitely.
	//
	// It is not written to Download.status -- see the type's doc comment.
	AddedAt time.Time
}

// Info is what a client reports about itself.
//
// Spec §7 names Info in the Client interface but never defines the type; this
// is the definition, and it is shaped by its only consumer. The DownloadClient
// controller (D2-3) renders it into DownloadClientStatus, so every field here
// corresponds to one there: FreeBytes feeds the DiskSpaceOK condition,
// Down/UpRateBps feed the aggregate rates, and the three counts feed
// status.active/queued/seeding.
type Info struct {
	// Protocol is the protocol this client speaks. The controller asserts it
	// against DownloadClient.spec.protocol rather than trusting the wiring.
	Protocol commonv1alpha1.Protocol

	// Implementation names the engine behind the interface, e.g. "anacrolix"
	// or "nntp". It is for logs and status messages only; nothing branches on
	// it, and it is never a metric label.
	Implementation string

	// Version is the engine's version string, or empty when it has none.
	Version string

	// FreeBytes is the free space on the volume the client writes to, from
	// statfs. Zero means "not known", which the controller reports as
	// DiskSpaceOK=Unknown rather than as a full disk.
	FreeBytes int64

	// DownloadRateBps and UploadRateBps are the client's aggregate rates
	// across every transfer it holds.
	DownloadRateBps int64
	UploadRateBps   int64

	// Active, Queued and Seeding count the client's transfers by state. They
	// are the client's own counts rather than a controller-side tally over
	// Download objects, because a re-attaching engine holds transfers the
	// controller has not yet seen.
	Active  int32
	Queued  int32
	Seeding int32
}

// AddRequest is everything a client needs to start one transfer.
//
// The payload is bytes or a magnet URI and never a URL: §4.4 keeps .torrent
// and .nzb bodies off the Download object entirely, and grabarr resolves
// spec.source through indexarr -- which applies the indexer's auth, rate limit
// and proxy -- before it ever reaches an engine. A client that fetched a URL
// itself would be a second, uncredentialed indexer client.
type AddRequest struct {
	// Name is the Download object's name. The engine uses it as the
	// per-transfer directory name under its category, so it must be a valid
	// path element; object names already are.
	Name string

	// Magnet is a magnet URI. Exactly one of Magnet and Payload is set.
	Magnet string

	// Payload is the .torrent or .nzb body. Exactly one of Magnet and Payload
	// is set.
	Payload []byte

	// ExpectedInfoHash, when non-empty, is the info hash the resolved torrent
	// must have; a mismatch is [ErrPayloadMismatch] and nothing is added.
	// Torrent only -- usenet has no equivalent identity to pin.
	ExpectedInfoHash string

	// Category is the subdirectory transfers of this media kind go in, from
	// DownloadClient.spec.categories. Empty means the media kind's own name.
	Category string

	// Paused starts the transfer suspended, for spec.paused on a new
	// Download.
	Paused bool

	// Priority orders this transfer within the client's queue.
	//
	// The design spec gives spec.priority no semantics beyond its enum; the
	// API type's own doc does ("high jumps the queue", "low runs only when
	// the engine is otherwise idle"), and each client honours it with the
	// lever its protocol has. A usenet client has a queue in all but name --
	// every job competes for one pool of provider connections -- so it
	// transfers strictly by priority class. A torrent client has no queue at
	// all (anacrolix runs every torrent at once, and a peerless torrent
	// consumes nothing), so it maps the class onto each torrent's share of
	// peer connections instead; see pkg/download/torrent.
	Priority downloadv1alpha1.DownloadPriority

	// SeedCriteria is the seed goal for this transfer, already merged from
	// the client default and the Download's override. Nil means the client
	// default. Ignored by a usenet client, for the reason [Client] gives for
	// SetSeedCriteria.
	SeedCriteria *commonv1alpha1.SeedCriteria

	// WantFile selects which files of a multi-file transfer to fetch, so a
	// season pack grabbed for one missing episode downloads that episode
	// rather than the whole season. It is called once per file, with the
	// path [Item.Files] will report for it, once the client knows the file
	// list -- for a magnet, only after its metadata arrives, which is why
	// this is a predicate and not a list of paths.
	//
	// Nil wants every file. Selection only ever narrows: a WantFile that
	// rejects EVERY file is ignored and the whole transfer is wanted, because
	// a transfer that wants nothing would report Completed with nothing on
	// disk. Files it rejects are still listed, with [File.Skipped] set, and
	// [Item.TotalBytes]/[Item.RemainingBytes]/[Item.ProgressPercent] measure
	// the wanted files only.
	//
	// Torrent only. A usenet release is a set of archive volumes and PAR2
	// files, none of which maps to one episode, so a usenet client ignores
	// it.
	WantFile FileSelector

	// AddedAt, when non-zero, is when this transfer was FIRST added -- a
	// caller re-attaching a transfer after an engine restart passes back the
	// [Item.AddedAt] it persisted, so the restart does not reset the
	// transfer's age. Zero means now. Ignored when the transfer already
	// exists (Add is idempotent and changes nothing about it).
	AddedAt time.Time

	// SeedHistory, when non-nil, is the seeding a torrent had already done
	// before an engine restart -- a caller re-attaching a transfer passes
	// back the [Item.UploadedBytes], [Item.SeedTime] and [Item.SeedGoalMet]
	// it persisted. The client counts on from those figures, so a restart
	// neither resets the ratio and seed time towards the goal nor makes a
	// torrent whose goal was already met upload again (design spec §6.3's
	// "persisted cumulative counters"). Nil means none. Ignored by a usenet
	// client, which never seeds, and when the transfer already exists.
	SeedHistory *SeedHistory
}

// SeedHistory is a torrent's seeding so far; see [AddRequest.SeedHistory].
type SeedHistory struct {
	// UploadedBytes is the upload counted before the restart.
	UploadedBytes int64
	// SeedTime is the seeding time counted before the restart.
	SeedTime time.Duration
	// GoalMet is true when the seed goal had already been met. It is final:
	// a torrent that met its goal stops uploading for good (design spec
	// §6.3), and it must not start again because its engine restarted.
	GoalMet bool
}

// FileSelector reports whether one file of a transfer should be fetched. path
// is the file's path as [File.Path] reports it; sizeBytes is its length. See
// [AddRequest.WantFile].
type FileSelector func(path string, sizeBytes int64) bool

// Client is one download client -- an embedded torrent engine or an NNTP
// engine -- as grabarr's controllers and engine runnables use it.
//
// Implementations are safe for concurrent use: the engine runnable polls
// [Client.List] on a timer while the Download informer calls [Client.Add] and
// [Client.Remove] from reconciles.
//
// Every method takes a context and must honour its cancellation; none of them
// blocks on a transfer completing. [Client.Add] returns as soon as the client
// has accepted the work, not when it is done.
type Client interface {
	// Info reports the client's own state. It is cheap enough to call on
	// every DownloadClient reconcile, and it is the only method that touches
	// the filesystem for free space.
	Info(ctx context.Context) (Info, error)

	// Add starts one transfer and returns the client's id for it, which the
	// controller records in status.downloadID.
	//
	// Add is idempotent on that id: adding the same payload twice returns the
	// same id and does not restart the transfer. This is what makes the
	// re-attach path safe -- an engine that restarted mid-transfer re-attaches
	// from persisted state, and a controller that did not observe the
	// re-attach and calls Add again gets the running transfer back rather
	// than a second copy of it.
	Add(ctx context.Context, req AddRequest) (string, error)

	// Get returns one transfer by id, or [ErrNotFound].
	Get(ctx context.Context, id string) (Item, error)

	// List returns every transfer the client currently holds, including ones
	// it re-attached and the controller has not yet matched to a Download.
	List(ctx context.Context) ([]Item, error)

	// Pause suspends a transfer without removing it. On a torrent it stops
	// requesting pieces and stops uploading; on usenet it stops fetching
	// articles and leaves the partial files in the scratch area.
	Pause(ctx context.Context, id string) error

	// Resume undoes Pause. Resuming a transfer that is not paused is a no-op,
	// not an error, because the controller calls it from a level-driven
	// reconcile that cannot know the client's current state.
	Resume(ctx context.Context, id string) error

	// SetPriority changes the transfer's priority class after its Add, for a
	// spec.priority edited on a running Download. Each client applies the
	// class with the same lever [AddRequest.Priority] documents. Setting the
	// priority a transfer already has is a no-op -- the engines call it from
	// a level-driven reconcile on every poll -- and an empty priority is
	// normal, the CRD default.
	SetPriority(ctx context.Context, id string, priority downloadv1alpha1.DownloadPriority) error

	// SetSeedCriteria updates the goal at which a torrent may stop seeding.
	// It may be called at any time, including after the goal is already met.
	//
	// On a usenet client it is a no-op returning nil. A usenet transfer never
	// seeds, so the goal is vacuously met; returning an error instead would
	// force the Download controller to branch on protocol for a call whose
	// result it does not use.
	SetSeedCriteria(ctx context.Context, id string, sc commonv1alpha1.SeedCriteria) error

	// MarkImported tells the client the content has been imported, so it may
	// release its claim on the files: a torrent stops reporting
	// CanMoveFiles and may set CanBeRemoved once its seed goal is met; a
	// usenet client may clean its scratch area.
	//
	// It does not remove the transfer. Removal is [Client.Remove], driven by
	// spec.removeOnImport.
	MarkImported(ctx context.Context, id string) error

	// Remove stops a transfer and forgets it. With deleteData it also deletes
	// the downloaded files; files already hard-linked into the library survive,
	// because a hard link is not a copy.
	//
	// Forgetting is durable. A client that persists its own per-transfer
	// state (usenet's scratch manifest) discards it on every Remove,
	// deleteData or not, or the next restart would re-attach the transfer
	// it was told to forget -- and an orphan reaper, which always passes
	// deleteData=false, would reap the same transfer after every restart.
	// deleteData governs only the downloaded content on the shared data
	// volume.
	//
	// Remove is idempotent: removing an unknown id returns [ErrNotFound],
	// which a finalizer treats as success.
	Remove(ctx context.Context, id string, deleteData bool) error

	// Close shuts the client down and releases its ports, connections and
	// file handles. It blocks until in-flight work has stopped, so the
	// process can exit without leaving a half-written file behind.
	Close() error
}
