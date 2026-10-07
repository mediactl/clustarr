# MediaFile remediation loop: per-file work as MediaFile status (ADR-0016)

**Status:** Accepted for implementation on branch `unify-manager-agent`, 2026-10-06.

**Basis:**

- Worktree `/home/appkins/src/mediactl/clustarr-unify`, branch `unify-manager-agent`, on main
  `0d3ae234`. ADR-0016 is commit `a63ca1e0`
  (`docs/adr/0016-per-file-work-is-mediafile-status.md`, Accepted). Line references are to
  that tree unless marked **main** (local main `75e651c8`).
- The owner's answers to ADR-0016, recorded at the end of `.superpowers/unify/rulings.md`.
- The manager/agent split design (`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`,
  "split spec" below), especially §5 (writers) and §6.5 (the probe protocol: KV bucket
  `clustarr-probes`, sequence-fenced records), which this design generalises. The split
  plan is `docs/superpowers/plans/2026-10-06-manager-agent-split.md` (cited as `plan:<line>`).
- Seven section drafts (api, loop, records, admission, subtitles, migration, integration),
  merged here. Where they disagreed, this document picks one answer and says why in
  Appendix A. Every name below is binding for the implementation.
- A five-lens review of this document (2026-10-06), checked against the branch at `8b3f67bb`
  (the committed split plan) and applied; Appendix B records what it found and where each fix
  is.

CLAUDE.md's invariants and gotchas bind throughout: one complete status declaration per
apply, forced ownership makes double claims silent, lost updates need a re-read before a
slow write's apply, typed clients cannot send a value a CRD default overrides, every status
list is capped, every NATS KV key goes through `events.KVKeyToken`, and single-node NATS
work streams are small memory streams that discard.

---

## 1. Owner decisions taken

The owner answered ADR-0016's three open questions (rulings.md, "ADR-0016 ... owner answers"):

1. **Fold scope: all three kinds, in this branch.** `status.subtitles`, `status.transcode`
   and `status.graft` replace SubtitleRequest, TranscodeJob and AudioGraft. The migration
   copy and the CRD removal are separate, ordered commits, which become two releases when
   deployed (§7).
2. **Loop scope: everything that watches MediaFile merges into the one remediation loop.**
   That is catalogarr's MediaFile, Movie, Episode, Album, Book, Audiobook and Issue
   reconcilers; importarr's rename controller; squasharr's TranscodeProfile, TranscodeJob and
   AudioGraft controllers; and captionarr's SubtitleProfile and SubtitleRequest controllers.
   Item rollups are the loop's second key type. After the fold,
   `TestOnlyTheLoopWatchesMediaFile` (§3.20) holds: nothing outside the loop has a `For`,
   `Watches` or `source.Kind` on MediaFile. The two profile kinds keep slim reconcilers that
   write only their own status and do not watch MediaFile (§2.13); transcode admission is a
   separate single-key controller that does not watch MediaFile either (§5).
   **This is a reading of the ruling, not its letter, and it is open (§9, D3).** The ruling
   lists squasharr's TranscodeProfile and captionarr's SubtitleProfile controllers among those
   that merge. This design merges their per-file halves (selection, wanting, job and request
   creation) into the loop and keeps their per-profile status writers outside it, because
   ADR-0016 gives the loop file and item keys only. Those writers are still driven by
   MediaFile changes, through `ledger.profileWake` and `subtitleProfileWake`, which the loop
   and the ledger signal; `TestOnlyTheLoopWatchesMediaFile` allows exactly those two channel
   wakes and no other MediaFile-driven source outside the loop.
3. **Sequencing: the fold lands after `cmd/manager` exists.** The split never moves the three
   kinds' controllers, squasharr's results consumer, or the `catalogarr-markers` and
   segment-result status writes only to delete them (§8).

The same ruling block also fixes the ffgo fork tag (this branch's fork work is tagged locally
`v0.0.0-clustarr.13`, on main's published `.12`) and the MP4 standard's reconciliation at each
rebase. §8 carries both.

---

## 2. API: MediaFile status after the fold

This section fixes the API the fold leaves behind:

- the complete `MediaFileStatus`, field by field, with caps;
- the annotations that carry user intent;
- the conditions, print columns and selectable fields;
- the size budget and the tests that hold it;
- who writes `TranscodeProfile.status`, `SubtitleProfile.status`, and the item statuses that
  come from file rollups.

Everything here is in `api/catalog/v1alpha1`. That package imports only
`api/common/v1alpha1` today (verified by `grep` of every `api/*/v1alpha1` import), so the new
types cannot reference `transcodev1alpha1` or `subtitlev1alpha1` and are defined here. Where
an enum duplicates a profile's enum, a crdcheck mirror test holds the two together.

### 2.1 Decisions in brief

1. **Blocks are pointers.** `status.subtitles`, `status.transcode`, `status.graft` and
   `status.handledNonces` are `*T` with `omitempty`. An absent block means the remediation
   does not apply to the file now. The loop is the only writer of MediaFile status (field
   manager `catalogarr`), so leaving a block out of its one apply clears it, the same
   mechanism `activeDownloadRef` uses today (movie/reconciler.go:594-599).
2. **Where each block exists.**
   - `status.subtitles`: on every managed, probed movie or episode file, with phase
     `NotWanted` when the profile wants nothing (§6.4.4).
   - `status.transcode`: on a file in its profile's window, and on a file holding a verdict.
     A verdict for another (profile hash, probe hash) is stale: it no longer stops planning,
     but the block is left untouched until the file re-enters the window, so a new hash writes
     nothing to verdict files (§5.11). A file in the backlog beyond the window has no block.
   - `status.graft`: only on a single-item file whose item has a donor and is missing
     languages, or has a graft result for this probe (§2.6).
3. **Every block has a scalar `phase` that is always written** (`json:"phase"`). It is a
   print column and a selectable field. A file with no block reads `""` under a field
   selector. `subtitles.phase` and `graft.phase` are `+required` from release N.
   `transcode.phase` is `+optional` in release N and `+required` from N+1 (F9.2), because the
   previous release applies `status.transcode` without a phase under the same field manager,
   and a required phase would make the apiserver refuse that apply after a rollback (§2.16).
4. **Sequences.** One per-file counter, `status.lastSeq`, never decreases while the MediaFile
   exists. Each in-flight remediation records the task it fenced in a `Dispatch` (`seq`,
   `answeredSeq`, `dispatchedAt`). The issue rule is `records.NextSeq` (§2.3, §4.6). A
   dropped block or language never re-issues a used number.
5. **Lists and defaults.** Every new list is `+listType=atomic`: there is one writer, so a
   map list buys nothing and costs a `managedFields` key per entry. `status.sidecars` is the
   one exception until N+1: it keeps today's map list keyed by `path` through release N
   (§2.7, §2.16). No new status field has a `+kubebuilder:default`.
   `status.transcode.lastResult`, the only status default today (mediafile_types.go:107-110),
   loses its default in release N and stays in the schema, deprecated and never written by
   the loop, beside the never-written `jobRef` (:103-105); both, with the `TranscodeResult`
   type, are deleted in N+1 (§2.15, §2.16).
6. **Markers stay frozen.** `status.markers` keeps its exact shape, because cluster-plex
   hashes the whole map into `SeedKey` (cluster-plex `pkg/clustarrwatch/fields.go:156-171`).
   No request bookkeeping for markers goes into status at all; markers records are fenced by
   their inputs (§4.12).
7. **Sidecars change shape, in two steps.** Release N adds `name` (basename) beside the
   absolute `path`, keeps `path` as the required map key, which the loop always writes, and
   lowers the cap from 50 to 32. N+1 drops `path`, makes `name` required and the list atomic.
   The list names every attributable sidecar on disk. It is empty on every live MediaFile
   today (0 of 11,958), so no data moves.
8. **Conditions.** MediaFile `conditions` MaxItems goes from 8 to 12 (mediafile_types.go:207).
   The set is closed at 10 types. The six planner-error types are present only while their
   planner is failing (§2.8).
9. **User intent is annotations on the MediaFile.** Three one-shot actions carry a nonce, and
   `status.handledNonces` records the last one handled. Three standing preferences carry a
   value, and `status.transcode` records the value in force (§2.9).
10. **Item-scoped graft state goes on the item, not the file.** `Movie|Episode.status.audio.donor`
    is derived from the item's imported donor Download, and `status.audio.rejectedReleases`
    records donor faults. A MediaFile is named per file path, so an upgrade under a new
    filename would lose both.
11. **Profile statuses keep their JSON names.** Slim profile reconcilers in the manager write
    them; neither watches MediaFile (§2.13).

### 2.2 `MediaFileStatus`, complete

| JSON path | Status | Type, cap | Owner (§3.5) |
|---|---|---|---|
| `observedGeneration` | kept | int64 | loop |
| `conditions` | **cap 8 to 12** | `[]metav1.Condition`, map by `type` | per type, §2.8 |
| `lastSeq` | **new** | int64, Minimum=0 | loop (issued by the planners' dispatches) |
| `probeHash` | **capped** | string, MaxLength=128 | probe |
| `probedAt`, `probeVersion` | kept | | probe (split §6.5 unchanged) |
| `mediaInfo` | **strings capped** | `*commonv1.MediaInfo` | probe; §2.11.2 lists the new MaxLengths |
| `sidecars` | **reshaped** | `[]Sidecar`, map by `path` in N (atomic in N+1), MaxItems=32 | subtitles |
| `naming` | kept | `*NamingStatus` | naming |
| `markers` | kept, **frozen shape** | `*FileMarkers` | markers (writer moves from `catalogarr-markers` to the loop) |
| `subtitles` | **new** | `*SubtitlesStatus` | subtitles; replaces SubtitleRequest, §2.4 |
| `transcode` | **expanded** | `*TranscodeState` | transcode; replaces TranscodeJob, §2.5 |
| `graft` | **new** | `*GraftState` | graft; replaces AudioGraft's per-file half, §2.6 |
| `graftTag`, `graftedAt` | kept, MaxLength=64 | | probe until F7, then graft |
| `handledNonces` | **new** | `*HandledNonces` | per leaf: subtitles, transcode |
| `legacyFold` | **new, release N only** | `*LegacyFoldStatus` | adopt (§7.3.1) |

```go
// MediaFileStatus is written by one remediation loop in the manager, under
// field manager catalogarr, always through k8s.PatchStatusCAS (ADR-0016).
type MediaFileStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=12
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastSeq is the last sequence the loop issued to any remediation task
	// for this file (subtitles, transcode, graft). It never decreases while
	// the MediaFile exists; the probe and markers keep their record-local
	// Seq (split §6.5.2).
	// +optional
	// +kubebuilder:validation:Minimum=0
	LastSeq int64 `json:"lastSeq,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`
	// +optional
	ProbedAt *metav1.Time `json:"probedAt,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	ProbeVersion int32 `json:"probeVersion,omitempty"`
	// +optional
	MediaInfo *commonv1.MediaInfo `json:"mediaInfo,omitempty"`

	// Sidecars are the subtitle sidecars beside the file, found on disk by
	// the subtitles planner's directory read, sorted by name. Release N keeps
	// today's map list keyed by path (§2.16); N+1 makes it atomic.
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=32
	Sidecars []Sidecar `json:"sidecars,omitempty"`

	// +optional
	Naming *NamingStatus `json:"naming,omitempty"`
	// +optional
	Markers *FileMarkers `json:"markers,omitempty"`
	// +optional
	Subtitles *SubtitlesStatus `json:"subtitles,omitempty"`
	// +optional
	Transcode *TranscodeState `json:"transcode,omitempty"`
	// +optional
	Graft *GraftState `json:"graft,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=64
	GraftTag string `json:"graftTag,omitempty"`
	// +optional
	GraftedAt *metav1.Time `json:"graftedAt,omitempty"`

	// +optional
	HandledNonces *HandledNonces `json:"handledNonces,omitempty"`
}
```

`LegacyFold *LegacyFoldStatus` is added by release N in its own file and removed by N+1
(§7.3.1).

### 2.3 `Dispatch` and the per-file sequence

```go
// Dispatch fences one remediation's task and the record that answers it.
type Dispatch struct {
	// Seq is the sequence of the last task issued for this remediation.
	// +kubebuilder:validation:Minimum=1
	Seq int64 `json:"seq"`
	// AnsweredSeq is the Seq of the last dispatch closed: by an incorporated
	// answer, a withdrawal or a timeout. A task is in flight while
	// Seq > AnsweredSeq.
	// +optional
	// +kubebuilder:validation:Minimum=0
	AnsweredSeq int64 `json:"answeredSeq,omitempty"`
	// DispatchedAt is when Seq was issued; the republish window and the
	// request timeout run from it.
	DispatchedAt metav1.Time `json:"dispatchedAt"`
	// Withdrawn is true when the dispatch at Seq was closed by a withdrawal,
	// not by an answer. For transcode and graft a fact (a change already
	// made on disk) can still land after that: the planner keeps reading
	// the record while Withdrawn is true and now < DispatchedAt +
	// records.FactWindow, and incorporates a fact at Seq whatever the
	// phase (§4.10, §5.9). Incorporating it clears Withdrawn.
	// +optional
	Withdrawn bool `json:"withdrawn,omitempty"`
}
```

**Issue rule.** A dispatch takes `seq = records.NextSeq(record.Seq, status.lastSeq, now)`,
which is `max(record.Seq+1, status.lastSeq+1, now.UnixMilli())` (§4.6). The planner writes
`lastSeq = seq` and `dispatch.seq = seq` in the same apply that moves the block to its
in-flight state. The record write and the publish follow the apply (§3.8).

- The time floor keeps `Seq` monotonic after a records-bucket TTL expiry or loss.
- `lastSeq` keeps it monotonic after a dropped block or language, and against a clock that
  went back across a leader handoff.

**Matching.** Records are also matched by their inputs (probe hash, plan hash, profile
generation). Seq orders writes; inputs decide applicability.

**Probe and markers.** The probe keeps split §6.5.2's record-local `Seq`, issued through
`NextSeq(rec.Seq, 0, now)` (§4.12). Its answers are matched by UID, path and hash, so this
design does not reopen §6.5.3's judge. Markers keep no sequence in status (§2.1 item 6).

### 2.4 `status.subtitles` (replaces SubtitleRequest)

```go
// api/catalog/v1alpha1/mediafile_subtitles_types.go

// SubtitlesPhase is the queue view of one file's subtitles.
// +kubebuilder:validation:Enum=NotWanted;Wanted;Searching;Satisfied;Blocked
type SubtitlesPhase string

const (
	SubtitlesPhaseNotWanted SubtitlesPhase = "NotWanted" // the profile wants nothing for this file's audio, and no item remains
	SubtitlesPhaseWanted    SubtitlesPhase = "Wanted"    // a language is missing and waits for its next search
	SubtitlesPhaseSearching SubtitlesPhase = "Searching" // a fetch is outstanding for at least one language
	SubtitlesPhaseSatisfied SubtitlesPhase = "Satisfied" // every wanted language is present
	SubtitlesPhaseBlocked   SubtitlesPhase = "Blocked"   // no plan is possible; reason says why
)

// SubtitleState is one language's state, derived by the loop on every plan.
// +kubebuilder:validation:Enum=pending;searching;downloaded;upgradable;unavailable;failed
type SubtitleState string

type SubtitlesStatus struct {
	// +required
	Phase SubtitlesPhase `json:"phase"`
	// Reason is why the phase is Blocked: ItemNotFound, NoProfile,
	// ProfileInvalid, MediaFileNotOnDataVolume, MediaDirUnreadable or
	// MediaFileNotOnDisk.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// Profile is the SubtitleProfile the plan used.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Profile string `json:"profile,omitempty"`
	// ProbeHash is the status.probeHash the items describe; a new one resets
	// every language's attempts. Written only while items exist, so a
	// NotWanted block with no items does not change with the bytes.
	// (No profileGeneration: nothing reads it from status, and on a block
	// every probed video file carries it would make one SubtitleProfile
	// edit rewrite every MediaFile. The task and the record carry it, §6.6.)
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`
	// Wanted is the langKeys still missing.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=64
	Wanted []string `json:"wanted,omitempty"`
	// CutoffMet is sent false as well as true (no omitempty, no default).
	CutoffMet bool `json:"cutoffMet"`
	// Items is one entry per wanted language or language on disk, sorted by langKey.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	Items []SubtitleItemStatus `json:"items,omitempty"`
}

type SubtitleItemStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	LangKey string `json:"langKey"`
	// +required
	State SubtitleState `json:"state"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	Score int32 `json:"score,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	ScoreOutOf int32 `json:"scoreOutOf,omitempty"`
	// Provider is the SubtitleProvider's name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Provider string `json:"provider,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	SubtitleID string `json:"subtitleID,omitempty"`
	// Name is the sidecar written for this language: a file name in the media
	// file's directory.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`
	// +optional
	DownloadedAt *metav1.Time `json:"downloadedAt,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=512
	LastError string `json:"lastError,omitempty"`
	// +optional
	Attempts commonv1.Attempts `json:"attempts,omitempty"`
	// +optional
	NextSearchAt *metav1.Time `json:"nextSearchAt,omitempty"`
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
}
```

**Further API changes:**

- The annotation constant `catalogv1alpha1.AnnotationSubtitleSearch = "subtitle.clustarr.io/search"`
  goes in shared_types.go beside `AnnotationRefreshMetadata` (:63).
- A print column `Subtitles` (`.status.subtitles.phase`) and a selectable field on the same
  path (§2.10).
- No new field carries `+kubebuilder:default`.

**Where every SubtitleRequest field goes:**

| SubtitleRequest | Fate |
|---|---|
| `spec.mediaFileRef` | Gone: the block sits on the file. |
| `spec.profileRef` | `status.subtitles.profile` records the winner. A user pins a profile with a MediaFile label a profile's selector matches (§6.4.2). |
| `spec.languages`, `spec.minScoreOverride` | Dropped (§9, decision D16). Neither is set live, and the ui has no action for them. |
| `spec.forceSearch` | The annotation `subtitle.clustarr.io/search` (§6.5). |
| `status.observedGeneration`, `fileFingerprint` | Gone. MediaFile has its own observedGeneration; `probeHash` covers size and mtime. |
| `status.phase`, `probeHash` | Kept under the same names. Phase gains `NotWanted`; `probeHash` is written only while items exist. |
| `status.profileGeneration` | Dropped from status: no code reads it (its only non-generated uses are the stamp at subtitlerequest/reconciler.go:539 and the re-apply at app/caption/status/status.go:126). It lives on in `FetchTask.profileGeneration` and the record (§6.6, §6.7.1), where the worker checks it. |
| `status.existing` (64 entries × 4096-byte paths, subtitlerequest_types.go:264-268) | Gone. The embedded half is recomputed from `status.mediaInfo`; the sidecar half is `status.sidecars`. |
| `status.items[]` | Become `items[]` with `dispatch`. `path` (MaxLength 4096, subtitlerequest_types.go:172-175) becomes `name` (255); it was already relative to the media file's directory. State is derived (§6.4.5). |
| Conditions `Planned`, `Satisfied`, `CutoffMet` | `phase` with `reason` and `message`; `wanted`; `cutoffMet`. |
| Condition `DeadLettered` | The MediaFile's single `DeadLettered`, folded by the loop. |
| The liveness protocol (app/caption/status/doc.go:44-96) | Gone: one writer drops a language by omitting it. |

**`state` is always written.** Its first value is `pending`. `searching` means
`dispatch.seq > dispatch.answeredSeq`; today nothing writes it.

### 2.5 `status.transcode` (replaces TranscodeJob)

```go
// +kubebuilder:validation:Enum=Pending;Planned;Queued;Running;Swapping;Succeeded;Failed;Skipped
type TranscodePhase string
// TranscodeHardware mirrors transcodev1alpha1.Hardware (crdcheck mirror test).
// +kubebuilder:validation:Enum=cpu;nvidia;intel;auto;gpu
type TranscodeHardware string
// TranscodeClass is the concrete class a task was dispatched to.
// +kubebuilder:validation:Enum=cpu;nvidia;intel
type TranscodeClass string
// +kubebuilder:validation:Enum=transcode;remuxOnly
type TranscodeMode string

type TranscodeState struct {
	// Phase is always written by the loop. +optional in release N only, so
	// the previous release's phase-less apply is still accepted after a
	// rollback (§2.16); +required from N+1.
	// +optional
	Phase TranscodePhase `json:"phase"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// Profile is the TranscodeProfile that won the file, and ProfileHash the
	// hash the block is planned under (jobspec.ProfileHash over the
	// standard's inputs and standard.Version, the function the profile's
	// status.hash uses). Pending, Failed and Skipped are verdicts for this
	// (ProfileHash, ProbeHash) only.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Profile string `json:"profile,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	ProfileHash string `json:"profileHash,omitempty"`
	// ProbeHash is the status.probeHash the plan or verdict was made for.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`
	// +optional
	Plan *TranscodePlan `json:"plan,omitempty"`
	// Hardware is the hardware in force: the transcode.clustarr.io/hardware
	// annotation when valid, else the profile's.
	// +optional
	Hardware TranscodeHardware `json:"hardware,omitempty"`
	// Priority is the priority admission used: the annotation's, else the
	// profile's spec.priority.
	// +optional
	Priority int32 `json:"priority,omitempty"`
	// Suspended is the transcode.clustarr.io/suspend annotation in force.
	// +optional
	Suspended bool `json:"suspended,omitempty"`
	// PlannedAt is when this (ProfileHash, ProbeHash) first reached Planned;
	// admission orders by priority, then PlannedAt, then name (it replaces
	// TranscodeJob.metadata.creationTimestamp in admitsBefore).
	// +optional
	PlannedAt *metav1.Time `json:"plannedAt,omitempty"`
	// +optional
	Class TranscodeClass `json:"class,omitempty"`
	// Pool is the pool Job of the in-flight or last dispatch.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Pool string `json:"pool,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	FallbackReason string `json:"fallbackReason,omitempty"`
	// Attempts counts dispatches in the current cycle; the retry budget
	// (transcodeplan.MaxAttempts) reads it. A retry, new bytes or a new
	// ProfileHash reset it. Dispatch.Seq never resets.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	NextAttemptAt *metav1.Time `json:"nextAttemptAt,omitempty"`
	// Blocked is a Failed verdict that is not retried without
	// transcode.clustarr.io/retry, new bytes or a new ProfileHash.
	// +optional
	Blocked bool `json:"blocked,omitempty"`
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	WorkerPod string `json:"workerPod,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Result stays until the swap is incorporated, and for good for a
	// replaceSource=false copy (the rescan protects result.outputPath).
	// +optional
	Result *TranscodeOutput `json:"result,omitempty"`
	// StderrTail is the last 1 KiB of the encoder's stderr on Failed; the
	// record keeps 4 KiB.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	StderrTail string `json:"stderrTail,omitempty"`
	// Compliant and ProfileTag are kept from today's TranscodeState: set when
	// a swap is incorporated (ProfileTag = <Profile>@<ProfileHash>, from this
	// block, never from a TranscodeProfile Get).
	// +optional
	Compliant bool `json:"compliant,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=320
	ProfileTag string `json:"profileTag,omitempty"`
	// JoinedGraft is the graft the dispatch at Dispatch.Seq carries
	// (task.Graft), written by the transcode planner in the apply that
	// dispatches, and kept until its next dispatch. The graft planner
	// derives its Running/JoinedTranscode block from it and incorporates the
	// joined result from the transcode record's Answer.Graft (§5.13), so the
	// join lives in the block of the planner that decided it (§3.5).
	// +optional
	JoinedGraft *TranscodeGraftJoin `json:"joinedGraft,omitempty"`

	// JobRef and LastResult are today's fields, kept in release N's schema
	// only so the previous release's apply is still valid after a rollback
	// (§2.16). The loop never writes them; LastResult loses its
	// +kubebuilder:default. Both are deleted in N+1 (F9.2).
	// +optional
	JobRef *string `json:"jobRef,omitempty"`
	// +optional
	LastResult TranscodeResult `json:"lastResult,omitempty"`
}

type TranscodeGraftJoin struct {
	// +kubebuilder:validation:MaxLength=512
	DonorRelease string `json:"donorRelease"`
	// +optional
	DonorImportedAt *metav1.Time `json:"donorImportedAt,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=35
	Languages []string `json:"languages,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash"`
}

type TranscodePlan struct {
	// +required
	Mode TranscodeMode `json:"mode"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Encoder string `json:"encoder,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=copy;encode
	VideoAction string `json:"videoAction,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=cpu;nvdec;upload;vaapi;qsv
	Decode string `json:"decode,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=64
	HDRMode string `json:"hdrMode,omitempty"`
	// +required
	// +kubebuilder:validation:MaxLength=64
	PlanHash string `json:"planHash"`
}

type TranscodeOutput struct {
	// +required
	// +kubebuilder:validation:MaxLength=4096
	OutputPath string `json:"outputPath"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	OutputSizeBytes int64 `json:"outputSizeBytes,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	OutputToSourcePercent int32 `json:"outputToSourcePercent,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10000
	VMAFCentis *int32 `json:"vmafCentis,omitempty"`
}
```

**Phases:**

| Phase | Meaning |
|---|---|
| `Pending` | In the window but cannot be planned yet. Reasons: `Unprobed`, `ProbePending` (`ProbeCurrent()` false), `Grafting` (a standalone graft holds the file). |
| `Planned` | Waiting for admission. Reasons: `WaitingForSlot`, `WaitingForGPU`, `PoolDraining`, `Suspended`, `Backoff` (`nextAttemptAt`), `Unclaimed`, `WorkerLost`, `Rerouted`. |
| `Queued`, `Running` | In flight (`dispatch.seq > dispatch.answeredSeq`). |
| `Swapping` | The worker reported success; the output's probe is awaited before the spec takeover. This replaces "a Succeeded job not yet incorporated still holds its file". |
| `Succeeded` | Incorporated. |
| `Failed` | A verdict for this (profileHash, probeHash). `blocked` is false only for `SourceChanged`, re-planned when the new probe lands. Other reasons: `RetriesExhausted`, `InvalidSource`, `DeadLettered`, `DeadlineExceeded`. |
| `Skipped` | The plan said skip (including the MP4 standard's `HoldImageSubtitles`, which **main** implements as a skip, `pkg/transcode/standard/plan.go:131-134`), the file is `Transcoded` (reason `Transcoded`), or a user cancelled (reason `Cancelled`). |

**Removed from TranscodeJob:**

- `plan.audioTracks` and `plan.subtitleTracks` (MaxItems 200 each,
  transcodejob_types.go:170-178). No non-test code reads them; only `Encoder`, `Mode`,
  `PlanHash` and `Engine` are read (dispatch.go:156-162, metrics.go:82,
  pkg/pipeline/project.go:190-191).
- `plan.engine`, since every plan is ffgo.
- `progress`, which lives only in `clustarr-progress` (ADR-0016).
- `result.mediaInfo`, which duplicates `status.mediaInfo` after the re-probe.
- `stderrTail` beyond 1 KiB, `jobRef` and `conditions`.

**Legacy objects.** A live `status.transcode` with no `phase` (171 objects today, carrying
`compliant`, `profileTag` and `lastResult`; also what the previous release writes after a
rollback, §2.16) is read as `Succeeded` when `profileTag` is set, else as absent. The loop's
first apply of such a block drops `lastResult` and `jobRef`. The only readers of the block today read `profileTag`
(rename/controller.go:219,311-314; transcodeprofile/profile.go:140; transcodejob/plan.go:74;
rescan/keptoutput.go:131).

### 2.6 `status.graft`, and the item's donor

```go
// +kubebuilder:validation:Enum=Waiting;Queued;Running;Swapping;Succeeded;Failed
type GraftPhase string

type GraftState struct {
	// +required
	Phase GraftPhase `json:"phase"`
	// Reason: Waiting -- FileUnprobed, TranscodeRunning, WaitingForTranscode,
	// WaitingForSlot, Backoff; Running -- JoinedTranscode; Succeeded --
	// Grafted, Present; Failed -- the grafttask reasons (grafttask.go:88-99)
	// and JobLost.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
	// DonorRelease and DonorImportedAt identify the item's donor this graft
	// is for (status.audio.donor); a Failed graft is retried only for another
	// donor or another ProbeHash.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	DonorRelease string `json:"donorRelease,omitempty"`
	// +optional
	DonorImportedAt *metav1.Time `json:"donorImportedAt,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MaxLength=35
	Languages []string `json:"languages,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ProbeHash string `json:"probeHash,omitempty"`
	// DonorFault marks a failure that rejects the donor's release
	// (AlignmentRejected, VerifyFailed, MuxFailed, DonorLacksLanguage).
	// +optional
	DonorFault bool `json:"donorFault,omitempty"`
	// JoinedTranscodeSeq is the transcode dispatch this graft rides.
	// +optional
	// +kubebuilder:validation:Minimum=0
	JoinedTranscodeSeq int64 `json:"joinedTranscodeSeq,omitempty"`
	// +optional
	Dispatch *Dispatch `json:"dispatch,omitempty"`
	// JobName is the standalone graft Job of the in-flight dispatch.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	JobName string `json:"jobName,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	RateName string `json:"rateName,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	RateMicros int64 `json:"rateMicros,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	RateMarginMilli int32 `json:"rateMarginMilli,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CoveragePercent int32 `json:"coveragePercent,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	Segments []GraftSegment `json:"segments,omitempty"`
	// +optional
	ResidualMillis int32 `json:"residualMillis,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Within80Percent int32 `json:"within80Percent,omitempty"`
	// Tag is the CLUSTARR_GRAFT tag the worker wrote; status.graftTag takes
	// it when the grafted bytes' probe is incorporated.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Tag string `json:"tag,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

type GraftSegment struct {
	DonorStartMillis  int64 `json:"donorStartMillis"`
	TargetStartMillis int64 `json:"targetStartMillis"`
	LengthMillis      int64 `json:"lengthMillis"`
}
```

**When the graft block exists.** Only when all of these hold:

- the file backs exactly one item (`spec.mediaRef.keys` is empty);
- the item's profile grafts;
- the item has `status.audio.donor`;
- the item's audio is missing languages, or a graft result exists for this `probeHash`.

**Multi-episode files are never grafted.** Today an AudioGraft would align a one-episode donor
against a two-episode file, fail as a donor fault and reject a good release. Instead the item
rollup reads `audio.graft: failed`, reason `MultiItemFile`, and the file has no graft block.

**AudioGraft's item-scoped half goes on the item.** `AudioState` (shared_types.go:102-128),
used by `Movie.status.audio` (movie_types.go:547) and `Episode.status.audio`
(episode_types.go:229), gains:

```go
	// Donor is the newest audio donor imported for this item: from the
	// item's Download with spec.purpose audioDonor whose import finished
	// (status.import.imported[].destPath, spec.release.title,
	// status.import.importedAt), carried forward from this status once seen,
	// so deleting the Download loses nothing.
	// +optional
	Donor *AudioDonor `json:"donor,omitempty"`
	// RejectedReleases are donor releases a graft of this item failed with
	// (status.graft.donorFault on its file); the donor search never takes
	// them again. Oldest dropped past 16.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=512
	RejectedReleases []string `json:"rejectedReleases,omitempty"`

type AudioDonor struct {
	// Path is the reduced donor audio (<stem>.mka), which fileimport writes
	// at import (§4.12, graft).
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path"`
	// +kubebuilder:validation:MaxLength=512
	Release string `json:"release"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	DownloadRef string `json:"downloadRef,omitempty"`
	ImportedAt metav1.Time `json:"importedAt"`
}
```

The donor fields exist today: `DownloadSpec.Purpose` with `DownloadPurposeAudioDonor`
(download_types.go:323-331,433-440), `ImportedFile.DestPath` (:497-500) and
`status.import.importedAt` (:531-533).

**What the graft planner takes from where:** `languages` from the item's
`status.audio.missing`; the anchor from the item's original language; the default from the
profile's `audioDefault`, at dispatch. These are no longer frozen at import as AudioGraft spec
was (donor.go:229-235). `status.audio` is rendered whenever the item has a donor or rejected
releases, even with no file; today `AudioStateFor` returns nil without a file
(audiostate.go:44-46).

### 2.7 `status.sidecars`

```go
type Sidecar struct {
	// Path is the sidecar's absolute path: today's field and the list's map
	// key, written by the loop on every entry in release N (dir(spec.path) +
	// "/" + Name) so that the previous release can read and re-send the
	// list after a rollback (§2.16). The loop also fills each half from the
	// other on a stored entry it carries forward: Name = filepath.Base(Path)
	// on an entry the previous release wrote, Path from Name on one N+1
	// wrote. Deleted in N+1.
	// +required
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path"`
	// Name is the sidecar's file name in the media file's directory.
	// +optional in release N (an entry the previous release wrote after a
	// rollback has none); +required with MinLength=1 from N+1.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Name string `json:"name,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=35
	Language string `json:"language,omitempty"`
	// +optional
	Forced bool `json:"forced,omitempty"`
	// +optional
	HI bool `json:"hi,omitempty"`
}
```

**What it lists.** Every attributable subtitle sidecar (`subtitles.ParseSidecar`) the
subtitles planner's directory read finds, whether downloaded, written by the MP4 standard, or
placed by hand, sorted by name and capped at 32. Today it lists only downloaded items
(sidecars.go:36-58), which is why a rename orphans hand-placed and MP4 sidecars.

**Consumers that move or delete files** (rename's `moveSidecars` rename.go:245,282,
importlist delete.go:155, librarydelete target.go:74) enumerate on disk at the moment they
act, because the list is capped and is a view; they keep reading `path` through release N.
In N+1 (F9.2) they, and ui/detail.go:246-247's extras, join `name` to `dir(spec.path)`
instead.

### 2.8 Conditions

The set is closed at 10 types; MaxItems is 12.

| Type | Present | Owner | Meaning |
|---|---|---|---|
| `Probed` | always | probe | unchanged (split §6.5.3 adds reason `ProbePending`) |
| `Ready` | always | probe | unchanged; rename's `Renameable` reads it |
| `NamingCurrent` | while `naming` exists | naming | unchanged |
| `DeadLettered` | only while `clustarr.io/dead-lettered` is set | loop | `k8s.MarkDeadLettered`. MediaFile joins the annotatable kinds (pkg/k8s/deadletter.go:47-63). |
| `ProbePlannerError`, `NamingPlannerError`, `TranscodePlannerError`, `GraftPlannerError`, `SubtitlesPlannerError`, `MarkersPlannerError` | only while that planner fails | loop | reason `PlannerError` (a returned error) or `PlannerPanic` (a recovered panic), message clamped to 1024 bytes. The planner's last block stays in place. The condition is removed on its next success. |

- **Transient faults set no condition.** Bus or KV unavailable, apiserver List errors,
  context deadlines and EIO on `/data` requeue with backoff and write nothing; otherwise one
  NATS blip becomes about 12k status writes (§3.6).
- **A block's own trouble is in the block.** Subtitles `Blocked`, transcode `Failed`, graft
  `Failed` carry reason and message in their blocks, not in conditions.
- **Rename safety.** `NamingCurrent` never carries a planner fault. A False `NamingCurrent` with
  Ready and Probed True is the rename trigger (rescan/rename.go:70-75).

Constants: `MediaFileConditionProbePlannerError` … `MediaFileConditionMarkersPlannerError`,
`MediaFileReasonPlannerError`, `MediaFileReasonPlannerPanic`.

### 2.9 User intent: annotations on the MediaFile

| Annotation | Kind | Value | Status that records it | Replaces |
|---|---|---|---|---|
| `subtitle.clustarr.io/search` | one-shot | nonce | `handledNonces.subtitleSearch` | SubtitleRequest `spec.forceSearch` |
| `transcode.clustarr.io/retry` | one-shot | nonce | `handledNonces.transcodeRetry` | deleting a Blocked TranscodeJob |
| `transcode.clustarr.io/cancel` | one-shot | nonce | `handledNonces.transcodeCancel` | deleting a TranscodeJob |
| `transcode.clustarr.io/suspend` | standing | `"true"` (anything else is not suspended) | `transcode.suspended` | TranscodeJob `spec.suspend` |
| `transcode.clustarr.io/priority` | standing | decimal int32 | `transcode.priority` | TranscodeJob `spec.priority` |
| `transcode.clustarr.io/hardware` | standing | `cpu\|nvidia\|intel\|auto\|gpu` | `transcode.hardware` | TranscodeJob `spec.hardware` (applies at the next dispatch, as today) |

```go
type HandledNonces struct {
	// +optional
	// +kubebuilder:validation:MaxLength=63
	SubtitleSearch string `json:"subtitleSearch,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	TranscodeRetry string `json:"transcodeRetry,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	TranscodeCancel string `json:"transcodeCancel,omitempty"`
}
```

The constants live in `api/catalog/v1alpha1/mediafile_annotations.go`.

**Nonce format.** The value must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`. By convention it
is the requester's Unix time (`$(date +%s)`); the migration writes `fold-<generation>` (§7.3.5).
A nonce is handled when it equals the recorded value; any other value is a new request.

**Handling:**

- Every one-shot nonce is recorded on the pass that sees it, even when there is nothing to act
  on. Such a nonce gets a Normal Event `NothingToRetry` or `NothingToCancel`. A nonce left
  unrecorded would cancel or retry work planned later.
- A one-shot nonce on a file that is held (Blocked subtitles, a pending probe) stays pending and
  is honoured when the hold lifts; it is recorded then.
- An invalid nonce or value is recorded (one-shot) or ignored (standing), with a Warning Event
  `InvalidAnnotation`.
- Retry clears a `Failed` or `Skipped` verdict for the current tag and resets `attempts`.
- Cancel withdraws any in-flight task (§5.10) and records `Skipped`, reason `Cancelled`, for
  the current (profileHash, probeHash).
- A backlog file with no transcode block still records its transcode nonces in
  `handledNonces`, which is why the nonces are not inside the blocks.

**Writers.** The loop never writes these annotations, and its MediaFile source wakes on a change
to any of the six. Other annotations on MediaFile: `catalog.clustarr.io/observed-fingerprint`
(importarr, as today); `clustarr.io/dead-lettered` (the DLQ projector, which gains `patch` on
mediafiles); `clustarr.io/replay` and `clustarr.io/dead-letter-seq`. In this branch the ui
offers none of the six (§9, D18); they are `kubectl annotate`.

**Dropped, with no annotation:** SubtitleRequest `spec.languages`, `minScoreOverride` and
`profileRef`; TranscodeJob `spec.outputPath`. Nothing live sets them, and a user-set
`profileRef` is already overwritten by the profile controller's forced apply
(subtitleprofile/controller.go:320-342). R-11's explicit-outputPath path takeover goes with
`outputPath`; the container-change takeover, which the MP4 standard makes universal, stays.

### 2.10 Print columns and selectable fields

```go
// +kubebuilder:selectablefield:JSONPath=`.spec.mediaRef.name`
// +kubebuilder:selectablefield:JSONPath=`.spec.mediaRef.kind`
// +kubebuilder:selectablefield:JSONPath=`.status.transcode.phase`
// +kubebuilder:selectablefield:JSONPath=`.status.subtitles.phase`
// +kubebuilder:selectablefield:JSONPath=`.status.graft.phase`
// +kubebuilder:selectablefield:JSONPath=`.status.naming.current`
// +kubebuilder:printcolumn:name="Media",type=string,JSONPath=`.spec.mediaRef.name`
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.mediaRef.kind`
// +kubebuilder:printcolumn:name="Quality",type=string,JSONPath=`.spec.quality.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Transcode",type=string,JSONPath=`.status.transcode.phase`
// +kubebuilder:printcolumn:name="Subtitles",type=string,JSONPath=`.status.subtitles.phase`
// +kubebuilder:printcolumn:name="Graft",type=string,JSONPath=`.status.graft.phase`,priority=1
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.status.transcode.class`,priority=1
// +kubebuilder:printcolumn:name="Worker",type=string,JSONPath=`.status.transcode.workerPod`,priority=1
// +kubebuilder:printcolumn:name="Probed",type=string,JSONPath=`.status.conditions[?(@.type=="Probed")].status`,priority=1
// +kubebuilder:printcolumn:name="Score",type=integer,JSONPath=`.spec.formatScore`,priority=1
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.spec.sizeBytes`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
```

**Selectable fields: 6 of the 8 allowed** (today only `.spec.mediaRef.name`,
mediafile_types.go:434).

- **Status paths are accepted; no label fallback is built.** The apiserver's validator rejects
  only a `metadata` root, array notation and non-scalar types, and allows 8 fields
  (apiextensions-apiserver@v0.37.0 `validation.go:66,849-891`). The api research's envtest on
  the 1.37.0 assets showed list and watch filtering on `.status.transcode.lastResult`,
  including a DELETED event when an object left the selection. The live cluster is v1.37.0.
  ADR-0016's label fallback (and its revisit trigger) therefore never fires.
- **Queue views:** `kubectl get mf --field-selector status.transcode.phase=Planned` (the
  window's waiting files) and
  `kubectl get mf --field-selector spec.mediaRef.kind=episode,status.subtitles.phase=Blocked`.
- **Admission uses the selectable field too.** Its rebuild lists through the APIReader with
  `client.MatchingFields{catalogv1alpha1.FieldTranscodePhase: p}` (§5.6). Constants:
  `catalogv1alpha1.FieldTranscodePhase = "status.transcode.phase"`,
  `catalogv1alpha1.FieldGraftPhase = "status.graft.phase"`.

### 2.11 Render boundary and the size budget

#### 2.11.1 Render rules (one function, `mediafilestatus.Render`)

1. **Bounded strings are clamped** to their MaxLength on a rune boundary. A path or file name is
   never truncated: an over-long sidecar, language or output path drops its entry, with a
   Warning Event. Truncating would record a path that does not exist (download_types.go:486-488
   states the same rule).
2. **Condition messages are clamped** to `k8s.MaxConditionMessage = 1024` bytes. metav1.Condition
   allows 32 KiB each.
3. **Control characters are replaced.** Every string has U+0080–U+009F (except U+0085), U+FFFE
   and U+FFFF replaced with U+FFFD. `json.Marshal` leaves C1 controls raw, and the
   apply-patch+yaml decoder refuses the whole body. One live MediaFile has failed every status
   apply this way about 346 times in 26.5 h (api research). In a single-apply loop, one bad title
   would block every block.
4. **Rendering is deterministic, and an unchanged render skips the apply.** Lists are sorted.
   `now` is whole seconds and is stamped only when a block changes. The `Probed` message uses
   RFC 3339, not `%s` of `now.Time`, which prints Go's monotonic reading `m=+…`
   (mediafile_controller.go:374).
5. **Stored values are never re-clamped.** Rules 1 and 3 apply to values a planner produces
   this pass. A string carried forward from the stored status is sent as stored, even when it
   exceeds a cap release N added (validation ratcheting accepts an unchanged value), because
   `status.mediaInfo` and `status.markers` feed cluster-plex's `SeedKey` and a first render that
   changed them would reseed Plex (§4.12, segment analysis; §7.10). A stored value with a C1
   control character is the exception: it cannot be applied at all, so it is replaced.

#### 2.11.2 Caps added so the budget is provable

| Where | Field | MaxLength |
|---|---|---|
| `commonv1.MediaInfo` | `container`, `videoCodec`, `videoProfile`, `pixelFormat` | 64 |
| audio streams | `codec`, `profile`, `language` | 64 |
| audio streams | `title` | 256 |
| subtitle streams | `codec`, `language` | 64 |
| subtitle streams | `title` | 256 |
| `commonv1.Quality` | `name` | 64 |
| MediaFile status | `probeHash` | 128 |
| MediaFile spec (importarr's) | `path` | 4096 |
| MediaFile spec | `releaseGroup`, `edition` | 256 each |
| MediaFile spec | `profileHash` | 64 |
| MediaFile spec | `languages[]` | 35 |
| MediaFile spec | `matchedFormats[]` | 128 |
| MediaFile spec | `mediaRef.keys[]` | 253 |
| `importedFrom` | `releaseTitle` | 512 |
| `importedFrom` | `downloadRef`, `indexerName` | 253 each |

- **Validation ratcheting** is GA on 1.37, so an unchanged stored value over a new cap still
  updates. The renderer clamps anyway.
- **The probe clamps stream titles on mapping.** This does not raise `mediainfo.ProbeVersion`.

#### 2.11.3 Budget

Measured worst case of the api draft's shape (scratchpad `foldapi/worst.py`: every list at
MaxItems, every string at MaxLength, condition messages at the clamp):

| Block | Bytes |
|---|---|
| `mediaInfo` | 95,286 |
| `subtitles` | 37,333 |
| `conditions` (12 × 1 KiB) | 15,001 |
| `sidecars` | 12,513 |
| `transcode` | 8,370 |
| `graft` | 4,681 |
| `naming` | 4,395 |
| `markers` | 3,902 |
| **status total** | **182,477 (178 KiB)** |

The assembled shape differs by a few KiB: it adds `subtitles.wanted` (20 × 64),
`transcode.probeHash`, `transcode.pool`, `transcode.joinedGraft`, `graft.jobName` and
`lastSeq`, and drops the draft's `markerDispatch` and `subtitles.profileGeneration`. **Release
N's sidecars also carry `path`** (§2.16): 32 × about 4.1 KB adds 131,392 B, so N's worst
case is about 306 KiB of status. The envtest below measures the real number. Spec is about
86 KiB at its new caps, and annotations can reach the apiserver's 256 KiB limit
(`TotalAnnotationSizeLimitB`, apimachinery objectmeta.go:39). The whole object comes to about
530 KiB in N+1 and about 660 KiB in N, against etcd's 1.5 MiB.

**Typical case.** Today a MediaFile is a mean of 6,334 B stored (3,885 B without
managedFields, from the 2026-10-06 dump `scratchpad/mfsize/mf.json`, 11,958 objects, every one
a probed movie or episode file). Every file gains a subtitles block, which dominates the
fold's growth: a NotWanted block with no items is
`"subtitles":{"cutoffMet":false,"phase":"NotWanted","profile":"english"}`, 72 B, plus a
68 B managedFields entry, **about 140 B per file** (the drafted block with `probeHash` and
`profileGeneration` was about 259 B, which is why both go). A file with wanted languages adds
about 250 B per item; a transcode or graft block adds a few hundred bytes to a window file.
The api research's "about 44 B on average" counted no subtitles block and is withdrawn.

Budget constants (`pkg/crdcheck`): `MediaFileStatusBudgetBytes = 320 << 10` in release N and
`256 << 10` from N+1 (F9.2, when `Sidecar.path` goes); `MediaFileObjectBudgetBytes = 768 << 10`.

#### 2.11.4 Tests

- **`pkg/crdcheck.TestMediaFileStringsAreBounded`.** Every string reachable from the MediaFile
  CRD's spec and status has a maxLength, an enum or a date-time format. It walks the generated
  `config/crd/bases/catalog.clustarr.io_mediafiles.yaml`. `conditions[].message` is exempt and
  covered by the next two tests. (Integration's wave gate names it
  `TestEveryStatusStringIsBounded`; this is that test.)
- **`pkg/crdcheck.TestMediaFileAtEveryCapFitsTheBudget`** (envtest; the wave gate's
  `TestMediaFileStatusFitsItsBudget`):
  1. Generate the worst-case object from the generated schema: every array at maxItems, every
     string at maxLength, 12 distinct condition types with 1 KiB messages, annotations filled
     to 256 KiB.
  2. Create it, then apply its status through `k8s.PatchStatus` under `catalogarr`, proving the
     apiserver accepts every field at its cap with no CEL or size refusal.
  3. Read it back with managedFields. Assert status ≤ `MediaFileStatusBudgetBytes` and the
     stored object ≤ `MediaFileObjectBudgetBytes`, and log both.
  4. It skips without `KUBEBUILDER_ASSETS`; `make test` sets it.
- **`mediafilestatus.TestRenderClampsEveryBoundedString`.** The renderer's clamp table equals the
  CRD's maxLength set. A 40 KiB condition message and a U+0096 in every block's strings render
  within caps and apply against envtest (the wave gate's
  `TestAC1ControlCharacterNeverBlocksTheStatusApply` is its envtest half).
- **`pkg/crdcheck.TestMediaFileStatusPhasesAreSelectable`** (envtest). Lists and watches with
  `status.transcode.phase=Failed` and `status.subtitles.phase=Blocked` return only matching
  objects, and a phase change emits DELETED to a filtered watch.
- **`TestEveryStatusListIsCapped`** (statuslists_guard_test.go:166-256) covers the new lists
  unchanged.
- **New mirrors in mirrors_test.go:** `TestTranscodeHardwareMirrorsTheProfileEnum`
  (`TranscodeHardware` ≡ `transcodev1alpha1.Hardware`), `TestTranscodeClassIsConcreteHardware`,
  `TestSubtitleLanguageStateMirrorsTheFetchVerdicts`.

### 2.12 Field ownership on MediaFile after the fold

| Manager | Object part |
|---|---|
| `catalogarr` | All of `status` (subresource), always through `PatchStatusCAS`. Main resource: the seven `catalog.clustarr.io/*` labels, plus spec `path`, `sizeBytes`, `modTime` and `original` after a swap or graft. |
| `importarr-worker` | Spec, through rescan's one renderer (`mediafilespec.Apply`), including the rename the loop's rename actuator runs. |
| `importarr` | The `catalog.clustarr.io/observed-fingerprint` annotation. |
| `clustarr-dlq-projector` (Update) | `clustarr.io/dead-lettered`. |
| `clustarr-legacy-fold` (Update, release N only) | The intent annotations it carries over from legacy objects (§7.3.5). |
| users (`kubectl`) | The six intent annotations. |

**Retiring `catalogarr-markers`.** Every live MediaFile carries a `catalogarr-markers` status
entry owning `f:markers` (11,958 of 11,958). `PatchStatus` forces ownership
(pkg/k8s/patch.go:88,121), so the loop's first `catalogarr` apply of `status.markers` makes the
two managers co-owners, and a leaf the loop later drops would survive under a manager nothing
applies as again. `notFoundSince` drives `markers.Due`'s ladder, so a stale leaf is a real
fault. Release N strips the entry once per file (§7.3.8). The planned
`TestMediaFileFieldManagersStayDisjoint` (plan Task W4.13, plan:20664) is replaced by
`TestMediaFileStatusIsOwnedByCatalogarrAlone`, which asserts on managedFields by manager name,
after a loop pass, against an object that already has status.

### 2.13 `TranscodeProfile.status` and `SubtitleProfile.status`

JSON names are unchanged (R7; nothing outside status renderers reads them: `grep` of
`.PendingJobs|.RunningJobs|.MatchingFiles|.WantedKeys`).

**Who writes them.** Each is written by a slim reconciler in the manager, keyed by profile and
leader-elected. Neither watches MediaFile. Each reads the manager cache with
`client.UnsafeDisableDeepCopy`, writes through its existing renderer
(`squasharrstatus.PatchProfile`, `captionstatus.PatchProfile`), and skips an unchanged apply.
Counts come from cache field indexes on MediaFile (a cached List, not a watch).

**TranscodeProfile** (controller `transcodeprofile`, field manager `squasharr`; it also renders
pool Jobs under `squasharr-pool`, §5.12). Triggers: its own generation, owned pool Jobs,
and `source.Channel(ledger.profileWake)`.

| Field | After the fold |
|---|---|
| `hash` | `jobspec.ProfileHash(spec)`, pure over spec and `standard.Version`; the transcode planner uses the same function for `status.transcode.profileHash`, so no file waits on `status.hash`. |
| `matchingFiles` | Files with `status.transcode.profile == name`, plus backlog files the ledger holds for it. |
| `pendingJobs` | Window files under the profile in `Pending`, `Planned` or `Queued`. The doc text changes to "files"; the name stays. |
| `runningJobs` | `Running` or `Swapping`. |
| `waitingFiles` | **New:** the profile's backlog beyond the window, from the ledger (§5.11). |
| `encoderLimits` | Unchanged: `clustarr-progress` `encoder-limits.<class>` (controller.go:319-358). |
| `Ready`, `Invalid` | `validateProfile` (profile.go:324), pure. |
| `Overlap` | A selector pass over cached video MediaFile labels: True when another profile wins a file this profile's selector matches. |

**SubtitleProfile** (controller `subtitleprofile`, field manager `captionarr`). Triggers:
generation, and `source.Channel(subtitleProfileWake)` that the loop signals (debounced 10 s)
when a file's winning profile or subtitle phase changes.

| Field | After the fold |
|---|---|
| `observedGeneration`, `wantedKeys` | Unchanged. |
| `matchingFiles` | Files with `status.subtitles.profile == name`, through the index `remediation.mediafile.subtitleProfile`. |
| `Invalid` | `DuplicateDefault`, `KeyMismatch`, plus new `InvalidFilter`: a `mustContain` or `mustNotContain` entry (subtitleprofile_types.go:339-351) fails `regexp2.Compile(p, regexp2.IgnoreCase)`. Today that fails per task in the worker (fetch/worker.go:287-290, select.go:124-143). |
| `Overlap`, `Ready` | As for TranscodeProfile; Ready's message reads `%d file(s) planned with this profile`. |
| `SeedDefault`/`Bootstrap` | Unchanged (defaults.go:46-84), a leader-only runnable in `app/caption/manager`. |

**One selection rule, today's.** Both planners pick the winner with the existing
`winningProfile` (transcodeprofile/profile.go:214, subtitleprofile/profile.go:193): the
`profileLess`-least selector match (creation time, then name), else `defaultWinner`. Invalid
profiles are **not** excluded from winning, as today: a file whose winner is Invalid plans
nothing (subtitles: `Blocked`, `ProfileInvalid`; transcode: not eligible, and the profile's
`Invalid` condition says why). It is never handed to a profile the user did not choose for it.
`chooseProfile` (fetch/profile.go:56-104) and `selectProfile` (subtitlerequest/profile.go:49-82),
which disagreed, are deleted.

### 2.14 Item statuses that come from file rollups

The loop's item keys render these (§3.12), under the same field manager `catalogarr`, through
each kind's existing single renderer (and `reassertKnownStatus` on Movie). Leaves stay disjoint
from `catalogarr-grab`, `catalogarr-metadata`, `catalogarr-artwork` and `catalogarr-series`.

| Kind | File-derived fields (unchanged names) | What changes |
|---|---|---|
| Movie | `hasFile`, `fileRef`, `fileQuality`, `fileFormatScore`, `cutoffMet`, phase (Downloaded/Transcoded/CutoffUnmet), `audio`, conditions `HasFile`, `CutoffMet`, `WrongLanguage` | `audio.graft` reads the file's `status.graft`, not the AudioGraft. New `audio.donor` and `audio.rejectedReleases` (§2.6). |
| Episode | same set (episode/reconciler.go:578 apply) | same as Movie; a multi-episode file gives `audio.graft: failed`, reason `MultiItemFile`. |
| Album | `tracks[].fileRef`, `trackFileCount`, `quality`, `formatScore`, `cutoffMet`, phase | none in the API |
| Book | `hasFile`, `fileRef`, `fileFormat`, `cutoffMet`, phase | none |
| Audiobook | `hasFile`, `fileRefs`, `quality`, `cutoffMet`, phase | none |
| Issue | `hasFile`, `fileRef`, `fileQuality`, `cutoffMet`, `state` | none |
| Series | from Episodes (not a MediaFile watcher) | unchanged |

**How `audio.graft` derives:**

| Item and file state | `audio.graft` |
|---|---|
| no donor, languages missing | `searching` |
| donor Download non-terminal | `grabbed` |
| file's graft `Waiting` or `Queued` | `pending` |
| `Running` with segments | `aligned` |
| `Failed` | `failed`, with `reason` + `message` clamped to 1024 |
| `Succeeded`, nothing missing | `done` |
| multi-item file | `failed`, reason `MultiItemFile` |

**Rejected releases.** When the file's graft block is `Failed` with `donorFault`, the item key
appends `donorRelease` to `rejectedReleases` (oldest dropped past 16).

**Readers that change:** the donor search reads `status.audio.rejectedReleases`, not the
AudioGraft (search/donor.go:68-73). fileimport's donor path stops applying an AudioGraft
(donor.go:217-247); it reduces the donor at import (§4.12) and removes a superseded donor by
`status.audio.donor.path`.

**Wake-ups.** A file pass enqueues every item in `spec.mediaRef` (keys included) when an input
to these rollups changes: spec, the `Transcoded` verdict, probed audio languages, the graft
block's phase, or a file appearing or going. This fixes Album, Book, Audiobook and Issue missing
probe-only verdict changes; today they watch `GenerationChanged` only (album:194, book:175,
audiobook:176, issue:147).

### 2.15 Removed from the API

**In the fold's API commit (F2):** nothing is removed. `TranscodeState.lastResult` loses its
`+kubebuilder:default=none`; every other change is additive or a new cap. The code that
writes `lastResult` (mediafile_controller.go:416,436,665) and reads `Sidecar.Path` keeps
compiling until the wave that retires it, so each F-commit builds (§7.11).

**In the CRD-removal commits (release N+1, F9):** SubtitleRequest, TranscodeJob, AudioGraft and
`GraftResult`; `pkg/k8s.AudioGraftName`; their applyconfigurations, ReplayKinds entries and DLQ
resolvers; `status.legacyFold`; `TranscodeState.jobRef` (never written) and
`TranscodeState.lastResult` with the `TranscodeResult` type; `Sidecar.path` (and the map
list keyed by it). `transcode.phase` and `Sidecar.name` become `+required`. The readers of
`Sidecar.Path` move to `name` in the same commit: ui/detail.go:246-247,
app/import/controller/librarydelete/target.go:74, app/import/worker/importlist/delete.go:155,
app/import/worker/rescan/rename.go:245,282, and their tests
(librarydelete/target_test.go:40,94, app/import/run_test.go:113, rescan/keptoutput_test.go:109,
test/e2e/transcode_test.go:690). `api/transcode/v1alpha1` keeps TranscodeProfile.
`api/subtitle/v1alpha1` keeps SubtitleProfile and SubtitleProvider.

### 2.16 Release N's schema accepts the previous release's writes

Helm never reverts CRDs (charts/clustarr/crds/README.md:21-29), and the previous release
applies MediaFile status under the same field manager, `catalogarr`, from its `knownStatus`
(app/catalog/controller/mediafile/mediafile_controller.go:602-694 on main): `transcode`
as `{compliant, profileTag, jobRef, lastResult}` with no phase, and `sidecars[]` re-sent
with `WithPath(s.Path)` from what it read. So release N's MediaFile schema is a superset
that accepts those applies:

| Leaf | Release N | N+1 |
|---|---|---|
| `status.transcode.phase` | `+optional`; the loop always writes it | `+required` |
| `status.transcode.jobRef`, `lastResult` | kept, deprecated, never written; `lastResult` has no default | deleted |
| `status.sidecars[]` | map list keyed by `path` (`+required`, MaxLength 4096, always written by the loop), plus `name` `+optional` | atomic, `name` `+required`, `path` deleted |

Without these, the previous release's first apply on every file whose transcode block or
sidecars release N wrote would be refused outright (the apply releases N's phase while
keeping the block; N's sidecar entries decode as `path: ""`), and that file's status would
never change again. The rollback still re-applies the previous commit's catalog CRDs (§7.6),
because N's new string caps (§2.11.2) would refuse a previous-release probe whose stream title
exceeds them; N's schema compatibility covers the window before that step and an operator who
skips it. `TestThePreviousReleasesStatusApplyIsAccepted` (§7.3.12) holds the table.

---

## 3. The remediation loop

The loop is one typed controller-runtime controller in `cmd/manager`, package
`app/remediation`, controller name **`mediafile`** (kept: split spec §3.11 keeps every
controller name, and this is the MediaFile controller grown). It replaces the thirteen
controllers that watch MediaFile and is the only writer of `MediaFile.status` (ADR-0016).

This section fixes the loop's package, keys, sources, planner contract, apply path,
scheduling, field managers, indexes and throughput, and gives every current watcher a fate.
§2 owns the blocks, §4 the records, §5 admission, §6 subtitles, §7 the migration copy.

**Verified starting point** (worktree HEAD `a63ca1e0`):

- **Thirteen controllers watch MediaFile.** Two use `For`: mediafile_controller.go:721 and
  rename/controller.go:343. Eleven use `Watches`: movie/reconciler.go:181, episode:230,
  album:194, book:175, audiobook:176, issue:147, transcodeprofile/controller.go:654,
  transcodejob/controller.go:941, audiograft/controller.go:137,
  subtitleprofile/controller.go:459 and subtitlerequest/reconciler.go:795. None uses `Owns`.
- **Their workers sum to 20.** mediafile has 8 (mediafile_controller.go:774); transcodejob is
  pinned to 1 (:946); the other eleven take controller-runtime's default of 1, including
  Episode, which has 15,553 live objects.
- **`k8s.PatchStatusCAS` exists** (pkg/k8s/patch_cas.go:75), generic over
  `CASApplyConfiguration`. On every attempt it reads through `r`, applies with that read's
  resourceVersion, and lets `render` skip. No new helper is needed.
- **controller-runtime v0.25.1** (go.mod:58) gives the loop what it needs:
  `TypedControllerManagedBy[request]` (pkg/builder/controller.go:76; `For` accepts only
  `reconcile.Request`, :315-316, so a custom key type uses `Watches` and `WatchesRawSource`);
  the priority queue on by default (pkg/controller/controller.go:115,272), with map-func
  handlers enqueuing initial-list events at `handler.LowPriority`
  (pkg/handler/enqueue_mapped.go:132-150); `reconcile.Result.Priority`
  (pkg/reconcile/reconcile.go:51); `source.TypedFunc`, which hands a source the queue
  (pkg/source/source.go:311); and `Apply` decoding the server's response back into the apply
  configuration (pkg/client/typed_client.go:185).

### 3.1 Packages and registration

| Package | Holds | Linked by |
|---|---|---|
| `app/remediation` | `Key`, `Reconciler`, `SetupWithManager`, every source, the pass (gather, plan, render, apply, effects, actuators), isolation, `Order`, `Planner`, `Effect`, `RegisterIndexes` | manager |
| `app/remediation/manager` | `Register(mgr ctrl.Manager, bus events.Bus, o Options) error`, the split's manager-side registration contract (.superpowers/unify/plan/04-registration.md:29) | manager |
| `app/remediation/mfindex` | MediaFile index names and extractors (§3.16) | manager: librarydelete, segmentplan, the item packages, the planners, the profile reconcilers |
| `app/remediation/probe`, `transcode`, `graft`, `naming`, `subtitles`, `markers` | the six planner adapters (`Gather`, `Plan`, `Copy`) over the pure domain planners | manager |
| `app/remediation/rename` | the rename actuator over `mediafilespec.RenameFile` | manager |
| `app/remediation/adopt` | release N only: the adoption planner over `legacyfold.Plan` (§7) | manager |

**Domain logic stays where it lives and is called, not moved:**

- `app/catalog/controller/mediafile`, which keeps no `Reconciler`: `evaluateProbe`,
  `judgeProbe`, `renderNaming`, `mirrorLabels`.
- `app/squash/transcodeplan` (pure): `Plan`, `Eligible`, `Decide`, `MaxAttempts`,
  `RequeueBackoff`; it calls `standard.Plan`, `planFor`, `ChooseClass` and the moved
  `winningProfile`/`validateProfile`/`WouldTranscode`. `app/squash/graftplan` (pure).
- `app/squash/admission`: the ledger (§5).
- `app/caption/subtitleplan` (pure): `subtitles.Plan` and the subtitlerequest schedule
  functions (§6.4).
- `app/catalog/markers`: `markers.Due`; `pkg/segments`: `segments.Due`, `segments.Merge`;
  `app/catalog/segmentplan.PublishPlan`.

**Registration changes:** split `internal/cli/manager` step 8 (split spec §3.4.2) becomes:
catalog, import, index, grab, caption, squash, **remediation**, autoscale.
`app/squash/manager` shrinks to the pool renderer and slim TranscodeProfile reconciler, the
admission controller and the task sweep. `app/caption/manager` keeps SubtitleProvider,
`subtitleprofile.Bootstrap` and the slim SubtitleProfile reconciler. `app/import/manager` loses
rename. `app/catalog/manager` loses mediafile, movie, episode, album, book, audiobook and issue.

**New manager flags:** `--remediation-concurrency`, default `16`, validated 1 to 64;
`--remediation-bulk-writes-per-second`, default `25` (0 disables; §3.7);
`--remediation-io-workers`, default `8`, validated 1 to 64 (§3.17).

### 3.2 Keys

```go
// Key is the loop's request: one controller, one queue; Kind picks the path.
type Key struct {
	Kind      KeyKind
	Namespace string
	Name      string
}

type KeyKind string

const (
	KindMediaFile KeyKind = "MediaFile"
	KindMovie     KeyKind = "Movie"
	KindEpisode   KeyKind = "Episode"
	KindAlbum     KeyKind = "Album"
	KindBook      KeyKind = "Book"
	KindAudiobook KeyKind = "Audiobook"
	KindIssue     KeyKind = "Issue"
)
```

ADR-0016 names two key types, and the loop has exactly two: the file key and the item key (six
item kinds). Admission and the two profile statuses are separate controllers that do not watch
MediaFile (§2.13, §5).

**From a file to its items.** `ItemKeys(mf)` maps `spec.mediaRef.kind` to the item kind; the
names are `spec.mediaRef.name` plus every `spec.mediaRef.keys` entry, deduplicated. `keys`
exists only for Episode and Issue packs (api/common/v1alpha1/media_types.go:53-56).
`mediaRef.track` narrows an Album reference and creates no key. A multi-episode file is one
file key and N Episode keys.

**From an item to its files.** A List with
`client.MatchingFields{mfindex.Item: mfindex.ItemKey(kind, name)}`, which covers `keys`, as
`coveredEpisodes` does today (episode/reconciler.go:296-307).

**Each key writes only its own objects.** A file key writes that MediaFile's status, labels and
spec takeover, its graft Job, and its records and tasks. An item key writes that item's status
and finalizer.

### 3.3 Sources and predicates

Every source is registered on the one controller
(`TypedControllerManagedBy[Key](mgr).Named("mediafile")`); every map function returns `[]Key`.
"Files of X" means a List through an index (§3.16) with `UnsafeDisableDeepCopy`.

| # | Source | Predicate | Enqueues |
|---|---|---|---|
| S1 | MediaFile, typed handler `mediaFileEvents`, wrapped in `handler.WithLowPriorityWhenUnchanged` | Create and delete always. On update: generation changed; a watched annotation changed: `catalog.clustarr.io/observed-fingerprint` (mediafile_controller.go:93), `clustarr.io/dead-lettered` (pkg/k8s/deadletter.go:78), `clustarr.io/replay` (history/replay.go:55), and the six intent annotations (§2.9); or a label changed other than the seven `catalog.clustarr.io/*` keys `mirrorLabels` writes (labels.go:32-52), which is how a user pins a file to a profile (§6.4.2) | The file key (at `PriorityUser` when an intent annotation or a user label changed), plus the item keys of the old and new `mediaRef` |
| S1' | Same handler, update that changes only status | `FileSignature` changed: `Transcoded()`, `rollup.AudioLanguagesObject`, probeHash empty or not, Ready status, `status.transcode.phase`, `status.graft.phase` and its tag. `ProfileSignature` changed: the winning subtitle profile or its phase | Item keys; a non-blocking send on `subtitleProfileWake` (§2.13). **Never the file key itself** |
| S2 | Movie | `moviePredicate` (movie/reconciler.go:211-230) | Movie key |
| S2' | Movie | Create or delete; generation changed; `movieFileInputs` (today `movieNamingInputs`, watch.go:225-232, plus `status.metadata.originalLanguage`) | Files of the movie |
| S3 | Episode | `episodePredicate` (episode/reconciler.go:249-269) | Episode key |
| S3' | Episode | Create or delete; generation changed; `episodeNamingInputs` | Files of the episode |
| S4 | Series | `seriesMonitoredChanged` (episode package) | Episode keys, through `series.EpisodeBySeriesRefIndex` |
| S4' | Series | generation changed; `seriesFileInputs` (today `seriesNamingInputs`, watch.go:234-244, plus `originalLanguage`) | Files of its episodes |
| S5 | Album; Artist | `albumPredicate`; Artist `GenerationChanged` | Album keys (`mapArtist`) |
| S6 | Book; Author | `bookPredicate`; `authorPredicate` | Book keys |
| S7 | Audiobook; Book | `audiobookPredicate`; Book `GenerationChanged` | Audiobook keys (`.spec.bookRef`) |
| S8 | Issue; Comic | `issuePredicate`; Comic `GenerationChanged` | Issue keys |
| S9 | Download | `downloadPredicate`, the same in all six packages: generation, `status.phase`, deleting | Target item keys: `spec.target.name` plus its `keys` (donor Downloads included, which feed `status.audio.donor`) |
| S10 | QualityProfile | `GenerationChanged` | Items, through the six `.spec.qualityProfileRef` indexes (Episodes through their Series) |
| S11 | RootFolder | `rootFolderNamingChanged` (watch.go), plus create and delete | Files, through `mediafile.clustarr.io/movie-rootfolder` and `series-rootfolder` |
| S12 | TranscodeProfile | `GenerationChanged` | Every movie and episode MediaFile at `LowPriority` |
| S13 | SubtitleProfile | `GenerationChanged`, or its `Invalid` condition changed | Every movie and episode MediaFile at `LowPriority` |
| S14 | SubtitleProvider | an embedded provider's generation changed (today subtitlerequest/reconciler.go:851-866) | Files with a subtitles block in its namespace (`mfindex.Subtitles`) |
| S15 | batch Job labelled `squasharr.clustarr.io/graft-of` | Complete or Failed condition changed; delete | The owning file key |
| S16 to S21 | One `recordsource.New` raw source per records bucket: `clustarr-probes`, `clustarr-transcodes`, `clustarr-grafts`, `clustarr-subtitles`, `clustarr-markers`, `clustarr-segments` (§4.9) | Put entries only; skip the loop's own states (`requested`, `withdrawn`) and delete or purge markers | The file key named by the value's `mediaFile` Ref |
| S22 | `admission.LoopSource()`, a `source.TypedFunc[Key]` that hands the admission ledger this queue (§5.5) | none | File keys for grants, directives and changed wait reasons, at `admission.GrantPriority` (100) |
| S23 | TranscodeJob, SubtitleRequest, AudioGraft | F3 to F7: today's watches, as the mediafile controller and items have them, until each kind's fold. Release N: create events only, for adoption (§7.3.2) | The owning file key |

**How a records source behaves** is §4.9: `UpdatesOnly` at first open, `FromRevision` on reopen,
enqueue from the value's `mediaFile` Ref with no List, never a data carrier into Reconcile.

**Nodes and pool Jobs are not watched by the loop.** Admission watches them (§5.7).

**The self-trigger rule.** A file pass never wakes itself:

- Its status applies change only status, and S1' never maps a status-only update to the file
  key.
- Its main-resource apply changes only the seven loop-owned labels, which S1's label arm
  ignores. When it also changes spec (after a swap or a graft), generation moves and the file
  wakes once; that pass applies nothing. Planners never select a profile from labels the pass
  is about to change: they read `v.Labels()`, the cached labels overlaid with `mirrorLabels`
  over the draft's mediaInfo (§3.5), so a probe that changes `video-codec` or `hdr` picks the
  right winner in the same pass and no wake is needed.
- Records the loop writes are filtered out by state in S16 to S21.

A file key is therefore enqueued only by an external change, a timer (`RequeueAfter`), a record
a worker wrote, a graft Job's completion, an admission grant, or a profile fan-out.

### 3.4 A file pass

`Reconcile(ctx, Key{KindMediaFile, ns, name})`:

1. **Load** the MediaFile from the cache.
   - **NotFound:** call `ledger.Forget(nn)`. If the ledger held an open transcode or graft under
     that name, run `withdrawAll(uid)` (§5.10) for the UID it recorded. Return. S1's delete has
     already enqueued the items.
   - **Deletion timestamp set:** return. A MediaFile carries no finalizer (§9, D11).
2. **View.** `v.File = mf.DeepCopy()`, `v.Prev = &v.File.Status`,
   `v.Now = clock().UTC().Truncate(time.Second)`, `v.Ledger = ledger.Snapshot(uid)`,
   `v.seqFloor = v.Prev.LastSeq`, and `v.Stale` (§3.7, the applied-status memo): true when
   this process applied a status to this UID less than `staleWindow` (30 s) ago and the cached
   status is not that status. Only the loop writes MediaFile status, so a cached status that
   differs from the loop's own last apply is older than it.
3. **Gather.** For each planner in `Order` that `Applies(mf)`, run `Gather` under `isolate` with
   its own deadline: probe 15 s, subtitles 30 s, the others 10 s. Gather may read the cache, the
   KV records, file metadata under `/data` and the ledger snapshot. It writes nothing. Every
   `/data` call (stat, `ReadDir`) goes through the I/O executor (§3.17): a fixed pool with a
   per-call timeout and a mount-health breaker. A saturated pool or an open breaker returns
   `remediation.Transient` at once, so no pass blocks a worker on NFS and no goroutine is
   spawned past the pool. Item keys never touch `/data`.
4. **Plan.** In `Order`, each planner runs `Plan(v, in, out)` under `isolate`, with `out` a deep
   copy of the draft. Then `next = draft.DeepCopy()` and:
   - on success, `p.Copy(out, next)`, which takes only the planner's own fields;
   - on failure, `p.Copy(v.Prev, next)`, which keeps the last block;
   - for a file the planner does not apply to, `p.Copy(&MediaFileStatus{}, next)`.
   `v.Draft` then becomes `next`.
5. **Loop-owned fields:** `observedGeneration`; `lastSeq = v.seqFloor` (the highest sequence any
   planner issued this pass, §3.5); the six `<Planner>PlannerError` conditions from the
   isolation outcomes (§3.6); `DeadLettered` via `k8s.MarkDeadLettered`
   (pkg/k8s/deadletter.go:128); then sanitise and normalise (§3.7).
6. **Main resource.** `renderMain` (§3.10); applied only when it differs from the cached labels
   and spec.
7. **Status.** One `k8s.PatchStatusCAS` (§3.7), skipped when the rendered status equals the
   stored one. A bulk apply first takes a token from the status-write limiter (§3.7); a pass
   refused one ends with `RequeueAfter` = the token's wait, no apply and no effects. When the
   apply is skipped while `v.Stale` holds, the pass ends with the backed-off stale requeue
   (§3.7) instead of a no-op.
8. **Ledger.** `ledger.Observe(admission.FileView(applied))`, from the applied status, or from
   the cached one when the apply was skipped and `v.Stale` is false. A stale view is never
   observed (§5.5).
9. **Effects** (§3.8), in `Order`, and within each planner in its own order.
10. **Actuators** (§3.9): replay, then rename.
11. **Result** (§3.11).

The only early returns are in step 1, and they write nothing. Every other path reaches step 7
with a complete status.

### 3.5 The planner contract

```go
type PlannerName string

const (
	PlannerAdopt     PlannerName = "adopt" // release N only (§7)
	PlannerProbe     PlannerName = "probe"
	PlannerTranscode PlannerName = "transcode"
	PlannerGraft     PlannerName = "graft"
	PlannerNaming    PlannerName = "naming"
	PlannerSubtitles PlannerName = "subtitles"
	PlannerMarkers   PlannerName = "markers"
)

// Order is the order planners run in within one pass. TestPlannerOrder holds it.
var Order = []PlannerName{PlannerProbe, PlannerTranscode, PlannerGraft, PlannerNaming, PlannerSubtitles, PlannerMarkers}

// View is everything a planner may read, read-only. Plan writes only out.
type View struct {
	File   *catalogv1alpha1.MediaFile       // the cached object this pass planned from
	Prev   *catalogv1alpha1.MediaFileStatus // the stored status, File.Status
	Draft  *catalogv1alpha1.MediaFileStatus // the status after the earlier planners in Order
	Now    metav1.Time                      // UTC, whole seconds
	Ledger admission.Snapshot               // this file's ledger entry, grant and directives; zero when none
	Stale  bool                             // the cache has not caught up with the loop's own last apply (§3.4 step 2)
	seqFloor int64
}

// Labels returns File's labels overlaid with mirrorLabels over Draft's
// mediaInfo: the labels this pass's main apply will leave. Every profile
// selection (transcode, subtitles, the graft profile) reads these, never
// File.Labels.
func (v *View) Labels() map[string]string

// Issue returns the next sequence for a dispatch whose record holds recordSeq:
// records.NextSeq(recordSeq, v.seqFloor, v.Now.Time), and raises the floor, so
// two dispatches in one pass never share a number.
func (v *View) Issue(recordSeq int64) int64

// Planner is one remediation. Gather reads, Plan decides, Copy names its fields.
type Planner[In any] interface {
	Name() PlannerName
	Applies(mf *catalogv1alpha1.MediaFile) bool
	Gather(ctx context.Context, env *Env, v *View) (In, error)
	Plan(v *View, in In, out *catalogv1alpha1.MediaFileStatus) (Result, error)
	// Copy copies exactly this planner's fields -- its block, flat fields,
	// nonce leaves and condition types -- from one status into another.
	Copy(from, into *catalogv1alpha1.MediaFileStatus)
}

type Result struct {
	Main    *MainIntent // probe only: the spec takeover of §3.10
	Effects []Effect    // run after the status apply lands (§3.8)
	Due     time.Time   // when this planner next needs a pass; zero is no timer
	Again   bool        // an earlier planner's input changed this pass: requeue in 1 s
	Events  []Event     // Kubernetes Events on the MediaFile, for transitions Prev -> out only
}
```

`Bind[In](p Planner[In])` erases `In`, so `Order` holds all planners in one slice. Release N
prepends `PlannerAdopt` (§7.3.3).

**Rules every planner follows:**

- **`Plan` is pure.** No I/O, no clock other than `v.Now`, no randomness, output independent of
  map order. `v.Issue` is deterministic given its inputs.
- **`Gather` writes nothing.** An error from `Gather` or `Plan` is the planner's failure (§3.6).
- **When a planner reads its record.** Only while its block has a request outstanding, or a
  dispatch is due, plus four unsolicited cases: the probe record before the first
  incorporation, which can hold an importer's `Seed` (split §6.5.2); the segments record of a
  probed movie or episode file, which can hold a `withIntro` amendment
  (app/segments/worker/worker.go:143); the transcode or graft record while its block's
  `dispatch.withdrawn` is true and `now < dispatchedAt + records.FactWindow`, since a swap can
  land after its withdrawal (§4.8); and, for the graft planner, the **transcode** record while
  `Prev.transcode.joinedGraft` names a dispatch whose joined result the graft block has not
  incorporated (§5.13). Reading another planner's record is a read; only the record's own
  planner incorporates its state.

**Every MediaFileStatus field has exactly one owner:**

| Owner | Status fields | Condition types |
|---|---|---|
| probe | `probeHash`, `probedAt`, `probeVersion`, `mediaInfo`; `graftTag`, `graftedAt` until F7 (today mediafile_controller.go:338-339) | `Probed`, `Ready` |
| transcode | `transcode` (including `joinedGraft`, the join it decided), `handledNonces.transcodeRetry`, `handledNonces.transcodeCancel` | none |
| graft | `graft` (including a joined graft's `Running`/`JoinedTranscode` state and result, derived from the draft's `transcode.joinedGraft`, §5.13); `graftTag`, `graftedAt` from F7 | none |
| naming | `naming` | `NamingCurrent` |
| subtitles | `subtitles`, `sidecars`, `handledNonces.subtitleSearch` | none |
| markers | `markers` (TheIntroDB results, the analysis and the merged segments) | none |
| adopt (release N) | `legacyFold` | none |
| loop | `observedGeneration`, `lastSeq` | the six `<Planner>PlannerError`, `DeadLettered` |

`TestPlannerCopyPartitionsTheStatus` fills every field with a distinct value and asserts that
the `Copy` functions plus the loop's own set cover each field exactly once.

**Why the order is probe, transcode, graft, naming, subtitles, markers:**

- **probe** runs first because every later planner reads its draft: transcode plans from
  `mediaInfo`; subtitles reads the audio streams; `ProbeCurrent()` (split §6.5.3) is evaluated
  on the draft. probe itself reads the *previous* transcode and graft blocks to find a swap or a
  graft waiting to be incorporated.
- **graft** runs after transcode because a join is decided by the transcode planner at
  dispatch (today `joinGraft`, dispatch.go:454-478) and recorded in the transcode block's own
  `joinedGraft`; the graft planner reads the draft transcode block and renders its own block
  from it. Neither planner writes the other's fields, so a graft planner that fails in the
  dispatching pass leaves its last block, and admission still sees the join in
  `transcode.joinedGraft` (it never counts such a file as a standalone graft candidate).
- **naming** runs after both because it holds a rename while either is in flight, reading the
  draft. Today `TranscodePending` (naming.go:122-141) reads the TranscodeJob list. A transcode
  dispatched in this pass already holds the rename the actuator would otherwise perform.
- **subtitles** runs after naming because sidecars follow the file's stem.
- **markers** runs last because it gates on the draft's probe hash, as `segmenting.Applier`
  does today (apply.go:92-94).

**Every planner after the probe returns its last block while `!view.ProbeCurrent()`** (on the
draft). `TestNoPlannerPlansFromAPendingProbe` holds this; it replaces the split's W4.14 gates
(§8).

**Swap incorporation takes three applies.** By the time transcode incorporates a Succeeded
record, probe has already run this pass, and the swap needs a probe of its target. transcode
moves the block to `Swapping` and sets `Again`; the next pass, 1 s later, requests that probe;
its incorporation applies the takeover and `Succeeded`/`Compliant`.

**What the loop requires of each remediation block:** a scalar `phase` that is always written;
a `Dispatch` per in-flight task (§2.3) for transcode, graft and each subtitle item. The probe
keeps split §6.5's record-local sequence; markers keep none in status.

### 3.6 Isolation

`isolate(name, phase, fn)` runs one planner's `Gather` or `Plan` under `defer recover()` with
its own deadline, and classifies the outcome:

| Outcome | Status | Requeue | Metric |
|---|---|---|---|
| ok | the planner's fields from `out`; its `<Planner>PlannerError` condition removed | per `Due` and `Again` | none |
| `remediation.Transient(err)`: bus, KV or apiserver unavailable, a context deadline, EIO from `/data`, the I/O executor saturated or its breaker open (§3.17) | last block kept; **nothing recorded**, the condition untouched | per-key backoff of 30 s, 1 m, 2 m, then 5 m; a clean pass resets it | `clustarr_remediation_planner_failures_total{planner,reason="transient"}` |
| any other error | last block kept; `<Planner>PlannerError` True, reason `PlannerError`, message clamped | `plannerFailedRetry` = 10 m | `reason="error"` |
| panic | as for an error, reason `PlannerPanic`; the stack goes to the log, never to status | 10 m | `reason="panic"` |

- **Transient errors are never written.** One NATS restart would otherwise become one status
  write per file, 11,958 of them.
- **A persistently failing planner writes once.** The condition changes only when its message
  does, and `lastTransitionTime` stays.
- **A failure stays contained.** One planner failing never stops another, the apply, or the
  others' effects. A later planner that depends on the failed one reads the kept block from the
  draft.
- **Effects are isolated too.** Each planner's effect batch runs under the same `isolate`: a
  panic or a non-transient error stops that planner's remaining effects only, is remembered for
  the key, and sets that planner's `<Planner>PlannerError` (reason `PlannerPanic` or
  `PlannerError`, message prefixed `effect:`) at the next pass, which comes after
  `plannerFailedRetry`; the other planners' effects and the actuators still run.
  `TestAPanickingEffectDoesNotStopOtherPlannersEffects` holds it.
- **Panics outside planners and effects** (the loop's own code, actuators) are caught by the
  controller's `RecoverPanic: true`.

### 3.7 The one apply

```go
_, applied, err := k8s.PatchStatusCAS(ctx, viewReader{obj: v.File, rv: rv, gen: gen}, r.Client,
	k8s.ManagerCatalogarr, client.ObjectKeyFromObject(v.File), newMediaFile,
	func(fresh *catalogv1alpha1.MediaFile) (*catalogac.MediaFileApplyConfiguration, bool, error) {
		if equality.Semantic.DeepEqual(normalize(draft), normalize(&fresh.Status)) {
			return nil, true, nil // nothing changed: no request, no watch event
		}
		return statusApply(fresh.Name, fresh.Namespace, draft)
	}, 1)
```

**The reader serves the planned view, and there is one attempt.** `viewReader` serves the object
the planners ran on. If this pass applied the main resource, it carries the resourceVersion and
generation that apply returned (that apply was preconditioned on the view's resourceVersion and
does not touch status). A Conflict means the object changed after the cache read (a user's
annotation, the DLQ projector, a rescan spec, or the loop's own last apply the cache has not
caught up with): the pass runs no effects and requeues with a per-key backoff of 1 s, 2 s, 4 s
… capped at 30 s, reset by a clean apply, and the next pass plans from the newer cache. The
loop never replans from an uncached read. `PatchStatusCAS`'s doc (patch_cas.go:56-58) asks for
an uncached reader; it gains a sentence that a single writer planning from its cache uses one
attempt and a requeue. The helper does not change.

**A stale view never ends a pass as a no-op.** The CAS catches a stale view only when the
pass wants to change something. A pass whose draft equals a stale cached status applies
nothing, so nothing conflicts, and the timer of the pass it lost can be gone: controller-runtime
v0.25.1's priority queue keeps an existing item's `ReadyAt == nil` and drops a later
`AddWithOpts{After}` for it (pkg/controller/priorityqueue/priorityqueue.go:255-258; the
controller turns `RequeueAfter` into exactly that add, pkg/internal/controller/controller.go:504-505).
So a worker's claim that enqueues the file while pass 1 (which applied Queued and asked for a
one-minute republish timer) is still running makes pass 2 run at once, possibly from a cache
that does not yet show Queued; pass 2 then changes nothing, sets no timer, and is never woken
by its own status update (S1'). The loop therefore keeps an **applied-status memo**, per UID,
in memory: the normalised status of its last landed apply and when it landed. While the cached
status differs from the memo and the memo is younger than `staleWindow` (30 s), the view is
stale: the planners still run (a change they want conflicts and requeues as above), but a
skipped apply ends the pass with `RequeueAfter` from the same 1 s-to-30 s backoff and outcome
`stale`, its `Observe` is skipped, and its `Due` timers are kept. The memo entry is dropped
when the cache shows the memoed status, when it expires, and on NotFound. Only the loop writes
MediaFile status, so a cached status unequal to the memo is never newer than it (the
apiserver's pruning of a removed field, as in §7.4, matches by expiry).

**The status-write limiter.** `--remediation-bulk-writes-per-second` (default 25, burst 50,
0 disables) is a token bucket in front of every MediaFile status apply except those a pass
makes because of: a held admission grant (dispatch, window or graft slot), an incorporated
worker answer (a `dispatch.answeredSeq` or probe incorporation it raised), a handled intent
nonce, or the first probe of a file never probed. Everything else, which is how a profile
fan-out (S12, S13), a planner-version change, release N's first render of every file and
§7.3.8's release reach the apiserver, waits for a token: a pass refused one ends with
`RequeueAfter` = the token's wait and runs no effects. The exemption is decided from the pass's
own content, because Reconcile is not told the priority its key was enqueued at
(controller-runtime v0.25.1 passes it only to the requeue, pkg/internal/controller/controller.go:464-483).
At 25/s, every MediaFile written once takes about 8 minutes.

**The status is complete by construction.** `statusApply` marshals the whole
`catalogv1alpha1.MediaFileStatus` to JSON and unmarshals it into
`catalogac.MediaFileStatusApplyConfiguration`. Every field the stored object holds is declared on
every apply, so no branch, early return or planner failure can release one, and conditions and
atomic lists are set once (no `With*` append doubling). `TestStatusApplyIsLossless` fills every
field and compares the JSON both ways.

**A pass that changes nothing writes nothing.** About 95% of status applies today are no-ops:
198,801 status APPLY 200s against at most 9,144 stored MediaFile changes in 26.5 h (api research).
The skip relies on `normalize` (lists sorted by key, nil and empty alike), `v.Now` in whole
seconds, and planners stamping time only on a transition (§2.11.1).

**Every string is sanitised before marshalling** (§2.11.1 rules 1-3).

**The field manager is `catalogarr`,** the live owner of MediaFile status on all 11,958 objects,
so no ownership moves (§9, D21).

### 3.8 Effects, after the apply (one outbox order for every remediation)

The status is written first, and then the records and messages it implies:

1. The status apply lands, or is skipped because the status already says it.
2. Each planner's effects run in `Order`, each planner's in its own order:
   - `RecordWrite{Bucket, Key, Revision, Value}`: `Create` when Revision is 0, else
     `Update(Revision)`, at the revision `Gather` read.
   - `Publish{Subject, MsgID, Payload}`: the Msg-Id carries the remediation's sequence.
   - `Withdraw{UID, Seq}`: a `withdrawn` record write, then `PurgeSubject` of the file's task
     subjects (and, for transcode, the lease cancel marker, §5.10).
   - `CreateGraftJob{...}`: AlreadyExists counts as done (§4.12).
   - `LedgerConfirm{UID, Seq}` and `LedgerRelease{UID}`.
   - `History{...}`: `clustarr.evt.*`, on transitions only.
   - `Observe{...}`: the transcode metrics, on the terminal edge (R9).
3. The planners' Kubernetes `Events` are emitted.

**Why status comes first, for every remediation.** After a crash the admission ledger is rebuilt
from status (ADR-0016), so a transcode or graft dispatch published before its Queued write
landed would be missing from the rebuilt ledger, which would over-admit slots or GPUs (today's
R16 case, results.go:98). Using the same order for the probe, subtitles and markers gives one
effect executor instead of two. This amends split §6.5.2/6.5.3, whose probe order is record,
publish, then ProbePending; the judge's rows are unchanged, only their position relative to the
apply moves (§8).

**Effects are owed until done, not fired once.** Every effect except Events and History is
derived from status plus record, not from the transition. A block that says "dispatched seq N"
whose record does not show seq N emits the `RecordWrite` and the `Publish` again on every pass,
so a crash or bus failure between steps 1 and 2 heals on the next pass. JetStream's 1 h dedup
window absorbs a republished task within `records.RepublishWindow` (50 min), and the record CAS
absorbs a repeated record write. As a safety net, a record found ahead of status (a `requested`
record with a higher Seq and the same inputs) is adopted, never re-requested (§4.7).

**When an effect fails:** a record CAS miss (`ErrKeyExists`, `ErrRevisionMismatch`) skips the
rest of that planner's effects and requeues the file in 1 s; a failed publish is transient and
uses the §3.6 backoff; the other planners' effects still run.

**The probe under this order.** ProbePending is applied before `Store.Request` runs. If the
process crashes in between, the file is left ProbePending with the same record; the next pass
judges again and requests under the same Msg-Id. A version-only request changes no status
field, so it still writes nothing to etcd, as split §6.5.3 intends.

**Events and History stay best-effort,** as today's `afterWrite` (transcodejob/controller.go:322-343):
emitted on the transition from `Prev` to the applied status, not retried if lost after the apply.

### 3.9 Actuators

Actuators run after the effects, against the applied status.

**Replay.** If the file carries `clustarr.io/replay`, the actuator calls `replay.Replayer.Replay`
on the file's metadata, which republishes the dead letter and clears the annotation with its JSON
patch (app/catalog/history/replay.go:302). MediaFile does not join `ReplayKinds` as a seventeenth
`replay-<kind>` controller, because that would be a second MediaFile watcher.

**Rename** (`app/remediation/rename`), today's `rename.Reconcile` (rename/controller.go:99-147)
behind a pure gate:

- **When it runs.** `rescan.Renameable(applied)` (rescan/rename.go:70-75); `status.transcode.phase`
  and `status.graft.phase` terminal or absent; `renameMode` not off; and `openScanOver` finds no
  LibraryScan walking the folder. If one is, the file requeues in 1 m (`recheckAfter`,
  rename/controller.go:77).
- **What it does.** `mediafilespec.RenameFile` (formerly `rescan.RenameFile`, split §5.14
  item 3): re-reads the file uncached through the APIReader (the loop's only uncached GET, once
  per rename attempt), moves the file on disk (sidecars included, enumerated on disk, §2.7), and
  applies the spec under `importarr-worker` with that read's resourceVersion
  (rescan/mediafile.go:613-670).
- **What follows.** The spec apply bumps generation, S1 wakes the file, and the next pass
  incorporates the path-only change without a probe (split §6.5.3) and lists the directory for
  subtitles (§6.4.3).
- **Why after the status apply.** A status write after the move in the same pass would conflict
  with the new resourceVersion.
- **Events** stay `Renamed`, `ScanInProgress`, `Collision`, `Changed` and `Held`, recorder
  `rename`.

### 3.10 Main resource and spec takeover

`renderMain` sends one `k8s.Apply` under `catalogarr`, preconditioned on the view's
resourceVersion. It declares:

- the seven `catalog.clustarr.io/*` labels, from `mirrorLabels` over the draft's mediaInfo
  (labels.go:32-52);
- after a transcode swap (`spec.original` false, or this pass incorporates a swap): `spec.path`,
  `sizeBytes`, `modTime` and `original=false`;
- for a grafted original (the flat `status.graftTag` is set, in the draft, and `spec.original`
  is true): `spec.path`, `sizeBytes` and `modTime`, but not `original`.

These are today's three cases (mediafile_controller.go:337-369). Once catalogarr owns the
takeover fields, every main apply re-sends them with current values. **The grafted-original
case is keyed on the persistent flat `status.graftTag`, as today (`original &&
known.GraftTag != ""`, mediafile_controller.go:342), never on the `status.graft` block**: the
block exists only under §2.6's conditions and goes when the profile stops grafting or the probe
hash moves on, and a main apply that then omitted `sizeBytes` and `modTime` would release them.
A forced takeover leaves `catalogarr` their sole owner (live managedFields: on the 171
transcoded files `catalogarr` owns spec `modTime` and `sizeBytes` and `importarr-worker` does
not), and both are optional (mediafile_types.go:137-144), so the release would delete them from
spec. `TestAGraftedOriginalKeepsItsSpecWhenTheGraftBlockGoes` (envtest, an object already in
steady state, asserting `managedFields`) holds it. The probe planner computes
the takeover in `Result.Main` (the swap target's path from the transcode block's
`result.outputPath`, size and mtime from its stat). The apply runs only when the rendered labels
or takeover values differ from the cached object, so in steady state it never runs. A Conflict
ends the pass with `RequeueAfter: 1s` and no status apply. `spec.path` stays co-owned with
`importarr-worker`, which renames transcoded files, as today. The MP4 standard makes every output
`<stem>.mp4`, so the path takeover is the normal case.

### 3.11 Scheduling

`RequeueAfter = max(min(Due of all planners, actuator wakes) - Now, 1s)`. There is no periodic
resync. These timers move into `Due`:

- **probe:** split §6.5.3's timeouts and retries, and today's 24 h `TranscodedRecheckInterval`
  (:483);
- **naming:** a 30 s retry;
- **transcode:** `nextAttemptAt`, republish and lost-worker checks (§5.8, §5.9);
- **graft:** waits, backoff and `graftJobGrace`;
- **subtitles:** the earliest `nextSearchAt` and outstanding timeouts, floored at 30 s
  (subtitlerequest/reconciler.go:743-752);
- **markers:** `markers.Due`'s interval (mediafile_controller.go:509-511).

`Again` requeues in 1 s. A Conflict or a stale view requeues on the per-key backoff of §3.7
(1 s doubling to 30 s, reset by a clean apply); five in a row on one key are logged and
counted. A records-pacer wait (§4.7) and a write-limiter wait (§3.7) requeue at their own slot
times.

**Priorities.** Every return sets `Result.Priority` explicitly:

| Priority | Used for |
|---|---|
| `admission.GrantPriority` = `PriorityUser` = 100 | admission grants and directives enqueued to files; S1 updates caused by an intent annotation |
| `0` | watch events |
| `PriorityTimed = -50` | requeues from `Due` |
| `handler.LowPriority = -100` | initial lists; the S12/S13 profile fan-outs |

Neither a wave of due subtitle searches nor a profile edit's 12,000-file fan-out can delay a
user's action, a new import or a dispatch.

### 3.12 The item path

- Each of the six item packages keeps its reconcile body, renamed
  `ReconcileItem(ctx context.Context, nn types.NamespacedName) (ctrl.Result, error)`.
- `SetupWithManager` is deleted. `Watches() []rollup.Watch` returns the watches on everything
  except MediaFile, as `{Object, Map handler.MapFunc, Predicates}`, and the loop adapts them to
  `Key`. `rollup.Watch` is a new type in `app/catalog/controller/rollup`, so the item packages
  never import `app/remediation`.
- **The MediaFile and AudioGraft watches go.** S1' replaces them with `FileSignature`; graft
  state is read from MediaFile `status.graft` and the item's own donor (§2.6).

**Status writes go through `applyItemStatus`:** `k8s.PatchStatusCAS` under `catalogarr`, a
`viewReader` of the cached item, 1 attempt; a Conflict requeues the item in 1 s.

- **Write sites converted:** movie :456, :486, :660; episode :603; album :472, :491, :506, :625;
  book :474, :495, :590; audiobook :440, :461, :562; issue :439.
- **Each still declares the whole status**, through movie's `reassertKnownStatus` (:723-750)
  and its siblings.
- **Why CAS.** `catalogarr-grab` and `catalogarr-metadata` also write items, and
  `catalogarr-series` writes Episodes. An apply from a stale cache would otherwise roll back
  fields the loop seeds from status, such as `addOptionsApplied` and `availableAt`.
- **Item applies are never skipped.** The loop does not own the whole item status, and the cache
  strips `managedFields`, so it cannot compare its own field set. About 16,500 item applies per
  manager start remain, as today, now with a resourceVersion precondition.

**Album's track sync leaves the hot path.** It goes through `album.ReleaseCache`, an in-memory
LRU of 4,096 entries keyed by (Album UID, `spec.releaseGroupID`, `status.metadata.refreshedAt`),
so the RPC runs only when the metadata changed
(`TestAnAlbumReconcileWithoutAMetadataChangeMakesNoRPC`). On a miss the RPC timeout drops from
30 s (`metadataSyncRPCBackoff`, album/reconciler.go:97) to 10 s; a failure keeps the tracks and
requeues in 30 s (:629-631). The `status.metadata.selectedReleaseID` leaf stays co-owned with
`catalogarr-metadata` (album :573, :685, :706).

**The item path also derives `status.audio.donor` and appends `rejectedReleases`** (§2.6, §2.14).
There is no item-keyed record and no item-keyed task: the donor is reduced at import (§4.12).

**Unchanged:** finalizers, metadata and artwork publishes, item Events (recorders `movie`,
`episode`, `album`, `book`, `audiobook`, `issue`).

### 3.13 Admission and the profile reconcilers, as the loop sees them

- The transcode planner reads grants and directives from `v.Ledger` and renders them; the loop
  calls `ledger.Observe` after every file pass and `ledger.Forget` on NotFound. Admission itself
  is the `transcode-admission` controller (§5), which enqueues file keys through S22.
- The slim `transcodeprofile` and `subtitleprofile` reconcilers (§2.13) are woken by
  `ledger.profileWake` and `subtitleProfileWake`, which S1' and the ledger signal; they count
  through the indexes of §3.16.

### 3.14 Field managers

| Object and fields | Manager | Write site |
|---|---|---|
| MediaFile status, all of it | `catalogarr` | `remediation.applyStatus`, the only `PatchStatus*` call made with a MediaFile apply configuration |
| MediaFile labels and spec takeover (§3.10) | `catalogarr` | `remediation.applyMain` |
| MediaFile spec on rename | `importarr-worker` | `mediafilespec.RenameFile`, from the rename actuator |
| Movie, Episode, Album, Book, Audiobook, Issue status | `catalogarr` | `applyItemStatus` |
| Item finalizers | `clustarr` (the default owner, split §5.3.5) | `k8s.EnsureFinalizer` (pkg/k8s/finalizers.go:83) |
| Graft Jobs | `clustarr` (create-only) | the graft planner's `CreateGraftJob` effect |
| TranscodeProfile status | `squasharr` | slim `transcodeprofile` |
| Pool Jobs | `squasharr-pool` | slim `transcodeprofile` (`pool.Render`) |
| SubtitleProfile status | `captionarr` | slim `subtitleprofile` |
| MediaFile status, empty release apply (release N only) | `catalogarr-markers` | §7.3.8 |

- **No manager name is added or renamed (R7),** except `clustarr-legacy-fold` for release N's
  annotation carry-over (§7.3.5).
- `ManagerCatalogarrMarkers` and `ManagerCaptionarrWorker` move from `k8s.FieldManagers()` into a
  new `k8s.RetiredFieldManagers()`; `Validate` still accepts `catalogarr-markers` in release N for
  the one release apply.
- `TestEveryFieldManagerHasItsHome` (split §5.15) is updated to this table.

### 3.15 Leader-only operation

- The loop is a controller, so it runs only on the holder of `manager.clustarr.io` (split §5.8).
  Its sources (the six records sources and S22) start only there. The admission ledger exists
  only in that process.
- Losing the lease exits the process. The next leader's admission controller rebuilds the ledger
  before it grants anything (§5.6).
- Nothing in the loop runs on every replica. A second manager pod during a rollout (`maxSurge 1`)
  runs none of it.
- ADR-0016's revisit trigger stands: more than one active leader, or sharding, breaks the ledger.
- The records sources do not replay at start; the informer's initial list reconciles every file,
  and every pass reads the records of its outstanding blocks (§4.9).

### 3.16 Cache indexes

All are registered once by `remediation.RegisterIndexes`, from `app/remediation/manager.Register`.

**New, on MediaFile** (`app/remediation/mfindex`):

| Name | Values | Serves |
|---|---|---|
| `remediation.mediafile.item` | `<kind>/<name>` for `mediaRef.name` and every `mediaRef.keys` entry | replaces nine indexes (below); covering `keys` adds wake-ups for the second episode of a multi-episode file, which the caption index missed |
| `remediation.mediafile.uid` | `metadata.uid` | the task sweep; resolving a record with no usable ref |
| `remediation.mediafile.transcodeProfile` | `<profile>` and `<profile>/<phase>` | the TranscodeProfile reconciler's counts |
| `remediation.mediafile.subtitleProfile` | `<profile>` and `<profile>/<phase>` | the SubtitleProfile reconciler's counts |
| `remediation.mediafile.subtitles` | `true` when the subtitles block exists | S14 |

`remediation.mediafile.item` replaces `.spec.mediaRef.movie` (movie:139),
`.spec.mediaRef.episode` (episode:142; exported as `MediaFileByEpisodeIndex`, which now points at
it so the segment planner keeps working), `.spec.mediaRef.album`, `.spec.mediaRef.book`,
`.spec.mediaRef.audiobook`, `.spec.mediaRef.issue`, `mediafile.clustarr.io/owner`
(watch.go:152), `captionarr.spec.mediaRef` (itemindex.go:89) and
`librarydelete.spec.mediaRef.target` (kinds.go:40).

**Kept, now registered by the loop, names unchanged:** Download
`.spec.target.{movie,episode,album,book,audiobook,issue}`; `.spec.qualityProfileRef` on Movie,
Series, Album, Book and Audiobook; Audiobook `.spec.bookRef`;
`mediafile.clustarr.io/movie-rootfolder` and `mediafile.clustarr.io/series-rootfolder`.

**Consolidated.** Three identical Episode-by-series indexes exist (split §5.7): `.spec.seriesRef`
(series:153), `episode.spec.seriesRef` (episode:160) and `mediafile.clustarr.io/episode-series`
(watch.go:152). The loop uses `series.EpisodeBySeriesRefIndex`; the other two are deleted.

**Switched to `remediation.mediafile.item`:** the segment planner (segmenting/plan.go:162, now
`app/catalog/segmentplan`) and librarydelete (its `librarydelete.parent` indexes on items stay).

**Kept in release N for adoption only, deleted in N+1:** TranscodeJob and SubtitleRequest
`spec.mediaFileRef`, AudioGraft `status.mediaFileRef` (mediafile_controller.go:754-765, taken
over by `legacyfold.RegisterIndexes`, §7.3.2). **Deleted with their controllers:**
`squasharr.transcodejob.spec.mediaFileRef` and `.spec.profileRef` (transcodejob/controller.go:912,
:921), AudioGraft's `spec.itemRef.name` index.

No cache index on `status.transcode.phase` is registered: the admission ledger rebuilds through
the APIReader and is itself the in-memory index (§5.6). Every informer the loop needs is in the
merged manager cache (split §5.6), whose MediaFile informer no longer strips `mediaInfo`.

### 3.17 Throughput

**Concurrency.** One queue with 16 workers replaces today's 20, of which 14 carry the load
(mediafile's 8 plus one per item kind).

**Cost of a file pass that changes nothing:** no apiserver request; cache reads; one or two
`os.Stat` calls on `/data`; KV Gets only for outstanding requests, due dispatches and the two
unsolicited cases; `os.ReadDir` only when the subtitles listing gate opens (§6.4.3); one JSON
marshal and compare.

**What leaves the shared queue:** the Album RPC (§3.12); admission's uncached Lists (§5.7); the
two profile controllers' whole-library pass on every probe change (transcodeprofile Lists every
profile, MediaFile, TranscodeJob, Movie, Episode and AudioGraft, controller.go:149-194;
subtitleprofile likewise, controller.go:134-171).

**Initial pass at start:** 11,958 file keys plus about 16,500 item keys, all at `LowPriority`.
Today the 15,553 Episodes go through one worker.

**First deploy of each fold step, in stored writes.** No-op applies write nothing to etcd, so
the comparison that matters is with stored changes: at most 9,144 stored MediaFile changes in
26.5 h today (about 345 an hour). Release N's first leader start stores one write per file for
the new subtitles block (11,958), about 3,000 adoptions and §7.3.8's release (11,958 more,
the claim rides the first write); that is about 27,000 whole-object writes, each sent to every
MediaFile informer (the manager, the ui, cluster-plex, every import-agent replica), or about 78
hours of today's stored changes. The status-write limiter (§3.7) spreads them at 25/s, about 18
minutes, instead of the loop's full concurrency inside the startup flood: the kind control
plane shares one NVMe with etcd, and WAL fsyncs past one second have already cost grabarr its
lease and the kube-apiserver a SIGKILL there (2026-09-24). The runbook watches etcd's WAL fsync
latency through it (§7.5).

**The I/O executor.** Every `/data` call a planner's `Gather` makes runs on
`remediation.IO`: `--remediation-io-workers` (default 8) goroutines, a per-call timeout (stat
5 s, `ReadDir` 20 s), and a breaker that opens after 5 timeouts in 30 s and half-opens after
60 s. A call that cannot get a worker at once, or meets an open breaker, is
`remediation.Transient` (§3.6). `/data` is NFS on the live cluster (`10.0.50.1:/volume2/data`),
and neither a context nor a timer can interrupt a blocked `os.Stat` or `os.ReadDir`; the pool
bounds how many goroutines a hung mount can pin to the pool size, so the loop's 16 workers,
item keys and admission grants keep running. Today no item reconciler does filesystem I/O (the
six item packages import `os` only in tests). `TestAHungMountNeverStallsItemsOrGrants` gives
Gather a stat that blocks forever and asserts that item keys and a granted file still reconcile
and that the goroutine count stays bounded.

**Bounds:** `ReconciliationTimeout` 5 m; per-planner `Gather` deadlines; `RecoverPanic` on;
workqueue metrics under `controller="mediafile"` cover both key kinds.

### 3.18 What becomes of every current watcher

| Today | Where | Fate |
|---|---|---|
| catalogarr `mediafile` | mediafile_controller.go:715-748 | Dissolved into the loop (below); the controller name survives as the loop's |
| catalogarr `movie`, `episode`, `album`, `book`, `audiobook`, `issue` | movie:179-186, episode:228-236, album:192-198, book:173-179, audiobook:174-180, issue:145-151 | Item path (§3.12) |
| importarr `rename` | rename/controller.go:342-344 | Rename actuator (§3.9), same gates, Events and field manager; `Predicate()` (:288-307) deleted |
| squasharr `transcodeprofile` | controller.go:650-670 | Selection, eligibility and the already-transcoded rule to `transcodeplan`; the window to admission; status and pools to the slim reconciler, which loses its MediaFile, TranscodeJob, Movie, Episode and AudioGraft watches; job creation and retention deleted |
| squasharr `transcodejob` and `ResultsConsumer` | controller.go:936-946; results.go:42-66 | Plan, dispatch, fail, suspend, dead-letter and `Decide` to the transcode planner; `(admission)` to `transcode-admission` (§5); results become `clustarr-transcodes` records; the withdrawal finalizer replaced by `Forget`, `withdrawAll` and the sweep |
| squasharr `audiograft` | controller.go:132-138 | Target half to the graft planner; donor intent to the item path; reduce to fileimport; concurrency to the ledger |
| captionarr `subtitleprofile` | controller.go:455-466 | Selection and wanting to `subtitleplan`; status to the slim reconciler, which loses its MediaFile, Movie and Episode watches |
| captionarr `subtitlerequest` | reconciler.go:792-803 | Becomes the subtitles planner; the liveness protocol (`status.IsLive`, app/caption/status/status.go:75-77) and `workerSignal` retire; the worker's CAS writes (fetch/apply.go:301) become `clustarr-subtitles` records; `spec.forceSearch` becomes `subtitle.clustarr.io/search` |

**How the catalogarr `mediafile` controller dissolves:**

- **To the probe planner:** `evaluateProbe` and `judgeProbe`; swap, kept and graft incorporation;
  FileMissing; Ready; the `Probed` Event.
- **To the naming planner:** `renderNaming`.
- **To the markers planner:** `followUpMarkers` and `planSegments` (:493-534).
- **To `renderMain`:** `mirrorLabels` and the spec takeover (:337-369).
- **To S16:** `probeRecordsSource` (split §6.5.3), as `recordsource.New(bus, events.BucketProbes, …)`.
- **Deleted, each as its kind folds:** `sidecarsFromSubtitleRequest`, `scanSidecars`,
  `transcodeJobsOf`, `latestUnincorporatedTranscode`, `transcodeProfileTag`, and the TranscodeJob,
  AudioGraft and SubtitleRequest watches.

**Readers and writers that are not controllers:**

- **The segment planner consumer** (catalog/run.go:456-467) stays a NATS consumer in the manager,
  as `app/catalog/segmentplan` (§8, W2.9); only its index changes.
- **The TheIntroDB handler** (markers/handler.go:289) **and segment results**
  (segmenting/setup.go:54) write `clustarr-markers` and `clustarr-segments` records, not status.
  `segmenting.Applier.ApplyMerged` is deleted; its merge, `segments.Merge`
  (pkg/segments/merge.go:47), becomes the markers planner's.
- **The DLQ projector** annotates MediaFiles; S1 folds the annotation.
- **librarydelete** changes only its index.

### 3.19 Observability

- **Spans:** `remediation.Reconcile` with `key.kind`, `key.namespace`, `key.name`; a child per
  planner phase, `remediation.<planner>.gather` and `remediation.<planner>.plan`.
- **Metrics** (`clustarr_` prefix, base units, never labelled by title or path):
  `clustarr_remediation_passes_total{kind,outcome}` (outcome `applied`, `unchanged`, `conflict`,
  `stale`, `paced`, `error`); `clustarr_remediation_planner_failures_total{planner,reason}`;
  `clustarr_remediation_planner_seconds{planner,phase}`;
  `clustarr_remediation_effects_total{planner,effect,outcome}`;
  `clustarr_remediation_io_calls_total{op,outcome}` (outcome `ok`, `timeout`, `saturated`,
  `breaker_open`). **Every new family in this design lives in `pkg/obs/metrics`**, declared
  through `newCounterVec`/`newGaugeVec`/`newHistogramVec` (pkg/obs/metrics/metrics.go:62-83), so the catalogue test
  `TestCatalogueMatchesTheAmendmentTable` (pkg/obs/metrics/metrics_test.go:110-125), which
  demands exactly the series amendment §A2.3 tables, covers them: these families, §4.15's
  record families, §5.16's transcode families and §7.3.11's legacy-fold families. Each task
  that adds a family also adds it to `wantSeries`, to amendment §A2.3
  (docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md:276-305) and to
  docs/observability.md's metric table (§8.6).
- **Events:** file Events use recorder `mediafile`, with `action` the planner's name; `rename` and
  the six item recorders keep their names.

### 3.20 Guards

- `TestOnlyTheLoopWritesMediaFileStatus` (AST, `test/guards`): no `PatchStatus`, `PatchStatusCAS`
  or `k8s.Apply` with a `catalogac.MediaFile` configuration outside `app/remediation`, except
  `mediafilespec`'s spec applies and the agent importer's.
- `TestOnlyTheLoopWatchesMediaFile` (`test/guards/mediafilewatch_test.go`): no `For`, `Watches` or
  `source.Kind` of MediaFile outside `app/remediation`. Its allow-list starts with today's other
  watchers, each tagged with the F task that removes it, and is empty at F8.12 (§8). It also
  allows exactly two MediaFile-driven channel sources outside the loop, `ledger.profileWake` and
  `subtitleProfileWake` (§1 item 2, D3), and fails on any other `source.Channel` fed from the
  loop's MediaFile handlers.
- `TestPlannerOrder`, `TestPlannerCopyPartitionsTheStatus`, `TestStatusApplyIsLossless`,
  `TestNoPlannerPlansFromAPendingProbe`.
- Behaviour (envtest, each on an object already in steady state):
  `TestAnUnchangedPassAppliesNothing` (counts apply requests);
  `TestOwnStatusWritesNeverEnqueueTheFile`; `TestAPanickingPlannerKeepsItsBlock` (the others'
  fields moved, the panicking planner's block did not, `managedFields` shows only `catalogarr`,
  `<Planner>PlannerError` True); `TestATransientErrorWritesNothing`;
  `TestAWriterBetweenGatherAndApplyIsNotRolledBack` (a real annotation patch interleaved between
  gather and apply conflicts and replans); `TestAMultiEpisodeFileWakesEveryEpisode`;
  `TestC1ControlsNeverReachTheApply` (U+0096 in every block);
  `TestAClaimArrivingBeforeTheCacheSeesQueuedIsIncorporated` (the informer delayed, a claim
  written during pass 1: pass 2 is stale, requeues, and a later pass incorporates the claim);
  `TestAUserLabelReplansTheFile`; `TestAProbeThatChangesTheCodecPicksTheWinnerInTheSamePass`;
  `TestAGraftedOriginalKeepsItsSpecWhenTheGraftBlockGoes`; `TestBulkWritesArePaced` (1,000
  files re-rendered by a profile edit store at the limiter's rate while a granted file and an
  answered record apply at once); `TestAHungMountNeverStallsItemsOrGrants` (§3.17).
- Start envtest: runs the loop under the manager role only.

---

## 4. Workers report through records

ADR-0016: "Every remediation follows the probe protocol. The worker writes a compare-and-swap
record in a Durable KV bucket keyed by file UID and remediation, fenced by a sequence number the
loop issued with the task. A raw source enqueues the file, and the loop incorporates the record in
its one apply."

This section turns split §6.5 (the probe protocol, still unbuilt) into one package, fixes two weak
points (the sequence restarts when a record's TTL expires; the watch replays every record each time
it opens), and applies the protocol to the five result paths that write Kubernetes status today,
plus the probe. Nothing here adds a field manager or a CRD writer; the loop's one
`k8s.PatchStatusCAS` stays the only Kubernetes write of any result.

### 4.1 What this replaces

| # | Result path today | Who writes, and how | How the write is fenced |
|---|---|---|---|
| 1 | TheIntroDB markers | The metadata gateway's `catalogarr-markers` handler (`app/catalog/markers/handler.go:100-168`) re-reads the MediaFile, Movie, Episode and Series uncached, then applies `status.markers` under `catalogarr-markers` (:285) through `segmenting.Applier.ApplyMerged` (`app/catalog/segmenting/apply.go:80-109`). | resourceVersion CAS plus `forProbeHash`; Msg-Id is uid + probeHash + last `fetchedAt` (`app/catalog/markers/due.go:96-105`). |
| 2 | Segment analysis results | `segmentarr-worker` publishes `SegmentsResult` (`app/segments/worker/worker.go:351-370`); the `catalogarr-segments-result` consumer (`app/catalog/segmenting/results.go:42-70`) does an unconditional `KV.Put` into `clustarr-segments` (`apply.go:126`), then the same `ApplyMerged`. | No CAS on the KV write. The result's Msg-Id carries no sequence (`worker.go:358`) and `env.ID` becomes the Msg-Id (`pkg/events/bus.go:78-87`), so a season amendment (`withIntro`, `worker.go:143`) inside the stream's 1 h `Duplicates` window is absorbed as a duplicate (inferred from code, not reproduced). |
| 3 | Transcode | Workers publish `StatusEvent`s (claimed, progress about every 10 s, finished; `app/squash/worker/serve.go:380,384,452-464`) to `clustarr.work.transcode.result.<jobUID>`; the leader-only `squasharr-transcode-results` consumer (`app/squash/controller/transcodejob/results.go:42-66`; MaxAckPending 1, `pkg/events/topology.go:856-861`) writes TranscodeJob status through `writeStatus`. | Job UID + attempt. A lost Queued write is repaired by adopting attempt N+1 (R16, `results.go:98-114`). Progress is written to etcd (`results.go:158`). |
| 4 | Graft | The graft pod writes a termination message (`cmd/squasharr-worker/main.go:169-187`); it is given no NATS (`NATS_URL` stripped at `app/squash/controller/audiograft/controller.go:676-680`). The controller lists pods through the APIReader (`:765-795`) and applies AudioGraft status with plain `PatchStatus` (`:839`). | None; the Job name embeds hash and generation. |
| 5 | Subtitle fetch | `captionarr-worker` writes per-leaf item fields through `PatchStatusCAS` (`app/caption/worker/fetch/apply.go:298-334`), under the item-liveness protocol. | resourceVersion, 8 CAS attempts to absorb sibling languages; Msg-Id carries the attempt or the generation (`pkg/events/subjects.go:542-558`). |
| 6 | Probe | Split §6.5, not yet built: CAS records in `clustarr-probes`. | A Seq issued by the loop. |

After the fold, paths 1–5 write no Kubernetes object at all.

### 4.2 Decisions

1. **One CAS core, `pkg/records`, and one waker, `pkg/records/recordsource`.** The split's
   `pkg/probestore` and `probeRecordsSource` are built as plan W4.4 and W4.12 write them; fold
   task F1.1 then extracts their CAS core into `pkg/records` and reimplements `probestore` on it
   with its API and the probe record's JSON unchanged (golden `TestProbeRecordWireFormatIsUnchanged`).
   `pkg/records` imports only `pkg/events`, `pkg/events/schema` and the standard library
   (`TestRecordsLinksNoKubernetes`), so `cmd/transcode` and `cmd/markers` link it without
   controller-runtime. `recordsource` (controller-runtime) is manager-only.
2. **One Durable bucket per remediation.** The key is the file's UID token, plus a langKey token
   for subtitles. The bucket name carries the remediation.
3. **The loop is the only issuer of `Seq`.** `records.NextSeq` is monotonic even across TTL
   expiry.
4. **One write order:** status, then record, then publish (§3.8). A record found ahead of status
   is adopted.
5. **One table of answer rules,** checked by the worker on a fresh re-read immediately before its
   CAS. A transcode or graft that has already changed the file on disk is a fact, recorded even if
   the request was withdrawn, and incorporated even after the withdrawal closed the dispatch: the
   planner keeps reading the record for `records.FactWindow` (§4.10), and nothing overwrites an
   unincorporated fact (§4.7).
6. **The waker only enqueues.** It skips the loop's own writes, opens with `UpdatesOnly`, and
   reopens from the last revision it saw. Records are always read inside `Reconcile`.
7. **Records are an inbox; status is authoritative.** Nobody deletes an inbox record; a 7-day TTL
   retires it. The exception is `clustarr-segments`: its records live forever and are swept when
   their UID dies.
8. **Segment analysis and markers are fenced by their inputs,** not by a status sequence: analysis
   is planned per season and amends itself unasked, and `status.markers` must stay frozen. A stated
   exception (§9, D7).
9. **No v1 shims.** The two results durables and their subjects are retired behind a drain gate,
   and every task payload moves to v2 (§9, D25).

### 4.3 Buckets

Every bucket shares `History: 1`, `Durable: true`, `Replicas: 3` (1 under `ForSingleNode`),
`LimitMarkerTTL: 5m`, and the new `BucketSpec.Records: true`. KV streams use `DiscardNew`
(nats.go `jetstream/kv.go:690`), so a full bucket refuses the write instead of evicting live
records.

| Constant (`pkg/events/subjects.go`) | Name | Key | TTL | MaxBytes | MaxValueSize | Requests by | Answers by |
|---|---|---|---|---|---|---|---|
| `BucketProbes` (split) | `clustarr-probes` | `RecordKey(uid)` (= `ProbeKey`) | 7 d | 256 MiB | 256 KiB | the loop | `agent --domain import` (probe worker; `Seed` from fileimport and rescan) |
| `BucketTranscodes` | `clustarr-transcodes` | `RecordKey(uid)` | 7 d | 128 MiB | 32 KiB | the loop | `cmd/transcode` pool pods |
| `BucketGrafts` | `clustarr-grafts` | `RecordKey(uid)` | 7 d | 16 MiB | 16 KiB | the loop | `cmd/transcode --graft-task` Job pods |
| `BucketSubtitles` | `clustarr-subtitles` | `RecordSubKey(uid, langKey)` | 7 d | 128 MiB | 8 KiB | the loop | `agent --domain caption` |
| `BucketMarkers` | `clustarr-markers` | `RecordKey(uid)` | 7 d | 64 MiB | 16 KiB | the loop | `agent --domain metadata` |
| `BucketSegments` (exists, `topology.go:904`) | `clustarr-segments` | `RecordKey(uid)`, the same bytes as today's `KVKeyToken(uid)` | 0 (kept) | 128 MiB (new) | 16 KiB (new) | none (no per-file request) | `cmd/markers`; the manager's `segmenting.Sweeper` deletes records of dead UIDs |

**Why one bucket per remediation, not one shared inbox:** TTL is per bucket (`KV.Update` clears a
key's own TTL, `pkg/events/bus.go:283-284`), and `clustarr-segments` must keep TTL 0 under its
live name (R7); value caps differ about 30x, from an 8 KiB subtitle answer to a 256 KiB probe
answer whose `MediaInfo` alone runs to about 153 KB at the schema's worst case; each watch replays,
reopens and is tested on its own; and each agent binds only the bucket it answers. ADR-0016's
"keyed by file UID and remediation" holds as (bucket, key).

**Sizing.** With History 1 a bucket holds one live value per key: probes 11,958 × ~3 KB ≈ 36 MB;
subtitles ~36k language keys × ~1 KB; transcodes, files dispatched in the last 7 days × ~6 KB. The
new reservations total 720 MiB. Durable buckets stay on file storage under `ForSingleNode`
(`topology.go:269-275`), outside the 64 MiB memory budget. Against the 20 GiB `max_file_store`
(`config/nats/configmap.yaml:16`; chart `nats.config.jetstream.fileStore.pvc.size`), single-node
file reservations come to artwork 5 GiB, fingerprints 1 GiB, `CLUSTARR_WORK_SEGMENTARR` 256 MiB,
`CLUSTARR_WORK_PROBE` 64 MiB plus these 720 MiB, ≈ 7.1 GiB.

### 4.4 Keys

```go
// RecordKey is a records bucket's key for one object: its UID through KVKeyToken.
func RecordKey(uid string) string { return KVKeyToken(uid) }

// RecordSubKey is the key of one of several records an object has in a bucket
// (a subtitle language): "<KVKeyToken(uid)>.<KVKeyToken(sub)>".
func RecordSubKey(uid, sub string) string { return KVKeyToken(uid) + "." + KVKeyToken(sub) }
```

- `KVKeyToken` emits only `[0-9A-Za-z-]` and is injective (`pkg/events/kvkey.go`), so a `.`-joined
  pair cannot be forged and `Watch` wildcards match one token each. `en:forced` becomes
  `en-3aforced`.
- **The UID, not namespace/name:** fileimport reuses a MediaFile's name for an upgrade to the same
  canonical path (`k8s.ChildName(..., dest)`, `app/import/worker/fileimport/process.go:363`). A
  recreated object gets a new UID, so its old records cannot answer for it.
- `events.ParseKVKeyToken(string) (string, error)` inverts `KVKeyToken` (round-trip property
  test); the segments sweeper needs it for v1 segment records, which carry no ref.
- `natsbus/kvkey_contract_test.go` gains `RecordSubKey(uid, "en:forced")` against a real embedded
  server.

### 4.5 The record (`pkg/records`)

Every record embeds one flat header, so the probe record keeps its JSON:

```go
// schema.RecordHeader is embedded (flat) in every record type.
type RecordHeader struct {
	Schema        string     `json:"schema,omitempty"`        // "records.<remediation>.v1"; absent on probe records
	MediaFile     Ref        `json:"mediaFile"`               // whose status incorporates it
	Sub           string     `json:"sub,omitempty"`           // the unescaped second key token (a langKey)
	Seq           int64      `json:"seq"`
	State         string     `json:"state"`
	RequestedAt   time.Time  `json:"requestedAt"`
	ClaimedAt     *time.Time `json:"claimedAt,omitempty"`
	DeferredUntil *time.Time `json:"deferredUntil,omitempty"`
	AnsweredAt    *time.Time `json:"answeredAt,omitempty"`
	Writer        string     `json:"writer,omitempty"`        // pod name of the last worker write
	WriterVersion string     `json:"writerVersion,omitempty"` // version.String() of that worker, for skew
	Failure       string     `json:"failure,omitempty"`       // at most 1024 bytes
	Transient     bool       `json:"transient,omitempty"`
	Failures      int32      `json:"failures,omitempty"`      // consecutive failures for the same inputs
}

// States of the new buckets. The probe keeps requested|probed|failed, its Spec
// mapping probed to answered.
const (
	StateRequested = "requested" // the loop
	StateWithdrawn = "withdrawn" // the loop
	StateClaimed   = "claimed"   // a worker started (transcode, graft)
	StateDeferred  = "deferred"  // a worker postponed to DeferredUntil (markers)
	StateAnswered  = "answered"  // a worker's answer: success or a remediation-level failure
	StateFailed    = "failed"    // no answer could be produced: a transient error's final delivery,
	                             // an oversized answer, inputs the worker cannot run
)

type Spec[R Record] struct {
	Remediation string       // "probe", "transcode", "graft", "subtitle", "markers"
	Bucket      string
	MaxValue    int          // the bucket's MaxValueSize
	Answered    func(state string) bool
	Fact        func(R) bool // an answer recording a change already made on disk
}
```

**Reading.** `Get` treats as absent, and counts in `clustarr_record_errors_total{op="decode"}`: an
undecodable value, a `Schema` other than the Spec's, or a `MediaFile.UID` whose token is not the
key's first token. Records are input to the loop, never instructions.

**Size.** Every write above `Spec.MaxValue` is refused with `records.ErrTooLarge`. Writers clamp
first: `Failure` 1024 bytes, transcode `StderrTail` 4096, subtitle `LastError` 512, markers
`Message` 512. A worker whose answer is still too large writes `StateFailed` with
`"answer exceeds <n> bytes"`.

**Two API halves,** each held to its callers by a guard (§4.15): `records.Requester[R]` for the loop
only (`Get`, `Request`, `Withdraw`); `records.Answerer[R]` for workers only (`Superseded`, `Claim`,
`Defer`, `Answer`, and `Seed` for the probe importer). Every write is `Create` when the key is
absent, else `Update(rev)`; a CAS miss re-reads and decides again, at most
`records.CASAttempts = 8` times. A miss on the loop's side returns `records.ErrRaced` and the
planner requeues.

Typed instantiations: `pkg/probestore` (the probe), `pkg/subtitlestore` (§6.6), and the transcode,
graft and markers stores in their task packages.

### 4.6 Sequence numbers

```go
// NextSeq is the Seq the loop issues for a new request.
func NextSeq(recordSeq, statusSeq int64, now time.Time) int64 {
	return max(recordSeq+1, statusSeq+1, now.UnixMilli())
}
```

**Why the time floor.** Split §6.5.2's "`Seq = prev.Seq + 1`, 1 when absent" restarts at 1 once the
7-day TTL retires a record, and a task that has waited longer in its queue could then outrank a
newer request; work streams have no `MaxAge` (`topology.go:551-563`).

**Where status keeps the sequence:** `status.lastSeq` and each `Dispatch` (§2.3), for
`status.transcode`, `status.graft` and each `status.subtitles.items[]` entry. They are needed for
the ledger and to avoid resurrecting an answer that was already incorporated. The probe and markers
keep no Seq in status: the probe is judged by its inputs (split §6.5.3); `status.markers` must stay
exactly what cluster-plex reads. Their requests use `NextSeq(rec.Seq, 0, now)`. Every Msg-Id
carries the Seq.

### 4.7 The loop's side

**One order (§3.8):** the planner issues the sequence and renders the in-flight block; the status
apply lands; then the effects `Requester.Request(key, rev, rec)` (state `requested`, the issued
`Seq`, the inputs) and the publish (Msg-Id with the Seq). For a graft, the Job create follows the
record (its name carries the Seq, so `AlreadyExists` means done).

**Owed effects.** A block in flight at seq N whose record is absent, below N, or `withdrawn`
below N writes the record and publishes on every pass until both land, for every remediation,
subtitle items included (§6.6). Two rules take precedence over it: a block that is withdrawing
seq N (§5.10, its reason set and its message `withdrawing attempt N`) emits the `Withdraw`
effect, never the request; and a record `answered` at a Seq the block has not incorporated is
incorporated first (§4.10), then the owed request follows in a later pass.

**Adoption (safety net).** A `requested` record whose Seq is above the block's and whose inputs equal
what the planner wants is adopted, never re-requested: the planner takes its Seq and republishes the
same Msg-Id.

**Republish.** While a record is `requested`, unclaimed, and `now - RequestedAt <
records.RepublishWindow` (50 min, inside the 1 h `Duplicates` window), a pass republishes with the
same Msg-Id; the duplicate is absorbed. This covers a publish that failed after the record write,
and a memory work stream lost with a NATS restart. Past the window, the remediation's request
timeout decides. A duplicate delivery is harmless, because workers check `Superseded` before
working. The same Msg-Id cannot recover a task a `DiscardOld` stream evicted, or one purged, inside
the dedupe window; §6.4.6 says how the caption and segmentarr streams republish then.

**Withdraw.** `pkg/records` has one `Withdraw(key, rev, seq)`, used by every remediation
(transcode, graft, subtitles, markers), with §5.10's semantics:

| Current record | Result |
|---|---|
| absent, or any state at a Seq below `seq` | `withdrawn{seq}`, so a task that surfaces late cannot run |
| `requested`, `claimed` or `deferred` at `seq` | `withdrawn{seq}` |
| `withdrawn` at `seq` | nothing (done) |
| `answered` or `failed` at `seq` | nothing; the planner incorporates the answer instead |
| any state above `seq` | nothing (a newer request owns the key) |

An `answered` record below `seq` that carries an unincorporated fact is not overwritten either:
`Withdraw`, like `Request`, refuses it with `records.ErrUnincorporatedFact`.

**Facts are never overwritten.** `Requester.Request` and `Requester.Withdraw` refuse to replace
a record that is `answered` with `Spec.Fact` true at a Seq the caller has not incorporated
(`records.ErrUnincorporatedFact`); the caller states what it has incorporated with
`records.IncorporatedThrough(seq)`, which the transcode and graft planners pass from their
block (`dispatch.answeredSeq` when `dispatch.withdrawn` is false). A refusal is a CAS miss: the
planner requeues, and its next pass reads the record and incorporates the fact (§4.10).
§6.6 already says the same for subtitles, whose answers are all facts.

**Pacing.** `records.Pacer` is a leader-local admission queue per remediation over new requests
(not republishes): probe low lane 600/min, subtitle 120/min, markers 120/min; the probe high
lane, transcode and graft are bounded by the import rate and the admission ledger. It holds a
**per-key reservation**: `Reserve(remediation, key)` returns the key's slot time, reserving the
next free slot only when the key holds none, so a requeued pass finds its own slot and never
reserves again; `Consume` releases it when the request's apply lands, and a reservation not
consumed within 10 minutes of its slot lapses. A planner whose slot is in the future returns
`Due` = the slot plus up to 5 s of jitter, and its adapter checks the reservation **before**
any I/O that serves only the dispatch: the subtitles listing (gate 1 of §6.4.3) and the record
reads of keys that are due but not in flight. So 1,000 due files at 60/min make about 1,000
passes in all, not one per file every 30 s. This guards the burst classes in CLAUDE.md (the
2026-10-01 discard-oldest loss and the 2026-10-06 indexer burst) after a records-bucket loss, a
migration, or a profile edit that makes every file due at once. Subtitles add a queue-depth
gate on top (§6.4.6). `TestThePacerReservesOneSlotPerKey` holds both properties.

**KV errors** in a planner's `Get` change no condition (§3.6): the planner keeps its last block and
returns a transient error, and the per-key rate limiter backs off. They show in
`clustarr_record_errors_total{remediation,op}`.

### 4.8 The worker's side

The worker re-reads the record immediately before its CAS, after any slow work (the lost-update
rule); `Answer` performs the re-read itself.

| Current record (task has Seq S) | `Superseded(S)`, checked before work | `Claim` / `Defer` | `Answer` |
|---|---|---|---|
| absent: expired, or the bucket was lost | false | **no write**: the worker naks with a 30 s delay and does not start (the loop's owed write recreates the record within a pass, §4.7); on the last delivery it acks | write, with Seq S and the task's inputs: work already started |
| Seq < S: the loop's record was lost or not yet rewritten | false | as for absent | write |
| Seq = S; `requested` | false | write | write |
| Seq = S; `claimed` or `deferred` | false | write only for a redelivery of this task with no live lease (transcode) or past `DeferredUntil` (markers) | write |
| Seq = S; `answered` or `failed` | true | drop: duplicate delivery, the first answer stands | drop |
| Seq = S; `withdrawn` | true | drop | drop, **except** an answer for which `Spec.Fact` is true (a transcode `succeeded`, a graft `Succeeded/Grafted`): it writes `answered`, because the file has already changed |
| Seq > S | true | drop (log; ack the task) | drop, **except** a fact, which writes nothing and is logged at Warn with the task's Seq and inputs; the loop recovers it from disk (§5.9, `FileMissing` with the record's `Inputs.OutputPath`) |

So a worker **starts** work only on a record `requested` at its Seq, or `claimed` at its Seq
with no live lease (a redelivery). That is the rule §5.8's dispatch-once argument rests on.

**A fact can land after its withdrawal.** The loop withdraws Running work whose lease is live
(Cancelled, Suspended, Rerouted, Orphaned, DeadlineExceeded, §5.10), and the worker re-asserts
its lease only once, immediately before the swap (`reassertBeforeSwap`,
app/squash/worker/lease.go:209-258). A cancel marker that lands after that re-assert does not
stop the swap: the in-place path re-asserts, recycles and renames
(app/squash/worker/run.go:426-445), and the elsewhere path (every MP4 output) places the
output and retires the source (:404-419), and only then does `finish` probe the output and
build the result (:606-626), seconds later. So a fact can be answered over `withdrawn{S}`, and
the planner keeps reading the record after the withdrawal closes the dispatch (§4.10).

**Side effects survive a crash** between the effect and its record: sidecars are replaced atomically
by name, and the loop's directory read attributes an unrecorded one; a transcode re-run finds its
own output through `producedEarlier`, `finishElsewhere` and `alreadySwappedOrChanged`
(`app/squash/worker/run.go:533-600`); a graft re-run finds the language `Present`; a probe or a
TheIntroDB query is simply asked again.

**Last delivery.** On a task's last delivery (`MaxDeliver`) a worker writes `StateFailed` with
`Transient: true` and the cause, rather than leaving it to the DLQ. This generalises
`fetch.Worker.transient` (`apply.go:243-254`).

### 4.9 The waker (`pkg/records/recordsource`)

```go
func New(bus events.Bus, bucket string,
	toKey func(schema.Ref) (remediation.Key, bool)) source.TypedSource[remediation.Key]
```

The loop registers six with `WatchesRawSource`, one per bucket (S16-S21). They start only on the
leader.

- **First open:** `KV.Watch(">", events.WatchUpdatesOnly())`. No replay is needed: every pass reads
  the records of its outstanding blocks, and controller-runtime starts workers only after every
  source has started and every cache has synced, so a record written before the watch opened is
  read by the pass the informer's initial list enqueues, and one written after is delivered.
- **For each put** it decodes only `mediaFile` and `state`, skips `requested` and `withdrawn` (the
  loop's own writes) and every delete or purge marker, enqueues `toKey(mediaFile)`, and remembers
  the revision as `lastRev`. For `clustarr-segments` it decodes `segments.Record` v2's `File`; v1
  records, which carry no ref, are skipped.
- **The first open is synchronous** in `Start`, retried with backoff for up to 60 s before
  `Start` returns an error (controller-runtime fails the controller on it,
  pkg/internal/controller/controller.go:372-375), so the "written before the watch opened" case
  above holds. At that open the source reads the bucket stream's last sequence and creation time
  and seeds `lastRev` from them, so a later reopen never replays a quiet bucket.
- **On a channel close** while ctx is live (natsbus closes only when nats.go's `Updates` closes,
  not on a plain reconnect, natsbus/kv.go:218-240), it reopens after a backoff of 1 s doubling to
  30 s with `events.WatchFromRevision(lastRev+1)`. If the stream was recreated (its creation time
  changed, or its last sequence is below `lastRev`, the "records bucket lost" row of §5.15), it
  instead replays the new bucket at `LowPriority` and enqueues, from the cache, every file whose
  status shows an outstanding request of its remediation, since their records went with the old
  bucket. `TestSourceSurvivesABucketRecreatedUnderIt` holds it.
- It writes nothing and decides nothing. A dropped wake costs latency, never correctness, because
  every pending block also requeues at its own timeout.

This replaces split §6.5.3's "replays every record at start" (about 12k records the informer already
enqueues) and "re-opens the watch after 5 s" (which misses or replays every update in the gap).

### 4.10 Incorporation

| Remediation | When the loop reads its record |
|---|---|
| probe | when the probe is due (split §6.5.3) |
| transcode | when `dispatch.seq > dispatch.answeredSeq`; when a dispatch grant is held; and while `dispatch.withdrawn` is true and `now < dispatch.dispatchedAt + records.FactWindow` |
| graft | likewise; the graft planner also reads the **transcode** record while `Prev.transcode.joinedGraft` names a dispatch whose joined result its block has not incorporated (§5.13) |
| subtitle | per item, when in flight, or when due (to adopt or to compute `NextSeq`) |
| markers | when `markers.Due` says due |
| segments | every pass of a probed movie or episode file, because analysis amends unasked |

In steady state that is about one `Get` per pass of a video file, and about 12k Gets at a leader's
start.

**A record is incorporated when** its state is `answered` or `failed`; its `Seq` equals the block's
`dispatch.seq` (markers: `AnsweredAt` after `status.markers.fetchedAt`); and its inputs equal those
the block dispatched (probe: `judgeProbe`'s rows). Incorporation sets `dispatch.answeredSeq = seq`.

**A fact after a withdrawal.** For transcode and graft, a record `answered` at `dispatch.seq`
with `Spec.Fact` true, found while `dispatch.withdrawn` is true and within
`records.FactWindow` (`Task.Deadline + deadlineGrace` for transcode, the Job's
`activeDeadlineSeconds` plus `graftJobGrace` for graft; at most 7 days, inside the bucket's
TTL), is incorporated from whatever phase the withdrawal left (Planned, `Skipped`/`Cancelled`,
`Failed`, Pending): the block becomes `Swapping` with the answer's result, and
`dispatch.withdrawn` is cleared. A dispatch never goes out over an unincorporated fact: the
grant pass reads the record (§4.10's row) and incorporates the fact instead, releasing the
grant, and `Request` refuses to overwrite it (§4.7). If a fact lands between that read and the
`Request` for S+1, the `Request` misses its CAS, so no S+1 task is published (§3.8 skips the
publish after a record miss); the next pass finds the block Queued at S+1 over a record still
holding the fact at S, closes S+1 without a record write (`answeredSeq = S+1`, nothing was
published) and incorporates S. A fact that arrives after `requested{S+1}` landed is the
`Seq > S` row of §4.8, recovered from disk. `TestAFactAfterTheClosingWithdrawalPassIsIncorporated`
runs the worker's `Answer` after the second withdrawal pass, for a Cancelled, a Suspended and a
Rerouted file.

**Determinism.** Timestamps come from the record (`ClaimedAt`, `AnsweredAt`, the answer's
`FetchedAt`, `DownloadedAt`, `FinishedAt`), never from the loop's `now`, so incorporating twice is
byte-identical and the apply is skipped. The probe keeps its designed `probedAt = now`.

**History events** are published only after the apply lands, with Msg-Id
`<fileUID>/<remediation>[/<sub>]/<seq>/<action>`. They are history, never the carrier of a result.
The subtitle worker publishes its event after its own `Answer` lands, as it does after its status
write today (`apply.go:366-395`).

### 4.11 Retention and cleanup

- **Inbox records are never deleted.** The 7-day TTL runs from the last write and outlasts every
  request timeout below; a deleted MediaFile's records simply expire. An expired record is not
  "redo": a block is redone only when its status says the work is due.
- **`clustarr-segments` has TTL 0.** `segmenting.Sweeper`, a leader-only runnable in the manager,
  runs once a day: it lists keys with the new `events.KV.Keys`, reads each value (v2 values carry
  `File.UID`; v1 keys are parsed with `ParseKVKeyToken`), and deletes with `DeleteRevision` every
  record whose UID is not a MediaFile in the cache.
- **In-flight work of a deleted MediaFile** is withdrawn from the ledger's memory of its UID
  (`withdrawAll`, §5.10), and the 5-minute task sweep (today's `sweep`,
  `app/squash/controller/transcodejob/withdraw.go:227`, re-keyed to MediaFile UIDs) catches what a
  down manager missed. There is no MediaFile finalizer (§9, D11).
- **`clustarr-progress` is never a records bucket:** its 10-minute TTL, its non-Durable storage and
  its 1 Hz telemetry already lost the ImportList checkpoint once (CLAUDE.md gap fixes).

### 4.12 Per remediation

#### Probe (designed in split §6.5)

The protocol, names and constants stay as split §6.5 has them: `CLUSTARR_WORK_PROBE`,
`importarr-probe-high|low`, `clustarr-probes`, `MsgIDForProbe`, `judgeProbe`, the constants and
`ProbePending`. Changes:

- `schema.ProbeRecord` embeds `schema.RecordHeader`; its JSON keys are unchanged. Its `Prober`
  stays the pod-name key; `AbandonedCount` stays its own counter.
- `Store.Request` issues `NextSeq(rec.Seq, 0, now)` instead of `prev.Seq+1`.
- `Store.Seed` is `Answerer.Seed`: unsolicited, Seq unchanged, judged by inputs only.
- `probeRecordsSource` becomes `recordsource.New(bus, events.BucketProbes, …)`.
- The request follows the ProbePending apply (§3.8).
- `clustarr_probe_requests_total{lane}` (split W4.5) becomes
  `clustarr_record_requests_total{remediation="probe",lane}`.

#### Transcode

- **Bucket and key:** `clustarr-transcodes`, `RecordKey(mediaFileUID)`.
- **Task:** `task.Task` v2 (schema `transcode.Task.v2`, `app/squash/task`): `File schema.Ref`
  replaces `Job`; `Seq int64` replaces `Attempt` (`task/task.go:50-74`); it adds
  `Inputs task.Inputs{ProfileHash, PlanHash, Class, SourcePath, ProbeHash, OutputPath}`. The
  profile snapshot, root, deadline and `Graft *grafttask.Task` stay. A v1 worker cannot decode it
  and dead-letters it (`worker/serve.go:354-362`).
- **Subject, Msg-Id, lease, parts,** re-keyed from the TranscodeJob UID to the MediaFile UID:
  `WorkTranscodeTaskSubject(profileUID, class, mfUID)` (same builder, `subjects.go:568-570`; the
  stream and pool durables are unchanged, R7); `events.MsgIDForTranscodeTask(mfUID string, seq int64)`
  = `transcode/<mfUID>/<seq>` (was `<jobUID>/<attempt>`, `:614-616`); `task.Lease` v2
  (`transcode.Lease.v2`: `File`, `Seq int64`, `State`, `Pod`, `Node`, `Since`) at
  `events.TranscodeLeaseKey(mfUID)` in `clustarr-transcode-leases` (TTL 90 s, unchanged), a cancel
  marker applying to its Seq and earlier; part files `<stem>.part-<mfUID8>-<seq><ext>`, with
  `fsops.TranscodePart.JobUID8` renamed `FileUID8` and `Attempt` renamed `Seq`
  (`pkg/fsops/parts.go:27-63`; `partAttemptRE`, `pkg/fsops/classify.go:90`, already accepts long
  digit runs).
- **Answer:** `task.Answer{Outcome, Reason, Message (≤1024), Class, Pod, Node, StartedAt,
  FinishedAt, Result *transcodev1alpha1.Result (MediaInfo always nil), StderrTail (≤4096),
  Graft *grafttask.Result}`. `Spec.Fact` is `Outcome == succeeded`.
- **Progress** lives only in `clustarr-progress` at `worker.ProgressKey(mfUID)` =
  `transcode.<KVKeyToken(mfUID)>` (`app/squash/worker/telemetry.go:45`), value
  `schema.TranscodeProgress` v2 (`transcode.Progress.v2`: `File`, `Seq`, …), written at 1 Hz
  (`telemetry.go:160`), read only by the ui. Deleted: the claimed and progress `StatusEvent`s,
  `Options.OnProgress`'s status path, `progressReporter` and `DefaultProgressInterval`
  (`progress.go:34`). Nothing writes progress to etcd.
- **Worker** (`app/squash/worker/serve.go`): decode v2 (a v1 task is acked with an info log that the
  loop requests it again); **first**, run the existing produced-earlier checks
  (`producedEarlier`, `finishElsewhere`, `alreadySwappedOrChanged`, run.go:533-600) against
  `task.Inputs`, and if this dispatch's swap already happened, answer the fact (allowed over
  `withdrawn{S}`, §4.8) and ack; then, if `Superseded`, ack; claim the lease (`lease.go`,
  re-keyed); `Claim` the record (if `withdrawn`, `DeleteRevision` the lease and ack); run
  `Process`, `BeforeSwap` reasserting the lease; `Answer`; `DeleteRevision` the lease; ack. A
  failed `Answer` of a non-fact deletes the lease and `Nak(0)`s, as with today's unpublished
  finished event (`serve.go:452-462`). **A failed `Answer` of a fact is retried** with backoff
  (1 s doubling to 30 s) for `factAnswerTimeout` (10 min), whatever the lease now says, as the graft
  pod's `graftAnswerTimeout` does, and only then naks: a withdrawal has usually purged the
  subject by then, so a redelivery cannot be relied on to carry the fact. Fenced, cancelled and
  drained runs that have not swapped settle as today (`serve.go:409-450`) and write no answer.
- **The lifecycle in status** (dispatch, running, lost workers, withdrawal, the swap) is §5.8-§5.10.

#### Graft

- **Vehicle:** a batch Job per standalone graft stays (§9, D9), with three changes: its pod keeps
  `NATS_URL` (the strip at `audiograft/controller.go:678` is removed) and writes its answer to
  `clustarr-grafts` by CAS; `TerminationMessagePolicy`, `--termination-log`,
  `grafttask.Result.Encode`/`Decode` and the pod list (`:765-795`) are deleted; the owner is the
  MediaFile, the name `k8s.ChildName(mf.Name, "graft", strconv.FormatInt(seq, 10))`, and the label
  `squasharr.clustarr.io/graft-of: <mediafile>`, replacing `LabelGraft`
  (`squasharr.clustarr.io/audiograft`, `:76`). The Job still carries the pool Job cache's
  managed-by label (`pools.go:598-604`), so the manager caches it. Pool pods keep
  `AutomountServiceAccountToken: false` (`pool/template.go:368`); graft pods mount none either.
- **Bucket and key:** `clustarr-grafts`, `RecordKey(mediaFileUID)`. One graft per file: multi-item
  files are never grafted (§2.6).
- **Task:** `grafttask.Task` (JSON in the Job's args) gains `File schema.Ref`, `Item string` (the
  Movie's or Episode's UID), `Seq int64` and
  `Inputs grafttask.Inputs{TargetProbeHash, DonorAudio, DonorRelease, Language, Anchor}`; the
  `Graft` name field is dropped.
- **Answer:** `grafttask.Result`, unchanged in shape. `Spec.Fact` is
  `Phase == Succeeded && Reason == Grafted`.
- **Pod** (`cmd/transcode --graft-task`): connect to NATS; if `Superseded`, exit 0; `Claim`; run
  `graft.Run`; `Answer`, retrying bus errors for `graftAnswerTimeout` (2 min); exit 0 on
  `Succeeded`, else 1.
- **The donor is reduced at import, not here.** fileimport, when it imports an `audioDonor`
  Download, runs `graft.Reduce` (app/squash/worker/graft/graft.go:102-116, an audio stream copy
  needing donor, root, language and anchor, which fileimport computes today for the AudioGraft
  spec, donor.go:229-235) and records the reduced `<stem>.mka` as the import's `destPath`. The
  import agent already links ffgo. This removes the only graft step with no target file, and with
  it every item-keyed task and record (§9, D8).

**Loop rows** (`status.graft`):

| Situation | Action |
|---|---|
| Granted a graft slot by the ledger (`--graft-concurrency`, §5.13) | apply `Queued{dispatch.seq, jobName}`; then `Request` and create the Job |
| record `claimed` | `Running`, `startedAt` from `ClaimedAt`; the pod's name in `message` |
| record `answered` | `Grafted`: `Swapping` with `tag`; the bytes changed, so the probe follows and its incorporation gives `Succeeded` and sets `status.graftTag`. `Present`: `Succeeded`. `DonorFault` reasons (`AlignmentRejected`, `VerifyFailed`, `MuxFailed`, `DonorLacksLanguage`): `Failed`, `donorFault: true` (the item key rejects the release). Any other failure: `Waiting`, reason `Backoff`, retried after `failureBackoff` (15 min). Then `dispatch.answeredSeq = seq`, and the Job is deleted with Background propagation. |
| Job `Complete`/`Failed` with no answer after `graftJobGrace` (2 min) | `Failed`, reason `JobLost`, message from the Job's condition; retried after the backoff |
| The draft's `transcode.joinedGraft` names the in-flight transcode dispatch S | `Running`, reason `JoinedTranscode`, `joinedTranscodeSeq: S`, derived by the graft planner from the transcode block (§5.13); the transcode planner never writes `status.graft`. The join wait stays 6 h (`audiograft/controller.go:100-102`). |
| The transcode record is `answered` at the joined S with `Answer.Graft` | the graft planner incorporates `Answer.Graft` as the `record answered` row does (its Gather reads the transcode record, §4.10); a transcode answer with no `Answer.Graft` (the worker never grafted) puts the block back to `Waiting`, reason `WaitingForSlot` |
| record `failed` (§4.8's last delivery) | as an answered failure: `Waiting`, reason `Backoff`, `message` the record's `Failure`, retried after `failureBackoff`; never `JobLost` |
| `dispatch.withdrawn` and the record `answered` `Grafted` at `dispatch.seq` within `records.FactWindow` | the fact row of §4.10: `Swapping` with `tag` |

#### Subtitle fetch

Bucket `clustarr-subtitles`, key `RecordSubKey(mediaFileUID, langKey)`; one key per language removes
today's 8-attempt contention between siblings (`apply.go:334`). Task, answer, worker and loop rows are
§6.6-§6.7.

#### TheIntroDB markers

- **Bucket and key:** `clustarr-markers`, `RecordKey(mediaFileUID)`.
- **Task:** `schema.MarkersTask` v2 (`catalog.MarkersTask.v2`): `File Ref`, `Seq`,
  `Inputs schema.MarkersInputs{ProbeHash, DurationMs, Query}` and `SeriesKey`. The loop builds
  `Query` from its cache (TMDB id, or TVDB id + season + episode), so the worker reads no
  Kubernetes object. The cases the handler decides without asking (a multi-episode file, a
  non-aired order, a kind with no markers; `handler.go:219-265`) are written by the loop directly
  as `NotFound` with the message, with no task.
- **Msg-Id:** `events.MsgIDForMarkers(fileUID string, seq int64)` = `markers/<fileUID>/<seq>`; a
  deferral uses `events.MsgIDForMarkersAt(fileUID, seq, at)` = `…/at-<unix>`.
- **Answer:** `schema.MarkersAnswer{Result, Segments []schema.SegmentJSON (≤20), Message (≤512), FetchedAt}`.
- **Worker:** if `Superseded`, ack; check the in-memory series-absent cache (`handler.go:86`,
  unchanged; the domain has a fixed 1 replica); ask the provider; a limit longer than 5 min is
  `Defer(seq, reset)` plus a republish at the reset with `WithScheduleAt`, then ack; a shorter one
  is `events.Retry`; otherwise `Answer`.
- **Loop rows:** not `markers.Due`: no `Get`. Due, and the record is absent, has another
  `ProbeHash`, is `requested` beyond `markersRequestTimeout` (24 h), or is `deferred` beyond
  `DeferredUntil` + 24 h: `Request`, then publish, with no status change. Answered for the current
  probe with `AnsweredAt` after `fetchedAt`: incorporate (`fetchedAt` from the answer;
  `notFoundSince` from status as today's `notFoundSince`, `handler.go:173`; merged with the
  analysis segments; an `Error` leaves TheIntroDB's segments in place, as `apply.go`'s `markers()`
  does). `status.markers` keeps its shape.

#### Segment analysis

- `cmd/markers` writes `clustarr-segments` itself by CAS, through `segments.Store` (`pkg/segments`,
  `Update(ctx, uid, func(cur *Record) (Record, bool))`, up to 8 attempts).
- `segments.Record` v2: `Schema "segments.Record.v2"`, `File schema.Ref`, `ProbeHash`, `Version`,
  `Result`, `Segments`, `AnalyzedAt`, `Amend int32`, `LastError *segments.Attempt{ProbeHash,
  Version, Message, At}`. An error sets `LastError` and keeps `Segments` only when `ProbeHash` and
  `Version` still match (today's "an Error is not stored" rule, `apply.go:116-123`). A season
  amendment (`withIntro`) increments `Amend`; no Msg-Id is involved, so the 1 h dedupe hazard goes.
- The season planner (`app/catalog/segmentplan.Planner`, durable `catalogarr-segments-plan`) stays
  in the manager and issues no Seq.
- **Fence:** inputs. The loop uses a record only when `ProbeHash == status.probeHash`.
- **v1 records are read.** Every record in `clustarr-segments` today is v1:
  `{probeHash, version, result, segments}` with no schema and no ref
  (pkg/segments/segments.go:49-53), read today with no schema check
  (app/catalog/segmenting/apply.go:146-149). `segments.Store` reads a value whose `Schema` is
  empty as a v2 record with `File.UID` taken from its key (`ParseKVKeyToken`), judged by
  `ProbeHash` alone, as today; only a non-empty foreign `Schema` reads as absent. The waker
  (§4.9) skips v1 puts because none is written after F3.4.
- **The stored markers are carried forward, not re-merged.** The markers planner re-renders
  `status.markers` only when an input changed: a TheIntroDB answer it incorporates this pass, a
  new probe hash, or a segments record whose `(ProbeHash, Version, Amend, AnalyzedAt)` differs
  from what `status.markers.analysis` records (`forProbeHash`, `version`, `analyzedAt`; a v1
  record, which has no `Amend` or `AnalyzedAt`, matches when `forProbeHash` and `version` do).
  Otherwise it copies `status.markers` verbatim. Re-merging on every pass would rewrite stored
  markers that are not the canonical merge: today's applier relabels untagged legacy segments
  as TheIntroDB/100 only inside `ApplyMerged`, when a new result arrives (apply.go:197-213), so
  stored maps still hold untagged segments, and cluster-plex hashes the whole map into
  `SeedKey` (cluster-plex `pkg/clustarrwatch/fields.go:156-171`) and deletes Plex marker rows
  that are no longer wanted (`pkg/plexseed/seeder.go:90-103`). The same rule holds for
  `status.mediaInfo`: the renderer clamps a string only when it is new; a stored value over a
  new cap (§2.11.2) is carried unchanged, which validation ratcheting accepts.
  `TestTheFirstLoopPassKeepsEverySeedKey` (§7.3.12) holds both.
- **Retired:** `segmenting.Results`, `segmenting.Applier`, `NewApplier`, `Setup{Results}`,
  `schema.SegmentsResult`, the `catalogarr-segments-result` durable and
  `clustarr.work.segmentarr.result.>`.

### 4.13 Names added and retired

| Added | Where |
|---|---|
| `pkg/records` (`RecordHeader` in schema, `Spec`, `Requester`, `Answerer`, `NextSeq`, `Pacer` with `Reserve`/`Consume`, `CASAttempts`, `RepublishWindow`, `FactWindow`, `IncorporatedThrough`, `ErrRaced`, `ErrTooLarge`, `ErrUnincorporatedFact`) | new |
| `pkg/records/recordsource.New` | new; manager only |
| `BucketTranscodes`, `BucketGrafts`, `BucketSubtitles`, `BucketMarkers`, `RecordKey`, `RecordSubKey`, `ParseKVKeyToken`, `KV.Keys`, `WatchUpdatesOnly`, `WatchFromRevision` | `pkg/events` |
| `MsgIDForSubtitleFetch`, `MsgIDForMarkers`, `MsgIDForMarkersAt`; `MsgIDForTranscodeTask(mfUID string, seq int64)` (signature) | `pkg/events/subjects.go` |
| `task.Task` v2, `task.Lease` v2, `task.Answer`, `task.Inputs`; `schema.FetchTask` v2, `SubtitleQuery`, `OnDiskSubtitle`, `SubtitleRecord`, `SubtitleEvent` v2, `MarkersTask` v2, `MarkersInputs`, `MarkersAnswer`, `TranscodeProgress` v2; `grafttask.Inputs` | payloads |
| `segmenting.Sweeper`; `segments.Store`; `segments.Record` v2 | manager; `pkg/segments` |

| Retired | Replaced by |
|---|---|
| durable `squasharr-transcode-results`; `FilterTranscodeResults` (subjects.go:102); `WorkTranscodeResultSubject`; `MsgIDForTranscodeEvent`; `task.StatusEvent`; `transcodejob.ResultsConsumer`; `ConsumerSquasharrResults` (:137) | `clustarr-transcodes` |
| durable `catalogarr-segments-result`; `FilterCatalogSegmentsResult` (subjects.go:119); `WorkSegmentsResultSubject` | `clustarr-segments` CAS |
| field managers `catalogarr-markers` (`fieldmanager.go:164`) and `captionarr-worker` (`:261`) | the loop's `catalogarr` |
| `MsgIDForSubtitle`, `MsgIDForForcedSubtitle`, `markers.MsgID` | Seq-carrying builders |
| graft termination message, `--termination-log`, `grafttask` `Encode`/`Decode` | `clustarr-grafts` |
| `worker.progressReporter`, `DefaultProgressInterval` | `clustarr-progress` only |

Existing consumer, stream and bucket names stay (R7). Retired durables are deleted through
`Topology.Retired` (§8, F1.2) only after the drain gate (§7.5).

### 4.14 Worker RBAC after the fold

No worker identity keeps any verb on `mediafiles/status`, `subtitlerequests` or `transcodejobs`.
Pool, graft and markers pods hold no Kubernetes credentials at all.

| Identity (split §4.6) | Package | Removed | Kept |
|---|---|---|---|
| `agent-caption` | `app/caption/worker/fetch` (`doc.go:107-114`) | `subtitlerequests` get/list/watch; `subtitlerequests/status` get, patch; `mediafiles`, `movies`, `episodes`, `series`, `rootfolders` get/list/watch | `subtitleprofiles` get/list/watch |
| `agent-caption` | `app/caption/providerset` (`doc.go:71-72`) | none | `subtitleproviders` get/list/watch; `secrets` get |
| `agent-metadata` | `app/catalog/worker/markers` (today `markers/handler.go:60-61`) | `mediafiles;movies;episodes;series` get; `mediafiles/status` patch | none: the task carries the query |
| `agent-metadata` | `app/catalog/metadata` (gateway) | none | item `*/status` under `catalogarr-metadata` (`gateway.go:49`): item status, not per-file work |
| `agent-import` | `app/import/worker/rescan` (`worker.go:157`) | `transcodejobs` get/list/watch | reads `status.transcode` through its `mediafiles` grant (kept-output and orphan-part guards) |
| `agent-import` | `app/import/worker/fileimport` (`donor.go:45`) | `audiografts` create/patch/update/get/list/watch | nothing new: the donor is derived from the Download (§2.6) |
| `cmd/transcode` (pools and graft Jobs) | | | none (`AutomountServiceAccountToken: false`); NATS only |
| `cmd/markers` | | | none (markers SA with no binding); NATS only |
| `manager` | `app/remediation` | | gains the graft `batch/jobs` verbs, moved from `audiograft/controller.go:69`; the graft `pods` list goes, while the pool's (`transcodejob/doc.go:174`) stays with the slim TranscodeProfile reconciler |

ADR-0016's "workers hold no status RBAC" holds for MediaFile and per-file work only: agent-metadata
keeps item status under `catalogarr-metadata`, the grab worker item status under `catalogarr-grab`,
and the engines Download status. NATS has no per-bucket authorisation here, so bucket writers are held
by the guards below.

### 4.15 Changes to `pkg/events`, metrics, tests

**`pkg/events`** (landed serially in F1, before any fan-out):

- `BucketSpec` gains `MaxBytes int64`, `MaxValueSize int32` and `Records bool`; `KeyValueConfig`
  (`topology_nats.go:136`) maps the first two.
- `Topology.Validate` (`topology.go:366`) checks every `Records` bucket for `Durable`,
  `History == 1`, `MaxBytes > 0` and `MaxValueSize > 0`, `TTL == 0 || TTL >= 7*24h`, and
  `LimitMarkerTTL > 0` when `TTL > 0`.
- `KV.Watch(ctx, pattern, opts ...WatchOption)` gains `WatchUpdatesOnly()` and
  `WatchFromRevision(rev uint64)`, mapped by natsbus to `jetstream.UpdatesOnly` and
  `jetstream.ResumeFromRevision` (nats.go v1.53.1 `jetstream/kv_options.go:41,70`).
- `KV.Keys(ctx) ([]string, error)` is added.
- **membus Watch stops dropping.** Today it discards updates once a watcher is 64 behind, and its
  replay drops every key past the 64th (`membus/kv.go:96-104,311-337`); it moves to a per-watcher
  unbounded queue drained by a goroutine, neither dropping nor blocking writers, matching natsbus
  (`natsbus/kv.go:218`).
- `Topology.Retired []RetiredConsumer{Stream, Durable, Purge}`: on both buses `Ensure` deletes each
  listed durable and purges its filter, idempotently.

**Metrics** (manager only; pool, graft and markers pods export none):
`clustarr_record_requests_total{remediation,lane}` (lane `high`, `low` or `none`),
`clustarr_record_incorporations_total{remediation,state}`, `clustarr_record_timeouts_total{remediation}`,
`clustarr_record_errors_total{remediation,op}` (op `get`, `request`, `withdraw`, `decode`, `publish`,
`watch`).

**Tests:**

- `pkg/records`: `TestAnswerRules` (every row of §4.8, including the withdrawn fact and the
  absent record, where `Claim` naks and `Answer` writes); `TestWithdrawRules` (every row of
  §4.7's table, absent and below-Seq included); `TestRequestNeverOverwritesAnUnincorporatedFact`;
  `TestThePacerReservesOneSlotPerKey`;
  `TestNextSeqIsMonotoneAcrossExpiry`; `TestOversizedRecordIsRefused`;
  `TestUndecodableRecordReadsAbsent`; `TestAnswerReReadsBeforeItsCAS` (a real second writer,
  interleaved); `TestRecordsLinksNoKubernetes`; `TestProbeRecordWireFormatIsUnchanged`.
- `pkg/events/contracttest`, both buses: `KVWatchUpdatesOnly`, `KVWatchFromRevision`, `KVKeys`,
  `KVWatchDeliversPastTheBuffer` (1,000 puts; the wave gate's
  `WatchDeliversEveryKeyPastTheBuffer`), `KVMaxValueSize` (natsbus, real server).
- `recordsource`, real embedded server: `TestSourceSkipsTheLoopsOwnWrites`,
  `TestSourceResumesFromRevisionAfterAClose`.
- Topology: `TestRecordsBucketsAreDurableHistoryOneAndBounded`;
  `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` (`natsbus/singlenode_limits_test.go:74`)
  extended to the 20 GiB file store.
- Loop envtests, each on an object that already has status: `TestQueuedWithoutARecordIsDispatched`,
  `TestRequestedWithoutATaskIsRepublished`, `TestLostWorkerIsRedispatched`,
  `TestSucceededAfterWithdrawalIsIncorporated`, `TestAFactAfterTheClosingWithdrawalPassIsIncorporated`
  (the interleaving withdrawal pass 1, pass 2, then the worker's `Answer`),
  `TestAStaleAnswerIsNotIncorporated` (a worker answers
  N after the loop issued N+1), `TestAMarkersRequestLeavesStatusMarkersUntouched`,
  `TestASeasonAmendmentWithinTheHourIsIncorporated`, `TestAGraftJobWithoutARecordFailsJobLost`.
- `test/guards`: `TestOnlyTheLoopWritesMediaFileStatus`, `TestNoAgentRoleGrantsMediaFileStatus`,
  `TestRecordsRequestersAreTheLoop` (`Request` and `Withdraw` called only from `app/remediation`),
  `TestRecordsBucketsOpenOnlyThroughTheirStores`, `TestNoV1TaskProducers`.

Every new test is falsified: revert the fix and watch it fail by name.

---

## 5. Admission ledger and dispatch

How the manager decides which files to transcode and graft, sends each task once, takes a task back,
and recovers after a crash. §2.5 and §2.6 own the block types, §4 the records; this section fixes the
status leaves admission depends on and names everything it adds.

### 5.1 What it replaces (verified)

| Today | Where | After the fold |
|---|---|---|
| The admission pass: one synthetic request `(admission)`, `MaxConcurrentReconciles: 1`, up to three **uncached** Lists of every TranscodeJob per pass | `transcodejob/controller.go:82`, `:673-813` (Lists at `:685`, `:711`, `:807`), `:946` | The `transcode-admission` controller in `app/squash/admission`. It reads the in-memory ledger; in steady state it makes no apiserver List (§5.7). |
| In-memory admission state: `recreate`, `poolBackoff`, `unschedulable`, `nextSweep` | `transcodejob/controller.go:169-186` | `admission.Ledger` (§5.5) |
| The slot scheduler and class choice: `Admit`, `admitsBefore`, `ChooseClass`, `assignClasses`, `unhealthyClasses` | `admit.go:91-133`, `class.go:51-71`, `:145-183`, `:206-245` | Moved unchanged into `app/squash/admission` |
| Dispatch: publish the task, **then** write Queued conditionally; a lost write is adopted from the worker's event (R16) | `dispatch.go:106-301`; adoption at `results.go:98-115` | The loop's transcode planner writes Queued **first**, then the record, then publishes (§5.8) |
| Results consumer `squasharr-transcode-results`, writing progress to etcd every 10 s per running job | `results.go:42-219`, `worker/progress.go:34` | Deleted; results are records (§4.12); progress stays in `clustarr-progress` only |
| Withdrawal: a cancel lease marker (90 s TTL), then a purge; a finalizer on the TranscodeJob; a 5-minute sweep | `withdraw.go:97-154`, `:164-191`, `:227-300`; `subjects.go:563` | §5.10 |
| Pools and holds: `pools`, `holding`, `rerouteUnschedulable`, `reroute` | `pools.go:167-192`, `:216-349`, `:412-506` | The slim `transcodeprofile` reconciler renders pools; admission computes holds and issues reroute directives (§5.12) |
| The window (`--job-window` 32, files taken in name order) and retention (`--job-retention` 24h) | `transcodeprofile/controller.go:178-253`, `:413-453`; `app/squash/run.go:236-246` | The window is kept, now counting files (§5.11); retention is deleted (§5.14) |
| Graft concurrency: unfinished graft Jobs counted per namespace | `audiograft/controller.go:78-80`, `:314-320`, `:418-431`, `:534-541` | Ledger graft slots (§5.13) |

Two defects disappear by construction:

- **The equal-timestamp retention leak.** `retireSucceeded` and `openFiles` test `!probed.After(done)`
  (`transcodeprofile/controller.go:392`, `:445`); metav1.Time has one-second precision, so all 48
  live Succeeded jobs outlived the 24 h retention. No Succeeded object exists any more.
- **The phantom Running job.** A memory-backed work stream and lease bucket can be wiped by a NATS
  restart (2026-10-05). §5.9 detects the lost worker from status plus the lease plus the stream.

### 5.2 Decisions

1. **Three components, one writer.** The loop's transcode and graft planners (§3) are the only code
   that changes `status.transcode` and `status.graft`. The `transcode-admission` controller makes
   every decision that spans files and writes nothing but memory; its decisions are *grants* and
   *directives*, which it enqueues into the loop's queue (S22). The slim `transcodeprofile`
   reconciler renders pool Jobs and writes TranscodeProfile status. Neither of the last two watches
   or writes MediaFile.
2. **Status first, then record, then publish.** The ledger is rebuilt from status, so status must
   name every task that might exist. This reverses the transcode-worker design §8 rule "nothing is
   marked Queued that was not published"
   (`docs/superpowers/specs/2026-09-23-transcode-worker-design.md:291-292`).
3. **The dispatch sequence is the file's** (`dispatch.seq`, from `status.lastSeq`, §2.3). It is the
   Msg-Id, lease, record and part-file identity, replacing the TranscodeJob UID plus `attempts`.
4. **Rebuild reads the apiserver, not the cache,** through the APIReader with the CRD selectable
   field `.status.transcode.phase` (§2.10). The cache may not yet hold the previous leader's last
   writes when the lease changes hands. This refines ADR-0016's "cache index" (§9, D6).
5. **The window survives, as a bound on files.** At most `--job-window` files per profile are in a
   window phase. The backlog beyond the window lives only in memory and is never written to status
   (§9, D4).
6. **`--job-retention` is removed.** A `Swapping` block keeps its `result` until the swap is
   incorporated, then becomes `Succeeded`.
7. **No MediaFile finalizer.** Deleting a file withdraws its task from the ledger's memory of its UID;
   the sweep catches what a down manager missed (§5.10).
8. **`CLUSTARR_WORK_SQUASHARR` keeps its storage** (not Durable, `topology.go:608-618`). Status is
   authoritative and the loop republishes, so a wiped stream heals itself. Making it Durable needs the
   stream recreated, because `EnsureTopology` cannot change storage in place
   (`topology_nats.go:29-36`); that stays an open owner decision (§9, D26).

### 5.3 Names

| Kind | Name |
|---|---|
| Ledger package | `app/squash/admission`: `type Ledger`, `func NewLedger(o Options) *Ledger`, `type Reconciler` (controller name `transcode-admission`), `var Request = reconcile.Request{NamespacedName: types.NamespacedName{Name: "(admission)"}}`, `Snapshot`, `FileView`, `LoopSource()` |
| Planner packages | `app/squash/transcodeplan`: `Plan`, `Eligible`, `Decide`, `MaxAttempts` (5), `RequeueBackoff` (1m, 5m, 15m, 30m); it defines `type Admission interface`, which `*admission.Ledger` implements, so `transcodeplan` never imports `admission`. `app/squash/graftplan` likewise. |
| Pool reconciler | `app/squash/controller/transcodeprofile`, controller name `transcodeprofile` (unchanged), `MaxConcurrentReconciles: 1` |
| Selectable fields | `.status.transcode.phase`, `.status.graft.phase`; constants `catalogv1alpha1.FieldTranscodePhase`, `catalogv1alpha1.FieldGraftPhase` |
| Records bucket | `events.BucketTranscodes = "clustarr-transcodes"` (§4.3) |
| Task subject | `events.WorkTranscodeTaskSubject(profileUID, class, mfUID)`; pool filters and durables unchanged (`subjects.go:600-607`) |
| Msg-Id | `events.MsgIDForTranscodeTask(mfUID string, seq int64)` = `transcode/<mfUID>/<seq>` |
| Lease | `clustarr-transcode-leases`, key `events.TranscodeLeaseKey(mfUID)`, `task.Lease` v2 |
| Progress key | `worker.ProgressKey(mfUID)` = `transcode.<KVKeyToken(mfUID)>` |
| Part file | `<stem>.part-<mfUID8>-<seq><ext>` (`fsops.TranscodePart{FileUID8, Seq}`) |
| Annotations | `transcode.clustarr.io/{cancel,retry,suspend,priority,hardware}` (§2.9) |
| Flags on `cmd/manager` | `--slots` (unchanged), `--job-window` (name and default 32 unchanged, new meaning §5.11), `--graft-concurrency` (unchanged, default 2; missing from split §3.4.1's table, which gains it). `--job-retention` is removed. |
| Constants in `admission` | `GrantPriority = 100`, `grantTTL = 2*time.Minute`, `grantBackoffMax = 30*time.Minute`, `admissionBackstop = time.Minute`, `ledgerResync = 5*time.Minute` (the sweep cadence, `withdraw.go:57`) |
| Constants in `transcodeplan` | `republishEvery = time.Minute`, `republishWindow = records.RepublishWindow` (50 min, under the stream's 1 h Duplicates, `topology.go:615`), `leaseLostGrace = 2*events.TranscodeLeaseTTL` (180 s), `inflightRecheck = time.Minute`, `deadlineGrace = time.Hour` |

### 5.4 The status admission reads

| Leaf (§2.5) | Admission's use |
|---|---|
| `phase` | Window phases: `Pending`, `Planned`, `Queued`, `Running`, `Swapping`. Slot phases: `Queued`, `Running`. `Succeeded`, `Failed`, `Skipped` are verdicts. |
| `dispatch.seq`, `dispatch.answeredSeq` | the in-flight dispatch |
| `attempts` | dispatches in the current cycle; reset by retry, new bytes or a new tag; never resets the sequence |
| `profile`, `profileHash`, `profileTag` | from `jobspec.ProfileHash(spec)` at planning and again at dispatch; never read back from the profile at incorporation |
| `probeHash` | the probe the plan or verdict was made for |
| `class`, `pool` | the class of the in-flight or last dispatch, and its pool Job name (`pool.Name(key)`) |
| `plannedAt`, `dispatch.dispatchedAt`, `startedAt`, `finishedAt`, `nextAttemptAt` | `plannedAt` replaces the TranscodeJob `creationTimestamp` in admission order (`admit.go:126-134`) |
| `hardware`, `priority`, `suspended` | the standing intents in force |

`status.graft` exposes `phase` (`Queued` and `Running`, not joined, hold a graft slot),
`dispatch.seq` and `jobName`.

### 5.5 The ledger

```go
type Ledger struct {
	mu         sync.Mutex
	files      map[types.UID]*entry             // every file with a window phase or in a backlog
	byKey      map[types.NamespacedName]types.UID
	grants     map[types.UID]*grant             // window and dispatch reservations, not yet written
	directives map[types.UID]Directive          // reroutes: withdraw seq N, back to Planned
	pools      map[pool.Key]*PoolState          // held message, unschedulable-until, backoff, recreate
	devices    map[transcodev1alpha1.Hardware]Device // tier and limits from clustarr-progress, per pass
	unhealthy  map[transcodev1alpha1.Hardware]string
	gpuNodes   map[transcodev1alpha1.Hardware]bool
	graft      graftSlots                       // per namespace: job names, reservations
	waitReason map[types.UID]string             // the Planned message, written only on change
	leaseMissingSince map[types.UID]time.Time
	rebuilt    bool
	loopQueue  priorityqueue.PriorityQueue[remediation.Key]
}
```

**What the loop reports.** After every file pass the loop calls `Observe(FileView)`, carrying `UID`
and `Key`; `Eligible` and `Profile` (from `transcodeplan.Eligible`); `Phase` (empty when the file
has no transcode block), `Seq`, `Class`, `Pool`, `PlannedAt`, `NextAttemptAt`, `Suspended`; the
effective `Priority` and `Hardware`; `Encodes` (the plan's mode is transcode); `Fallback`;
`JoinedGraft`; and the graft view. The view comes from the applied status, or from the cached
status when the apply was skipped and the view is not stale (§3.4 step 8).

**`FileView.Seq` is `status.lastSeq`,** not `dispatch.seq`. `lastSeq` never decreases while the
MediaFile exists and is at least every `dispatch.seq` the file has had (§2.3), so it orders every
observation of the file, including one with **no block**: a cleared block observes as
`(lastSeq, rank 3, Phase "")` and so replaces the entry it supersedes, freeing its slot or window
place. (Keyed on `dispatch.seq`, an absent block would observe as seq 0 and never replace an
in-flight or window entry, leaking the slot until a restart.) A subtitle dispatch also raises
`lastSeq`; that only re-observes the same transcode phase at a higher sequence.

**Observations merge monotonically.** With rank(Queued)=1, rank(Running)=2 and rank(anything
else, no block included)=3, an observation replaces the entry only when `(seq, rank)` is
lexicographically **≥** the entry's. A stale cached observation can never free a slot
(Queued(5) after Running(5) is ignored; Planned(4) after Queued(5) is ignored), and every real
transition raises or keeps the pair (Planned(4) → Queued(5) → Running(5) → Planned(5) on a
requeue; Running(5) → Swapping(5); a retry's Skipped(5) → Planned(5); Queued(5) → none(5) when
new bytes clear a block). Stale views are also never observed at all (§3.4 step 8), so the merge
is a second line of defence. `TestObserveIgnoresAStaleObservation` and
`FuzzLedgerNeverOverAdmits` include an absent-block row, and the fuzz invariant gains "never
under-admits after a clear": once an applied status has no block, the file holds no slot or
window place after the next `Observe`.

**Grants** are `{Kind: Window|Dispatch|GraftSlot, Class, Expires}`. `TakeGrant(uid)` marks a grant
*in use*, and an in-use grant does not expire. The planner must call `Confirm(uid, seq)` after its
apply lands, or `Release(uid)`; the loop defers `Release`, so a panic releases. A grant that expires
unconsumed puts the file on a backoff of 1 minute, doubling to `grantBackoffMax`.

**Keys.** `Forget(key)` handles a NotFound pass and returns any in-flight or granted entry, for
withdrawal. An Observe whose UID differs from `byKey[key]` retires the old UID's entry and withdraws
it if it was in flight (a file deleted and recreated under the same name).

**Waking the loop.** `LoopSource()` returns a `source.TypedFunc` (`controller-runtime@v0.25.1/pkg/source/source.go:308-316`)
that captures the loop's queue. Grants and changed wait reasons are added with
`priorityqueue.AddOpts{Priority: ptr.To(GrantPriority)}`; initial-list events are `LowPriority` = -100
(`pkg/handler/eventhandler.go:135-137`, `:178`), so a grant jumps the 11,958-file startup flood
rather than expiring behind it.

**Size and locking.** One mutex guards everything. Every method is O(1) except the pass, O(window
files), and the backlog scan, built outside the lock and swapped in. Memory is about 12k entries ×
~200 B.

### 5.6 Rebuild, resync, sweep

**Rebuild** runs in the first admission pass. Controllers start only on the leader and a lost lease
ends the process, so the first pass always follows a fresh start.

1. Through the APIReader, with `client.MatchingFields{catalogv1alpha1.FieldTranscodePhase: p}` for
   each window phase `p` (Pending, Planned, Queued, Running, Swapping) in each watched namespace,
   then `FieldGraftPhase` for `Queued` and `Running`: seven server-filtered quorum Lists returning at
   most `window × profiles + budget` objects (field selectors have no `in`). Each result is
   `Observe`d.
2. Unfinished graft Jobs from the pool Job cache (`HasLabels{squasharr.clustarr.io/graft-of}`, under
   `PoolJobCache`'s managed-by label, `pools.go:598-604`).
3. The backlog: one cache scan of every MediaFile, TranscodeProfile and Movie/Episode metadata
   through `transcodeplan.Eligible`, the whole-collection pass `transcodeprofile.Reconcile` makes
   today on every MediaFile probe change (`transcodeprofile/controller.go:149-166`), now made once.
4. Set `rebuilt`. No grant is issued before this point.

The loop's concurrent `Observe` calls are harmless because the merge is monotonic.

**Resync** runs every `ledgerResync` and repeats steps 1–3. Its quorum reads **replace**
entries rather than merge into them: each entry records when it was last observed, and an entry
observed before the resync's Lists started takes the List's view outright (a newer `Observe`
from a loop pass during the resync stands). An in-flight or window entry the Lists did not
return is re-read with an APIReader `Get` (at most the slot budget plus the window's worth) and
replaced from that read; a NotFound is `Forget`. Any correction increments
`clustarr_transcode_ledger_corrections_total` and logs at Warn: a correction means an
observation was missed. This is what makes risk 4's "the resync corrects it" hold for an
over-count as well as an under-count.

**Sweep** runs at the first pass and with each resync (moving `withdraw.go:214-300` here). For each
stored subject under `clustarr.work.transcode.task.>`, the last token is a MediaFile UID: if that UID
is not in the cache's MediaFile UIDs (`mfindex.UID`), the file was deleted, so run `withdrawAll`
(§5.10); if the file exists but is neither in flight nor granted, purge the subject. Then
`sweepDurables`, unchanged (`withdraw.go:276-300`).

### 5.7 The admission pass

The `transcode-admission` reconciler: single key `admission.Request`, `MaxConcurrentReconciles: 1`,
`RecoverPanic: true`. It is woken by `source.Channel(ledger.wake)` (non-blocking, buffer 1) when a
slot or window place frees or a pool state changes; by `Watches(Node)` on a `gpuNodeSignal`
predicate (Ready, `spec.unschedulable`, the two GPU labels, allocatable GPU quantity; new, today the
1-minute requeue covers it); by `Watches(TranscodeProfile)` on generation, which also triggers the
backlog rescan; and by `Owns`-style pool Job events through `PoolJobCache` (`poolJobPredicate`,
`pools.go:628`). It returns `RequeueAfter = min(admissionBackstop, the earliest nextAttemptAt, the
earliest grant expiry, the earliest unschedulable or backoff expiry, the next resync)`.

Each pass:

1. **Inputs.** Profiles and Nodes from the cache (`gpuNodes`, `capacity.go:43-64`);
   `ReadEncoderHealth`, `ReadEncoderLimits` and `ReadEncoderTier` for nvidia and intel from
   `clustarr-progress` (`task/limits.go:174`, `:250`) into `devices` and `unhealthy` (an unreadable
   bucket counts as no report, as at `class.go:213-218`); pool states from the ledger. No apiserver
   request.
2. **Reroutes.** For each **Queued** entry that chooses its class (`auto` or `gpu`, `class.go:79-98`),
   is not suspended, and whose pool is unschedulable or whose class is unhealthy, issue
   `Directive{Reroute, Seq, Reason, KeepGPU: gpu}` and enqueue the file. The planner accepts a
   reroute in Queued or Running at that seq (R24, `pools.go:310-317`). A rerouted file holds its slot
   until its Planned write is observed.
3. **Holds.** `holding` (`pools.go:167-192`) from cached pool Jobs and the ledger's backoff and
   recreate state, plus the unhealthy classes (`controller.go:727-739`).
4. **Candidates:** Planned entries not suspended, `nextAttemptAt ≤ now`, profile present, no grant or
   directive outstanding, not on grant backoff. Each becomes
   `Slot{Key, Profile, Priority: intent priority else the profile's, Created: plannedAt}`.
5. **Running:** Queued or Running entries plus in-use, unexpired dispatch grants, each with its class.
   Orphaned entries (§5.9) count toward no pool, as at `controller.go:751-757`.
6. `assignClasses(cands, running, gpuNodes, held, profileLimits)`, then
   `Admit(queued, running, Budget{Slots: --slots, ProfileLimits})`. Both pure and unchanged.
7. **Dispatch grants.** Each admitted file gets `Dispatch` with its class and is enqueued. Each held
   or unadmitted candidate gets a wait reason (`waitingForGPU`, `class.go:105`; the pool's held
   message; or `waiting for a free <class> slot`), and is enqueued only when its reason changed.
8. **Window grants** (§5.11). 9. **Graft slot grants** (§5.13).
10. **Metrics.** `setActive` (`metrics.go:42-59`) over in-flight entries plus dispatch grants; when a
    per-pool dispatched count changed, send on `ledger.profileWake` for that profile.

Today every pass makes up to three uncached Lists of every TranscodeJob, plus pool Jobs, Nodes and
pods (`controller.go:685-716`, `pools.go:85-96`, `:236-238`).

### 5.8 Dispatch: the loop's transcode planner

The planner runs inside the loop's pass against the cached view; the one CAS apply guards it
(§3.7). It never acts on a grant without checking it against that view.

**Window grant.** The planner plans from the stored probe with `planFor` for the profile's hardware
(today's Pending→Planned step, `controller.go:495-543`):

- a verdict: `Skipped` (including a standard hold such as `HoldImageSubtitles`), or `Failed` for a
  plan error; or
- `Planned`, with `plannedAt=now`, `profile`, `profileHash`, `profileTag`, `probeHash`, the plan
  summary and `attempts=0`; or `Pending` with its reason while the probe is pending or a standalone
  graft holds the file.

If the file is no longer eligible, the planner releases the grant.

**Dispatch grant for class C**, only while the view's `phase == Planned`:

1. `S = v.Issue(record.Seq)` (§3.5).
2. Re-plan for C with `ledger.Device(C)` (`dispatch.go:147-163`). If it skips or fails, record the
   verdict and release. If it needs another class, stay Planned (an `auto` file gets a
   `fallbackReason`, as `keepPlanned` does, `dispatch.go:310-339`), release and wake admission.
3. `jobspec.BuildTask`. A missing RootFolder or invalid output gives `Failed`, blocked,
   `InvalidSource` (`dispatch.go:166-183`).
4. Join the graft if it can ride along (§5.13), recording it in `transcode.joinedGraft`.
5. Render `Queued`: `dispatch{seq: S, dispatchedAt: now}`, `lastSeq`, `attempts+1`, `class=C`,
   `pool=pool.Name(key)`, the dispatched plan, `profileTag` and `joinedGraft` (cleared when
   this dispatch carries no graft).

After the apply lands (`applied == true`), as effects:

6. The record: `Create`, or `Update(rev)`, to `requested{Seq: S, Inputs}`, unless already at `S`.
7. `Publish` on `WorkTranscodeTaskSubject(profileUID, C, mfUID)` with
   `WithMsgID(transcode/<mfUID>/<S>)` and `WithExpectStream(CLUSTARR_WORK_SQUASHARR)`.
8. `ledger.Confirm(uid, S)`; the `Dispatched` Event; history `clustarr.evt.transcode.job.queued.<mfUID>`.

**Republish.** A Queued block whose record shows no `claimed` at `S` requeues every
`republishEvery`; while `now - dispatchedAt < republishWindow`, each pass re-runs steps 6–7 with the
same Msg-Id. A failed publish is held in the ledger (`PublishFailed`), and the next pass writes
`message: attempt S: publish failed: <err>; retrying`. Past `republishWindow` with no claim, the
planner withdraws `S` (§5.10) and returns the file to Planned, reason `Unclaimed`, `attempts`
unchanged, message `attempt S was not claimed within 50m; requeued`, with the `DispatchUnclaimed`
Warning Event.

**Why one dispatch per seq.** Per file: the CAS apply dispatches only from a Planned view, a stale
view conflicts, and the loop never runs two passes of one key. Per seq: the Msg-Id dedupes within
the 1 h window and republishing stops at 50 minutes; past the window the record fences, since a
worker **starts** a task only when its record is `requested` at the task's `Seq`, or `claimed`
at it with no live lease (a redelivery); an absent record or one below the task's `Seq` makes
it nak and wait for the loop's owed write (§4.8), so a duplicate of a claimed or answered `S`
never runs twice. Across files: only the
serialised admission pass grants, and the ledger counts grants plus everything in flight. Across
leaders: one lease, and the rebuild is a quorum read. The class-correction path
(`results.go:125-138`) is deleted: a seq is only ever published on the one subject its status names.

### 5.9 Running, finishing, lost workers

The worker side is §4.8 and §4.12. Admission relies on: a worker claims only a record `requested` at
`task.Seq` (or `claimed` at `task.Seq` with no live lease, a redelivery) and then holds the lease
(`worker/lease.go:54-95`); it naks without running when the record is absent or below `task.Seq`
(the loop's owed write follows); it acks without running when the record's Seq is higher, or it is
`withdrawn` or answered at `task.Seq`; it answers before deleting its lease and acking, retrying a
fact's answer for 10 minutes; a redelivered task whose swap already happened answers the fact
before any other check (§4.12).

**Loop rows** (the record's Seq is the block's `dispatch.seq` unless stated):

| Block | Record or lease | Action |
|---|---|---|
| `Planned` with a dispatch grant | — | §5.8 |
| `Queued` or `Running`, withdrawing `S` (reason set, message `withdrawing attempt S`) | any | the `Withdraw` effect (§5.10); never the owed request below |
| `Queued` | absent, or Seq below the block's and not an unincorporated fact | write the record and publish (owed) |
| `Queued` at `S` | an unincorporated `answered` fact at an earlier Seq (the `Request` for `S` missed its CAS, so it never landed) | close `S` without a record write (nothing was published), then incorporate the fact (§4.10) |
| `Queued` | `requested`, under 50 min | republish; requeue every minute |
| `Queued` | `requested`, unclaimed past 50 min | withdraw; `Planned`, reason `Unclaimed`; attempts unchanged |
| `Queued` / `Running` | `claimed` | `Running`; `workerPod = Writer`; `startedAt = ClaimedAt` |
| `Running` | `claimed`; lease absent (`ErrKeyNotFound`) | note `leaseMissingSince` in the ledger; after `leaseLostGrace` (180 s) ask `Admin.Subjects(CLUSTARR_WORK_SQUASHARR, task.*.*.<mfUID>)`: if the task is still stored, JetStream will redeliver after AckWait, so wait; otherwise `Decide` as retriable, reason `WorkerLost`, back to Planned with backoff, counted toward `MaxAttempts` |
| `Running` | `claimed` beyond `Task.Deadline + deadlineGrace` | withdraw; `Failed`, reason `DeadlineExceeded` |
| `Queued` / `Running` | `answered` | `Decide` (`decide.go:80-124`, moved and re-typed to `(task.Answer, TranscodeState, auto)`; logic unchanged): `Swapping` (success, swap pending), `Skipped`, `Failed` (blocked or not), Planned with `nextAttemptAt` and/or `fallbackReason`, or a no-op when withdrawn. Then `dispatch.answeredSeq = seq`. `Answer.Graft` is not the transcode planner's: the graft planner reads it from this record and renders it into `status.graft` (§5.13). The R9 metrics are observed after the apply that first makes `S` terminal lands (`metrics.go:61-127`). |
| `Queued` / `Running` | `failed` (§4.8's last delivery) | `Decide` as retriable with the record's `Failure` as the message, `Transient` steering the backoff, counted toward `MaxAttempts`; never `WorkerLost`, whose 180 s wait would hide the cause |
| any phase, `dispatch.withdrawn` and within `records.FactWindow` | `answered` succeeded at `dispatch.seq` (a fact) | incorporate as `Swapping` whatever the phase, clear `dispatch.withdrawn` (§4.10) |
| `Queued` / `Running` | dead-letter annotation names `clustarr.work.transcode.task.*.*.<mfUID>`, dated after `dispatchedAt` | `Failed`, `blocked`, reason `DeadLettered` (replaces `deadLetteredTask`, `controller.go:404-413`, `:478-491`) |
| `Queued` | `probeHash != status.probeHash` | withdraw, then `Pending`, reason `ProbePending` (the slot frees, the window place stays); a Running one is left to the worker's `SourceChanged` report |
| `Queued` / `Running` | orphaned: `pool` is not `pool.Name` of its current profile and class (`dispatch.go:77-85`) | profile recreated under the same name: withdraw, back to Planned; profile gone: withdraw, then `Skipped`, reason `ProfileGone`, kept until `dispatchedAt + records.FactWindow` so a late fact is still incorporated, then cleared (the file's winner changed; replaces `Failed/ProfileDeleted`, `controller.go:98-102`) |
| any (`FileMissing`: `spec.path` is gone) | the record, in any state, at the block's last `dispatch.seq` names `Inputs.OutputPath`, and that file exists | `Swapping` with `result.outputPath` from the record's inputs and size from a stat; the swap's probe decides (its incorporation checks the `CLUSTARR_PROFILE` tag against the block's `profileHash` and refuses the takeover otherwise). This recovers a swap whose answer never landed (§4.8's `Seq > S` row, a lost bucket). |

The `WorkerLost` row is the live case today: a TranscodeJob Running since 2026-10-02 on a vanished
pod, after `clustarr-transcode-leases` and the memory work stream were recreated on 2026-10-05.

**The swap.** `Swapping` keeps `result.outputPath`; the probe planner requests a high-lane probe of
that path; that probe's incorporation applies the spec takeover (§3.10) and `Succeeded`, `Compliant`,
`profileTag = <profile>@<profileHash>` (the hash the task was dispatched under). This replaces
`latestUnincorporatedTranscode`'s `probedAt` comparison (`mediafile_controller.go:803`) and
`transcodeProfileTag`'s Get of the current profile (`:840-846`), which records the wrong hash after a
profile edit and fails on every retry once the profile is deleted. Because the block keeps
`outputPath`, a swap whose record was lost is still visible on disk (`FileMissing` at `spec.path`
while `outputPath` exists).

### 5.10 Withdrawal

`withdraw(uid, S)` runs **before** any status apply that leaves Queued or Running. Under the one
outbox order (§3.8) that takes two passes: the first keeps the phase (Queued or Running), sets
`reason` to the cause (`Cancelled`, `Suspended`, `Rerouted`, `Unclaimed`, `SourceChanged`,
`Orphaned`, `DeadlineExceeded`) with message `withdrawing attempt S`, and emits the `Withdraw`
effect; the next pass, finding the record `withdrawn` at `S`, writes the state the cause dictates
(Planned, `Skipped`/`Cancelled`, `Skipped`/`ProfileGone`, `Failed`, or `Pending`) and sets
`dispatch.answeredSeq = S` and `dispatch.withdrawn = true`. The block always survives the
closing pass with its `dispatch`, so a fact that lands afterwards is still incorporated
(§4.10); only `withdrawAll`, for a deleted file, leaves no block. While a block is withdrawing,
that row of §5.9 takes precedence over the owed-effects row: it never re-requests or republishes
the seq it is taking back. Writing Planned first and crashing would leave a task the rebuilt
ledger cannot see. Suspending a Queued or Running file withdraws it this way, as `spec.suspend`
does today (transcodejob/controller.go:388-401), and returns it to Planned, reason `Suspended`.

1. **Record CAS,** through the one `records.Withdraw` (§4.7): absent, or any state below `S`,
   or `requested`, `claimed` or `deferred` at `S`, becomes `withdrawn{S}`, so a late task cannot
   run; `answered` or `failed` at `S` writes nothing and the planner incorporates the answer
   instead.
2. **`Leases.Put(lease.<mfUID>, {Seq: S, State: cancelled})`**, unconditional, as today
   (`withdraw.go:130-143`). The running worker's next renewal fails on the revision, reads
   `cancelled`, and stops (`worker/lease.go:159-178`). The durable record outlives the 90 s TTL,
   which today's marker does not.
3. **`PurgeSubject(CLUSTARR_WORK_SQUASHARR, "clustarr.work.transcode.task.*.*.<mfUID>")`.** A file
   has at most one live seq.

If any step fails, the block stays in flight with message `withdrawal failed: <err>` and the pass
requeues.

`withdrawAll(uid)`, for a deleted file, writes `withdrawn{math.MaxInt64}` to the record and a cancel
at `math.MaxInt64` to the lease, then purges. It runs from the loop's NotFound pass via
`ledger.Forget`; from an Observe that sees a new UID under the same name; and from the sweep, for a
manager that was down at the delete. The residual risk is a records-only library delete while the
manager is down: a running transcode can swap a file no MediaFile tracks until the new leader's first
pass withdraws it, at most one lease renewal (20 s) after.

### 5.11 Window and backlog

- **The window holds files.** `open(P)` counts P's files in a window phase (Pending, Planned, Queued,
  Running, Swapping) plus outstanding window grants. `--job-window` 0 means unlimited, as at
  `transcodeprofile/controller.go:243`.
- **Filling it.** While `open(P) < window`, the pass grants a place to the best backlog file of P,
  ordered by effective priority (descending), then `namespace/name`: today's name order
  (`controller.go:182-184`) with the priority intent added.
- **Membership.** A file belongs to P's backlog when `Eligible` holds, the file has no window phase,
  and no verdict stops it. `Eligible` is the one pure function, used by the planner and the rebuild
  scan. It requires: the winning profile is P (`winningProfile`) and P is valid (`validateProfile`);
  the item exists (`managedFiles`); the kind is movie or episode; `ProbeCurrent()`; the file is not
  `Transcoded()`, except as the MP4 standard's one-time remux rule defines; and no standalone graft is
  in flight on the file.
- **Verdicts.** A verdict stops planning only while `profileTag` equals the current tag **and**
  `probeHash` equals `status.probeHash`. A new tag (a profile edit or a `standard.Version` raise) or
  new bytes make the file eligible again, Blocked included (§9, D13). Today a Blocked job stays until
  someone deletes it (`transcodeprofile/controller.go:220-229`).
- **Leaving the window.** A Pending or Planned file that stops being eligible (another profile wins,
  new bytes, the item is gone) is cleared, and its place frees, unless its `dispatch.withdrawn`
  is true within `records.FactWindow`: then the block stays, outside the window count, until the
  window passes (§5.10). A profile edit does not evict a Planned file; it is re-planned at
  dispatch, as today.
- **Bounded writes.** A re-roll writes `Planned` to at most `window × profiles` files and holds the
  rest in memory. Wait-reason messages are written only to window files, and only when they change.
  A file holding a verdict under the old hash keeps its block untouched (admission reads a tag
  mismatch as eligible without writing) until the window takes it. A `standard.Version` raise
  therefore costs at most `window × profiles` writes at a time, not one per eligible or verdict file
  (about 12k eligible; 541 Skipped and 3 Failed verdicts live today, and the MP4 standard's
  `HoldImageSubtitles` adds durable ones).

### 5.12 Pools: the `transcodeprofile` reconciler

Triggers: `For(TranscodeProfile)` on generation, `Owns(batchv1.Job)` through `poolJobPredicate`
(`pools.go:626-630`), and `source.Channel(ledger.profileWake)`. It requeues after 1 minute while any
GPU pool has `Active > Ready`. Per profile it:

1. computes `hash = jobspec.ProfileHash(spec)`, `Invalid` and `Overlap`, as today;
2. reads its pool Jobs through the APIReader, so render never mistakes a just-created pool for a
   missing one (`pools.go:82-96`);
3. detects unschedulable GPU pools from pods (`pools.go:216-300`), writing
   `PoolState.UnschedulableUntil` into the ledger, the `PoolUnschedulable` Warning Event on the
   profile, and waking admission;
4. applies `pool.Next(cur, n, drift)` and `pool.Render` under `squasharr-pool`, with delete,
   backoff and recreate unchanged (`pools.go:412-586`), where `n, ok :=
   ledger.DispatchedPerPool(key)`; backoff and recreate live in `PoolState`. **While `ok` is
   false** (the ledger has not set `rebuilt`, §5.6), the reconciler takes no `pool.Next` action
   that suspends or deletes a stored pool: it may create a pool or raise its parallelism for a
   count it already sees, and it requeues in 5 s;
5. writes status (§2.13), skipped when unchanged; while `ok` is false the counts are not
   written either.

**The pool waits for the rebuild.** `pool.Next` suspends a stored, unsuspended pool whose
dispatched count is zero (app/squash/controller/pool/next.go:68-71), and suspending a Job deletes
its active pods. On every manager start or leader handoff the ledger is empty until the
`transcode-admission` controller's first pass rebuilds it (seven quorum Lists plus a cache scan,
§5.6), while this reconciler's triggers (its own `For`, pool Job events, `profileWake`) fire at
once. Sized from that empty ledger, every running pool would be suspended and every in-flight
transcode killed, each becoming `WorkerLost` after 180 s and counting toward `MaxAttempts` (5),
so five restarts during one long encode would leave the file `Failed`/`RetriesExhausted`.
Today's code never sees a partial count: the admission pass re-lists every TranscodeJob and
passes `dispatchedPerPool` in the same pass (transcodejob/controller.go:803-811; pools.go:389).
`TestARestartedManagerNeverSuspendsARunningPool` (envtest: a running pool Job and a Running
MediaFile, then a fresh manager whose admission rebuild is held back; the pool is never
suspended) holds it.

Pools size from **confirmed** dispatches only (Queued + Running), never from grants, so a grant that
fails to plan cannot leave an idle pod (a pool shrinks only to zero, `pool/next.go:46-86`).

### 5.13 Grafts

- **A joined graft holds no graft slot, and the join lives in the transcode block.** At dispatch
  the transcode planner reads the file's previous graft block and the item's donor from the
  view. When the `JoinTask` conditions hold (`audiograft/controller.go:637-660`), it sets
  `task.Graft` and writes its own `status.transcode.joinedGraft{donorRelease, donorImportedAt,
  languages, probeHash}` in the same apply as Queued. It never writes `status.graft`: §3.5 gives
  `graft` to the graft planner alone, and `Copy` takes only each planner's own fields (§3.4), so
  such a write would be discarded. The graft planner, which runs after transcode, derives
  `status.graft{phase: Running, reason: JoinedTranscode, joinedTranscodeSeq: S}` from the draft's
  `transcode.joinedGraft` and `dispatch.seq`; when the transcode record is `answered` at S its
  Gather reads that record and it incorporates `Answer.Graft` (§4.12, graft rows). If the graft
  planner fails or panics in the dispatching pass, its last block (`Waiting`) stays, but the join
  is still in the transcode block: admission excludes any file whose `transcode.joinedGraft`
  names an in-flight dispatch from the graft-slot candidates (§5.5's `FileView.JoinedGraft`), so
  no standalone graft Job is ever granted beside a transcode that grafts, and the next pass
  derives `Running`. Today's cross-object race and its `transcodingLive` re-check
  (`audiograft/controller.go:469-476`) are gone; today the join is recorded on the transcode's
  own object too (TranscodeJob `status.graft`, `GraftJoined`, dispatch.go:450-480). The join wait
  (6 h, `:100-102`) is a decision made within the file. The transcode planner keeps
  `joinedGraft` until its next dispatch and holds that dispatch (the grant is released and the
  file stays Planned, message `waiting for the joined graft of attempt S to be recorded`) while
  `Prev.graft` has not incorporated the joined result of S (`joinedTranscodeSeq == S` and a phase
  other than `Running`), so the graft planner can always still read it.
  `TestAPanickingGraftPlannerNeverLeavesAJoinedTaskBesideAWaitingBlock` holds it.
- **Graft slots.** `--graft-concurrency` is counted per namespace, as today (`:418-431`): a slot is
  in use for the union of unfinished Jobs labelled `squasharr.clustarr.io/graft-of`, graft blocks in
  `Queued` or non-joined `Running`, and outstanding graft-slot grants. Candidates are standalone
  grafts in `Waiting` with reason `WaitingForSlot` whose file's transcode block carries no
  in-flight `joinedGraft`, ordered by `donorImportedAt`, then key (today the order is whichever
  reconciles first).
- **Dispatch** follows §4.12: the graft planner writes `Queued{dispatch.seq, jobName}` first, then
  the record, then creates the Job named `k8s.ChildName(<file>, "graft", seq)`; `AlreadyExists` makes
  a re-create idempotent. There are no reduce Jobs (§4.12).

### 5.14 Retention

Nothing is retained for a reader that no longer exists. A `Swapping` block keeps `result.outputPath`,
size and `finishedAt` until the re-probe of the target is incorporated, then becomes `Succeeded` with
`compliant: true` and `profileTag`. A kept copy (`replaceSource=false`) keeps `outputPath` for good.
`retireSucceeded`, its 1-hour requeue (`transcodeprofile/controller.go:305-309`), `--job-retention`
and `DefaultJobRetention` are deleted. Failed and Skipped "records that stop re-planning"
(`:413-421`) become the verdicts of §5.11.

### 5.15 Crash and failure semantics

| Point of failure | State left | Recovery |
|---|---|---|
| After a grant, before the file pass | Planned, grant in memory | Lost with the process; the new leader rebuilds and the file competes again. |
| After Queued landed, before the record or publish | Queued(S), no record or message | Rebuild counts the slot from the apiserver; the initial pass writes `requested{S}` and publishes `transcode/<uid>/<S>` (owed effects). |
| After the publish, before `Confirm` | Queued(S), task stored | Rebuild counts it; a republish is absorbed. |
| Publish keeps failing (NATS down) | Queued(S), no task | Republished each minute for 50 minutes, then withdrawn and requeued; at most budget-many files affected. |
| Worker claimed, manager down | record `claimed{S}`, status Queued | Incorporated as Running at start: the initial list enqueues every file and each reads its outstanding record. |
| Worker answered, manager down | record `answered{S}` (Durable, 7 d) | Incorporated at start. |
| Single-node NATS restart wipes the memory stream and lease bucket | Queued with no task, or Running with no lease or message | Queued is republished; Running becomes `WorkerLost` after 180 s and is retried (§5.9). |
| Records bucket lost (NATS volume gone) | Queued or Running with no record | Queued: rewrite `requested`, republish. Running: the lease path. A finished swap whose record is lost is redone, and the worker finds `CLUSTARR_PROFILE` and reports success. |
| Withdrawal fails | in flight, message says so | Retried on requeue; nothing becomes Planned until withdrawal succeeds. |
| Planner panics on a granted file | grant in use | The deferred `Release` runs; the grant backs off 1 to 30 minutes; `TranscodePlannerError` is set. |
| Leader handoff mid-pass | grants lost | Rebuild; the CAS stops a double dispatch of one file; a Queued written by the old leader is counted by the quorum List. |
| File deleted while the manager is down | task stored or running | The first sweep runs `withdrawAll`; the worker stops within one renewal (20 s). |
| Two active leaders | — | Not designed for (ADR-0016 revisit trigger); per-file CAS still prevents double dispatch, but over-admission across files is possible while both run. |
| Manager start or leader handoff, before the rebuild | pool Jobs running, ledger empty | The pool reconciler suspends or deletes nothing until `rebuilt` (§5.12); running encodes continue. |
| Swap done, cancel marker landed after the re-assert | `withdrawn{S}`, then the worker's fact | The worker answers the fact (retrying for 10 min, §4.12); the planner reads the record for `records.FactWindow` and incorporates it as `Swapping` (§4.10). An answer that never lands is recovered from disk by the `FileMissing` row (§5.9). |

### 5.16 Metrics, Events, history

- **Kept:** `clustarr_transcode_jobs_active{tier}` (name unchanged), and the three R9 histograms,
  observed on the terminal edge.
- **New:** `clustarr_transcode_backlog_files{profile}` (gauge; the profile is bounded configuration,
  not a title); `clustarr_transcode_ledger_corrections_total`;
  `clustarr_transcode_dispatches_total{class,outcome}` (outcome `published`, `republished`,
  `unclaimed`, `withdrawn`, `lost`).
- **Events on the MediaFile:** `Dispatched`, `Withdrawn` (reason Suspended, Cancelled, Rerouted,
  Orphaned or Unclaimed), `WorkerLost`, `DispatchUnclaimed`, and the terminal edges.
- **History:** `clustarr.evt.transcode.job.<action>.<mfUID>`, id
  `<mfUID>/transcode/<seq>/<action>` (§4.10).

### 5.17 Tests

**Pure, in `app/squash/admission`:** `TestAdmitNeverExceedsBudget` and
`TestChooseClassGPUOnlyWaitsWhileAGPUIsUsable` (moved); `TestFillWindowTakesPriorityThenName`;
`TestWindowCountsWindowPhasesAndGrants`; `TestObserveIgnoresAStaleObservation` (the (seq, rank)
table); `TestGrantInUseNeverExpires`; `TestExpiredGrantBacksOff`;
`TestWaitReasonEnqueuesOnlyOnChange`; `FuzzLedgerNeverOverAdmits` (random Observe stale and fresh,
Pass, TakeGrant, Confirm, Release, expiry, crash-and-Rebuild; invariant: in-flight + grants ≤
`Slots[class]` and per-profile `maxConcurrent` after every Pass that granted).

**envtest with the real MediaFile CRD and its selectable fields:**
`TestRebuildCountsEveryInFlightFileFromTheAPIServer` (40 files across every phase, the cache reader
stubbed empty; exact per class, pool and profile window); `TestRebuildSeesALastMomentDispatch` (a
Queued write by a direct client immediately before Rebuild).

**envtest plus an embedded NATS server:** `TestDispatchPublishesOneTaskPerSeq` (two passes, the
second from a stale cached Planned: one Queued write, one stored message, Msg-Id
`transcode/<uid>/<seq>`); `TestCrashAfterQueuedBeforePublishRepublishes`;
`TestRepublishStopsOnceClaimed`; `TestUnclaimedDispatchIsWithdrawnAfterTheWindow` (fake clock);
`TestWorkerLostIsRetried`; `TestWithdrawalPrecedesThePlannedWrite` (an interceptor client and KV
record the order; a failing purge keeps the block Running); `TestDeletedFileTaskIsWithdrawnAtOnce`;
`TestSweepWithdrawsADeletedFilesTaskAfterRestart`; `TestTwoFilesNeverShareTheLastSlot` (`cpu=1`, 10
Planned files, the loop at 8 workers plus admission; a watch asserts at most one Queued or Running at
every resourceVersion); `TestPoolParallelismFollowsConfirmedDispatches`;
`TestHeldFilesAreWrittenOnlyOnChange` (drift a pool under 32 Planned files: 32 status writes, then
none over 5 more passes); `TestARestartedManagerNeverSuspendsARunningPool` (§5.12);
`TestAClearedBlockFreesItsSlot` (new bytes clear a Queued block; the slot frees at the next
`Observe`); `TestAFinalDeliveryFailureShowsItsCause` (a `failed` record, not `WorkerLost`);
`TestAPanickingGraftPlannerNeverLeavesAJoinedTaskBesideAWaitingBlock` (§5.13); intents
`TestSuspendKeepsAWindowPlace`,
`TestCancelRecordsAVerdictAndRetryClearsIt`, `TestHardwareIntentAppliesAtTheNextDispatch`,
`TestPriorityIntentOrdersWindowAndAdmission`; grafts `TestGraftSlotsCountJobsStatusAndGrants`,
`TestGraftJobNameIsTheDispatchIdentity`; natsbus contract `TestTranscodeTaskMsgIDDedupesOnTheRealServer`.

**Guards (`test/guards`):** `TestAdmissionWritesNoMediaFileStatus` (AST: `app/squash/admission` and
`app/squash/controller/transcodeprofile` call no `PatchStatus`, `PatchStatusCAS` or `Apply` with a
MediaFile apply configuration); `TestTranscodePhaseIsSelectable`; `TestPlannerTagIsTheProfileStatusHash`.

**Falsification, each run once and recorded in the commit:** swap §5.8 steps 5–7 back to
publish-before-Queued, and `TestRebuildSeesALastMomentDispatch`'s crash variant and
`FuzzLedgerNeverOverAdmits` must fail; drop the (seq, rank) check, and
`TestObserveIgnoresAStaleObservation` and `TestTwoFilesNeverShareTheLastSlot` must fail.

### 5.18 Deleted, and documents that change

**Deleted:** `transcodejob`'s `Reconciler`, `ResultsConsumer`, `writeStatus`/`patchCAS`,
`FinalizerTaskWithdrawal`, the `admissionRequest` key and the `indexMediaFileRef`/`indexProfileRef`
indexes; `transcodeprofile`'s per-file pass (`countOpen`, `openFiles`, `ensureTranscodeJob`,
`retireSucceeded`, `jobForFile`); the `audiograft` controller's `running`, `start` and `create`,
whose logic moves into `graftplan`; `MsgIDForTranscodeEvent`, `WorkTranscodeResultSubject` and
`ConsumerSquasharrResults`.

**Documents to amend:** ADR-0009's refinement bullet already states the loop admits from MediaFile
status; the transcode-worker design §8; CLAUDE.md's Transcoding paragraph (`--job-window` now counts
files, `--job-retention` is gone); split spec §3.4.1 (drop `--job-retention`, add
`--graft-concurrency`), §3.4.3 and §5.13 (drop `ResultsConsumer` and `transcodejob`).

---

## 6. Subtitles in MediaFile status

`status.subtitles` (§2.4) replaces SubtitleRequest. One planner inside the loop replaces the per-file
work of the SubtitleProfile and SubtitleRequest controllers. The fetch worker reports through
sequence-fenced records. SubtitleProfile keeps a slim reconciler for its own status (§2.13). This
section covers only what subtitles add to the loop.

### 6.1 What it replaces (verified)

- **Creation.** On every pass, `subtitleprofile.Reconciler.Reconcile` lists every SubtitleProfile,
  MediaFile, Movie, Episode and SubtitleRequest (app/caption/controller/subtitleprofile/controller.go:134-171),
  picks each file's winning profile (`winningProfile`, profile.go:193), skips files that are not
  probed (controller.go:187), deletes requests `MayWant` calls unneeded (:191-201), and applies
  `{mediaFileRef, profileRef}` with the MediaFile as controller owner under `captionarr` (:320-342).
- **Planning.** `subtitlerequest.Reconciler` reads the MediaFile, the item (`itemGone`,
  reconciler.go:297) and the Series (`originalLanguage`, :321); calls `os.ReadDir` on the media
  directory (:251) and plans (:397); publishes with `MsgIDForSubtitle` or `MsgIDForForcedSubtitle`
  (:467-470) and stamps attempts at dispatch (:488-490); applies its half of status with no
  resourceVersion (:640-652); resets `spec.forceSearch` with a merge patch (:704-710).
- **Fetching.** The worker re-reads the request and checks the item is still live
  (worker/fetch/worker.go:250); resolves the profile by a third rule (profile.go:43-104); lists
  RootFolders for the sidecar file mode (mode.go:45); writes the sidecar, then records its half with
  `PatchStatusCAS`, 8 attempts (apply.go:298-334); decides downloaded versus upgradable with a
  hard-coded margin of 3 (worker.go:66-70, apply.go:145-148).
- **Mirror.** catalogarr copies downloaded and upgradable items into `MediaFile.status.sidecars`
  (app/catalog/controller/mediafile/sidecars.go:35-58; mediafile_controller.go:444-451), woken by a
  SubtitleRequest watch (:733-734).

Live state on 2026-10-06 (read-only, from the migration inventory): 2,445 SubtitleRequests (2,164
Satisfied, 267 Searching, 12 Wanted, 2 Blocked); 283 items (269 pending behind an OpenSubtitles
throttle, 14 failed), 277 of them at `attempts.count` 14; no item has ever downloaded a subtitle, and
no MediaFile has `status.sidecars`.

### 6.2 Decisions

1. **One block per managed, probed movie or episode file,** phase always written, `NotWanted`
   included. No `existing` list, no conditions of its own, no fingerprint.
2. **One pure planner, `app/caption/subtitleplan`,** run by the loop through the `subtitles` adapter.
   The SubtitleRequest reconciler is deleted; the SubtitleProfile reconciler shrinks to validation and
   its own status.
3. **The manager reads `/data` for subtitles.** The loop lists the media directory, gated (§6.4.3).
   `status.sidecars` becomes that listing, not a mirror of downloads.
4. **Workers report through records** in `clustarr-subtitles` (`pkg/subtitlestore`, a `pkg/records`
   instantiation). The worker writes no Kubernetes object.
5. **Every dispatch carries a sequence the loop issued.** The Msg-Id is
   `subtitle/<fileUID>/<langKey>/<seq>`; `MsgIDForSubtitle` and `MsgIDForForcedSubtitle` are deleted.
6. **Forced search is a nonce annotation,** `subtitle.clustarr.io/search`, recorded in
   `status.handledNonces.subtitleSearch`.
7. **The loop derives item state,** `upgradable` included, from the worker's facts and the profile's
   `upgrade` block. The worker's margin of 3 is deleted.
8. **The task carries everything item-specific.** The worker reads no MediaFile, Movie, Episode,
   Series, RootFolder or SubtitleRequest.
9. **`clustarr-provider-throttle` is unchanged.**
10. **A backlog gate protects the memory work stream:** scheduled dispatches wait while the caption
    queue is deep, on top of the records pacer.

### 6.3 API

The block is §2.4. Realistic size about 150 B plus about 250 B per item; worst case, every string at
its cap and 20 items, about 31 KB plus `wanted`, inside the §2.11 budget test. Its print column and
selectable field are `.status.subtitles.phase` (§2.10).

### 6.4 The planner (`app/caption/subtitleplan`)

**Purity.** The planner imports no `os`, `net`, `sigs.k8s.io/controller-runtime/pkg/client` and no
bus (`TestThePlannerImportsNoIO`). The adapter's `Gather` does the I/O; the loop runs it after the
probe, transcode, graft and naming planners in the same pass and before markers.

```go
type Input struct {
	File        *catalogv1alpha1.MediaFile         // the pass's view
	Item        Item                               // from the owner objects the loop already reads (naming.go:199-264)
	Profiles    []subtitlev1alpha1.SubtitleProfile // cache, cluster-scoped, not deep-copied
	Extractors  []providerset.Entry                // enabled embedded SubtitleProviders in the namespace (providerset's light half, split P1)
	RootFolders []catalogv1alpha1.RootFolder       // the namespace's, already listed for naming
	Records     map[string]subtitlestore.Entry     // records read this pass, by langKey (record, revision, ok)
	Listing     *Listing                           // nil when Needs said not to list
	ForceNonce  string                             // the annotation's value
	BacklogOpen bool                               // §6.4.6
	DataDir     string
	Now         time.Time
}
type Item struct { Exists bool; OriginalLanguage string; Query schema.SubtitleQuery }
type Listing struct { Names []string; Err error } // regular files in filepath.Dir(local path)

func Needs(in Input) Need // which records to read; whether to list
type Need struct { List bool; Records []string }
func Plan(in Input, issue func(recordSeq int64) int64) Decision
func ItemFromMovie(m *catalogv1alpha1.Movie) Item
func ItemFromEpisode(ep *catalogv1alpha1.Episode, s *catalogv1alpha1.Series) Item
```

`Decision` holds the planned block; `Sidecars` and `SidecarsListed`; `Dispatches []Dispatch` (each
with `LangKey`, `Upgrade`, `Forced`, `MinScore`, `OnDisk`, `Priority`, `Seq` and `Task`); `Withdraws
[]Withdraw` (`LangKey`, `Seq`); `Wake time.Time`; and the nonce handled. The adapter turns
dispatches and withdraws into effects (§3.8). `issue` is `View.Issue`.

`ItemFromMovie` and `ItemFromEpisode` port `buildQuery`'s ids, title, year, season and episode
(fetch/query.go:56-133, without the hash) and `originalLanguage` (reconciler.go:321-343).

**Moved here with behaviour unchanged** unless a subsection says otherwise: `plannerProfile`,
`normalizeLang`, `normalizeKey`, `ignoredByPolicy`, `extractors`, `extractableStreams`,
`embeddedExisting`, `sidecarExisting`, `audioLanguages` and `minScoreFor` (its override argument
gone), from existing.go; `planItems` (items.go) without the liveness rule; all of schedule.go;
`sidecarMode` and `parseFileMode` (fetch/mode.go:57-86).

#### 6.4.1 Holds and blocks

The first row that applies decides.

| Condition | Result |
|---|---|
| Kind is not movie or episode | No block. |
| `!Item.Exists`: the Movie, or the Episode named by `spec.mediaRef.name`, is gone (removeAndKeep) | `Blocked`, `ItemNotFound`. Items stay verbatim; no listing, no dispatch, no wake (the item watch brings it back). |
| `!File.ProbeCurrent()` (covers "never probed") | Hold: the previous block (or none) stays verbatim; nothing is listed or dispatched. Incorporating the probe runs this planner in the same pass, so no wake is needed. |
| No winning profile (§2.13) | `Blocked`, `NoProfile`. |
| The winner is Invalid | `Blocked`, `ProfileInvalid`; the message names the profile. |
| `datapath.Local(DataDir, spec.path)` refuses the path | `Blocked`, `MediaFileNotOnDataVolume`, no wake. |
| `Listing.Err` | `Blocked`, `MediaDirUnreadable`, wake in 5 m (an EIO is transient instead, §3.6). |
| The listing lacks the video's base name | `Blocked`, `MediaFileNotOnDisk`, wake in 5 m (reconciler.go:268-275's distrust of a foreign listing). |
| A record read failed (KV error) | A transient planner error (§3.6): the block stays verbatim, nothing is written, the key backs off. A NATS outage must not write onto about 12k files. |

A Blocked block keeps items, attempts, `nextSearchAt` and dispatches verbatim: today's `block()`
(reconciler.go:596-618) without the liveness rules.

#### 6.4.2 One profile rule

The winner is §2.13's rule: among profiles whose selector matches the MediaFile's labels
(profile.go:143-152; a nil or malformed selector matches nothing), the `profileLess`-least (creation
time, then name; :161); else `defaultWinner` (:171). An Invalid winner is not skipped; the file is
`Blocked`, `ProfileInvalid`. A user pins a file to a profile by labelling the MediaFile with a key
outside `catalog.clustarr.io/*`, which survives the loop's label apply because that apply declares
only `mirrorLabels`' keys (labels.go:32-52).

#### 6.4.3 Which process reads `/data`, and when

**The manager lists:** `os.ReadDir(filepath.Dir(datapath.Local(DataDir, spec.path)))`, moved from
subtitlerequest/reconciler.go:243-276. The manager already mounts `/data` read-write (split §5.14,
whose item 7 becomes "the subtitles planner's listing"). The caption agent keeps `/data` too, for
`stat`, moviehash, embedded extraction and writing sidecars.

`Need.List` is true only when one of these holds:

1. **A dispatch may go out this pass:** an item is due by its schedule while the backlog gate is
   open **and the file's records-pacer reservation is due** (§4.7; the reservation is checked
   before the listing, so a paced-out file does not list), or a force nonce is pending (a
   hand-placed sidecar must be able to stop a search).
2. **An `answered` record is being incorporated** (the worker wrote or replaced a file).
3. **The directory changed:** this pass incorporated a probe of new bytes or a swap or graft, or the
   previous pass renamed the file.
4. **The UID is not yet in `subtitles.listed`,** an in-memory set reset when the process becomes
   leader; each leader start therefore relists every video file once (about 12k `ReadDir` calls at
   the loop's concurrency).
5. **The block is new.**

Nothing else lists. A naming-only or markers-only pass never touches NFS for subtitles, and there is
no periodic relist.

**What the listing yields:**

- **`status.sidecars`:** one entry per name `subtitles.ParseSidecar(stem, name)` attributes (`name`,
  and in release N its absolute `path`; language, forced, hi), sorted by name, capped at 32 (§2.7). Not filtered by profile: hand-placed
  sidecars count, and so do the `.srt`, `.ass` and `.sdh` sidecars the MP4 standard writes
  (mp4-standard §4.1, on main). This replaces `sidecarsFromSubtitleRequest` (sidecars.go:35-58),
  `scanSidecars` (mediafile_controller.go:864), the SubtitleRequest watch (:733-734) and its index
  (:762-764). Without a listing, `status.sidecars` is carried from the stored status.
- **The sidecar half of existing:** only profile languages count, as in `buildExisting`
  (existing.go:234-254). Nothing is stored, so its 64-entry cap goes.
- **Rebinding item names.** If a downloaded item's `name` is missing from the listing: when
  `filepath.Base(subtitles.SidecarName(spec.path, key, hiExt))`, or its `.ass` twin, is present, the
  item is rebound to it (a rename moved it); otherwise its candidate is cleared (state `pending`,
  lastError `sidecar <name> is gone`), so the language is wanted and downloaded again. This fixes a
  bug: today a deleted downloaded sidecar is never replaced, because the worker treats the item as on
  disk (worker.go:385-388) and keeps it unless something better turns up (apply.go:73-74, 137-140).

#### 6.4.4 What is wanted

- **Existing subtitles:** `embeddedExisting(mediaInfo, policy, extractable)` plus
  `sidecarExisting(names)`, the names from the listing or, without one, from `status.sidecars`.
- **Audio languages:** `audioLanguages(mediaInfo, Item.OriginalLanguage)` (existing.go:273-288).
  Tagged tracks win; untagged or `und` audio counts as the item's original language (the Movie's
  `status.metadata.originalLanguage`, or the Episode's Series'). `audioExclude` and
  `audioOnlyInclude` apply through `plannerProfile` and `subtitles.Plan`
  (pkg/subtitles/planner.go:113).
- **The plan:** `wanted, cutoffMet := subtitles.Plan(pp, audio, existing)`.
- **`MayWant` retires.** It only decided whether to create an object (controller.go:191) and it
  ignored the original language (existing.go:366), so the 2,571 files with untagged audio each got a
  request that was then planned against the original language. The planner instead runs
  `subtitles.Plan(pp, audio, nil)`; if that wants nothing and no item remains, the phase is
  `NotWanted`.
- **Items, one per langKey** (`planItems`, items.go:48-74, without liveness): keep an item for a
  profile key that is wanted or has a subtitle on disk (`downloadedAt` or `name`); append one for
  each wanted key with none; drop the rest by omitting them. Dropping an item with a dispatch in
  flight also emits a `Withdraw` (§6.6).
- **New bytes.** When the block's `probeHash` differs from `status.probeHash`, every item's attempts
  reset, every outstanding request is withdrawn, and the block takes the new hash
  (reconciler.go:411-420, plus the withdrawal).

#### 6.4.5 Schedule, state and phase

- **`nextSearchAt`:** wanted items get `wantedDueAt`, items with a subtitle on disk get
  `upgradeCheckAt` (schedule.go:108-196), recomputed from attempts on every plan.
- **What is due:** a wanted item when `now >= wantedDueAt` and nothing is in flight; an upgrade when
  `upgradeCandidate`, `upgradeScheduled` and `now >= upgradeCheckAt` hold and nothing is in flight.
- **A dispatch ends three ways:** an answer, a withdrawal, or `subtitleRequestTimeout = 26h`. The
  26 h covers the fetch durables' worst span: seven expired deadlines (30 s, 2 m, 10 m, 1 h, 6 h,
  6 h, 6 h) plus the eighth delivery's 6 h is 25 h 12 m (topology.go:839-852). The schedule never
  re-dispatches an outstanding request.
- **`MinScore` has one home:** `minScoreFor(kind, profile)`, or for an upgrade
  `upgradeMinScore(score, threshold)`; with a subtitle on disk, `max(that, score+1)` (the worker's
  on-disk raise, worker.go:395-410, moved here).
- **Priority:** high when forced, low for an upgrade, normal otherwise (reconciler.go:459-465).
- **Wake:** the minimum of the scheduled `nextSearchAt` values; `dispatch.dispatchedAt + 26h` for
  each outstanding item; `now + 5m` when the backlog gate deferred a due item; 5 m for disk blocks;
  floored at 30 s (reconciler.go:747-752).
- **Item state:** with a subtitle on disk (`name` and `downloadedAt` set), `upgradable` if
  `upgradeCandidate(now, item, cadenceFor(profile))` holds (schedule.go:145-159, reading the profile's
  `upgrade.enabled`, `lookbackDays` and `minDeltaPoints`), else `downloaded` (a profile at the CRD
  default `minDeltaPoints` of 3 sees no change); otherwise `searching` while a dispatch is in flight;
  otherwise the last verdict incorporated, `unavailable`, `failed`, or `pending` (throttled, or never
  answered).
- **Phase:** Blocked (§6.4.1); else `Searching` if any dispatch is in flight; else `Wanted` if
  `wanted` is not empty; else `NotWanted` if `Plan(pp, audio, nil)` is empty and no items remain;
  else `Satisfied`. Today a throttled or never-reported item holds its request in Searching
  (reconciler.go:519-523; live, 267 requests read Searching behind one throttle); it no longer does.

#### 6.4.6 Dispatch and the backlog gate

`Plan` decides dispatches in langKey order; the adapter turns them into effects after the apply
(§3.8):

1. **Gather reads the record** for every due key and every key in flight (`Needs`). A `requested`
   record with `Seq` above the item's `dispatch.seq` and matching inputs is adopted (§4.7): the item
   takes `rec.Seq`, and the effect republishes the same Msg-Id. An `answered` record above it is
   incorporated (§6.6) instead of dispatched.
2. **Issue the sequence:** `seq = issue(rec.Seq)` = `records.NextSeq(rec.Seq, status.lastSeq, now)`.
3. **Render** the item `dispatch{seq, dispatchedAt: now}`, state `searching`, and stamp attempts
   (schedule.go:126). The apply lands.
4. **Effects:** `subtitlestore.Request` writes the `requested` record by CAS at the revision Gather
   read (a miss requeues the file at once); then publish the v2 FetchTask (§6.7.1) on
   `events.WorkFetchSubject(prio, fileUID, langKey)` with
   `events.WithMsgID(events.MsgIDForSubtitleFetch(fileUID, langKey, seq))` and
   `events.WithExpectStream(events.StreamWorkCaptionarr)`. A failed publish is a transient effect
   failure: the record stays `requested`, and later passes republish while `now - dispatchedAt <
   records.RepublishWindow`; past it, the 26 h timeout decides. (`ErrQueueFull` cannot occur here:
   `CLUSTARR_WORK_CAPTIONARR` is a `work()` stream with `DiscardOld`, topology.go:541-563, so a full
   stream evicts its oldest task instead.) Republishing the same Msg-Id recovers a failed publish
   and a stream wiped by a NATS restart, but not an evicted or purged task, because JetStream keeps
   the Msg-Id in its dedupe map for the whole window after the message is gone (nats-server v2.15.0
   `stream.go`: `purge` at :3122 leaves `ddmap` alone, and entries expire only on the window timer,
   :5562-5571). So on the `DiscardOld` streams (captionarr, segmentarr) an owed republish first asks
   `Admin.Subjects` whether the task is still stored, and if not republishes under
   `<Msg-Id>/r<unix-minute>`; workers fence on the record, so a duplicate does no harm. Today's duplicate-receipt handling (reconciler.go:481-490) goes: a duplicate can only be
   a republish of the same sequence.

**Why the backlog gate.** `CLUSTARR_WORK_CAPTIONARR` is a `work()` stream that is not Durable
(topology.go:600), so on a single node `ForSingleNode` makes it a memory stream of about 1.7 MiB that
discards the oldest messages (split §9.1.1). A profile edit that adds a language makes every file's
new item due at once (`wantedDueAt` is "now" for a language never searched, schedule.go:108-112), and
today that silently discards queued tasks.

**How it works.** `captionBacklog` is a leader-only runnable in `app/remediation`. Every 30 s it reads
`StreamAdmin.ConsumerState(ctx, events.StreamWorkCaptionarr, events.ConsumerCaptionFetchNormal)`
(split §9.2). `BacklogOpen` is true while that reading is under 2 m old and
`Lag() < subtitleBacklogCeiling` (500). A closed gate defers scheduled and upgrade dispatches (no
record, no stamp, wake in 5 m). Forced dispatches go to the high durable and bypass the gate. 500
tasks of about 1.5 KB fit the scaled stream with headroom. The records pacer (120/min, §4.7) applies
as well.

#### 6.4.7 What wakes a file for subtitles

| Source (§3.3) | Predicate | Enqueues |
|---|---|---|
| S1 MediaFile | the `subtitle.clustarr.io/search` annotation changed, beside `observedFingerprint` (mediafile_controller.go:721-724, :883-888) | the file |
| S19 `clustarr-subtitles` | `answered` or `failed` values | the record's file |
| S2' Movie | `movieFileInputs` = `movieNamingInputs` plus `status.metadata.originalLanguage` | the movie's files |
| S4' Series | `seriesFileInputs` = `seriesNamingInputs` plus `originalLanguage` | the series' episodes' files |
| S2'/S3' create or delete | already passed by `GenerationChanged` (predicates.go:44-76) | their files (`ItemNotFound` and back) |
| S13 SubtitleProfile | `GenerationChanged`, or `Invalid` changed | every movie and episode file, `LowPriority` |
| S14 SubtitleProvider | embedded type and `GenerationChanged` (reconciler.go:799-800, 843-846) | files with a subtitles block in its namespace |
| time | the planner's `Wake` | the file |

No watch existed on `originalLanguage`; a metadata change reached a request only at its next requeue.
A profile event enqueues about 12k keys; no listing follows (the directory has not changed), and only
files whose block changes are written, through the status-write limiter (§3.7). Since the block
records no profile generation (§2.4), an edit that changes no file's wanted languages writes no
MediaFile at all. Today such an event re-plans 2,445 requests with a `ReadDir` each.

### 6.5 Forced search: the annotation nonce

- **The annotation** `subtitle.clustarr.io/search` (`catalogv1alpha1.AnnotationSubtitleSearch`), a
  nonce in §2.9's format. From a shell:
  `kubectl annotate mediafile <name> subtitle.clustarr.io/search=$(date +%s) --overwrite`.
- **When it fires.** A nonce is pending when it is non-empty and differs from
  `status.handledNonces.subtitleSearch`. A pending nonce dispatches every wanted item and every
  upgrade candidate now, as today's `force` does (reconciler.go:424-454), including items with a
  dispatch in flight: the new sequence supersedes it, and the old task's worker finds a newer
  `requested` record and acks without searching. Forced dispatches go high, list the directory first
  and bypass the backlog gate.
- **When it is recorded:** in the same apply that renders the forced dispatches, including when there
  is nothing to dispatch. While the file is Blocked or held, the nonce stays pending and is honoured
  when the block lifts.
- **The loop never writes the annotation** (ADR-0016).
- **Msg-Id uniqueness.** `MsgIDForForcedSubtitle` keyed a forced search on the SubtitleRequest's
  generation (subjects.go:546-558); an annotation edit does not bump a MediaFile's
  `metadata.generation`. No key is needed: every dispatch carries its own sequence, so
  `subtitle/<fileUID>/<langKey>/<seq>` is unique per dispatch for the life of the file, and the 1 h
  window absorbs only a republish of that dispatch. A reused nonce never dispatches twice, because
  `handledNonces.subtitleSearch` already holds it. `MsgIDForSubtitle` (:542) and
  `MsgIDForForcedSubtitle` (:556) are deleted;
  `events.MsgIDForSubtitleFetch(fileUID, langKey string, seq int64) string` replaces them.

### 6.6 Records: `clustarr-subtitles` and `pkg/subtitlestore`

| Kind | Name and settings |
|---|---|
| Bucket | `events.BucketSubtitles = "clustarr-subtitles"` (§4.3). |
| Key | `events.RecordSubKey(fileUID, langKey)` = `KVKeyToken(fileUID) + "." + KVKeyToken(langKey)`; each language has its own key, so concurrent languages never contend, and the 8-attempt CAS loop (apply.go:334) goes. |
| Record | `schema.SubtitleRecord`, schema `captionarr.SubtitleRecord.v1`, embedding `schema.RecordHeader` (`mediaFile`, `sub` = langKey, `seq`, `state`, `requestedAt`, `answeredAt`, `writer`) plus `ProbeHash`, `ProfileGeneration`, and when answered `Verdict` (`downloaded`, `kept`, `unavailable`, `failed`, `throttled` or `dropped`), `Score`, `ScoreOutOf`, `Provider` (≤253), `SubtitleID` (≤256), `Name` (base name, ≤255), `ReplacedName`, `LastError` (≤512), `ThrottledUntil`. Capped at the bucket's 8 KiB. An undecodable value, or one whose `mediaFile.UID` differs from its key's, reads as absent. |
| Package | `pkg/subtitlestore`, the `pkg/records` instantiation: `New(bus)`, `Get`, `Request`, `Withdraw`, `Superseded`, `Answer`. |
| Waker | S19, `recordsource.New(bus, events.BucketSubtitles, …)` (§4.9). |

**Protocol (§4.7-§4.8):** the loop's `Request` creates the record when absent, else updates at the
revision Gather read, and never overwrites an `answered` record it has not incorporated
(`records.ErrUnincorporatedFact`; every subtitle answer is a fact, since the worker may have
written or replaced a sidecar). The loop's `Withdraw(fileUID, langKey, seq, rev)` is
`records.Withdraw` with §4.7's table (absent, below `seq`, or `requested` at `seq` become
`withdrawn{seq}`), on a dropped language, new bytes or the 26 h timeout; a task that surfaces
late is acked unworked. The worker's `Superseded(task)` is true when the current `Seq` is greater than `task.Seq`,
or the record is `withdrawn` or answered at a `Seq` of at least `task.Seq`; it is checked before any
provider request. The worker's `Answer` re-reads immediately before its CAS and writes when the record
is absent, `requested` with `Seq <= task.Seq`, or answered with `Seq < task.Seq`; otherwise it drops
its result. Nobody deletes records; the 7-day TTL retires them.

**Incorporation,** for each item with a dispatch in flight, and for records §6.4.6 step 1 found:

| Record | Verdict |
|---|---|
| answered, `Seq == dispatch.seq`, `ProbeHash == block.probeHash` | incorporate (next table); `answeredSeq = seq` |
| answered, `Seq == dispatch.seq`, another `ProbeHash` | discard; `answeredSeq = seq` |
| `requested`, `Seq == dispatch.seq`, `now < dispatchedAt + 26h` | outstanding (Searching); wake at the timeout |
| `requested`, `Seq == dispatch.seq`, timed out | `Withdraw`; on the next pass `answeredSeq = seq`, lastError `no answer within 26h` |
| `requested` with a newer `Seq` and matching inputs | adopt it (§6.4.6 step 1) |
| answered with an older `Seq` above `answeredSeq`, `ProbeHash == block.probeHash` | an answer the item never incorporated (a forced search superseded a dispatch whose worker answered between Gather and the new `Request`, so that `Request` missed its CAS): incorporate it (next table), keep `dispatch.seq`, and owe the request for it (below) |
| absent, `withdrawn` or `requested` with an older `Seq`, or an older answer already incorporated, and `now - dispatchedAt < records.RepublishWindow` | **owed** (§4.7): `Request{dispatch.seq}` at the revision read, then publish under the same Msg-Id; the item stays Searching. A crash, a KV error or a CAS miss between the status apply and the record write heals here, and a forced nonce recorded in that apply still gets its search |
| the same, past `records.RepublishWindow` | `answeredSeq = seq`, lastError `request lost`: the request was lost; attempts were stamped at dispatch, so the schedule decides the next search |

| Verdict | Effect on the item |
|---|---|
| `downloaded` | sets `score`, `scoreOutOf`, `provider`, `subtitleID`, `name` and `downloadedAt` (= `AnsweredAt`), clears `lastError`; state is derived (§6.4.5) |
| `kept` | nothing beyond closing the dispatch: the upgrade found nothing better |
| `unavailable`, `failed` | with a subtitle on disk, only `lastError` (apply.go:224-227); otherwise the candidate is cleared (apply.go:212-218) and the state follows the verdict |
| `throttled` | sets `lastError`; state `pending` unless a subtitle is on disk; `nextSearchAt` at or after `ThrottledUntil` |
| `dropped` | sets `lastError` to `fetch dropped: <reason>`; nothing else |

### 6.7 The fetch worker (agent `caption` domain)

#### 6.7.1 Task v2

`schema.FetchTask` moves to schema `subtitle.FetchTask.v2`; `schema.FetchTaskV1Schema =
"subtitle.FetchTask.v1"` is kept to recognise old tasks.

```go
type FetchTask struct {
	MediaFile           Ref                       `json:"mediaFile"` // namespace, name, uid
	Path                string                    `json:"path"`      // logical /data path planned against
	ProbeHash           string                    `json:"probeHash"`
	LangKey             string                    `json:"langKey"`
	Seq                 int64                     `json:"seq"`
	Profile             string                    `json:"profile"`
	ProfileGeneration   int64                     `json:"profileGeneration"`
	MinScore            int32                     `json:"minScore"`            // absolute, from the loop (§6.4.5)
	UpgradeMarginPoints int32                     `json:"upgradeMarginPoints"` // the profile's upgrade.minDeltaPoints
	Upgrade             bool                      `json:"upgrade,omitempty"`
	Forced              bool                      `json:"forced,omitempty"`
	OnDisk              *OnDiskSubtitle           `json:"onDisk,omitempty"`      // {Name, Score}
	SidecarMode         uint32                    `json:"sidecarMode,omitempty"` // the RootFolder's fileMode; 0 means 0664
	Query               SubtitleQuery             `json:"query"`
	Extract             []commonv1.SubtitleStream `json:"extract,omitempty"` // embedded streams to write out (extractableStreams)
}
type SubtitleQuery struct {
	Kind         commonv1.MediaKind `json:"kind"`
	IDs          map[string]string  `json:"ids,omitempty"` // pkg/subtitles.Query keys: tmdb, imdb, tvdb, parent_imdb, parent_tmdb
	Title        string             `json:"title,omitempty"`
	Year         int32              `json:"year,omitempty"`
	Season       int32              `json:"season,omitempty"`
	Episode      int32              `json:"episode,omitempty"`
	ReleaseTitle string             `json:"releaseTitle,omitempty"` // spec.importedFrom.releaseTitle, else the file stem
}
```

`Extract` is enough for the embedded provider, which reads only `Info.Subtitles`
(pkg/subtitles/providers/embedded/provider.go:88). It is the exact set the planner computed with that
provider's own `Search` (existing.go:139-166), so planner and extractor cannot disagree.

#### 6.7.2 What the worker reads and checks

1. Schema `subtitle.FetchTask.v1`: ack and log "a v1 task names a SubtitleRequest; the loop
   re-plans" (not dead-lettered, which would resolve to a deleted kind).
2. `Superseded`: ack without working.
3. The SubtitleProfile `task.Profile`, from the cache: gone, or `wantFor(profile, nil, langKey)`
   fails: answer `dropped`; `ProfileGeneration` below the task's: `Retry(30s)` (the cache lags);
   above it: answer `dropped` (the loop requests again); `compileFilters` fails: answer `failed` (the
   profile reconciler also marks it Invalid, §2.13).
4. `datapath.Local(task.Path)` fails: `Discard`, as today (worker.go:293-296).
5. Stat and probe hash: `ProbeHash(path, size, mtime)` differs from `task.ProbeHash`: answer `dropped`,
   "file changed since planned" (the loop re-probes and re-plans; today's 5-minute retry against a
   MediaFile not yet re-probed, worker.go:301-309, goes). A failed stat retries in 5 m; on the final
   delivery, answer `failed`, "gave up after N deliveries" (apply.go:243-254).
6. The query is `task.Query` plus the file size, `MovieHash` (query.go:117-129) and the release
   parsed from `ReleaseTitle` (query.go:136-150).
7. Providers: `Build` then `Order(profile.spec.providers)`, with
   `FileSource.Info.Subtitles = task.Extract`.
8. Threshold `task.MinScore`, on-disk subtitle `task.OnDisk`. The worker's `minScore`, `findItem` and
   `hasSubtitle` (worker.go:370-410) are deleted.

The worker's cache holds only SubtitleProfile and SubtitleProvider. This matters because the caption
HPA scales from zero: each scale-up used to sync a MediaFile informer (about 69 MB on the live
library) plus 15,553 Episodes.

#### 6.7.3 Search, write, answer

- The search is unchanged (search.go:138-302): Bazarr-style pooling, the local tier first, throttled
  providers skipped and paced.
- `writeSidecar` (search.go:445-458) is unchanged except that it takes the mode from
  `task.SidecarMode` (0 means 0664) and re-stats `task.Path` immediately before writing; a video that
  moved or was replaced answers `dropped` rather than leaving a sidecar beside a stem that no longer
  exists.
- `decide` (apply.go:69-88) still maps the outcome to a verdict, but the record carries facts; the
  worker no longer decides `upgradable`.
- When a `downloaded` answer lands and `task.OnDisk` is set, the worker calls
  `removeReplaced(task.OnDisk.Name)` (apply.go:341-352) and records `ReplacedName`.
- A download whose answer was dropped may leave a sidecar written but unrecorded; the loop's next
  listing finds it, and §6.4.3 rebinds it when it carries the canonical name.

#### 6.7.4 Events and metrics

- `SubtitleEvent` moves to `subtitle.SubtitleEvent.v2`: `MediaFile Ref` replaces `RequestRef`, `Seq`
  is added, the subject is `events.SubtitleEventSubject(action, fileUID)` and the envelope ID
  `<fileUID>/subtitle/<langKey>/<seq>/<action>` (§4.10). It is published only after `Answer` lands, as
  today after the apply (apply.go:166-176, 203-205); the history sink records it as an Event on the
  MediaFile.
- `clustarr_subtitle_fetches_total{provider,language,outcome}` is incremented once per answer that
  lands (provider: the chosen SubtitleProvider's name or `none`; language: the langKey's base
  language; outcome: the verdict). `clustarr_provider_quota_remaining{provider}` is set by the
  SubtitleProvider controller from `throttle.State.Quota`, which it already reads
  (subtitleprovider/controller.go:155-161). Both are declared and documented today but never written
  (pkg/obs/metrics/domain.go:226-243).

#### 6.7.5 RBAC

`agent-caption` keeps `subtitleprofiles` get/list/watch (fetch/doc.go:109), `subtitleproviders`
get/list/watch and `secrets` get (providerset/doc.go:71-72), and drops `subtitlerequests` and
`subtitlerequests/status`, plus `mediafiles`, `movies`, `episodes`, `series` and `rootfolders`
(doc.go:107-108, 110-114). It holds no write verb; `k8s.ManagerCaptionarrWorker` has no writer left.

### 6.8 The throttle KV: unchanged

`clustarr-provider-throttle` (24 h, not Durable; topology.go:892); `ProviderKey` and `TokenBucketKey`
(throttle/keys.go:32-40); `Acquire`, `Get`, `RecordError`, `RecordSuccess`, `SetQuota`, `SetAuth`; the
OpenSubtitles JWT through `providerset.TokenCache`. The SubtitleProvider controller remains the only
writer of provider status, projecting from the KV (ruling R2). All of it is keyed by provider, so the
fold touches none of it.

### 6.9 ui: projection and pipeline

- **No new ui action in this branch** (§9, D18): forcing a search is `kubectl annotate`. The ui has no
  subtitle action today (`grep -rn forceSearch ui` finds none), so nothing regresses. A later
  `actions.SearchSubtitles` (a merge patch of `metadata.annotations` only, under `clustarr-ui`, route
  `POST /library/{namespace}/mediafiles/{name}/subtitles/search`) needs `patch` on mediafiles in
  `actions.Grants()`, which RBAC cannot restrict to annotations; that is an open owner decision.
- **The item page** shows a Subtitles table per file from `status.subtitles.items` (langKey, state,
  score out of scoreOutOf, provider, nextSearchAt, lastError) with the phase; its extras still come
  from `status.sidecars` (detail.go:246-255), which now include hand-placed sidecars.
- **The projection** and **pipeline** changes are §7.7: `buildRelatedIndex` stops listing
  SubtitleRequests and buckets MediaFiles by `spec.mediaRef`; `pkg/pipeline` reads
  `status.subtitles` (Blocked → `StageBlocked` with `subtitles: <reason>`; `Satisfied` or `NotWanted`
  → done; `Searching` or `Wanted` → `StageSubtitleSearching`; a downloaded item while others are still
  wanted → `StageSubtitleFound`; `StageSubtitleFetching` and `fetchingSubtitle`, project.go:380-392,
  deleted as unreachable), so `NotWanted` files, most of the library, can complete.

### 6.10 History, DLQ, replay

- `resolveFetchTask` and `resolveSubtitleEvent` (app/catalog/history/target.go:403-417) decode v2 and
  return `mediaFileTarget` (:269). New `resolveFetchTaskV1` and `resolveSubtitleEventV1` return a
  namespace-only target.
- The DLQ projector gains `mediafiles` patch (dlq.go:88-93). Segment payloads already resolve to
  MediaFile (target.go:269-303).
- `ReplayKinds` (replay.go:75-92) loses SubtitleRequest when the CRD goes; MediaFile replay is the
  loop's actuator (§3.9).
- The loop folds `DeadLettered`. A replayed v2 task gets a fresh Msg-Id, and the worker's
  `Superseded` check decides whether it still applies.

### 6.11 Deleted, kept, renamed

- **Deleted:** app/caption/controller/subtitlerequest (all of it); app/caption/itemindex (the loop's
  `remediation.mediafile.item` covers keys); in app/caption/status `IsLive`, `LiveItemKeys`,
  `RequestControllerFields`, `RequestWorkerFields` and `PatchRequest`; `subtitleprofile.selectFiles`,
  `newItemSet`, `unneeded`, `requestCurrent`, `ensureSubtitleRequest` and `mapMediaFileToProfiles`; in
  fetch `profile.go`, `mode.go`, `query.go`'s catalog reads, `record`, `recordAttempts` and
  `upgradeMargin`; `sidecarsFromSubtitleRequest`, `scanSidecars`, `extractSubtitleItemsSignature`,
  `mediaFileForSubtitleRequest` and the SubtitleRequest `spec.mediaFileRef` index (kept in release N
  for adoption, §7.3.2); `events.MsgIDForSubtitle` and `events.MsgIDForForcedSubtitle`;
  `k8s.ManagerCaptionarrWorker` (to `RetiredFieldManagers()` in F5, removed in F9).
- **Kept, same names (R7):** `CLUSTARR_WORK_CAPTIONARR`, `captionarr-fetch-high`,
  `captionarr-fetch-normal`, the subject builders, `clustarr-provider-throttle`,
  `k8s.ManagerCaptionarr`. app/caption/status keeps `ProfileFields` and `PatchProfile`
  (status.go:313-340).

### 6.12 Tests

- **`app/caption/subtitleplan`,** pure and table-driven, porting the subtitlerequest unit tests:
  `TestUntaggedAudioUsesTheItemsOriginalLanguage`, `TestNotWantedNeedsNoItemAndNoProbeTag`,
  `TestANewProbeHashResetsAttemptsAndWithdraws`, `TestARemovedSidecarIsWantedAgain`,
  `TestARenamedSidecarIsRebound`, `TestTheBacklogGateDefersScheduledNotForced`,
  `TestForcedSearchIsHandledOnce`, `TestALateAnswerForAnOlderSeqIsIgnored`,
  `TestARecordAheadOfStatusIsAdoptedNotRedispatched`, `TestUpgradableFollowsTheProfilesMinDeltaPoints`,
  `TestAFailedRecordWriteAfterTheApplyIsRedispatched`, `TestAForcedSearchRacingAnInFlightAnswerKeepsBoth`,
  `TestAProfileEditThatWantsNothingNewWritesNoMediaFile` (envtest: 0 MediaFile writes),
  `TestThePlannerImportsNoIO` (AST guard against `os`, `net`, `client.Client` and the bus).
- **`pkg/subtitlestore`** contract tests on membus and natsbus: `TestAnswerRefusesASupersededSeq`,
  `TestWithdrawBeatsALateAnswer`; `RecordSubKey(uid, "en:forced")` in natsbus/kvkey_contract_test.go.
- **Loop envtests:** `TestSubtitlesSurviveABlockedPass` (a steady-state block, then an unreadable
  directory; the items stay intact); `TestTheLoopIncorporatesAnAnsweredRecord` (a real second writer
  interleaved); the MediaFile budget test fills `status.subtitles` to every cap.
- **Guards:** `TestTheFetchWorkerWritesNoKubernetesObject` (AST);
  `TestAgentCaptionRoleHasNoWriteVerbs`.
- **e2e scenario 13** is rewritten against `status.subtitles` (§7.8).

---

## 7. Migration and removal

Nothing in this section has run. The branch deploys as two releases (§8, §9 D1):

- **Release N** is the split plus the fold: the manager, agents and ui, the remediation loop, and
  adoption of the three kinds' state (`app/catalog/legacyfold`).
- **Release N+1** removes the kinds.

In this branch these are ordered commits: F8 (adoption) and F9 (removal) in §8. The ruling: "the
migration copy and the CRD removal are separate, ordered commits (two releases when deployed)".
Every kind-cluster-plex rollout needs the owner's explicit OK ("Build, don't deploy").

### 7.1 What has to survive (kind-cluster-plex, read-only, 2026-10-06)

Counted from read-only dumps of `clustarr-system` (`scratchpad/migr/{sr,tj,mf}.json`), re-checked with
jq.

| Kind | Objects | State nothing can rebuild | Rebuildable, or already on the MediaFile |
|---|---|---|---|
| SubtitleRequest | 2,445 | 283 items (269 `pending`, 14 `failed`): `attempts{initial,latest,count}` (277 at count 14), `nextSearchAt` and `lastError`. No item has a `provider`, `subtitleID`, `path`, `score` or `downloadedAt`, and `status.sidecars` is empty on all 11,958 MediaFiles. | `existing[]` (998 entries), phase, conditions, probeHash, fileFingerprint, profileGeneration (2 everywhere) |
| TranscodeJob | 626 | None. All 48 Succeeded jobs are incorporated: their MediaFile's `probedAt` equals the job's `finishedAt` on 48/48, which is incorporation by catalogarr's test (`mediafile_controller.go:803-818`, `!FinishedAt.After(probedAt)`). The 31 Planned and 1 Running jobs all have attempts 0. The 2 Blocked Failed jobs sit under superseded hashes. | 541 Skipped (a pure plan over the stored probe); 3 Failed `SourceChanged` |
| AudioGraft | 0 | — | — |

What follows:

- **The copy protects the search backoff,** not what ADR-0016's Consequences say ("would lose the
  provider, score and subtitle ID the upgrade decision reads"): on this cluster those fields are
  empty. Without the copy, 283 searches fall due at once, against a provider already throttled (274
  of the 283 `lastError`s read "every eligible provider is throttled"), onto a work stream of about
  2 MiB in memory with discard-oldest on single-node NATS (`pkg/events/topology.go:249-283`). The ADR
  body is frozen (docs/adr/README.md, Lifecycle), so the correction goes in as an index refinement
  (§7.9).
- **All user intent is at its defaults today.** Every SubtitleRequest spec is
  `{profileRef: english, forceSearch: false}`. Every TranscodeJob has `spec.priority` 50, the profile
  default (`api/transcode/v1alpha1/transcodeprofile_types.go:262`), and no `hardware`, `suspend` or
  `outputPath`. The runbook's quiesce (§7.5) sets `spec.suspend` on the 32 open jobs; release N
  carries that over faithfully and quiesces everything else with zero slots.
- **Exactly one object carries a finalizer:** `squasharr.clustarr.io/task-withdrawal` on the phantom
  Running job `avatar---the-last-airbender-403da1849a-s02e09-debec48de0-c5a05678`, whose pod has been
  gone since 2026-10-02 and whose task and lease went with the 2026-10-05 23:11:30 NATS restart.
- **The migration must not copy squasharr's retirement test.** squasharr's `!probed.After(done)`
  (`transcodeprofile/controller.go:392,445`) keeps all 48 Succeeded jobs alive. Adoption uses
  catalogarr's test.
- **Every TranscodeJob verdict on this cluster is superseded** once standard.Version 2 (main
  `b0bd01ee`) reaches the branch at its next rebase: it changes every profile hash. Then release N's
  transcode copy carries nothing on kind-cluster-plex; only withdrawal and the suspend intent matter.

### 7.2 Shape: the loop adopts, a Migrator does everything else

**The copy runs inside the loop's own `k8s.PatchStatusCAS` render** (`pkg/k8s/patch_cas.go:75`), as
its first planner, `adopt` (`app/remediation/adopt`, release N only). A leader-only runnable,
**`legacyfold.Migrator`**, does everything that is not a MediaFile status write: withdrawal, intent
annotations, the census, deletion after retention, retirement reporting for the results durables,
the `catalogarr-markers` pending set, and metrics. Everything lives in **`app/catalog/legacyfold`**,
a package that exists only in release N.

**Why a runnable may not copy status:** it would be a second MediaFile status writer, which ADR-0016
forbids; a copier that applied only `status.subtitles` under the loop's manager would release every
other field that manager owns (the partial-apply gotcha), and under a manager of its own it would own
the seeded leaves forever; and the loop already reads the file fresh, holds the CAS, and runs the
planners that must see the adopted state as their own previous block, so no planner writes a block it
is about to contradict.

**Two dry runs:** `manager legacy-fold report`, read-only against the live cluster before a deploy;
and `--legacy-fold=hold` inside a running manager.

### 7.3 Release N

#### 7.3.1 The adoption record: `MediaFileStatus.LegacyFold`

In `api/catalog/v1alpha1/mediafile_legacyfold_types.go`, which N+1 deletes:

```go
// LegacyFoldStatus is release N's record of the SubtitleRequest, TranscodeJob
// and AudioGraft objects whose state the remediation loop adopted onto this
// file (ADR-0016). Release N+1 removes it.
type LegacyFoldStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=uid
	// +kubebuilder:validation:MaxItems=16
	Adopted []LegacyAdoption `json:"adopted,omitempty"`
	// Held says why adoption waits on this file; empty when nothing waits.
	// +optional
	// +kubebuilder:validation:Enum=Mode;Preparation;TooMany
	Held LegacyFoldHold `json:"held,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Message string `json:"message,omitempty"`
}

type LegacyAdoption struct {
	// +kubebuilder:validation:Enum=SubtitleRequest;TranscodeJob;AudioGraft
	Kind LegacyKind `json:"kind"`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=36
	UID types.UID `json:"uid"`
	// +kubebuilder:validation:Enum=Copied;Dismissed
	Outcome LegacyOutcome `json:"outcome"`
	At metav1.Time `json:"at"`
}
```

It is `status.legacyFold`, owned by the `adopt` planner. No condition (the condition slots are the
planners'); no `+kubebuilder:default`; every list and string capped. Live, at most 4 legacy objects
exist per file (1 SubtitleRequest plus at most 3 TranscodeJobs); a file with more than 15 is
`Held: TooMany` and reported, never truncated. The loop carries each entry forward unchanged, so `at`
is stamped once and a re-render is byte-identical.

#### 7.3.2 Lookup and wake-up

`legacyfold.RegisterIndexes` takes over the three indexes `mediafile.RegisterIndexes` registers today
(`mediafile_controller.go:754-765`), under the same names: TranscodeJob `spec.mediaFileRef`,
SubtitleRequest `spec.mediaFileRef`, AudioGraft `status.mediaFileRef`. Release N deletes their old
registration sites, including transcodejob's second copy (`transcodejob/controller.go:912`).

`legacyfold.Watches(b)` is S23 (§3.3) in release N: the three kinds on **create events only**,
mapping SubtitleRequest and TranscodeJob through `spec.mediaFileRef`, and AudioGraft through
`status.mediaFileRef`, else its item's `status.fileRef`. Nothing writes these objects in N; the
create watch exists for the informer's initial list, for objects an N-1 rollback created (§7.6), and
for the e2e scenario. An AudioGraft is looked up by `k8s.AudioGraftName(item)` (`pkg/k8s/names.go:58`)
when the item's `status.fileRef` names the file; the item key also reads it (below).

#### 7.3.3 Adoption rules (`legacyfold.Plan`, pure)

```go
func Plan(in Input) Output
```

`Input` holds the fresh MediaFile; its SubtitleRequest, TranscodeJobs and AudioGraft; the
TranscodeProfiles; the mode; `Prepared func(types.UID) bool`; and `Now`. `Output` holds the seeds for
the `subtitles`, `transcode` and `graft` planners (each nil when there is nothing to seed), per-planner
hold flags, the next `LegacyFoldStatus`, and one outcome per object, for metrics. Seeds are the
planners' *previous block*, not their output: each planner then runs as usual (probe gates, wanting,
due times, admission). The same `Plan` runs in the loop, in the Migrator's census and in the report
command, so all three agree.

**SubtitleRequest → `status.subtitles`:**

| From | To | Rule |
|---|---|---|
| `status.items[]`: `langKey`, `state`, `score`, `scoreOutOf`, `provider`, `subtitleID`, `path`, `lastError`, `downloadedAt`, `attempts.{initial,latest,count}`, `nextSearchAt` | `items[]` leaf for leaf; `path` → `name` | One per langKey, cap 20 on both sides. `path` is taken as a base name; one over 255 characters is dropped and the item reads `pending`. `state` `""` and `searching` become `pending`. `lastError` is clamped. `dispatch` starts nil. |
| `status.probeHash` | `probeHash` | Copied, so the planner resets `attempts` when it differs from `status.probeHash`, as `plan` does today (`subtitlerequest/reconciler.go:411-415`). |
| `spec.profileRef` | `profile` | Copied. (`status.profileGeneration` is not copied: the block has no such field, §2.4.) |
| `spec.forceSearch: true`, whatever `status.observedGeneration` says | annotation `subtitle.clustarr.io/search: fold-<generation>` | Written by the Migrator before adoption (§7.3.5). Today a force can be pending beside `observedGeneration == generation`: the reconciler records the generation before it knows the publish landed, and an `ErrQueueFull` requeue returns before `resetForceSearch` (subtitlerequest/reconciler.go:537,566,570-582,704-707). One extra search is the cost of carrying a handled one. |
| `status.existing`, `fileFingerprint`, `phase`, `conditions`, `observedGeneration` | — | Not copied; the planner recomputes them. |
| `spec.languages`, `spec.minScoreOverride` | — | Dropped (§9, D16); counted as `intent_dropped` when set. None is set live. |

Outcome: `copied` when any item exists, else `dismissed_empty`. Live: 283 copied, 2,162 dismissed.
Copying `attempts` keeps the 277 items at count 14 on their weekly adaptive cadence (`gateOpen`)
instead of firing 283 searches at a throttled provider.

**TranscodeJobs → `status.transcode`.** The rules run in order over every job of the file; the first
match decides:

1. **Non-terminal** (no phase, Pending, Planned, Queued or Running) → `dismissed_open`. This requires
   `Prepared(uid)` (the Migrator has withdrawn it); until then the transcode planner and the rename
   actuator are held for the file (§7.3.6). The planner plans afresh.
2. **Succeeded with `finishedAt > status.probedAt`**, strictly (equal means incorporated, catalogarr's
   test at `mediafile_controller.go:810`) → `copied_swap`, a pending swap. Only the newest such job
   counts; older ones are `dismissed_superseded`. The seed is `phase: Swapping`,
   `result.outputPath` and `result.outputSizeBytes` from `status.result`, `finishedAt`, and
   `profileTag` from `status.result.mediaInfo.transcodeProfile` (the tag the worker wrote into the
   output), with `profile` and `profileHash` split from it. Never from the profile's current hash,
   today's bug (`mediafile_controller.go:840-846`). The planners then stat the target, re-probe and
   take over the spec.
3. **Succeeded, incorporated** → `dismissed_incorporated`; the legacy block's `profileTag` already
   says it (live: 48).
4. **Failed with reason `SourceChanged`** → `dismissed_source_changed`.
5. **Skipped or Failed under the hash the loop computes now** (`jobspec.ProfileHash` of the named
   profile's spec, matched by `name == transcodeJobName(mf, hash)`,
   `transcodeprofile/profile.go:98`, of which the loop carries a copy) → `copied_verdict`: `phase`
   (`Skipped` or `Failed`), the clamped reason, `blocked` (the job's Blocked condition was True),
   `attempts`, `fallbackReason`, `nextAttemptAt`, `profileHash` and `probeHash`.
6. **Anything else** → `dismissed_superseded`.

Never carried: `plan`, `progress`, `result.mediaInfo`, `stderrTail`, `workerPod`, `jobRef`, and the
job's `status.graft` (a joined graft is the AudioGraft's to carry).

Live, before standard.Version 2: `copied_verdict` 19, `dismissed_source_changed` 3,
`dismissed_superseded` 524, `dismissed_open` 32, `dismissed_incorporated` 48. With Version 2 the 19
become `dismissed_superseded`.

**AudioGraft → the item and `status.graft`** (§2.6):

- **To the item** (the item key, in release N, reading the AudioGraft named
  `k8s.AudioGraftName(item)` when `status.audio.donor` is absent): `status.audio.donor` from
  `status.donorAudioPath` (else `spec.donorPath`), `spec.release` and `metadata.creationTimestamp`;
  `status.audio.rejectedReleases` from `status.rejectedReleases` (≤16).
- **To the target file's `status.graft`** (the target is `status.mediaFileRef`, else the item's
  `status.fileRef`): `phase`, `reason`, `message`, `targetProbeHash` → `probeHash`,
  `spec.languages`, `rateName`, `rateMicros`, `rateMarginMilli`, `coveragePercent`, `segments` (≤16),
  `residualMillis`, `within80Percent`, `graftTag` → `tag`, `startedAt`, `completedAt`,
  `spec.release` → `donorRelease`. A `Running` phase becomes `Waiting` (the pod's result is never
  read; the §7.5 gate requires that no graft Job exists). Not copied: `jobName`, `conditions`,
  `observedGeneration`, `spec.anchor`, `spec.default`, `spec.hi` (derived at dispatch now).
- **No target file:** the AudioGraft stays `held_no_target` and the Migrator never deletes it; the
  N+1 gate requires zero such objects. Live count: 0. A multi-item target is dismissed
  (`dismissed_multi_item`), since such files are never grafted.

#### 7.3.4 Idempotency and precedence

- **Adopt only an object whose UID is not yet in `status.legacyFold.adopted`.** Block presence is the
  wrong key: a block is legitimately absent when a file leaves the transcode window or its planner
  clears it, so a stale legacy object would be adopted twice; and a block can exist beside a newer
  legacy object after a rollback and roll-forward (§7.6).
- **When the target block already exists,** only a pending swap is carried into it, and only when the
  block holds no swap of its own and its last incorporated result finished earlier: a swap is a fact
  on disk, and dropping it strands `spec.path` after a container change, which the MP4 standard makes
  universal. Anything else is recorded `Dismissed` with outcome `dismissed_block_exists`.
- **Metrics count landed writes only.** Adoption is in the CAS render, so a Conflict redoes it from a
  fresh read; outcome metrics are counted after `applied == true`.

#### 7.3.5 Preparation (the Migrator, before adoption)

Once the cache has synced, the Migrator lists the three kinds from the cache and, for every object
`legacyfold.NeedsPreparation` names, does the following idempotently.

**Withdrawal** of every non-terminal TranscodeJob, Planned with attempts 0 included (the R16
lost-Queued-write case publishes attempt+1 while status still reads Planned): put a cancel
`legacyfold.leaseV1{Job, Attempt: math.MaxInt32, State: cancelled}` (today's `task.Lease` shape,
JSON `job`/`attempt`/`state`, schema `transcode.Lease.v1`, app/squash/task/task.go:163-173, kept in
legacyfold with a golden test because §4.12 makes `task.Lease` v2; F9.1 deletes it; a previous-release
worker honours only `cur.Attempt >= t.Attempt`, worker/lease.go:79) at `events.TranscodeLeaseKey(tjUID)` in
`clustarr-transcode-leases`, then purge `events.WorkTranscodeTaskSubjectAnyPool(tjUID)` from
`CLUSTARR_WORK_SQUASHARR`. `withdraw.go`'s `cancelAndPurge` and `cancelAttemptsForDeletion` move into
`legacyfold/withdraw.go` (`transcodejob/withdraw.go:110-160`). Live: 32 withdrawals, the phantom job
included.

**Intent** is a JSON merge patch of `metadata.annotations` on the MediaFile under field owner
`clustarr-legacy-fold` (new `k8s.ManagerLegacyFold`, added to the list at
`pkg/k8s/fieldmanager.go:315-333`). The loop never writes these annotations, as ADR-0016 requires; the
Migrator acts as the user's proxy, once.

| Legacy intent | Annotation |
|---|---|
| TranscodeJob `spec.suspend: true` on a non-terminal job | `transcode.clustarr.io/suspend: "true"` |
| `spec.priority` | not carried and not counted as `intent_dropped` (every live job holds 50, the profile default): it is the profile's priority copied at creation (transcodeprofile/controller.go:552-558) and never re-applied (:231-233), not a user's choice, so an annotation would pin a stale copy over every later profile edit |
| `spec.hardware` set | `transcode.clustarr.io/hardware: "<class>"` |
| SubtitleRequest `spec.forceSearch: true` | `subtitle.clustarr.io/search: fold-<generation>` |
| TranscodeJob `spec.outputPath` | dropped (§9, D16), `intent_dropped` |

Live, after the runbook's suspend: 32 suspend annotations and nothing else. Prepared UIDs are held in
an in-memory set that `Prepared` reads; the loop and the Migrator share the leader process; after a
restart the pass repeats, both steps idempotent. `NeedsPreparation` excludes every object whose UID
is in its MediaFile's `status.legacyFold.adopted`, so intent is carried exactly once and a restart
inside `retain` never reapplies `suspend` over a user's later edit; and the annotation merge patch
carries `metadata.resourceVersion` and writes only keys absent on that read.

#### 7.3.6 Hold, and an unadopted file

- **`--legacy-fold=hold`** writes nothing about legacy objects. The `subtitles`, `transcode` and
  `graft` planners return their last block unchanged and publish nothing, for any file with an
  unadopted object of their kind; probe, naming and markers proceed. `status.legacyFold.held` reads
  `Mode`.
- **In `apply` mode,** a file waits only while its legacy objects are not yet `Prepared`; it reads
  `held: Preparation` and requeues after 10 s.
- **The rename actuator** treats an unadopted non-terminal legacy TranscodeJob as a transcode in
  flight, as `transcodeInFlight` does today (`mediafile_controller.go:825`).

#### 7.3.7 Census, deletion and retention (the Migrator, every 60 s)

**Census.** `Plan` over the cache sets `clustarr_legacy_fold_objects{kind,state}`, state one of
`unadopted`, `held_mode`, `held_preparation`, `held_no_target`, `adopted`, `deleting`.

**Deletion,** only when the object's UID is in its MediaFile's `status.legacyFold.adopted` (read from
the cache after the loop's write landed) and `at + --legacy-fold-retain ≤ now`:

1. A TranscodeJob carrying `task-withdrawal` is withdrawn again, idempotently, and the finalizer is
   removed (`k8s.RemoveFinalizer`, an Update, `pkg/k8s/finalizers.go:121`).
2. The object is deleted with `Preconditions{UID}` and background propagation. Owner references need
   no handling: deleting a dependent never touches its owner.

Deletes are paced at 20 per second by a client-side limiter; 3,071 live objects take about 2.5
minutes. A legacy TranscodeJob the garbage collector is deleting (its MediaFile went) is withdrawn and
released at once, adopted or not, since in N no controller would otherwise remove its finalizer.
When every kind reads zero and both retired durables are gone (§7.3.9), the Migrator logs
`legacy fold complete` and returns nil, ending as a one-shot; after a restart it finishes at once.

#### 7.3.8 Retiring `catalogarr-markers` on every MediaFile

Every live MediaFile carries a `catalogarr-markers` status entry owning `f:status.f:markers`; once the
loop writes markers as `catalogarr`, the two managers co-own them, and a leaf the loop later drops
(`message`, `notFoundSince`, `analysis`) would survive under the retired manager. `notFoundSince`
drives `markers.Due`'s 7/30/90-day ladder, so a stale leaf is a real fault.

1. **The pending set.** At start the Migrator makes one paged APIReader List of
   `metav1.PartialObjectMetadataList` for MediaFiles (metadata only, managedFields included; the
   cache strips managedFields) and hands the loop the UIDs whose managedFields still hold a
   `catalogarr-markers` entry, as `legacyfold.MarkersPending`.
2. **The claim.** For a file in that set, the loop's next apply is never skipped as a no-op and sends
   the full markers block, so `catalogarr` co-owns every markers leaf.
3. **The release.** The loop then applies an empty status configuration,
   `catalogac.MediaFile(name, ns).WithResourceVersion(rv)`, once under `k8s.ManagerCatalogarrMarkers`.
   The leaves `catalogarr` co-owns stay; the entry disappears. A Conflict is retried at the next pass;
   success removes the UID from the set.

Cost: two writes per MediaFile, about 24,000 once (a restart finds the set empty), roughly two
informer starts for every MediaFile watcher. `TestOnlyLegacyFoldAppliesAsCatalogarrMarkers`
(`test/guards`) holds this to one call site; N+1 deletes it. The report counts MediaFiles still
carrying the entry; the N+1 gate requires 0. This is a stated, release-N-only exception to "one
manager name on MediaFile status", inside the loop's own process. Both writes go through the
status-write limiter (§3.7).

**This step makes a plain rollback to the previous release lossy, so the rollback restores
the entry first.** After it, `catalogarr` alone owns `status.markers`. The previous release's
MediaFile controller applies status under `catalogarr` on every reconcile and never declares
markers (its `knownStatus` has no markers, mediafile_controller.go:602-612; `statusAC`, :633-694,
never emits them), so its first apply on each file would release `f:markers` and delete
`status.markers` on all 11,958 files: TheIntroDB results, the `notFoundSince` ladder and the
analysis bookkeeping. The previous `markers.Due` would then read every file as never fetched (a
library-wide TheIntroDB refetch on its counted allowance), and cluster-plex's `SeedKey` would
change on every file. **`manager legacy-fold restore-markers`** (release N only, §7.3.11) undoes
it: run with the manager scaled to 0 (so the Migrator cannot release again), it lists
MediaFiles through an uncached client and applies each file's stored `status.markers`, whole and
unchanged, under `catalogarr-markers` with a resourceVersion precondition, paced by the same
limiter (about 8 minutes at 25/s). `catalogarr-markers` then co-owns every leaf again, the
previous release's `catalogarr` apply releases only its own ownership, and the markers stay. The
values are unchanged, so cluster-plex's `SeedKey` does not move. §7.6 makes it a rollback step.
Keeping co-ownership through release N instead was rejected: a leaf the loop drops would
survive under `catalogarr-markers` for the whole release, the stale-`notFoundSince` fault above.

#### 7.3.9 NATS state the old kinds leave behind

- **Transcode tasks keyed by TranscodeJob UID:** withdrawn at preparation; the task sweep (§5.6)
  purges any task whose UID token names no MediaFile.
- **`squasharr-transcode-results` and `catalogarr-segments-result`:** in N they leave the default
  topology and enter `Topology.Retired` (§8, F1.2), so `Ensure` deletes each durable and purges its
  filter (`FilterTranscodeResults`, `FilterCatalogSegmentsResult`). `Ensure` first reads
  `ConsumerState(...)` and returns the lag in its report; a non-zero lag is a result nobody will
  apply (no shim, §9 D25), which the Migrator counts as
  `clustarr_legacy_fold_preparations_total{action="retire_results",result="undrained"}` and logs at
  Warn per subject. The pre-deploy drain gate (§7.5) prevents it.
- **v1 `FetchTask`s naming a SubtitleRequest** are acked as stale by the caption agent (§6.7.2); the
  copied `nextSearchAt` dispatches them again. `CLUSTARR_WORK_CAPTIONARR` held 0 messages at
  inventory.
- **Leftover leases and progress keys** expire: cancel markers with `TranscodeLeaseTTL`, 90 s
  (`pkg/events/subjects.go:563`); `transcode.<tjUID>` progress keys with `clustarr-progress`'s
  10-minute TTL.
- **v1 `catalog.MarkersTask`s** (`schema/catalog.go:276-282`, `{MediaFile}` only) wait in the
  Durable, file-backed `CLUSTARR_WORK_SEGMENTARR` (consumer `catalogarr-markers`), including
  tasks rescheduled to TheIntroDB's allowance reset. Today's handler dead-letters a task it
  cannot decode (`events.Discard`, markers/handler.go:103-105), and nothing resolves a
  MarkersTask dead letter (history/target.go:149-174). The v2 handler therefore acks a
  `catalog.MarkersTask.v1` as stale, as §6.7.2 does for v1 FetchTasks; the loop requests again
  when `markers.Due`. The runbook's drain step (§7.5) records `catalogarr-markers`' pending count, so the acks are expected.

#### 7.3.10 Flags, values, RBAC (release N only)

| Flag | Default | Meaning |
|---|---|---|
| `--legacy-fold` | `apply` | `apply` or `hold` (§7.3.6) |
| `--legacy-fold-retain` | `72h` | how long an adoption must stand before the Migrator deletes the object; `0s` deletes at the next census |

Chart `manager.legacyFold.mode` (enum `apply|hold` in `values.schema.json`) and
`manager.legacyFold.retain`; kustomize `config/manager` renders
`--legacy-fold=apply --legacy-fold-retain=72h` explicitly, held to the chart by
`TestChartAndKustomizeAgreePerComponent`; `config/e2e` patches `--legacy-fold-retain=0s`. W6 writes
the installers after F8 and keeps these.

RBAC markers in `app/catalog/legacyfold/rbac.go`, the only legacy-kind grants left in N:

```
subtitle.clustarr.io   subtitlerequests            get;list;watch;delete
transcode.clustarr.io  transcodejobs               get;list;watch;update;delete
transcode.clustarr.io  transcodejobs/finalizers    update
transcode.clustarr.io  audiografts                 get;list;watch;delete
catalog.clustarr.io    mediafiles                  patch      (annotations; already granted)
```

Every other grant on the three kinds goes in N with the code that held it:
`app/squash/controller/audiograft/controller.go:67-73`, `transcodejob/doc.go:165-167`,
`dispatch.go:448`, `app/squash/status/doc.go:42-43`, `transcodeprofile/controller.go:87-88`,
`fileimport/donor.go:45`, `rescan/worker.go:157`, `mediafile/graft.go:33`,
`mediafile_controller.go:69,71`, `episode/reconciler.go:89`, `movie/reconciler.go:81`,
`history/dlq.go:92-93`, `history/replay.go:61-62`, `search/donor.go:35`,
`subtitlerequest/doc.go:159-160`, `subtitleprofile/controller.go:91`, `fetch/doc.go:107-108`. After
RBAC regeneration only the manager role names the three resources;
`TestOnlyLegacyFoldNamesTheFoldedKinds` enforces it.

#### 7.3.11 Metrics, logs and the report command

In `pkg/obs/metrics/legacyfold.go`, through `newGaugeVec`/`newCounterVec`, so
`TestNoMetricIsLabelledByAnUnboundedDimension` sees them; N+1 deletes the file.

| Metric | Labels |
|---|---|
| `clustarr_legacy_fold_objects` (gauge) | `kind`, `state` |
| `clustarr_legacy_fold_adoptions_total` | `kind`, `outcome` ∈ {`copied`, `dismissed_empty`, `copied_swap`, `copied_verdict`, `dismissed_open`, `dismissed_incorporated`, `dismissed_source_changed`, `dismissed_superseded`, `dismissed_block_exists`, `dismissed_multi_item`, `copied_graft`} |
| `clustarr_legacy_fold_deletions_total` | `kind`, `result` ∈ {`deleted`, `not_found`, `error`} |
| `clustarr_legacy_fold_preparations_total` | `action` ∈ {`withdraw`, `annotate`, `retire_results`, `release_markers`}, `result` ∈ {`ok`, `error`, `undrained`} |

Each census logs one Info line when its totals change; individual objects at Debug.

**`manager legacy-fold report [--namespace clustarr-system] [-o table|json]`** (release N only): the
operator's kubeconfig and an uncached client; no manager, cache or bus; writes nothing. It lists
MediaFiles (with managedFields), the three kinds, TranscodeProfiles, Movies and Episodes, runs `Plan`
in `apply` mode with every object treated as prepared, and prints per-kind totals by outcome, the
intent to be annotated, `intent_dropped`, non-terminal TranscodeJobs, TranscodeJobs carrying
`task-withdrawal`, MediaFiles carrying a `catalogarr-markers` entry, and **MediaFiles whose first
loop render would change `status.markers` or `status.mediaInfo`** (the markers and probe
planners' render rules of §2.11.1 rule 5 and §4.12 run over the stored status, with the
`clustarr-segments` record read through `--nats-url` when given, else counted unknown), which
must read 0. `-o json` adds one record per object (`kind`, `name`, `uid`, `mediaFile`, `outcome`,
`intent`). It exits non-zero only on a read error.

**`manager legacy-fold restore-markers [--namespace clustarr-system] [--writes-per-second 25]`**
(release N only, for the rollback, §7.3.8, §7.6): the operator's kubeconfig and an uncached
client. It refuses to run while `manager.clustarr.io` is held. For each MediaFile with
`status.markers` and no `catalogarr-markers` entry in its managedFields, it applies
`status.markers`, exactly as stored, under `catalogarr-markers` with the read's resourceVersion
(a Conflict is re-read and retried). It prints the count restored and exits non-zero on any
file left without the entry.

#### 7.3.12 Tests in release N

- **`app/catalog/legacyfold/plan_test.go`** (table-driven), fixtures
  `test/data/legacyfold/{mediafiles,subtitlerequests,transcodejobs}.json` cut from the 2026-10-06
  dumps (inputs from the real producer, not shaped like the answer), including an equal-timestamp
  Succeeded job, the phantom Running job, both Blocked jobs, the three SourceChanged jobs and items at
  count 14. It asserts §7.3.3's live totals. Falsify: swap in squasharr's `After` test and watch
  `dismissed_incorporated` become `copied_swap`.
- **`app/catalog/legacyfold/migrator_envtest_test.go`:** the cancel marker and the purge exist before
  the finalizer Update; deletion needs both the record and the retention, and an object recreated
  under the same name survives the UID precondition; `hold` withdraws, annotates and deletes nothing;
  a garbage-collected TranscodeJob is released; a retired durable is reported undrained at a
  non-zero lag, on membus and natsbus.
- **Loop envtests:** adoption into a MediaFile already in steady state keeps every prior leaf (a
  release test against an object that has status); a real second writer (the Migrator's annotation
  patch) interleaved between read and apply produces a Conflict and a redo; re-adoption — after
  adoption, the subtitles planner clears its block while the legacy object still exists, and nothing
  is adopted again (falsify by keying on block presence); managedFields — after §7.3.8, `catalogarr`
  alone owns `f:status.f:markers`, the co-owner dropped deliberately, and a markers leaf the loop
  omits disappears.
- **The transition, both ways** (`app/catalog/legacyfold/transition_envtest_test.go`; these are
  the only tests that start from the previous release rather than from N):
  - `TestThePreviousReleasesStatusApplyIsAccepted`: goldens of the previous release's status
    apply bodies (`test/data/legacyfold/previous-applies/*.json`, rendered by main's
    `knownStatus.statusAC` for a transcoded file, a file with two sidecars and a phase-less
    legacy block). Install main's MediaFile CRD, seed objects through those bodies under
    `catalogarr`, install N's CRD, run one loop pass, then replay the bodies under `catalogarr`
    against N's CRD **and** against the previous CRD re-applied; every apply is accepted, and
    no leaf the previous release owns is lost.
  - `TestRestoreMarkersMakesARollbackLossless`: after §7.3.8's release, run `restore-markers`,
    replay the previous release's apply (no markers), and assert `status.markers` is unchanged
    and owned by `catalogarr-markers`; without the restore, assert it is gone (the falsifier).
  - `TestTheFirstLoopPassKeepsEverySeedKey`: `test/data/legacyfold/mediafiles.json` plus v1
    segment records cut from the live `clustarr-segments` bucket; cluster-plex's `SeedKey`
    (re-implemented over the same five fields, cluster-plex `pkg/clustarrwatch/fields.go:156-171`)
    is identical for every object before and after one loop pass.
- **Upgrade and rollback on a throwaway kind cluster** (`test/e2e/upgrade_test.go`, tag
  `e2eupgrade`, written in F8.9 and, like every scenario, run by Phase H, never against
  kind-cluster-plex): deploy main's commit, populate it from the e2e fixtures, follow §7.5 into
  hold and then apply, then follow §7.6 back, and diff MediaFile status and managedFields at
  each step.
- **`test/guards/legacyfold_test.go` `TestOnlyLegacyFoldNamesTheFoldedKinds`:** an AST walk over
  non-test Go outside `api/`; the selectors `SubtitleRequest`, `SubtitleRequestList`, `TranscodeJob`,
  `TranscodeJobList`, `AudioGraft`, `AudioGraftList` and `GraftResult` appear only in
  `app/catalog/legacyfold` (and `app/remediation/adopt`); every `config/rbac/*_role.yaml` other than
  the manager's names none of the three resources. This guard makes N+1 a deletion of one package
  plus the types.

### 7.4 Release N+1: removal

Code first, then the CRDs, and the CRDs only by hand: Helm never updates or deletes `crds/`
(`charts/clustarr/crds/README.md:21-40`), `make install` is `kubectl apply --server-side` with no prune
(`Makefile:254-255`), and the live CRDs are owned by kubectl.

- **Packages and hooks:** `app/catalog/legacyfold`; `app/remediation/adopt` and the loop's `adopt`
  hook, `legacyfold.Watches`/`RegisterIndexes`; the §7.3.8 step and its guard; `manager legacy-fold
  report`; the two flags; `pkg/obs/metrics/legacyfold.go`; `k8s.ManagerLegacyFold`;
  `test/data/legacyfold/`; `test/e2e/legacyfold_test.go`; `test/guards/legacyfold_test.go`.
- **Types:** `api/subtitle/v1alpha1/subtitlerequest_types.go` (registration at `:322`);
  `api/transcode/v1alpha1/transcodejob_types.go` (`:393`; `GraftResult`, `Plan`, `Result` and
  `Progress` go with it unless a block reuses them — `task.Answer` reuses `Result`, which moves to
  `app/squash/task`); `api/transcode/v1alpha1/audiograft_types.go` (`:251`);
  `api/catalog/v1alpha1/mediafile_legacyfold_types.go` and the `LegacyFold` field. The
  `subtitle.clustarr.io` and `transcode.clustarr.io` groups stay.
- **Generated code:** `make generate` regenerates `zz_generated.deepcopy.go`; `git rm` the
  applyconfigurations the generator no longer writes (it does not delete stale files):
  `api/applyconfiguration/subtitle/subtitle/v1alpha1/subtitlerequest{,spec,status}.go`,
  `api/applyconfiguration/transcode/transcode/v1alpha1/{audiograft,audiograftsegment,audiograftspec,audiograftstatus,graftresult,transcodejob,transcodejobspec,transcodejobstatus}.go`,
  and the item, plan, result and progress files only those types used (`git status` after a
  regenerate into a clean tree lists them).
- **CRDs:** `git rm config/crd/bases/{subtitle.clustarr.io_subtitlerequests,transcode.clustarr.io_audiografts,transcode.clustarr.io_transcodejobs}.yaml`,
  drop `config/crd/kustomization.yaml:50,53,54`; `make manifests` drops `status.legacyFold`;
  `config/crd/bases/crdbases_test.go:54` goes from 31 to 28 and `test/e2e/main_test.go:84-85`
  `expectedCRDCount` to 28; `charts/clustarr/crds/README.md` says 28 (it says 28 today, stale against
  31) and drops TranscodeJob and SubtitleRequest from its uninstall sentence.
- **pkg:** `pkg/k8s/names.go:58` `AudioGraftName`; the two kinds in `pkg/k8s/deadletter.go:58-59`;
  `pkg/k8s/scheme_test.go:46-47`; `pkg/crdcheck/mirrors_test.go:201-205`;
  `pkg/crdcheck/donor_cel_test.go:94-96`; the TranscodeJob- and SubtitleRequest-keyed subjects,
  Msg-Ids, lease keys and v1 task decoders in `pkg/events/subjects.go` and `pkg/events/schema`
  (`TranscodeJobSubject` `:306`, the job-keyed `WorkTranscodeTaskSubjectAnyPool`), once the one-release
  stale-ack window has passed; `k8s.RetiredFieldManagers()`'s two names.
- **Installers:** RBAC regenerated (`config/rbac/*`, `charts/clustarr/templates/rbac.yaml`,
  byte-for-byte guard); `manager.legacyFold` leaves `values.yaml` and `values.schema.json` rejects it
  (`additionalProperties: false`), so an upgrade comes from fresh values, never `--reuse-values`
  (`--set manager.legacyFold=null` does not remove the key; CLAUDE.md, the `image.transcoderCuda`
  precedent); the `config/manager` flags and the `config/e2e` patch go.
- **Pruning on read:** after N+1's MediaFile CRD is applied, the apiserver drops a stored
  `status.legacyFold` on read from etcd (`k8s.io/apiextensions-apiserver@v0.37.0/pkg/apiserver/customresource_handler.go:1274-1290, 1428-1434`);
  `catalogarr`'s next apply then drops `f:legacyFold` from its managedFields.

### 7.5 Runbooks (kind-cluster-plex; not executed; each step needs the owner's OK)

**Release N** (the split and the fold together). This is the whole runbook for release N. It
replaces split §11.3's runbook and its rollback (F8.11 points split §11.3 here), and carries
every split step in order, so a plain `helm upgrade` (which split §11.3 explains would run old and
new writers together and delete the helm-owned `clustarr-index` PVC) never happens. In the fold
the overlap would be worse: the previous `catalogarr` and N's loop would both apply MediaFile
status as `catalogarr` with different field sets, each releasing the other's fields.

0. **Freeze and save** (split step 0). Ask the other sessions to stop helm upgrades. Save
   `helm get values -o yaml` (other sessions upgrade this release) and `helm get manifest`, and
   record the current revision `P`, the rollback target. Save the previous commit's CRDs beside
   them: `git show <P's commit>:config/crd/bases/<file>` for every `catalog.clustarr.io`,
   `subtitle.clustarr.io` and `transcode.clustarr.io` CRD (§7.6 re-applies them).
1. **Pre-flight report.** `bin/manager legacy-fold report --namespace clustarr-system -o json >
   legacyfold-before.json` from N's commit. Compare with §7.1: SubtitleRequests `copied` 283 and
   `dismissed_empty` 2,162; TranscodeJobs per §7.3.3; AudioGraft 0; MediaFiles carrying
   `catalogarr-markers` 11,958; MediaFiles whose first render would change markers or mediaInfo 0.
   Any `intent_dropped`, any `copied_swap` the inventory did not predict, or a non-zero render
   count stops the runbook for the owner.
2. **Images** (split step 1, first half). Build all images and `kind load` them now, while the old
   system is healthy (a load stalls etcd and can cost every lease); wait until lease transitions
   stop moving.
3. **Protect data** (split step 2). `kubectl -n clustarr-system annotate pvc clustarr-index
   helm.sh/resource-policy=keep`; confirm the art key's keep annotation; record the facade Secret
   name.
4. **Quiesce transcode under the running release** (split step 3; the owner approves the pause).
   Set `spec.suspend=true` on every non-terminal TranscodeJob; the controller withdraws Queued and
   Running jobs (`transcodejob/controller.go:388-401`), the phantom included. Wait until the pool
   Job reads suspend=true with Active=0, then delete it.
   `kubectl -n clustarr-system get jobs -l app.kubernetes.io/managed-by=squasharr` shows no graft
   or reduce Job.
5. **Quiesce downloads and imports** (split step 4).
6. **Drain.** `nats consumer info CLUSTARR_WORK_SQUASHARR squasharr-transcode-results` shows 0
   pending and 0 ack-pending; likewise `catalogarr-segments-result` on `CLUSTARR_WORK_SEGMENTARR`;
   `nats stream info CLUSTARR_WORK_SQUASHARR` shows 0 messages; no live transcode lease remains.
   Record `catalogarr-markers`' pending count on `CLUSTARR_WORK_SEGMENTARR`: release N acks those
   v1 tasks as stale (§7.3.9). Do not restart NATS (single-node work streams are memory-backed).
7. **Stop every old writer** (split step 5). Scale the ten old Deployments to 0 and wait until
   their pods are gone and all five leases show an empty `holderIdentity` or a `renewTime` more
   than 60 s old. The engines and `clustarr-ui` keep running.
8. **CRDs.** `kubectl apply --server-side --force-conflicts -f config/crd/bases/` from N's commit
   (the MediaFile CRD gains the blocks and `status.legacyFold`; without the new schema the
   apiserver prunes them).
9. **Deploy in hold with zero slots** (split step 6). `helm upgrade clustarr <chart> -n
   clustarr-system -f <fresh values> --timeout 15m --wait`, never `--reuse-values`, with the
   split's fresh values plus `manager.legacyFold.mode=hold` and the manager's slots at
   `cpu=0,intel=0,nvidia=0`, so admission dispatches nothing: release N's backlog includes every
   file whose verdict a new standard hash superseded, and the 32 suspend annotations cover only
   the formerly open jobs. **Watch etcd** through the first leader start's write wave (§3.17):
   on the control-plane node, `curl -s http://127.0.0.1:2381/metrics | grep
   etcd_disk_wal_fsync_duration_seconds`; if fsyncs pass 500 ms, lower
   `manager.remediation.bulkWritesPerSecond` with a values change.
10. **Verify the split and hold.** Split step 7's checks. Then
    `clustarr_legacy_fold_objects{state="held_mode"}` matches step 1; no legacy object is deleted;
    no held file shows a subtitles or transcode phase; `catalogarr-markers` entries fall to 0
    (§7.3.8 runs in both modes; check with the report); both retired durables are gone.
11. **Apply.** `helm upgrade` with `manager.legacyFold.mode=apply` (the manager restarts; the
    lease hands over within 60 s).
12. **Verify apply.** `state="unadopted"` and `state="held_preparation"` reach 0 within one full
    pass; `adopted` reads 3,071; a copied item matches
    (`kubectl get subtitlerequest <n> -o jsonpath='{.status.items}'` against
    `kubectl get mediafile <n> -o jsonpath='{.status.subtitles.items}'`, names shared on
    2,445/2,445, `path` now `name`); subtitle backoff is kept (no burst on
    `CLUSTARR_WORK_CAPTIONARR`); 32 MediaFiles carry `transcode.clustarr.io/suspend=true`, and no
    pool Job appears.
13. **After `retain` (72 h).** The census reads 0, and `kubectl get
    subtitlerequests,transcodejobs,audiografts -A` returns nothing.
14. **Resume transcode** (owner's OK): remove the runbook's suspend annotations
    (`kubectl -n clustarr-system annotate mediafile --all transcode.clustarr.io/suspend-`), then
    restore the slots in values.
15. **Clean up** (split step 9). Delete the stale per-service `.clustarr.io` leases.

**Release N+1:**

0. **Gates.** N's report shows 0 objects of each kind, no `task-withdrawal` finalizer, and 0
   MediaFiles carrying `catalogarr-markers`; N has run in `apply` mode for at least `retain`.
   Save `helm get values`, `helm get manifest` and N's MediaFile CRD.
1. **Stop the manager.** Scale it to 0 and wait until `manager.clustarr.io` lapses: N's loop
   writes `sidecars[].path`, which N+1's schema no longer has, and N+1's loop writes no `path`,
   which N's schema requires, so neither may run against the other's CRD.
2. **Apply the MediaFile CRD** without `legacyFold` and `Sidecar.path`: `kubectl apply
   --server-side --force-conflicts -f config/crd/bases/catalog.clustarr.io_mediafiles.yaml`.
3. **Deploy.** `helm upgrade` to N+1 with fresh values carrying no `manager.legacyFold`.
4. **Verify.** The manager is Ready and its log has no `no matches for kind`.
5. **Delete the CRDs.** `kubectl delete crd subtitlerequests.subtitle.clustarr.io
   transcodejobs.transcode.clustarr.io audiografts.transcode.clustarr.io` (order does not matter).
6. **Check the API groups.** `kubectl api-resources --api-group=transcode.clustarr.io` lists only
   `transcodeprofiles`; `--api-group=subtitle.clustarr.io` lists `subtitleprofiles` and
   `subtitleproviders`.

### 7.6 Rollback

"Previous" is the release before N (revision `P`), which predates the split. **Every rollback from
N to the previous release is this procedure**, which replaces split §11.3's rollback:

1. **Stop N.** Scale the manager, every agent Deployment and markers to 0; wait until
   `manager.clustarr.io` lapses (`renewTime` more than 60 s old) and the pods are gone.
2. **Delete N's pool Jobs** (pool names are unchanged; the old workers would dead-letter N's v2
   tasks).
3. **Restore markers ownership.** `bin/manager legacy-fold restore-markers --namespace
   clustarr-system` from N's commit (§7.3.8, §7.3.11); it must report every file restored.
   Without it, the previous release's first `catalogarr` apply deletes `status.markers` on every
   file.
4. **Revert the CRDs.** `kubectl apply --server-side --force-conflicts -f` the previous commit's
   CRDs saved in §7.5 step 0. N removed no CRD, so nothing is deleted. N's extra status fields
   are then pruned on read (`customresource_handler.go:1274-1290`), and N's caps, which a
   previous-release probe could exceed, go. N's schema also accepts the previous release's applies
   on its own (§2.16), which covers the window between this step and the next, and an operator who
   skips it.
5. **`helm rollback clustarr P`** (the old images are still loaded). Remove N's new streams,
   buckets and consumers only if they interfere (they do not: nothing old consumes them). The
   previous handlers dead-letter any v2 `FetchTask` or `MarkersTask` left in the work streams
   (their schema does not decode), and the previous planners request again.
6. **Unsuspend** the TranscodeJobs with the owner's OK.

| From → to | When | Effect | Operator |
|---|---|---|---|
| N (`hold`) → previous | any time | Lossless with steps 1-6: nothing was adopted, prepared or deleted; markers survive through step 3; N's blocks are pruned (step 4) or released by the previous `catalogarr` apply; sidecars N listed are on disk and read as existing. | Steps 1-6. |
| N (`apply`) → previous | within `retain` | Legacy objects are present and frozen, and the previous controllers resume from them. Transcodes N did carry `CLUSTARR_PROFILE`, so the old worker skips them; cancel markers expire after 90 s; the previous `catalogarr` apply releases the blocks and `legacyFold`. A later roll-forward adopts again, because UIDs the previous release created are new and the record was released. | Steps 1-6. The 32 jobs keep `spec.suspend=true` until unsuspended with the owner's OK. |
| N (`apply`) → previous | after deletion | Lossy: the previous SubtitleProfile recreates requests with fresh backoff (the §7.1 burst), the TranscodeProfile recreates current-hash jobs in the window (Blocked and fallback verdicts lost), AudioGraft intent is lost (0 live). Markers survive through step 3. | Not recommended; roll forward. If forced, steps 1-6, and scale the previous caption worker to 0 first. |
| N+1 → N | CRDs still present | Lossless; the census reads 0 at start. | Scale the manager to 0, re-apply N's MediaFile CRD (saved at N+1's step 0; N's loop requires `Sidecar.path`, and fills it from `name` on entries N+1 wrote, §2.7), then `helm rollback`. |
| N+1 → N | CRDs deleted | Lossless; the state lives in the blocks. | As above, plus `git show <N commit>:config/crd/bases/<file>` for the three legacy CRDs and `kubectl apply --server-side`, before `helm rollback`. |
| N+1 → anything before N | — | Not direct. | N+1 → N, then N → previous. |

### 7.7 ui projection and actions (F8)

- `ui/projection/projection.go:470-479` (`watchedKinds`): drop `TranscodeJob` and `SubtitleRequest`;
  `TestEveryListedKindIsWatched` keeps lists and watches together.
- `ui/projection/index.go:55-166`: delete the `jobs` and `subtitles` maps and `mediaFileOwner`;
  bucket MediaFiles by `spec.mediaRef`, keyed `<kind>/<name>` for `name` and each of `keys`, so a
  multi-episode file belongs to each of its Episodes; `Related.MediaFile` is the file named by the
  item's `status.fileRef` where the kind has one (Movie, Episode, Book, Issue), else the newest file.
  This fixes the existing defect: no production MediaFile has an owner reference, so
  `Related.{Jobs,Subtitles,MediaFile}` is always empty and scenario 13's pipeline leg could never
  pass.
- `pkg/pipeline/stage.go:110-128` (`Related`): drop `Jobs` and `Subtitles`, add
  `TranscodePercent *int32`.
- `pkg/pipeline/project.go`: transcode stages from `MediaFile.Status.Transcode` (Queued and Running
  are `StageTranscoding` with the percent; `Swapping`, `Succeeded` or `Transcoded()` is
  `StageTranscodeDone`); subtitle stages from `MediaFile.Status.Subtitles` (§6.9); `StageComplete` no
  longer requires a job or a subtitle request, so a file the profile skips or that wants no subtitles
  is complete (this also fixes "SubtitleDone instead of Complete after retention");
  `fetchingSubtitle` (`:380-392`) deleted.
- `ui.Options.TranscodeProgress func(ctx context.Context, mediaFile types.UID) (percent int32, ok bool, err error)`
  beside `PlexExtended` (`ui/server.go:184`); `internal/cli/ui` binds it to the read-only bus's
  `clustarr-progress` key `transcode.<KVKeyToken(uid)>` under a 2 s deadline; a nil option renders no
  percent. `TestBothUICommandsWireEveryUIOption` covers it; `TestUINeverWrites` is unaffected.
- `config/rbac/ui_role.yaml:98-114`: drop `subtitlerequests` and `transcodejobs`; the chart's copy
  follows (`TestUIRoleChartMatchesConfig`).
- **`ui/actions`: no change** (§9, D18). The ui holds no write on mediafiles, so the intent
  annotations are `kubectl annotate` only. `settings.go`'s TranscodeProfile priority action stays.

### 7.8 e2e (F8; every scenario is still written and never run, as Phase H owns that)

- `test/e2e/transcode_test.go`: `TestTranscodeMediaFileThroughTranscodeJob` (`:584`) becomes
  `TestTranscodeMediaFileThroughItsStatus` (asserting `status.transcode`'s phases, plan and admission
  fields, the pool resuming and suspending, the swap (`Succeeded`, `profileTag`, hevc, `Transcoded()`),
  the recycled original, a rehash on a profile edit, and the `transcode.clustarr.io/suspend` round
  trip); `TestTranscodeContainerChangeMovesTheFile` (`:765`) asserts the `spec.path` takeover to
  `.mp4`; `TestTranscodeDolbyVisionSkipped` (`:847`) asserts the Skipped verdict;
  `TestDownloadScenario1TranscodeLeg` (`:903`) asserts on the block.
- `test/e2e/subtitle_test.go`: `TestSubtitleRequestSidecarPipelineAndLanguageRemoval` (`:310`) becomes
  `TestSubtitlesSidecarPipelineAndLanguageRemoval` (its `data-stage="SubtitleDone"` leg can now pass);
  `TestSubtitleThrottleFallsThroughToGestdown` (`:535`) and `TestDownloadScenario1SubtitleLeg` (`:728`)
  read `status.subtitles`; the helper `describeSubtitleRequest` (`:182-190`) becomes
  `describeSubtitles`; new `TestForcedSubtitleSearchAnnotation`.
- `test/e2e/zzz_observability_test.go` `TestObservabilityMetricsNonZero` (`:186`) waits for
  `status.transcode` to read `Succeeded`; `test/e2e/download_test.go:100-101,283` comments are updated;
  `test/e2e/main_test.go:84-85` `expectedCRDCount` becomes 31 in N (correcting the stale 29) and 28 in
  N+1.
- New, release N only: `test/e2e/legacyfold_test.go` `TestLegacyFoldAdoptsThenDeletes` (through the
  dynamic client: a SubtitleRequest whose status has an item at count 5 with `nextSearchAt` in an hour
  and a `lastError`; a Succeeded TranscodeJob with `finishedAt > probedAt` over a fixture output; an
  open TranscodeJob carrying `task-withdrawal`; asserting the copied item leaves, the pending swap
  incorporated, the withdrawal's cancel marker in `clustarr-transcode-leases`,
  `status.legacyFold.adopted`, and deletion under `retain=0s`).
- New: `test/e2e/graft_test.go` `TestAGraftRidesAFilesTranscode`.

### 7.9 Docs (W10, after F8; N+1 in F9)

- `docs/superpowers/specs/2026-09-18-clustarr-design.md` (35 mentions today): §4.2 the MediaFile
  status blocks, "as built (ADR-0016)"; §4.5 TranscodeJob and AudioGraft removed, TranscodeProfile
  stays; §4.6 SubtitleRequest removed; §6.4 and §6.5 the services' controllers, now the loop; §8 the
  transcode and subtitle data flows; §19 an ADR-0016 summary line.
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md:433-434` (A3.3, the pipeline
  mapping): subtitle and transcode stages from `status.subtitles` and `status.transcode`, progress from
  `clustarr-progress`. The amendment wins over the spec, so its text must not keep the old kinds.
- `CLAUDE.md`: `:7` `kubectl get movies,downloads,transcodejobs` becomes
  `kubectl get movies,downloads,mediafiles`; `:54-57` the window, retention and SubtitleRequest text;
  `:360-380` the AudioGraft text; `:409` and `:959` the TranscodeJob status invariant and the "two
  write paths need compare-and-swap" gotcha, restated for the loop; a Status paragraph for the fold.
  The historical phase paragraphs stay as records.
- `README.md:8,59-60`: the ground-truth `kubectl get` line and the list of kinds.
- `docs/adr/README.md` refinement bullets (the ADR body is frozen): **0016** — the copy is adoption
  inside the loop and protects subtitle backoff (`attempts`, `nextSearchAt`, `lastError`), not
  provider, score or subtitle ID; AudioGraft migrates too and the CRDs go in a second release;
  remediation records live in one Durable bucket per remediation; segment analysis and markers are
  fenced by inputs, not a loop-issued sequence; the ledger rebuilds through the APIReader with a
  status selectable field, which the apiserver accepts on 1.37, so the label fallback is not built;
  user intent is nonce annotations for one-shot actions and value annotations for standing
  preferences; planner failures are per-planner conditions present only while failing, with
  conditions MaxItems 12. **0005** — TranscodeJob is gone. The split's ADR numbers need no
  change: the committed split spec (§12, spec:6281-6297, and its header) and plan (8b3f67bb)
  already account for ADR-0016, with **ADR-0018** `docs/adr/0018-manager-agents-and-ui.md`
  (superseding 0013; plan Task W10.2) and **ADR-0017** `docs/adr/0017-no-external-media-programs.md`
  (plan Task W10.3), 0017 keeping its number because the plan's code cites it from Wave 0 on.
- `docs/observability.md:206`, and its metric table for every family this design adds (§3.19,
  §4.15, §5.16, §7.3.11; the legacy-fold four removed in N+1), with the same rows in amendment
  §A2.3, which the catalogue test reads (§3.19). The tasks that add the families update both
  (§8.6); W10 only checks them. `docs/gpu-nodes.md:16`. Historical plans and research notes stay.
- N+1: `charts/clustarr/crds/README.md` (§7.4), and CLAUDE.md's Status ("the three CRDs are removed;
  deleting them was a manual step").

### 7.10 cluster-plex (cross-repo)

**No cluster-plex change is required.** `pkg/clustarrwatch` reads `spec.path`, `spec.mediaRef`,
`spec.sizeBytes`, `spec.modTime`, `status.mediaInfo`, `status.probeHash` and `status.markers` from
MediaFile, plus item metadata (`/home/appkins/src/mediactl/cluster-plex/pkg/clustarrwatch/fields.go:32-54`);
its Role grants `get,list,watch` on mediafiles, movies, series and episodes only
(`charts/cluster-plex/templates/rbac.yaml:85-92`); its `kind-clustarr` fixture defines only those four
CRDs, schemaless (`k8s/overlays/kind-clustarr/clustarr-fixture.yaml:71-123`); nothing in it names the
three kinds.

Release N's one-time MediaFile write waves (adoption on about 3,000 files; §7.3.8 on 11,958) reach its
informer and are dropped by its update handlers, because `FileOf` and `SeedKey` are unchanged
(`pkg/clustarrwatch/watcher.go:466-482`): no Plex scan, no seed. That holds only because the
loop's first render reproduces every stored `status.markers` and `status.mediaInfo` byte for byte:
the markers planner carries stored markers forward unless an input changed, reads today's v1
segment records, and the renderer never re-clamps a stored value (§2.11.1 rule 5, §4.12). A
re-merge or a clamp would change `SeedKey` (path, size, probe hash, probe, markers;
`fields.go:156-171`), reseed Plex, and delete marker rows no longer wanted
(`pkg/plexseed/seeder.go:90-103`). `TestTheFirstLoopPassKeepsEverySeedKey` (§7.3.12) and the
pre-flight report's render count (§7.5 step 1) hold it. Optional wording only: `pkg/clustarrwatch/fields.go:60-61` and
`pkg/plex/prefs/required.go:67` name squasharr. Not the fold's doing: the MP4 standard turns every
transcode into a path change, which cluster-plex sees as a delete and an add.

### 7.11 Commits

Commits are path-scoped (`git commit -m '…' -- <paths>`), with no stash and no push (R14), found by
subject, never by SHA. The release-N series is F8.1-F8.11 and the N+1 series F9.1-F9.5 (§8), each
keeping `go build ./... && go vet ./... && make test` green.

### 7.12 Names this section takes from other sections

The copy is leaf for leaf, so a renamed leaf renames its row: `status.subtitles.{items[].{langKey,
state,score,scoreOutOf,provider,subtitleID,name,lastError,downloadedAt,attempts,nextSearchAt},
probeHash,profile}` (§2.4); `status.transcode` pending swap (`phase: Swapping`,
`result.outputPath`, `result.outputSizeBytes`, `finishedAt`, `profile`, `profileHash`, `profileTag`)
and verdict (`phase`, `reason`, `blocked`, `attempts`, `fallbackReason`, `nextAttemptAt`,
`profileHash`, `probeHash`) (§2.5); `status.graft` leaves and the item's `status.audio.donor` and
`rejectedReleases` (§2.6); the intent annotations (§2.9); the loop's field manager `catalogarr` and
its no-op skip (§3.7); v2 tasks, stale-acking v1, the re-keyed `clustarr-progress` key and the task
sweep (§4, §5).

---

## 8. Integration with the split plan

This section says, task by task, what the fold changes in the split plan
(`docs/superpowers/plans/2026-10-06-manager-agent-split.md`, cited as `plan:<line>`), then sets out the fold's
own waves, the ffgo tag, and how the work interacts with the MP4 standard on main.

### 8.1 State this section was written against (re-verified 2026-10-06)

- **The branch.** `unify-manager-agent` is at `8b3f67bb` (`docs(plan): manager, agents and ui split
  -- 223 tasks …`), on main `0d3ae234`: the split spec (`352ac3d9`), ADR-0016 (`a63ca1e0`) and
  the **committed** split plan, which says it is "not yet revised for ADR-0016's fold". Every
  `plan:<line>` below is a line of that commit, and every row also names its task, so a line that
  moves can be found by the task heading. No plan task has been executed
  (`/home/appkins/src/mediactl/ffgo-unify` does not exist). go.mod:218 still replaces ffgo with
  `github.com/mediactl/ffgo v0.0.0-clustarr.11`.
- **The committed plan already carries** the ffgo `.12` base and `.13` tag (W0.1's preconditions,
  plan:323-327; W0.2's comment, plan:437; W0.23's shim comparison and tag, plan:4071, 4131; W0.25's
  replace, plan:4252; W0.28's check, plan:4506; W6.1, plan:39840, 39865; W6.3's `FFGO_REF`,
  plan:40386; W7.1, plan:47146; W7.2's `MinShimAPI` comment, plan:47440; 30 uses of
  `clustarr.13` in all) and the split's ADR numbers after ADR-0016 (W10.2 is ADR-0018, plan:53264;
  W10.3 is ADR-0017, plan:53400; split spec §12, spec:6281-6297). This section lists only the edits
  the committed plan still lacks.
- **Local main** is at `adf9372c`, 17 commits past the base: `35db28ff`, `ebbb2322` (go.mod:218 →
  `v0.0.0-clustarr.12`), `fb57194d` (`fsops.SidecarPath`), `b0bd01ee` (`standard.Version` 2, MP4),
  `fbfd754c`/`75e651c8` (every subtitle becomes a sidecar), the MP4 engine and squash work through
  `b33e4417` (`worker.OutputContainer`), **`dea6d010`, MP4 phase 1's gate commit** ("docs: the MP4
  standard's phase 1 as built"), and fixes after it (`fa12e1b5`: a rename and a delete take the
  transcode's own sidecars with the file). So F6.1's prerequisite is met on main; the branch takes
  it at the next rebase.
- **ffgo.** `v0.0.0-clustarr.12` is `a18455764d9f79215f3973867bcb1206d6b8371a`, a lightweight tag
  already on origin. It added `avcodec.NewPacket` (binds `av_new_packet`, avcodec/avcodec.go:471),
  `avcodec.SetCodecParCodecID`/`SetCodecParExtradata`, `ffgo.NewPacketFromData` and `(*Packet).Data`
  (ffgo.go:289,305). `git diff v0.0.0-clustarr.11 v0.0.0-clustarr.12 -- shim` is empty.
  `v0.0.0-clustarr.13` does not exist, locally or on origin. The rulings call ebbb2322 "the .12 main
  published"; ebbb2322 is clustarr's go.mod bump, and the fork commit tagged `.12` is `a184557`.

### 8.2 Rules this section applies

1. **The split moves only code the fold keeps** (the owner's sequencing ruling, rulings.md:56). Code
   the fold deletes stays in its package, and the manager or agent registers it from there.
   Registration-only tasks are kept; mechanical edits doomed code needs to keep compiling are kept
   (import rewrites to `app/squash/jobspec`, prefixed index names, test-fixture methods).
2. **The fold runs after W5.18 and before W6.1.** By then `cmd/manager` exists (W5.7), `cmd/clustarr`
   is deleted (W5.17), and `TestEveryFieldManagerHasItsHome` (W5.19) is in force. Images, installers,
   RBAC, KEDA removal, the e2e renames (Wave 6), native media (Waves 7–9) and the docs (Wave 10) are
   then written once, for the system after the fold.
3. **One deploy for the split and the fold together** (§9, D1): release N is everything through
   W10.8; release N+1 is F9. No commit between W4.13 and F8.12 is deployed.

### 8.3 Task-by-task disposition

Rows cite the committed plan (`8b3f67bb`) by task and line. Tasks not named are kept unchanged.

**Wave 0 (ffgo, go.mod):** keep every task. The one edit the committed plan still lacks:

| Task | Disposition | Change |
|---|---|---|
| W0.29 (plan:3773) | Change | Drop `avcodec.NewPacketData` (Files, plan:3780; Produces, plan:3785; the snippet, plan:3880-3882; its use in `WriteSubtitle`, plan:3934): `.12` already binds `av_new_packet` as `avcodec.NewPacket`, so a second binding is a duplicate symbol. `WriteSubtitle` builds its packet with `ffgo.NewPacketFromData(ev.Data)` (or `avcodec.NewPacket` plus a copy). Remove `NewPacketData` from Produces. |

**Wave 1:** keep all. W1.3, W1.8 and W1.9 touch `transcodejob`, SubtitleProfile and `ffprobeexec` only
as callers that survive until the fold, or as transitional code.

**Wave 2a (catalog leaves, C6 and C7):**

| Task | Disposition | Change |
|---|---|---|
| W2.9, C6 (plan:8368) | Change (inverted) | As written it moves `segmenting.Applier` and `Results` (the `catalogarr-markers` write and the `catalogarr-segments-result` consumer) into a new agent package `app/catalog/worker/segmentresults` (plan:8373-8381), which the fold then deletes: the move the ruling forbids. **Instead** move the planner, which the fold keeps (`plan.go`, `plan_test.go`, the `Planner` half of `setup.go`; today's `segmenting.Setup(… Planner: true)`, plan:8544), to a new manager package `app/catalog/segmentplan`, producing `segmentplan.{PublishPlan, Planner, Options{Bus, Reader}, Setup, Due}`. `app/catalog/segmenting` keeps `Applier`, `NewApplier`, `Results`, `TheIntroDBUpdate`, `AnalysisUpdate`, and `Options{Bus, Reader, Client}`/`Setup` for Results only; once the planner leaves it imports no `app/catalog/controller/*` (today `go list` shows `controller/episode`, `controller/series`, which W3.14's rule forbids for agents). **Guards:** `go list -deps ./app/catalog/segmenting` contains no `app/catalog/controller/`; `go list -deps ./app/catalog/segmentplan` contains no `app/catalog/segmenting`. The MediaFile reconciler's `segmenting.PublishPlan` call becomes `segmentplan.PublishPlan`. |
| W2.10, C7 (plan:8582) | Keep the move; change the imports | The TheIntroDB handler survives; only its write changes, to a record, in F3.4. It imports `segmenting.{Applier, TheIntroDBUpdate}` instead of `segmentresults.*`; the Step 1 guards (plan:8603-8604) deny `app/catalog/(controller/\|segmentplan$)` instead of `segmenting$`. |
| W2.11, C8 (plan:8683) | Keep | F8.6 retargets `target.go` and `ReplayKinds`. |
| W2.12 gate (plan:8951) | Change | The manager-side loop (plan:8969-8971) names `./app/catalog/segmentplan` where it names `./app/catalog/segmenting`; the agent-side loop (plan:8975-8976) adds `./app/catalog/segmenting`, its deny regex `(controller/\|history/replay$\|segmentplan$)`. |

**Wave 2b:**

| Task | Disposition | Change |
|---|---|---|
| W2.21, I2 (plan:9278, `mediafilespec.RenameFile`) | Keep | The rename actuator calls it (F3.3). |
| W2.30, S1 (plan:10581, `app/squash/jobspec`) | Keep, with an MP4 note | The transcode planner reuses `BuildTask`, `OutputPath`, `ProfileHash`, `StandardProfile`; the edits to `audiograft/controller.go` and `transcodejob/*.go` are import rewrites the doomed controllers need to compile. **MP4 note:** main's `b33e4417` added `worker.OutputContainer`; if it is on the branch when W2.30 runs, `OutputContainer` and `ProfileHashAt` move into `jobspec` with `profile.go`, and their `transcodejob/controller.go` and `dispatch.go` call sites read `jobspec.OutputContainer`; if not, F6.1's rebase resolves the same points. |
| W2.31, P1 (plan:10800, providerset split) | Keep | The subtitles planner and the slim SubtitleProfile reconciler use the light half. |

**Wave 3 (registrations):**

| Task | Disposition | Change |
|---|---|---|
| W3.7 metadata domain (plan:13279) | Change | Register `segmenting.Setup(ctx, segmenting.Options{Bus, Reader: APIReader, Client})` instead of `segmentresults.Setup` (plan:13416), with the import (plan:13389) changed the same way. F3.4 removes it. |
| W3.8 catalog manager (plan:13477) | Change | Register `segmentplan.Setup` where it registers the planner half of `segmenting.{Setup, Options}` (plan:13500). |
| W3.9 import manager (plan:13805) | Keep | It registers the rename controller from its package; F3.3 removes it. |
| W3.12 squash manager (plan:14977) | Keep | Registration only. F6.6 and F7.3 shrink `squashmanager.Register` to the pool renderer and slim TranscodeProfile reconciler, the `transcode-admission` controller and the task sweep, and rewrite its `TestRegisterAddsEverySquasharrController` (plan:15029, which waits for a TranscodeJob to reach Pending and an AudioGraft to reach `ReasonItemMissing`) to assert the slim TranscodeProfile reconciler and `transcode-admission` instead. F6.6 also drops `JobRetention` and `DefaultJobRetention` from its `Options` and tests (plan:15008, 15092-15097, 15146, 15179, 15191) and from the W3.15 and Wave 5 symbol checks (plan:15614, 34905). |
| W3.13 caption manager/agent (plan:15218) | Keep | Registers subtitleprofile and subtitlerequest where they are; F5.3 shrinks the manager side to SubtitleProvider, Bootstrap and the slim SubtitleProfile reconciler. The agent keeps both fetch durables. |

**Wave 4a (the probe queue):**

| Task | Disposition | Change |
|---|---|---|
| W4.1-W4.4, W4.6-W4.13 | Keep | `pkg/probestore`, `schema.ProbeRecord`, `clustarr-probes`, `judgeProbe`, `probeRecordsSource` (W4.12, plan:19345) and the reconciler's queued probe (W4.13, plan:19600) are the template the fold generalises: F1.1 moves the CAS core into `pkg/records` without changing probestore's API or the record's JSON; F1.4 turns `probeRecordsSource` into `recordsource.New(bus, events.BucketProbes, …)`; F3.2 recasts the probe path as the probe planner (status before request, §3.8). No snippet in these tasks changes. |
| W4.5 (plan:17406) | Keep, renamed by F1.1 | W4.5 adds `clustarr_probe_requests_total` and `clustarr_probe_duration_seconds` to `wantSeries` (plan:17413-17432) and observability.md. F1.1 renames the first to `clustarr_record_requests_total{remediation,lane}` (§4.12) in `wantSeries`, amendment §A2.3 and observability.md. |
| W4.14 (plan:20794) | **Drop** | It gates five controllers and the `segmentresults` Applier, all of which the fold deletes. Replacement: F3.2's structural gate (every planner after the probe returns its last block while `!view.ProbeCurrent()`), held by `TestNoPlannerPlansFromAPendingProbe`. Between W4.13 and F8.12 the backstops are the worker-side checks split §6.5.3 cites (the transcode worker re-probes and exits `ExitInvalidSource`, `app/squash/worker/run.go:211-214`; the fetch worker compares `ProbeHash(path,size,mtime)`; `markers.Due` fetches again on the new probe hash). The window is accepted only because nothing in it is deployed (§9, D1). |
| W4.15 gate (plan:21199) | Change | Remove W4.14's tests from the gate's list. |

**Waves 4b, 4c, 4e:** keep. W4.41 (plan:24650) and W4.43 (plan:25118) edit `transcodejob` test
fixtures mechanically; the per-profile pool durables they touch survive.

**Wave 4d (autoscaled consumers):**

| Task | Disposition | Change |
|---|---|---|
| W4.63 `pkg/agentdomain` (plan:28613) | Keep | F3.4 removes `ConsumerCatalogSegmentsResult` from the `Metadata` domain's consumers; F6.6 removes the `ConsumerSquasharrResults` row from `Fixed()` (plan:28647-28665), leaving only `catalogarr-segments-plan`. No new consumer appears: records wakers are leader-only KV watches in the manager, not durables. `Throttled()` is unchanged; caption stays 0..1. |

**Wave 5 (binaries and guards):**

| Task | Disposition | Change |
|---|---|---|
| W5.4 `starttest.ManagersOf` (plan:35333; the map, plan:35528) | Keep | F3.4 sets `"agent-metadata": {"catalogarr-metadata"}`; F5.3 sets `"agent-caption": {}`. |
| W5.6 (plan:35765, `/usr/bin/transcode` for pool and graft Jobs) | Keep | Pools survive; the graft Job survives as `transcode --graft-task` (F7.2). |
| W5.7 `cmd/manager` (plan:35820) | Keep | The `squasharr.audiograft.*` index prefix (plan:35827, 35846-35847) is needed for the manager to start at all. F4.1, F5.3, F6.6 and F7.3 amend `TestManagerFieldIndexes` (plan:36082). F6.6 removes the `--job-retention` flag row (plan:36305) and its default test (`assert.Equal(t, 24*time.Hour, o.JobRetention)`, plan:35905). F3.1 adds `--remediation-concurrency`, `--remediation-bulk-writes-per-second` and `--remediation-io-workers` to the flag table and their default tests. |
| W5.8 start envtest (plan:36508) | Keep | F5.3 rewrites `verifyCaptionarrController` (plan:36520) and F6.6 rewrites `verifySquashController` (plan:36530, 36609) against MediaFile status. |
| W5.12 renames to `cmd/transcode` and `cmd/markers` | Keep | — |
| W5.13 `test/system` (plan:38084) | Keep | F8.12 extends it: under `f:status`, a MediaFile carries only `catalogarr`. |
| W5.14 deps guards (plan:38204) | Keep | F6.6 and F7.3 replace the anchors `app/squash/controller/transcodejob` and `.../audiograft` (plan:38320-38321) with `app/remediation`, `app/squash/transcodeplan`, `app/squash/admission` and `app/squash/graftplan`. |
| W5.19 `TestEveryFieldManagerHasItsHome` (plan:39492) | Keep | F3.4 and F5.3 move `ManagerCatalogarrMarkers` and `ManagerCaptionarrWorker` into `k8s.RetiredFieldManagers()` and drop their `wantHomes` rows; F5.3 drops the `fetch.FieldManager` alias and `PatchRequest` from `writeFuncs`; F6.6 drops `app/squash/status.PatchCAS` from `fixedManagerWrappers`. |

**Wave 6 (installers, RBAC, images, e2e renames)**, text amendments, written after the fold:

| Task | Disposition | Change |
|---|---|---|
| W6.1 (plan:39823) | Change | Depends on F8.12 (it depended on Wave 5). Its `.13` checks are already in the plan. |
| W6.2 (plan:39911, `TestEveryConsumerHasExactlyOneHome`) | Change | Reads the post-fold `Fixed()` and domain table. |
| W6.9 (plan:41974) | Change | `RBAC_PATHS_manager` names `./app/catalog/segmentplan`, `./app/remediation/...`, `./app/squash/admission` and `./app/catalog/legacyfold` instead of `./app/catalog/segmenting`; `RBAC_PATHS_agent-metadata := ./app/catalog/metadata/... ./app/catalog/worker/markers`. In release N the manager role keeps the adoption grants (§7.3.10). agent-caption and agent-metadata hold no write verb on any `*.clustarr.io` per-file resource. |
| W6.11 (plan:42501, both installers' manager args) | Change | The kustomize `config/manager` args and the chart's manager args carry release N's `--legacy-fold`/`--legacy-fold-retain` (§7.3.10), `--remediation-concurrency`, `--remediation-bulk-writes-per-second`, `--remediation-io-workers` and `--graft-concurrency`, and drop `--job-retention`. The grace-period comment (plan:43625) names only `catalogarr-segments-plan`; the value (70) is unchanged, the 60 s AckWait at pkg/events/topology.go:735 still the longest. |
| W6.13 (plan:45433, the closed `values.schema.json`) | Change | Admits `manager.legacyFold.{mode,retain}` (mode an enum `apply\|hold`) and `manager.remediation.{concurrency,bulkWritesPerSecond,ioWorkers}`, and rejects `manager.jobRetention`. F9.3 removes `manager.legacyFold`. |
| W6.15 (plan:45931) | Change | Renames the files F8.9 rewrote. |
| W6.20, W6.21, W6.22 (plan:46820, 46839, 46858) | Change | They also convert main's MP4 tests that run the CLIs (`pkg/transcode/engine/mp4_capabilities_test.go:50,63`, plus MP4 Tasks 5-9's). `app/squash/controller/transcodejob/parity_envtest_test.go` (plan:46841) becomes `app/squash/transcodeplan/parity_envtest_test.go`. |

**Waves 7-10:**

| Task | Disposition | Change |
|---|---|---|
| W7.4 (plan:48101) | Change | Drop the `segmentresults` file edits (the code is gone). `Due` moves to `app/catalog/segmentplan.Due`, not `segmenting.Due` as its heading says. `app/segments/worker/worker.go` is edited on top of F3.4's record writer. |
| W8.5 (plan:50687), W8.9 (plan:51359) | Keep | They touch worker internals that survive; their file lists follow F5.2 and F6.4. |
| W8.7 (plan:50999) | Change | The parity test path follows its move to `transcodeplan` (plan:51048, 51103, and the final gate's list, plan:54512). |
| W10.2 (plan:53264), W10.3 (plan:53400) | Keep | Their ADR numbers (0018 and 0017) already account for ADR-0016; no ADR reference in the plan or the split spec is renumbered. |
| W10.4 (plan:53540), W10.7 (plan:53794) | Change | Describe release N: the loop, records, the retired managers and durables, and the three kinds being adopted; carry §7.9's documentation. CLAUDE.md starts from main's MP4 phase-1 transcoding text (`dea6d010`). |
| W10.8 (plan:54425) | Change | Its AudioGraft and segmentresults mentions follow F7 and F3. |

**Split spec sections to amend** (F8.11): §3.4.1 (drop `--job-retention`, add `--graft-concurrency`,
`--remediation-concurrency`, `--remediation-bulk-writes-per-second`, `--remediation-io-workers`,
`--legacy-fold*`), §3.4.3, §4.3 C6 (spec:1338), §4.4, §4.6 (spec:1751), §5.2 row
`catalogarr-markers` (spec:1833), §5.13, §5.14 item 7, §5.15, §6.5.2 and §6.5.3 (the request
follows the ProbePending apply; `NextSeq`; the waker opens `UpdatesOnly`; "Downstream gates"
spec:2779-2806 replaced by the structural gate; the retention sentence dropped), §11.1 (the
F-steps), and §11.3's runbook and rollback (spec:6175-6280), replaced by §7.5-§7.6, which carry its
steps. §7.5's title and §12 need no change: the committed split spec already reads `.13` and the
post-ADR-0016 numbers.

### 8.4 The ffgo fork moves to `.13`

1. **Before W0.1** (the plan is committed, `8b3f67bb`), the controlling session rebases the branch
   onto local main; go.mod:218 then reads `.12`, and the MP4 docs are present. The committed plan
   already carries steps 2 and 3 (§8.1); they are restated here as the fold relies on them.
2. **W0.1** runs `git -C /home/appkins/src/mediactl/ffgo worktree add -b clustarr/unify-media
   /home/appkins/src/mediactl/ffgo-unify v0.0.0-clustarr.12`. F1-F17 (the fork's own tasks), the
   testmedia additions and F12's `FFSHIM_API_VERSION 1` land on top of `a184557`. `.12` changed no shim
   file, so the prebuilt-shim check in W0.23 diffs against `.12`.
3. **W0.23** creates the annotated tag `v0.0.0-clustarr.13`, local only. **W0.25** replaces go.mod's
   `.12` line with `../ffgo-unify`. From then on `FFGO_REF`, `MinShimAPI`'s comment and every gate read
   `.13`. OD18 (pushing the tag) becomes "push `v0.0.0-clustarr.13` to github.com/mediactl/ffgo, then
   `replace … => github.com/mediactl/ffgo v0.0.0-clustarr.13`" (public: owner's OK).
4. **Collision rule.** If origin or the local repository holds `.13` when W0.23 runs, W0.23 stops; the
   controlling session rebases `clustarr/unify-media` onto the highest published
   `v0.0.0-clustarr.N`; the branch tags `N+1`, and every `.13` here reads `N+1`. §9 D27 asks to reserve
   `.13` so this never fires; the plan records the shared-refs hazard (plan:54476).

### 8.5 How the MP4 standard on main interacts

1. **ffgo.** MP4 Task 1 published `.12` and the branch builds on it; W0.29 reuses its packet API.
2. **The W2.30 move collides with MP4 Task 9** (mp4-standard-phase1.md:1638-1650 edits
   `app/squash/worker/{profile.go,buildtask.go:60}` and the `transcodejob` call sites, which W2.30
   moves to `jobspec`). The W2.30 MP4 note resolves it; there is an optional rebase before W2.30.
3. **The U2 rule.** MP4's tests run the `ffmpeg`/`ffprobe` CLIs (mp4_capabilities_test.go:50,63), which
   the split forbids in tests (Global Constraints, plan:24); W6.20 and W6.22 convert them, and W6.23 records their goldens
   before the exec implementations go.
4. **The transcode planner (F6) is written after MP4 phase 1.** F6.1 waits for the phase-1 gate commit
   ("docs: the MP4 standard's phase 1 as built", mp4-standard-phase1.md:1820; on local main as
   `dea6d010` since 2026-10-06, §8.1) and for
   `OutputContainer = transcodev1alpha1.ContainerMP4` in `app/squash/jobspec`. Every output is
   `<stem>.mp4`, so the path takeover is the normal case; MP4 ruling R1 ("a held file is Skipped, not
   Planned", mp4-standard-phase1.md:55) becomes a `status.transcode` verdict, `phase: Skipped`, keyed by
   (profileHash, probeHash), so phase 2's `standard.Version` raise plans the file again; the plan's
   audio codec comes from `standard.Plan` (b0bd01ee's `standardStatusPlan` change).
5. **MP4 phases 2 and 3 land in the fold's planner.** Phase 2 (OCR) and phase 3 (remux of transcoded
   files, which "the TranscodeProfile controller selects", mp4-standard-design.md:205) edit exactly the
   controllers F6 deletes. If phase 3 is on main before F6.1, F6.2 ports its selection rule into
   `transcodeplan.Eligible`; if not, phase 3 is planned against `app/squash/transcodeplan` (§9, D28).
6. **Sidecars.** The MP4 worker writes `<stem>.<lang>[.forced|.sdh].{srt,ass}` beside the output before
   the swap and never overwrites an existing name (MP4 R3). Today `status.sidecars` mirrors only
   captionarr's downloads, so a `renameFiles` rename orphans them. After the fold `status.sidecars` is
   the loop's own listing (§6.4.3); the rename actuator moves them, and the subtitles planner counts them
   as existing. Both sides spell the HI flag `sdh` (pkg/subtitles/sidecarname.go:37; standard/plan.go:640-652).
7. **Grafts.** MP4 Task 8 makes a surround dub AC-3 5.1 plus AAC 2.0, which does not change the graft
   planner's model.
8. **Live state.** MP4 phases deploy from main one at a time (mp4-standard-design.md §10), so release
   N's adoption meets jobs under the Version 2 hash; the §7.1 inventory is re-read before cutover.

### 8.6 The fold's waves (F0-F9)

F0-F8 run after W5.18/W5.19 and before W6.1; with Waves 6-10 they are release N. F9 runs after W10.8
and is release N+1. The tasks run in order: every wave edits the loop, so there are no parallel lanes.
Each wave gate runs `make test` (envtest, pg-assets, chart dependencies) and `make lint`, plus the
wave's guards.

| Wave | Tasks | Depends on | Gate adds |
|---|---|---|---|
| F0: prerequisites | F0.1-F0.3 | W5.18, W5.19 | `TestOnlyTheLoopWatchesMediaFile` with its allow-list |
| F1: records core | F1.1-F1.5 | F0 | `pkg/records` suite; natsbus and membus Watch contract; records-bucket `Validate` rule; KV key contract |
| F2: API | F2.1-F2.4 | F1 | `TestMediaFileStringsAreBounded`, `TestMediaFileAtEveryCapFitsTheBudget`, `TestMediaFileStatusPhasesAreSelectable` |
| F3: loop core, probe, naming, rename, markers | F3.1-F3.6 | F2 | allow-list loses `rename`; `catalogarr-markers` writes gone; disjointness test inverted |
| F4: item keys | F4.1-F4.4 | F3 | allow-list loses the six item reconcilers |
| F5: subtitles | F5.1-F5.5 | F4 | allow-list loses `subtitleprofile`, `subtitlerequest`; `captionarr-worker` retired |
| F6: transcode and admission | F6.1-F6.7 | F5, MP4 phase-1 gate on main | allow-list loses `transcodeprofile`, `transcodejob`; ledger rebuild tests |
| F7: graft | F7.1-F7.4 | F6 | allow-list empty |
| F8: adoption, history, ui, e2e, release-N close | F8.1-F8.12 | F7 | adoption envtests on live-shaped fixtures; the fold gate |
| F9: kind removal (release N+1) | F9.1-F9.5 | W10.8 | 28 CRDs; no non-history reference to the kinds |

**F0: prerequisites**

- **F0.1 Prerequisite gate.** W5.18 and W5.19 green; `cmd/{manager,agent,ui,markers,transcode}` exist
  and `cmd/clustarr` does not; `git -C ../ffgo-unify describe --exact-match` reads
  `v0.0.0-clustarr.13`; ADR-0016 accepted.
- **F0.2 Rebase** (controlling session) onto local main; record which MP4 tasks the branch carries;
  resolve the `jobspec` conflicts with the W2.30 MP4 note; add any new MP4 CLI tests to W6.20 and
  W6.22's lists.
- **F0.3 `test/guards/mediafilewatch_test.go`: `TestOnlyTheLoopWatchesMediaFile`,** an AST scan for
  `For`, `Watches` or `source.Kind` on `catalogv1alpha1.MediaFile` outside the loop (today
  `app/catalog/controller/mediafile`, from F3.1 `app/remediation`). Its allow-list starts with the
  other 12 watchers, each tagged with the task that removes it: rename (F3.3); movie, episode, album,
  book, audiobook, issue (F4.2, F4.3); subtitleprofile, subtitlerequest (F5.3); transcodeprofile,
  transcodejob (F6.6); audiograft (F7.3). Falsify it with a scratch watch.

**F1: records core**

- **F1.1 `pkg/records`.** `schema.RecordHeader`, embedded flat in `schema.ProbeRecord` with its JSON
  keys unchanged (golden `TestProbeRecordWireFormatIsUnchanged`); `Spec`, `Requester`, `Answerer`,
  `NextSeq` (§4.6), `Pacer` with per-key `Reserve`/`Consume` (§4.7), `CASAttempts`, `RepublishWindow`,
  `FactWindow`, `IncorporatedThrough`, `ErrRaced`, `ErrTooLarge`, `ErrUnincorporatedFact`; the one
  `Withdraw` of §4.7's table; the §4.8 answer rules (an absent record: `Claim` naks, `Answer`
  writes); per-bucket `MaxValue` refusal. §4.15's four `clustarr_record_*` families, with W4.5's
  `clustarr_probe_requests_total` renamed `clustarr_record_requests_total{remediation,lane}`, each
  added to (or renamed in) `wantSeries`, amendment §A2.3 and docs/observability.md's table. `pkg/probestore` is reimplemented on it with its exported API unchanged (W4.4's
  suite and contract test are the regression check). Imports: `pkg/events`, `pkg/events/schema`,
  stdlib only (`TestRecordsLinksNoKubernetes`).
- **F1.2 Topology.** `BucketSpec.{Records, MaxBytes, MaxValueSize}`; `Topology.Validate`'s records rule
  (§4.15); `clustarr-probes` marked Records; the four new buckets and `clustarr-segments`' new caps
  (§4.3); `RecordKey`, `RecordSubKey`, `ParseKVKeyToken`; `Topology.Retired
  []RetiredConsumer{Stream, Durable, Purge}`, which `Ensure` on both buses deletes and purges
  idempotently, returning each durable's lag in its report (empty until F3.4). Extend
  `natsbus/kvkey_contract_test.go`; `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` stays green
  (the buckets are file-backed).
- **F1.3 Watch parity.** membus `Watch` stops dropping past its 64-entry buffer (membus/kv.go:96-104,
  311-337); `WatchUpdatesOnly`, `WatchFromRevision`, `KV.Keys` on both buses; contract tests
  `KVWatchDeliversPastTheBuffer`, `KVWatchUpdatesOnly`, `KVWatchFromRevision`, `KVKeys`.
- **F1.4 `pkg/records/recordsource.New`.** Generalises W4.12's `probeRecordsSource` (§4.9);
  `probeRecordsSource` becomes `recordsource.New(bus, events.BucketProbes, …)`; W4.12's tests keep
  passing.
- **F1.5 Gate.**

**F2: API** (§2)

- **F2.1 MediaFile status blocks:** §2.2-§2.10 and §2.16 (`lastSeq`, `Dispatch` with `withdrawn`,
  `status.subtitles` without `profileGeneration`, the expanded `status.transcode` with
  `joinedGraft`, its `phase` `+optional`, and `jobRef` and `lastResult` kept with no default,
  `status.graft`, `sidecars` with `name` beside the required `path` map key and MaxItems 32,
  conditions MaxItems 12 and the planner-error constants, `handledNonces`, the intent annotation
  constants, the six selectable fields and the print columns). Nothing is removed, so every
  reader and writer of today's fields keeps compiling (§2.15). Nothing new inside `status.markers`.
- **F2.2 Bounds and sanitizing:** §2.11 (the new MaxLengths, `mediafilestatus.Render`'s clamp and C1
  rules, the budget constants and tests).
- **F2.3 The graft's item home:** `AudioState.Donor` (`AudioDonor`) and `AudioState.RejectedReleases`
  on Movie and Episode status (§2.6), written by the loop's item keys under `catalogarr`.
- **F2.4 Gate.** `make generate manifests`; the diff is limited to the MediaFile, Movie and Episode
  CRDs, apply configurations and deepcopy, and is additive (no field removed, `lastResult`'s default
  gone); the three legacy CRDs are unchanged; `go build ./...` is green.

**F3: loop core, catalog and import planners**

- **F3.1 `app/remediation`:** keys, sources (S1 with its user-label arm), `View` (with `Stale` and
  `Labels()`)/`Planner`/`Result`, isolation, the one apply with the applied-status memo, the
  per-key conflict backoff and the status-write limiter (§3.7), the I/O executor (§3.17), the
  outbox, `RegisterIndexes`, `app/remediation/manager.Register`; controller name `mediafile`;
  `--remediation-concurrency` 16, `--remediation-bulk-writes-per-second` 25,
  `--remediation-io-workers` 8 (amending W5.7's flag table). §3.19's `clustarr_remediation_*`
  families in `pkg/obs/metrics`, `wantSeries`, amendment §A2.3 and docs/observability.md. The mediafile controller's `Reconciler` is dissolved into it; its
  domain functions stay in `app/catalog/controller/mediafile`.
- **F3.2 The probe planner:** W4.13's path in planner form, the request after the ProbePending apply;
  `TestNoPlannerPlansFromAPendingProbe` replaces W4.14.
- **F3.3 The naming planner and the rename actuator** (`app/remediation/rename`, over
  `mediafilespec.RenameFile`). `status.sidecars` is read from disk by a temporary listing in the naming
  adapter until F5 moves it to the subtitles planner; `sidecarsFromSubtitleRequest` goes (the
  SubtitleRequest watch and its `spec.mediaFileRef` index stay, as a wake to relist, until F5.3).
  `app/import/controller/rename` is deleted.
- **F3.4 The markers planner** (`app/catalog/markers.Planner` behind the `markers` adapter): TheIntroDB
  through `clustarr-markers` (`schema.MarkersTask` v2 carrying the query, so the metadata domain reads
  no MediaFile; `app/catalog/worker/markers.Handler` answers by `records.Answer`); `cmd/markers`
  (`app/segments/worker`) writes `segments.Record` v2 by CAS with an `Amend` counter for `withIntro`
  (the same-Msg-Id hazard at worker.go:358 goes); `segments.Store` reads today's v1 records; the
  planner carries stored markers forward unless an input changed and merges through
  `pkg/segments.Merge` when one did (§4.12); the v2 handler acks a v1 `MarkersTask` as stale.
  Delete `app/catalog/segmenting`; `catalogarr-segments-result` goes into `Topology.Retired` (purge
  `FilterCatalogSegmentsResult`, subjects.go:119). `ManagerCatalogarrMarkers` moves to
  `RetiredFieldManagers()`. W4.13's `TestMediaFileFieldManagersStayDisjoint` (plan:20664) becomes
  `TestMediaFileStatusIsOwnedByCatalogarrAlone`. Amend W5.4, W5.19, W4.63; agent-metadata's role loses
  `mediafiles/status patch` and `mediafiles get`. `segmenting.Sweeper` lands here.
- **F3.5 DeadLettered and replay inside the loop:** S1 wakes on `DeadLetteredAnnotationChanged` and
  `clustarr.io/replay`; the loop folds `DeadLettered` and runs the replay actuator. No separate
  MediaFile replay controller (it would be another watcher).
- **F3.6 Gate.**

**F4: item keys**

- **F4.1 `remediation.Key` and one index** `remediation.mediafile.item` (§3.16), replacing the nine;
  `episode.MediaFileByEpisodeIndex` points at it, so the segment planner keeps working; amend W5.7's
  `want`.
- **F4.2 Movie and Episode** become item keys (§3.12); their controllers go, their non-file watches
  move onto the loop; a file pass enqueues every covered item when a rollup input changes;
  `applyItemStatus`.
- **F4.3 Album, Book, Audiobook and Issue,** with `album.ReleaseCache`
  (`TestAnAlbumReconcileWithoutAMetadataChangeMakesNoRPC`).
- **F4.4 Gate.**

**F5: subtitles** (§6)

- **F5.1 `app/caption/subtitleplan`** and the `subtitles` adapter, the listing (moved from F3.3's
  temporary home, gated by the pacer's reservation, §6.4.3), the owed-request and unincorporated-
  answer rows of §6.6, the backlog gate `captionBacklog`, `pkg/subtitlestore`, `schema.FetchTask`
  v2, `MsgIDForSubtitleFetch`.
- **F5.2 The fetch worker** answers `RecordSubKey(mfUID, langKey)` in `clustarr-subtitles`; v1 tasks are
  acked as stale; the upgrade margin comes from the task; `SubtitleEvent` v2 names the MediaFile;
  `clustarr_subtitle_fetches_total` is incremented.
- **F5.3 Retire the SubtitleRequest machinery:** the SubtitleRequest reconciler, SubtitleProfile's
  request creator and MediaFile watch, `app/caption/status`'s `RequestControllerFields`,
  `RequestWorkerFields`, `PatchRequest` and `IsLive`, the loop's SubtitleRequest watch;
  `ManagerCaptionarrWorker` retired; the slim SubtitleProfile reconciler (validation with
  `InvalidFilter`, counts from the index). Amend W3.13, W5.4, W5.8, W5.19.
- **F5.4 Forced search and history:** the `subtitle.clustarr.io/search` nonce; history resolvers for
  FetchTask v2 and SubtitleEvent v2 targeting MediaFile.
- **F5.5 Gate.**

**F6: transcode and admission** (§5)

- **F6.1 Prerequisite:** MP4 phase 1's gate commit is on local main and in the branch (rebase by the
  controlling session), and `app/squash/jobspec` holds `OutputContainer`.
- **F6.2 `app/squash/transcodeplan`** and the `transcode` adapter: profile selection moved from
  `transcodeprofile/profile.go`; plans from `standard.Plan` at Version 2; verdicts per (profileHash,
  probeHash); the swap incorporated through `Swapping`, the tag from the dispatched hash; the `<stem>.mp4`
  path takeover under `catalogarr`.
- **F6.3 `app/squash/admission`:** the ledger (`FileView.Seq` = `status.lastSeq`, the absent-block
  observation, resync by replacement, §5.5-§5.6), the `transcode-admission` controller, the rebuild
  through the APIReader with `FieldTranscodePhase`/`FieldGraftPhase`, the window, `LoopSource`, and
  the slim `transcodeprofile` reconciler (pools under `squasharr-pool`, never suspended or deleted
  before `rebuilt`, §5.12; status with `waitingFiles`). §5.16's transcode families in
  `pkg/obs/metrics`, `wantSeries`, amendment §A2.3 and docs/observability.md. It uses
  catalogarr's `!finishedAt.After(probedAt)`, never squasharr's strict `After`.
- **F6.4 Task v2 and records:** `clustarr-transcodes`; subject
  `clustarr.work.transcode.task.<profileUID>.<class>.<mfUID>`; Msg-Id `transcode/<mfUID>/<seq>`;
  `cmd/transcode` answers by CAS; progress only to `clustarr-progress` `transcode.<mfUID>`; lease
  `lease.<mfUID>` with Seq; parts `.part-<mfUID8>-<seq>`; the rescan's kept-output and orphan-part guards
  read `status.transcode`; `squasharr-transcode-results` into `Topology.Retired` (purge
  `FilterTranscodeResults`, subjects.go:102).
- **F6.5 Withdrawal and intent:** §5.10's withdrawal and `withdrawAll`, `dispatch.withdrawn` and the
  fact window (§4.10), the worker's produced-earlier check before `Superseded` and its fact-answer
  retry (§4.12), the `FileMissing` recovery from the record's inputs (§5.9), the sweep keyed on
  live MediaFile UIDs, no MediaFile finalizer; the cancel, retry, priority, suspend and hardware annotations; JobEvent
  v2 resolvers targeting MediaFile.
- **F6.6 Retire the TranscodeJob machinery:** the TranscodeJob controller, `ResultsConsumer`
  (app/squash/run.go:416, then `squashmanager.Register`), and `app/squash/status`'s TranscodeJob
  `ControllerFields` and `PatchCAS` (`ProfileFields` and `PatchProfile` stay); TranscodeProfile's
  MediaFile and TranscodeJob watches; `--job-retention` (W5.7's flag row and default test, W3.12's
  `Options` and the symbol checks, §8.3). Amend W3.12's `TestRegisterAddsEverySquasharrController`,
  W5.8, W5.14, W5.19, W4.63 and W2.30's `TestManagerSideSquashLinksNoWorker` roots.
- **F6.7 Gate.**

**F7: graft**

- **F7.1 Donor on the item:** fileimport's donor import reduces the donor (`graft.Reduce`) and stops
  creating an AudioGraft; the item keys derive `status.audio.donor` from the Download and append
  `rejectedReleases`; `search/donor.go` reads `status.audio.rejectedReleases`.
- **F7.2 `app/squash/graftplan`** and the `graft` adapter: a joined graft derived from the transcode
  block's `joinedGraft` and its result read from the transcode record (§5.13); a
  standalone graft Job (`transcode --graft-task`, with `NATS_URL`, owner MediaFile, label
  `squasharr.clustarr.io/graft-of`) answering `RecordKey(mfUID)` in `clustarr-grafts`; S15; graft slots
  in the ledger.
- **F7.3 Retire the AudioGraft controller** and graftstate's cross-object readers. Amend W3.12's
  register test (no AudioGraft leg), W5.6's tests, W5.7's index table and W5.14's roots.
- **F7.4 Gate.**

**F8: adoption, history, ui, e2e, release-N close** (§7)

- **F8.1** `feat(api): MediaFile status.legacyFold, release N's record of adopted SubtitleRequests,
  TranscodeJobs and AudioGrafts` (§7.3.1).
- **F8.2** `feat(legacyfold): Plan, the pure adoption rules` (§7.3.3), with `test/data/legacyfold/`.
- **F8.3** `feat(legacyfold): Migrator (prepare, census, delete after retention, retired-durable
  reports, the catalogarr-markers pending set) and its metrics` (§7.3.5-§7.3.9), `k8s.ManagerLegacyFold`;
  the four `clustarr_legacy_fold_*` families go into `wantSeries`, amendment §A2.3 and
  docs/observability.md (F9.1 removes them from all three).
- **F8.4** `feat(remediation): adoption and hold in the loop; catalogarr-markers released`
  (`app/remediation/adopt`, §7.3.6, §7.3.8; the item keys' AudioGraft read), with the transition
  envtests of §7.3.12 (`TestThePreviousReleasesStatusApplyIsAccepted`,
  `TestRestoreMarkersMakesARollbackLossless`, `TestTheFirstLoopPassKeepsEverySeedKey`) and their
  fixtures under `test/data/legacyfold/`.
- **F8.5** `feat(manager): legacy-fold report and restore-markers, and the --legacy-fold flags`
  (§7.3.10-§7.3.11).
- **F8.6** `feat(history): dead letters and replays target MediaFile; TranscodeJob and SubtitleRequest
  leave ReplayKinds and the projector` (`app/catalog/history/{target,replay,dlq}.go`,
  `pkg/k8s/deadletter.go`); v1 payloads resolve as unresolvable, a Warning Event on the Namespace.
- **F8.7** `feat(ui): the projection reads MediaFile blocks and buckets files by spec.mediaRef;
  transcode progress from clustarr-progress; the item page's Subtitles table` (§7.7, §6.9:
  ui/detail.go and ui/views/detail.templ, with their tests).
- **F8.8** `chore(rbac): release N's grants` (`app/catalog/legacyfold/rbac.go`; every other grant on the
  three kinds removed with its code, §7.3.10). The installers' `manager.legacyFold` values land with W6.
- **F8.9** `test(e2e): scenarios 12, 13 and scenario 1's legs read MediaFile status; legacy-fold and
  graft scenarios; the upgrade-and-rollback scenario` (§7.8, §7.3.12's `test/e2e/upgrade_test.go`).
- **F8.10** `test(guards): only app/catalog/legacyfold names SubtitleRequest, TranscodeJob or AudioGraft`.
- **F8.11** Split spec amendments (§8.3's list), including §11.3 replaced by §7.5-§7.6.
- **F8.12 Fold gate:** `make test` and `make lint`, plus `TestEveryFieldManagerHasItsHome`,
  `starttest.AssertWroteOnlyAs` for every identity, `TestOnlyTheLoopWatchesMediaFile` with an empty
  allow-list, `TestMediaFileStatusIsOwnedByCatalogarrAlone`, the transition envtests (§7.3.12), and
  `go vet -tags e2e,e2eupgrade ./test/e2e/...`.

**F9: kind removal (release N+1, after W10.8)** (§7.4)

- **F9.1** `refactor: remove the legacy fold` (`app/catalog/legacyfold`, `app/remediation/adopt`, the
  loop hook, flags, report and restore-markers, metrics file and its `wantSeries`, amendment §A2.3
  and observability.md rows, `k8s.ManagerLegacyFold`, `test/data/legacyfold`, the legacyfold and
  transition tests, `test/e2e/upgrade_test.go`).
- **F9.2** `feat(api)!: remove SubtitleRequest, TranscodeJob, AudioGraft and MediaFile status.legacyFold`
  (types, generated code, CRDs, kustomization, `crdbases_test.go`, `test/e2e/main_test.go`, pkg/k8s and
  pkg/crdcheck sites, each retired test with a named replacement), and tighten MediaFile status per
  §2.15-§2.16: `jobRef`, `lastResult` and `TranscodeResult` deleted, `transcode.phase` and
  `Sidecar.name` required, `Sidecar.path` deleted and the list atomic, its readers moved to `name`
  (the files §2.15 names), `MediaFileStatusBudgetBytes` back to 256 KiB.
- **F9.3** `chore(installers): RBAC without the folded kinds; the values schema rejects
  manager.legacyFold`.
- **F9.4** `refactor(events): drop the TranscodeJob- and SubtitleRequest-keyed subjects, Msg-Ids and v1
  task decoders`; `RetiredFieldManagers()` emptied.
- **F9.5** `docs: the folded kinds are gone; CRD removal runbook` (CLAUDE.md, README.md:8,
  `charts/clustarr/crds/README.md`, design of record §4.5/§4.6), and the gate.

### 8.7 Execution order changes

- **New rows:** F0-F8 depend on W5.18 and W5.19; W6.1 depends on F8.12 (it depended on Wave 5); F9
  depends on W10.8. The task total is 223 − 1 (W4.14) + 55 = 277.
- **Rebase points** (controlling session only, between tasks, with a clean worktree): before W0.1; at
  W0.28 (as planned); before W2.30, only if MP4 Task 9 is on local main; at F0.2; at F6.1; before
  hand-back.
- **"What the waves call one another"** gains: "the fold, the remediation loop" → F0-F8; "kind removal,
  release N+1" → F9.
- **"Cross-wave interface gaps"** gains item 13 (ADR-0016's fold, this document) and item 14 (main's MP4
  standard, §8.5).

---

## 9. Owner decisions

Merged from the seven drafts. A recommendation is **taken** unless it is public (pushing a tag or
anything to a remote) or destructive on the live cluster (kind-cluster-plex); those are **open** and
need the owner's explicit OK. Where two drafts recommended different things, the one taken is named and
the other is recorded in Appendix A. Every deploy step stays under "Build, don't deploy".

| # | Decision | Status | Basis |
|---|---|---|---|
| D1 | The split and the fold deploy together as release N (everything through W10.8); release N+1 removes the kinds (F9). No commit between W4.13 and F8.12 is deployed, so W4.14's interim probe gates are dropped. | Taken (deploying it is open) | integration D1. Migration's "split and fold as separate releases with a soak" is not taken: the fold lands before W6, so no split-only release with images and installers can be built; release N's `hold` stage (D30) is the soak. |
| D2 | The fold's waves F0-F8 run between W5.18 and W6.1, so installers, RBAC, images and docs are written once for the folded system. | Taken | integration D2 |
| D3 | The loop is `app/remediation`, controller name `mediafile`, with two key types (file and item). Transcode admission (`transcode-admission`) and the slim `transcodeprofile` and `subtitleprofile` reconcilers are separate controllers that never watch MediaFile; they are woken by two channels the loop and the ledger signal, the only MediaFile-driven wakes outside the loop that `TestOnlyTheLoopWatchesMediaFile` allows. | **Open: confirm the reading.** The ruling lists the TranscodeProfile and SubtitleProfile controllers among those that merge into the loop; this design merges their per-file halves and keeps their per-profile status writers outside it (§1 item 2). The alternative is per-profile keys in the loop's queue (Appendix A item 2, the loop draft's), which ADR-0016's two key types rule out. | ADR-0016's two key types; admission and api drafts |
| D4 | `--job-window` survives as a bound on files (32 per profile in a window phase); the backlog lives in memory, counted in `TranscodeProfile.status.waitingFiles` and `clustarr_transcode_backlog_files`. | Taken | admission D1. The api draft's "retire the window, write Planned to every eligible file" is not taken: every `standard.Version` raise would write about 12k statuses and churn their wait messages. |
| D5 | `--job-retention` and Succeeded retention are removed. | Taken | api, admission, integration |
| D6 | The ledger rebuilds through the APIReader with the status selectable field, not a cache index; no label fallback is built. Recorded as an ADR-0016 refinement bullet. | Taken | admission, api |
| D7 | Segment analysis and TheIntroDB markers are fenced by their inputs, not by a sequence in status; `status.markers` stays frozen for cluster-plex. | Taken | records D5 |
| D8 | AudioGraft's item-scoped half lives on the item: `status.audio.donor`, derived by the item key from the imported `audioDonor` Download, and `status.audio.rejectedReleases`. fileimport reduces the donor to `<stem>.mka` at import. No item-keyed task or record exists. | Taken | api D1 (donor), records D2 (reduce). Not taken: an annotation or `spec.audioGraft` on the item under `importarr-worker` (loop D3, records D3, integration D6), because the import list's applies under the same manager would release it; folding the reduce into the graft task (api D2), which leaves full-size donors on disk for up to the 6 h join wait. |
| D9 | A standalone graft stays a batch Job, now with `NATS_URL`, answering in `clustarr-grafts`; the termination message, `--termination-log` and the pod list are retired. | Taken | records D1, integration D5. Admission D4's "termination message as a stated exception" is not taken. |
| D10 | Multi-episode files are never grafted; the item reads `audio.graft: failed`, reason `MultiItemFile`, and the file has no graft block. | Taken | api D6, loop D4 |
| D11 | No MediaFile finalizer for withdrawal: `withdrawAll` from the ledger's memory of the UID, the sweep for a down manager. | Taken | admission D5, integration F6.5. Records D4's `catalog.clustarr.io/remediation-withdrawal` finalizer is not taken. |
| D12 | User intent: nonce annotations for the one-shot actions (`subtitle.clustarr.io/search`, `transcode.clustarr.io/retry`, `transcode.clustarr.io/cancel`), value annotations for the standing ones (`transcode.clustarr.io/suspend`, `priority`, `hardware`), handled nonces in `status.handledNonces`. ADR-0016's "carrying a nonce" gets a refinement bullet. | Taken | api D5, admission objection 4 |
| D13 | A verdict (Skipped, Failed, Blocked, Cancelled) holds only for its (profileHash, probeHash); a profile edit, a `standard.Version` raise or new bytes make the file eligible again. | Taken | admission D3 |
| D14 | Cancel is a durable verdict (`Skipped`, reason `Cancelled`); retry clears it and resets attempts. | Taken | admission D6, api |
| D15 | Planner failures are six `<Planner>PlannerError` conditions present only while failing; conditions MaxItems 8 → 12; no aggregate condition. | Taken | api D7 (ADR-0016's "sets its own condition"). Loop D1's `PlannersHealthy` plus `status.plannerFailures` is not taken. |
| D16 | SubtitleRequest `spec.languages`, `spec.minScoreOverride`, `spec.profileRef` and TranscodeJob `spec.outputPath` are dropped, not given annotations; a profile is pinned by a MediaFile label; R-11's explicit-outputPath takeover goes with `outputPath`. | Taken | api D4, subtitles D1, migration |
| D17 | Transient planner errors (bus, KV, apiserver, deadlines, EIO) are never written to MediaFile status. | Taken | loop D2 |
| D18 | No ui action and no ui `patch` on mediafiles in this branch; intents are `kubectl annotate`. | Taken | migration D6. The subtitles draft's `actions.SearchSubtitles` is deferred to D19. |
| D19 | A ui "Search subtitles" (and transcode) action, which needs `patch` on mediafiles in `actions.Grants()`, optionally narrowed by a ValidatingAdmissionPolicy. | Open (later; widens the ui's write surface) | subtitles D3 |
| D20 | `status.sidecars` reshaped (basename `name`, MaxItems 32; `path` kept as the map key through release N, atomic from N+1, §2.16) and listing every attributable sidecar, hand-placed and MP4 ones included. | Taken | api D8 |
| D21 | `catalogarr` is the field manager of all MediaFile status, blocks included. | Taken | loop D6 |
| D22 | Subtitle dispatches pass a backlog gate (`captionarr-fetch-normal` lag under 500) and the records pacer; forced dispatches bypass the gate. | Taken | subtitles D2, records |
| D23 | One outbox order for every remediation: status, then record, then publish, with owed effects and record adoption as a safety net. Split §6.5.2/§6.5.3's probe order is amended. | Taken | loop objection 1. The records and subtitles drafts' record-first order for non-admission remediations is not taken. |
| D24 | Adopted legacy objects are deleted after `--legacy-fold-retain` = 72 h. | Taken | migration D1 |
| D25 | No v1 shims: a drain gate before release N, `squasharr-transcode-results` and `catalogarr-segments-result` retired through `Topology.Retired`, v1 tasks acked as stale. | Taken | records D6, migration D7, integration D7 |
| D26 | Make `CLUSTARR_WORK_CAPTIONARR` and `CLUSTARR_WORK_SQUASHARR` Durable (delete and recreate while empty at the drain). | Open (destructive on the live cluster) | records D7; the admission and subtitles drafts recommended keeping the storage, which is the default until the owner rules. |
| D27 | Reserve `v0.0.0-clustarr.13` for the branch's ffgo fork (main uses `.14` next); pushing `.13` to github.com/mediactl/ffgo. | Reserving: taken, with §8.4's collision rule. Pushing: open (public). | integration D3, R12 |
| D28 | F6 waits for MP4 phase 1's gate commit; from F6.1 until merge, main stops editing `app/squash/controller/{transcodejob,transcodeprofile,audiograft}`; MP4 phases 2 and 3 are planned against `app/squash/transcodeplan`. If phase 1 is not landed when F5's gate is green, the owner chooses between waiting and building F6 on Version 1. | Taken as the plan; the pause on main is the owner's to announce | integration D4 |
| D29 | The controller names `movie`, `episode`, `album`, `book`, `audiobook`, `issue`, `rename`, `transcodejob`, `audiograft` and `subtitlerequest` disappear (an exception to split §3.11, justified by ADR-0016's merge); `mediafile`, `transcodeprofile` and `subtitleprofile` keep theirs; `transcode-admission` is new. | Taken | integration D9 |
| D30 | Release N starts in `--legacy-fold=hold`, the census is compared with the pre-flight report, then it flips to `apply`. | Taken as the procedure; each rollout is open | migration D3 |
| D31 | The cutover quiesces transcoding by suspending the open TranscodeJobs under the running release (to drain), deploying release N with `--slots=cpu=0,intel=0,nvidia=0`, and resuming by removing the runbook's suspend annotations and restoring the slots. | Taken as the procedure; executing it is open | migration runbook step 2 with integration D8 |
| D32 | Accept release N's one-time `catalogarr-markers` release wave (about 24,000 writes, once), paced by the status-write limiter (D39). | Taken | migration D9 |
| D33 | The phantom Running TranscodeJob is handled by the suspend and release N's withdrawal, then dismissed; not deleted by hand. | Taken | migration D8 |
| D34 | Rebase main's MP4 standard (`standard.Version` 2, `b0bd01ee`) onto the branch before release N. | Taken | migration D10 |
| D35 | The split's ADR numbers stand as committed: ADR-0018 is manager, agents and ui (supersedes 0013; plan W10.2) and ADR-0017 is no external media programs (plan W10.3), which the committed split spec (§12) and plan already chose after ADR-0016 took 0016. Nothing is renumbered. | Taken | split spec §12 (spec:6281-6297), plan:53264, 53400. The drafts' "0017 = topology, 0018 = no external programs" (migration D11, integration objection 4) was written against an earlier, untracked plan and is withdrawn. |
| D36 | Each kind-cluster-plex rollout (release N in hold, the flip to apply, the transcode resume, release N+1, the CRD deletion) needs the owner's explicit OK. | Standing rule | rulings "Build, don't deploy" |
| D37 | Release N's MediaFile schema accepts the previous release's status applies (`transcode.phase` optional, `jobRef` and `lastResult` kept without a default, `Sidecar.path` kept as the required map key beside an optional `name`); N+1 tightens it (§2.16). | Taken | review: without it, a rollback's first previous-release apply is refused on every file N wrote |
| D38 | A rollback from N to the previous release restores `catalogarr-markers`' ownership of `status.markers` with `manager legacy-fold restore-markers` before `helm rollback`, and re-applies the previous CRDs (§7.6). Release N still releases `catalogarr-markers` in both modes (§7.3.8). | Taken | review. Not taken: keeping co-ownership through release N, which keeps a dropped markers leaf alive for the whole release. |
| D39 | MediaFile status applies that no grant, answer, intent or first probe caused pass a token bucket, `--remediation-bulk-writes-per-second` 25 (§3.7). | Taken | review; the 2026-09-24 etcd fsync incident on kind-cluster-plex |
| D40 | Every `/data` call from the manager goes through a bounded I/O executor with a breaker (§3.17); item keys never touch `/data`. | Taken | review |
| D41 | A transcode or graft fact that lands after its withdrawal is incorporated: `Dispatch.withdrawn`, `records.FactWindow`, `ErrUnincorporatedFact`, the worker's fact-answer retry, and recovery from the record's inputs (§4.7-§4.10, §5.9). | Taken | review |
| D42 | The transcode block records the graft it carries (`transcode.joinedGraft`); the graft planner derives its joined state from it (§5.13). | Taken | review: the Copy partition (§3.5) |

---

## 10. Risks

1. **One loop is one blast radius.** A fault in the loop's own code stalls every remediation of every
   file. Mitigations: planner isolation with per-planner deadlines and `recover` (§3.6), `RecoverPanic`
   on the controller, `TestAPanickingPlannerKeepsItsBlock`.
2. **Write amplification.** Every status change rewrites the whole MediaFile (about 6.5 KB typical
   after the fold, about 140 B more than today, §2.11.3) and sends it to every MediaFile informer
   (the manager, the ui, cluster-plex, every import-agent replica). The no-op skip removes about 95%
   of today's applies, but release N's first start stores about 27,000 whole-object writes (§3.17),
   about 78 hours of today's stored changes; the status-write limiter spreads them over about 18
   minutes at 25/s, and the runbook watches etcd's WAL fsync latency (§7.5 step 9).
3. **Object size.** The worst case is about 178 KiB of status and 530 KiB of object; a field added
   without a cap breaks the proof. `TestMediaFileStringsAreBounded` and
   `TestMediaFileAtEveryCapFitsTheBudget` guard it; one C1 control character used to block a file's
   every status apply (§2.11.1).
4. **The ledger is new in-memory state.** A missed `Observe` miscounts slots until the 5-minute resync
   corrects it (`clustarr_transcode_ledger_corrections_total`). Two active leaders, or sharding, would
   over-admit across files (ADR-0016's revisit trigger); per-file CAS still prevents a double
   dispatch of one file.
5. **Status-first effects can be owed for a long time.** With NATS down, blocks read in flight with no
   task. Transcode withdraws after 50 minutes; a subtitle language whose publish never landed waits
   for its 26 h timeout; markers wait for 24 h. The backlog gate and pacer keep publishes off a full
   stream in normal operation.
6. **Single-node work streams still discard.** `CLUSTARR_WORK_CAPTIONARR` (about 1.7 MiB,
   discard-oldest) and `CLUSTARR_WORK_SQUASHARR` stay memory streams unless D26 is ruled. Records
   recover a lost task, a subtitle only at its timeout.
7. **Lost workers count toward `MaxAttempts`.** Repeated NATS wipes could exhaust a file's attempts and
   Block it; `transcode.clustarr.io/retry` or new bytes clear it. Not counting would let a file that
   kills its worker retry forever.
8. **The backlog is not in `kubectl get mediafiles`.** Only window files carry a transcode block; the
   backlog is a count in profile status and a metric (D4).
9. **The manager reads NFS.** Each leader start relists every video file's directory once (about 12k
   `ReadDir`s), and every subtitles dispatch lists first. Every call goes through the bounded I/O
   executor (§3.17), so a hung mount stalls file passes that need `/data`, never item keys or
   grants, and pins at most the pool's goroutines.
10. **Item applies stay unconditional,** about 16,500 per manager start, because the loop cannot see
    its own managed fields in a stripped cache.
11. **The migration's rollback is lossless only within `retain`, and only through §7.6's steps.**
    After deletion, rolling back recreates subtitle requests with fresh backoff and loses transcode
    verdicts. Skipping `restore-markers` deletes every file's `status.markers`; skipping the CRD
    revert leaves N's caps able to refuse a previous-release probe (§7.6, §2.16).
12. **Co-ownership traps.** If §7.3.8's claim is skipped for a file, `catalogarr-markers` keeps stale
    markers leaves for good; the report's count and the N+1 gate catch it. The empty release apply is
    the one place a second manager name writes MediaFile status, release N only.
13. **Intermediate commits are not deployable.** With W4.14 dropped, nothing between W4.13 and F8.12
    has probe-pending gates on the old controllers (D1).
14. **Coordination with main.** MP4 phases 2 and 3 and any new fork surface land on main while the fold
    rewrites the same controllers (D28); an ffgo tag collision renames every `.13` (§8.4).
15. **Graft pods gain network reach to NATS** (no Kubernetes credentials), as pool pods already have.
16. **Force search and transcode intents have no ui** in this branch (D18, D19).

---

## Appendix A: contradictions resolved

Between the drafts, and between the drafts and ADR-0016 or the split spec. Each entry names the
disagreement and the resolution taken in this document.

1. **Where the loop lives and what it is called.** Loop draft: new package `app/remediation`,
   controller `remediation`, 16 workers. Integration, records and subtitles: the loop stays in
   `app/catalog/controller/mediafile`, controller `mediafile`, 8 workers. **Resolved:** package
   `app/remediation` (the loop imports four domains' planners, and catalog should not import squash
   and caption), controller name `mediafile` (split §3.11 keeps every controller name), 16 workers
   under `--remediation-concurrency`.
2. **Key types.** Loop draft: five non-file kinds, including `TranscodeProfile`, `SubtitleProfile` and
   `Admission` keys in the loop's queue. Admission, api, integration and subtitles: admission and the
   profile statuses are separate controllers that do not watch MediaFile. **Resolved:** the loop has
   exactly ADR-0016's two key types; `transcode-admission`, slim `transcodeprofile` and slim
   `subtitleprofile` are separate, MediaFile-free controllers woken by channels.
3. **Planner failure reporting.** api: six `<Planner>PlannerError` conditions, MaxItems 12. Loop: one
   `PlannersHealthy` condition plus `status.plannerFailures`. Subtitles: a `SubtitlesPlanned`
   condition and a cap of 16. Integration: "sets its own condition". **Resolved:** api's per-planner
   conditions, present only while failing, cap 12; no `PlannersHealthy`, no `plannerFailures`, no
   `SubtitlesPlanned` (a subtitles block's Blocked reason lives in the block).
4. **Sequence fields.** api: per-file `status.lastSeq` plus a `Dispatch{seq, answeredSeq,
   dispatchedAt}` per block. Records and loop: per-block `seq` and `observedSeq`. Subtitles: per-item
   `seq` and `pendingSeq`. Admission: `status.transcode.seq`. **Resolved:** api's `lastSeq` and
   `Dispatch`, used by transcode, graft and every subtitle item; in flight means
   `seq > answeredSeq`.
5. **The sequence issue rule.** Split §6.5.2: `prev.Seq + 1`. Records: `max(record+1, status+1,
   now.UnixMilli())`. Subtitles: `max(item.Seq+1, rec.Seq+1, now.UnixMilli())`. Admission:
   `max(status.seq, record.Seq) + 1`. **Resolved:** `records.NextSeq(record.Seq, status.lastSeq, now)`
   through `View.Issue`, which also keeps two dispatches in one pass distinct; the probe and markers
   pass 0 for the status floor.
6. **Write order.** Records, subtitles and split §6.5.2: record and publish before status for
   non-admission remediations, status first only for transcode and graft. Loop: status first for
   everything. **Resolved:** one outbox order, status then record then publish, with owed effects and
   record adoption as the safety net; split §6.5.2/§6.5.3 amended.
7. **Records waker.** Split §6.5.3 and integration: replay every record at start, reopen after 5 s.
   Loop: replay the bucket at `LowPriority`. Records: `UpdatesOnly` at first open, `FromRevision` on
   reopen. **Resolved:** records' waker, `pkg/records/recordsource.New`; the informer's initial list
   covers what a replay would.
8. **How `pkg/records` is built.** Records: re-scope W4.2-W4.4 to build `pkg/records` with the probe as
   its first user, wrapping inputs and answer in a generic `Record[I, A]`. Integration: build W4 as
   written, then extract the CAS core in F1.1 with probestore's API and JSON unchanged.
   **Resolved:** integration's order with a flat embedded `schema.RecordHeader`, so the probe record's
   wire format is unchanged and no W4 snippet changes.
9. **Record value cap.** Integration: one `records.MaxValueBytes` = 64 KiB. Records: per-bucket
   `MaxValueSize`, 256 KiB for probes. **Resolved:** per bucket; a probe answer's MediaInfo can reach
   about 153 KB, which a 64 KiB cap would refuse.
10. **Records bucket names.** Admission: `clustarr-transcode-records` (`BucketTranscodeRecords`).
    Records, loop, integration: `clustarr-transcodes`. **Resolved:** `clustarr-transcodes`
    (`BucketTranscodes`).
11. **Record key builders.** Integration: `MarkersKey(uid, source)`, `SubtitleKey`, `TranscodeKey`,
    `GraftKey`, `GraftReduceKey`. Subtitles: `SubtitleRecordKey`. Records: `RecordKey`,
    `RecordSubKey`. **Resolved:** `RecordKey` and `RecordSubKey` only.
12. **Record states.** Admission: `requested`, `claimed`, `finished`, `cancelled`. Subtitles:
    `requested`, `answered`, `cancelled`. Records: `requested`, `withdrawn`, `claimed`, `deferred`,
    `answered`, `failed`. **Resolved:** records' set for the new buckets; the probe keeps
    `requested|probed|failed`.
13. **Segment analysis storage.** Integration: `MarkersKey(uid, "analysis")` in `clustarr-markers` plus
    the existing `clustarr-segments` Put. Records: `segments.Record` v2 by CAS in `clustarr-segments`.
    **Resolved:** records' (one store, fenced by inputs, no Msg-Id amendment hazard).
14. **Markers bookkeeping.** api: a sibling `status.markerDispatch{theIntroDB, analysis}`. Records: no
    markers sequence in status at all. **Resolved:** no `markerDispatch`; TheIntroDB records are fenced
    by inputs and `AnsweredAt` after `fetchedAt`, analysis by inputs; the analysis is not loop-issued.
15. **Graft donor intent.** api: derived on the item (`status.audio.donor`) from the imported donor
    Download. Loop: annotation `remediation.clustarr.io/graft-donor` on the item. Records: annotation
    `catalog.clustarr.io/audio-donor`. Integration: `Movie|Episode.spec.audioGraft` under
    `importarr-worker`. Migration: the item, field unspecified. **Resolved:** api's derivation; an
    annotation or spec field applied by `importarr-worker` would be released by the import list's
    applies under the same manager (the complete-declaration gotcha).
16. **Donor reduce.** api: fold into the graft task. Loop: item path. Records: fileimport at import.
    Admission: a reduce Job counted against graft slots. Integration: item-keyed `GraftReduceKey`.
    **Resolved:** fileimport at import (it links ffgo and already computes language and anchor), so no
    item-keyed task, record or Job exists.
17. **Graft result vehicle.** Admission: the Job's termination message read through the APIReader, a
    stated exception. Records and integration: the Job keeps `NATS_URL` and writes `clustarr-grafts`.
    **Resolved:** records (ADR-0016's "workers report through records").
18. **Graft cardinality and key.** Records: `status.graft[item]` per item, key `RecordSubKey(uid,
    itemUID)`. api and loop: one block per file, multi-item files never grafted. Integration:
    `GraftKey(mfUID)`. **Resolved:** one block per file, `RecordKey(uid)`; multi-item files show
    `failed`/`MultiItemFile` on the item (api), not a file-level `Skipped` (loop).
19. **Graft phases.** api: `Waiting;Queued;Running;Swapping;Succeeded;Failed`. Admission: `Pending`,
    `Running`, `Joined`. Records: `Queued`, `Joined`. Migration: `Running` becomes `Pending`.
    **Resolved:** api's enum; a joined graft is `Running`, reason `JoinedTranscode`, with
    `joinedTranscodeSeq`; adoption rewrites `Running` to `Waiting`; slots count `Queued` and non-joined
    `Running`.
20. **Graft Job label.** Admission: `squasharr.clustarr.io/audiograft` (today's `LabelGraft`).
    Records: `squasharr.clustarr.io/graft-of`. **Resolved:** `squasharr.clustarr.io/graft-of`, owner
    the MediaFile.
21. **Transcode phases.** api: `Pending;Planned;Queued;Running;Swapping;Succeeded;Failed;Skipped`.
    Admission: `Planned`, `Queued`, `Running`, `Succeeded`, `Skipped`, `Failed`, `Cancelled`, empty for
    none. Integration: adds a `Blocked` phase. **Resolved:** api's enum; Cancelled is `Skipped`/
    `Cancelled`, Blocked is `Failed` with `blocked: true`, and a swap awaiting its probe is `Swapping`.
22. **Transcode block fields.** api: `profileHash`, flat `hardware`/`priority`/`suspended`, no
    `pool`. Admission: `sourceProbeHash`, `pool`, `queuedAt`, an `intent{}` struct. **Resolved:** api's
    fields plus `pool` and `probeHash` (one name for "the probe this block describes" in all three
    blocks, replacing `sourceProbeHash` and the graft's `forProbeHash`); `dispatch.dispatchedAt`
    replaces `queuedAt`; standing intents are flat; nonces live in `handledNonces`.
23. **`lastResult`.** api: deleted with its CRD default. Admission and migration: kept and written.
    **Resolved:** never written by the loop; the phase carries the outcome, and a legacy
    phase-less block with a `profileTag` reads as `Succeeded`. The field and its type stay in
    release N's schema without the default, so the previous release's apply is accepted after a
    rollback, and are deleted in N+1 (§2.16, review).
24. **The window.** api: retire `--job-window`, write Planned to every eligible file. Admission, loop and
    integration: keep it. **Resolved:** keep it as a file bound with an in-memory backlog (D4).
25. **Ledger rebuild.** ADR-0016 and integration: a cache index on `status.transcode.phase`. Loop: from
    the cache in a syncing source. Admission: APIReader quorum Lists through the selectable field.
    **Resolved:** admission's, recorded as an ADR refinement (D6).
26. **Lost workers and attempts.** Records: `WorkerLost` after 3 minutes, attempts unchanged. Admission:
    after 180 s check whether the task is still stored; if not, retry counted toward `MaxAttempts`.
    **Resolved:** admission's (a worker-killing file must not retry forever).
27. **Unclaimed dispatch.** Records: withdraw after 30 minutes. Admission: after the 50-minute republish
    window. **Resolved:** 50 minutes, attempts unchanged.
28. **MediaFile finalizer.** Records: a withdrawal finalizer while in flight. Admission, loop,
    integration: none. **Resolved:** none (D11).
29. **Intent annotation names.** Loop: `remediation.clustarr.io/subtitles.search` and
    `remediation.clustarr.io/transcode.*`. Integration: `subtitle.clustarr.io/force-search`. api,
    admission, subtitles, migration: `subtitle.clustarr.io/search` and `transcode.clustarr.io/*`.
    **Resolved:** the latter.
30. **Nonce format and home.** Subtitles: 1-20 decimal digits, `forceSearchHandled` in the block.
    Admission: `intent{cancel, retry}` in the transcode block. Loop: a `nonce` per block. Migration
    writes `fold-<generation>`. **Resolved:** api's `^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$` (which admits
    the migration's value) and top-level `status.handledNonces`, so a backlog file with no transcode
    block can still record a cancel or retry.
31. **ui write access.** Subtitles: a `SearchSubtitles` ui action with `patch` on mediafiles. api: only
    if the ui offers the actions. Migration: no ui change. **Resolved:** no ui change in this branch
    (the ui has no subtitle action today); the action is open decision D19.
32. **Subtitles block shape.** api: `languages[]` with `name` and `forProbeHash`, block only when
    `MayWant`. Subtitles: `items[]` with `path`, `probeHash`, `wanted`, `NotWanted` on every managed
    file, `forceSearchHandled`, `seq`/`pendingSeq`. Integration: adds `satisfied`. **Resolved:**
    subtitles' semantics and `items`, `probeHash`, `wanted`; the item's sidecar field is `name` (as
    `Sidecar.name`, a basename); `Dispatch` replaces `seq`/`pendingSeq`; atomic list; no `satisfied`.
33. **Sidecars.** api: reshape to `name`, atomic, MaxItems 32. Subtitles: keep absolute paths, cap 50.
    **Resolved:** api's reshape, in two steps: release N adds `name` beside the absolute `path`,
    which stays the required map key, with cap 32; N+1 drops `path` and makes the list atomic
    (§2.16, review).
34. **Invalid profiles.** api and integration: an Invalid profile never wins a file. Subtitles: an
    Invalid winner is not skipped; the file is Blocked. **Resolved:** today's `winningProfile`
    (Invalid profiles can win), and an Invalid winner plans nothing (subtitles `ProfileInvalid`,
    transcode not eligible), so a file is never handed to a profile the user did not choose.
35. **SubtitleProfile invalid reason.** api: `BadPattern`. Subtitles: `InvalidFilter`. **Resolved:**
    `InvalidFilter`.
36. **Profile status freshness.** api: periodic 1-minute and 5-minute requeues. Subtitles: 10 minutes.
    Loop: debounced wakes from file passes. **Resolved:** no periodic resync; the ledger's
    `profileWake` and the loop's `subtitleProfileWake` channels plus generation events.
37. **Adoption placement and key.** Subtitles and integration: per-kind adoption copies (F5.5, F6.7,
    F7.4) when the block is absent. Migration: one `legacyfold` package, adoption inside the loop's
    render, keyed by legacy UID in `status.legacyFold.adopted`. **Resolved:** migration's, as F8.1-F8.5.
38. **Retiring `catalogarr-markers`.** api and integration: strip the managedFields entry with a
    resourceVersion-preconditioned metadata patch. Migration: a full `catalogarr` claim, then an empty
    status apply under `catalogarr-markers`, tracked in memory (which would repeat on every restart).
    **Resolved:** migration's SSA mechanism, driven by a one-time pending set the Migrator reads from a
    metadata-only APIReader List, so it runs about 24,000 writes once.
39. **Release shape.** Migration: the split's cutover and fold release N as separate releases with a
    soak. Integration: one release N for both. **Resolved:** integration's (D1); release N's hold stage
    is the soak.
40. **Cutover quiesce.** Split §11.3 and migration: suspend TranscodeJobs, carry the suspend as
    annotations. Integration: `--slots` all zero in release N's first values. **Resolved:** both:
    suspend under the running release to drain, zero slots so no backlog file starts, resume by
    removing the annotations and restoring the slots (D31).
41. **Retired durables.** Migration: the Migrator deletes `squasharr-transcode-results` only at lag 0.
    Integration: `Ensure` deletes through `Topology.Retired`. **Resolved:** `Topology.Retired`, with
    `Ensure` reporting the lag and the Migrator counting an undrained one.
42. **Commit numbering.** Migration: F-N1..F-N11, F-R1..F-R5. Integration: F8.1-F8.6, F9.1-F9.3, with
    adoption in F5.5/F6.7/F7.4. Loop: L1-L5. **Resolved:** integration's F-waves, with migration's
    series as F8.1-F8.11 (gate F8.12) and F9.1-F9.5; the loop's L-steps map to F3-F7.
43. **Fold documents vs Wave 10.** Migration: a docs commit in the fold series. Integration: W10.4 and
    W10.7 describe release N. **Resolved:** the docs land in W10 (written once, after the fold).
44. **The rename step.** Integration: `app/import/renameplan`. Loop: an actuator after the effects,
    `app/remediation/rename`. **Resolved:** the loop's actuator.
45. **The MediaFile index name.** Integration: `catalog.clustarr.io/mediaref`. Loop:
    `remediation.mediafile.item`. **Resolved:** `remediation.mediafile.item`.
46. **Subtitle Msg-Id builder.** Integration: `MsgIDForSubtitle(mfUID, langKey, seq)`. Subtitles and
    records: `MsgIDForSubtitleFetch`. **Resolved:** `MsgIDForSubtitleFetch`; `MsgIDForSubtitle` is
    deleted.
47. **History envelope ids.** Admission: `<mfUID>:<action>:<seq>`. Records:
    `<fileUID>/<remediation>/<seq>/<action>`. Subtitles: `<fileUID>/<langKey>/<action>/<seq>`.
    **Resolved:** `<fileUID>/<remediation>[/<sub>]/<seq>/<action>`.
48. **ADR corrections.** Loop, records and integration ask to amend ADR-0016's wording. The ADR index's
    Lifecycle rule freezes an accepted ADR's body. **Resolved:** refinement bullets under the index
    (§7.9).
49. **ADR-0016 versus the drafts:** "rebuilding would lose the provider, score and subtitle ID" (the
    copy actually protects `attempts`, `nextSearchAt` and `lastError`; the upgrade decision reads
    state, score, scoreOutOf, downloadedAt, path and attempts); "a size test that fills every list to
    its cap" (strings and condition messages must be bounded too); "keyed by file UID and
    remediation" (one bucket per remediation); "fenced by a sequence number the loop issued" (analysis
    and markers by inputs); "workers hold no status RBAC" (true for per-file work only); "falls back to
    labels" (not needed on 1.37); "status.graft replaces AudioGraft" (its item half goes to the item);
    "annotations carrying a nonce" (values for standing preferences); the migration names two kinds
    (the owner ruled all three). Each is recorded as a refinement, not an ADR edit.
50. **Split spec versus the fold:** W2.9 moved `segmenting.Applier` only to delete it (inverted to
    move the planner to `app/catalog/segmentplan`); W4.14 gated controllers the fold deletes
    (dropped); §6.5.3's squasharr retention claim was wrong on the live cluster and is moot; §5.2 kept
    `captionarr-worker` as a writer (retired); §3.4.1 omitted `--graft-concurrency` (added); W0.29
    re-bound `av_new_packet`, which ffgo `.12` already binds as `avcodec.NewPacket` (dropped); the
    rulings' "ebbb2322 is the .12 main published" (the fork commit is `a184557`). Two items an
    earlier draft listed here were already settled by the committed split spec and plan
    (8b3f67bb): the branch's tag reads `.13` throughout, and the split's ADRs are ADR-0018
    (topology) and ADR-0017 (no external media programs), numbered after ADR-0016; nothing is
    renumbered (D35).

---

## Appendix B: Review notes

A five-lens review (invariants, scale, records and NATS, migration, completeness) of this
document on 2026-10-06 raised 42 findings (two critical, 22 important, 18 minor), several of them the same defect seen through two lenses. Each was checked against the tree at `8b3f67bb`, the
committed plan, controller-runtime v0.25.1, nats-server v2.15.0 and the 2026-10-06 dumps. Every
critical and important finding held and is fixed in the body; B.1 indexes where. The minor ones
are recorded in B.2 with what was verified and what binds the implementation; where the fix was
a sentence, it is in the body too.

### B.1 Critical and important findings, and where the body fixes them

| Finding | Fixed in |
|---|---|
| A transcode or graft fact that lands after the closing withdrawal pass was never read, could be overwritten by the next `Request`, and was lost when its `Answer` failed after the purge; §4.8's "a Seq above S cannot coexist with a fact" was false (worker/run.go:404-445 re-asserts the lease, then swaps, then probes the output in `finish`, :606-626) | §2.3 (`Dispatch.withdrawn`), §4.2 item 5, §4.7 (`ErrUnincorporatedFact`), §4.8 (table and the corrected paragraph), §4.10, §4.12 (worker), §5.9, §5.10, §5.15, D41 |
| The transcode planner wrote the graft block, which the Copy partition discards | §2.5 (`joinedGraft`), §3.5, §5.8 step 4-5, §5.13, §4.12 graft rows, D42 |
| Subtitle items closed a dispatch as lost instead of re-sending the owed record, and a forced search racing an answer lost both | §4.7 (owed effects), §6.6 |
| A pass planned from a stale cache that changed nothing could swallow the previous pass's timer (priorityqueue.go:255-258) and leave a Queued file claimed with no wake | §3.4 steps 2, 7, 8; §3.7 (applied-status memo) |
| Rollback to the previous release was not lossless: its status apply is refused under N's schema (`transcode.phase` required, `sidecars[].path` gone), and §7.3.8 leaves `catalogarr` sole owner of markers the previous release never declares | §2.1 items 3, 5, 7; §2.5; §2.7; §2.15; §2.16; §7.3.8; §7.3.11 (`restore-markers`); §7.6; D37, D38 |
| The grafted-original spec takeover was keyed on the `status.graft` block, which can go | §3.10 |
| The records pacer's wait was not a reservation, so a backlog either re-passed every 30 s or starved | §4.7, §6.4.3 |
| Nothing paced MediaFile status writes; §3.17 compared stored writes with no-op applies | §3.7 (limiter), §3.17, §7.5 step 9, risk 2, D39 |
| `subtitles.profileGeneration`, read by nothing, made one SubtitleProfile edit rewrite every MediaFile | §2.4, §2.11.3, §6.4.7, §7.3.3, §7.12 |
| Blocking `/data` calls ran on the shared queue's workers with no bound | §3.4 step 3, §3.6, §3.17, risk 9, D40 |
| The pool reconciler could size pools from the empty ledger before the rebuild and suspend every running pool (pool/next.go:68-71) | §5.12, §5.15 |
| `Withdraw` had two incompatible definitions, and the dispatch-once proof assumed a worker never runs on an absent record while §4.8 let it | §4.7, §4.8, §5.8, §5.10, §6.6 |
| The ledger's `(seq, rank)` merge could never record a cleared block, and resync could not repair it | §5.5, §5.6 |
| §7.5 omitted the split runbook's steps (keep the index PVC, stop every old writer) and §7.6's rollback referred back to the text F8.11 replaces | §7.5, §7.6 |
| The first loop render could change `status.markers` or `status.mediaInfo` and so cluster-plex's `SeedKey` (v1 segment records, untagged legacy segments, re-clamped strings) | §2.11.1 rule 5, §4.12, §7.3.11, §7.5 step 1, §7.10 |
| No test ran the transition from the previous release | §7.3.12, F8.4, F8.9, F8.12 |
| The ADR renumbering contradicted the committed split spec and plan | §7.9, §8.3, D35, Appendix A item 50 |
| §8 was checked against an earlier, untracked plan; its line anchors and Wave 0 rows were stale | §8.1, §8.3, §8.4 |
| F2 removed fields that code living until F3-F8 still uses | §2.15, §2.16, F2.1, F2.4, F9.2 |
| User labels and the pass's own mirrored labels never re-selected a profile | §3.3 (S1, self-trigger rule), §3.5 (`View.Labels`) |
| New metric families were outside the catalogue `TestCatalogueMatchesTheAmendmentTable` enforces, and W4.5's rename had no task | §3.19, §7.9, §8.3 W4.5, F1.1, F3.1, F6.3, F8.3, F9.1 |

### B.2 Minor findings

1. **The ledger could keep counting a cleared block** (invariants, minor). Holds; the same defect
   as the records lens's important finding. Fixed in §5.5-§5.6 (`FileView.Seq` = `lastSeq`,
   resync by replacement).
2. **Label changes never wake the file key** (invariants, minor). Holds; the same as the
   completeness lens's important finding. Fixed in §3.3 and §3.5.
3. **A Migrator restart reapplies legacy intent over a user's later edit** (invariants). Holds:
   §7.3.5 repeated preparation after a restart, and adopted objects live for 72 h. Binding:
   `NeedsPreparation` excludes UIDs already in `status.legacyFold.adopted`, and the annotation
   patch is preconditioned on the read's resourceVersion and writes only absent keys (§7.3.5).
4. **Effect panics were not isolated** (invariants). Holds: §3.6 isolated only Gather and Plan.
   Binding: each planner's effect batch runs under `isolate`, a failure is attributed to its
   planner's condition at the next pass, and the others' effects and the actuators run (§3.6).
5. **A `standard.Version` raise did not cost at most `window × profiles` writes** (scale). Holds:
   every stale verdict block would have been rewritten. Binding: a stale verdict block is left
   untouched until the file re-enters the window (§2.1 item 2, §5.11).
6. **A quiet bucket replays every key on reopen** (scale). Holds in part: the `lastRev == 0`
   reopen did replay, but natsbus closes a watch only when nats.go's `Updates` closes, not on a
   plain reconnect (natsbus/kv.go:218-240). Binding: `lastRev` is seeded from the stream at first
   open (§4.9).
7. **A Conflict requeued in 1 s with no backoff** (scale). Holds. Binding: 1 s doubling to 30 s
   per key, reset by a clean apply (§3.7, §3.11).
8. **"About 44 B on average" understated the typical growth** (scale). Holds: the dump's 11,958
   files are all probed movie or episode files, and a NotWanted block costs about 140 B with its
   managedFields entry (259 B as drafted). Fixed in §2.11.3; `profileGeneration` is dropped and
   `probeHash` omitted on an item-less block.
9. **The waker misses a recreated bucket, and the first open's timing was unstated** (records).
   Holds: a `WatchFromRevision` past a new stream's last sequence waits. Binding: the first open is
   synchronous with a 60 s bounded retry; a recreated stream is replayed and the files with an
   outstanding request are enqueued from the cache (§4.9).
10. **Republishing under the same Msg-Id cannot recover an evicted or purged task, and
    `ErrQueueFull` cannot occur on the caption stream** (records). Holds: nats-server v2.15.0's
    `purge` (stream.go:3122) leaves the dedupe map alone and entries expire only on the window
    timer (:5562-5571); `CLUSTARR_WORK_CAPTIONARR` is `DiscardOld` (topology.go:541-563), while
    `CLUSTARR_WORK_SQUASHARR` is `DiscardNew` (:609-618). Binding: on the `DiscardOld` streams an
    owed republish checks `Admin.Subjects` and republishes under a fresh suffix (§6.4.6).
11. **No `failed` row for transcode or graft** (records). Holds: §4.8 writes `StateFailed` on the
    last delivery (the pool consumer's `MaxDeliver` is 16, topology.go:144-153), and §5.9 had no
    row, so the cause was lost behind `WorkerLost`. Fixed in §5.9 and the graft rows of §4.12.
12. **The forced-search carry rule dropped a force not yet run** (migration). Holds:
    subtitlerequest/reconciler.go sets `observedGeneration` (:537) and applies (:566) before an
    `ErrQueueFull` return (:570-576) skips `resetForceSearch` (:578-582, :704-707). Fixed in
    §7.3.3 and §7.3.5: any `forceSearch: true` is carried.
13. **`TranscodeJob.spec.priority` is the profile's copy, not intent** (migration). Holds
    (transcodeprofile/controller.go:552-558, never re-applied, :231-233). Fixed in §7.3.5: not
    carried, and not counted as `intent_dropped`, so the runbook's stop rule does not fire on the
    50 every live job carries.
14. **The legacy cancel marker must keep the v1 lease shape** (migration). Holds:
    `task.Lease{Job, Attempt}` with schema `transcode.Lease.v1` (task.go:163-173), honoured only when
    `cur.Attempt >= t.Attempt` (worker/lease.go:79), while §4.12 makes `task.Lease` v2. Binding:
    `legacyfold.leaseV1` with a golden test, deleted in F9.1 (§7.3.5).
15. **v1 `MarkersTask`s were missing from the leftover NATS state** (migration). Holds: the
    handler dead-letters what it cannot decode (markers/handler.go:103-105) and nothing resolves a
    MarkersTask dead letter (history/target.go:149-174). Fixed in §7.3.9 (acked as stale), §7.5
    step 6 and §7.6 step 5.
16. **W3.12's register test, W5.7's `--job-retention` row and the "W6.x" placeholder had no
    disposition** (completeness). Holds (plan:15029, 36305, 35905; the closed schema is W6.13,
    plan:45433). Fixed in §8.3 and F3.1, F6.6, F7.3.
17. **The item page's Subtitles table had no task** (completeness). Holds: no task touched
    ui/detail.go or ui/views/detail.templ, and detail.go:246-247 reads `sc.Path`. Fixed in F8.7;
    the `sc.Path` reader moves to `name` in F9.2 (§2.15).
18. **D3 departs from the owner's literal loop scope without saying so** (completeness). Holds:
    the ruling lists the TranscodeProfile and SubtitleProfile controllers among those that merge.
    §1 item 2 now states the reading, the watch guard admits exactly the two channel wakes, and D3
    is open for the owner's confirmation.
