# ADR-0016: Per-file work is MediaFile status, written by one remediation loop

**Status:** Accepted, 2026-10-06

## Context

ADR-0004 made every unit a human would want to see, cancel or retry its own custom resource,
with exactly one controller writing its status. For per-file work that gave `SubtitleRequest`
and `TranscodeJob`, and on 2026-10-06 `AudioGraft`. It rejected per-file state in
`MediaFile.status` written by several services, because no reviewer could state the conflict
semantics of several server-side-apply managers on one object. Its revisit trigger planned one
exception: past about 50,000 media objects, fold `SubtitleRequest` into
`MediaFile.status.subtitles` under a `captionarr` field manager.

That trigger has not fired. kind-cluster-plex held about 31,500 media objects on 2026-10-06:
11,958 MediaFiles (about 4 KB each), 15,553 Episodes, 837 Movies, 2,445 SubtitleRequests
(1.6 KB), 626 TranscodeJobs (1.8 KB, at most 5.3 KB) and 122 Downloads. Three other premises
have changed:

- **Every reconciler moves into one process.** The owner adopted the unified topology ADR-0013
  deferred (`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`): `cmd/manager`
  runs every reconciler on one cache under one lease.
- **The owner plans to merge the reconcilers that act on a file into one loop.** Thirteen
  controllers watch MediaFile today: catalogarr's MediaFile, Movie, Episode, Album, Book,
  Audiobook and Issue reconcilers; importarr's rename controller; squasharr's TranscodeProfile,
  TranscodeJob and AudioGraft controllers; and captionarr's SubtitleProfile and SubtitleRequest
  controllers. One loop in one process is one writer: ADR-0004's rule, kept without a resource
  per work item.
- **A worker no longer has to write status to report a result.** The split design's probe
  protocol (§5.4, §6.5) has the probe worker write only a compare-and-swap record in the Durable
  KV bucket `clustarr-probes`, keyed by MediaFile UID. A raw source on the MediaFile controller
  only enqueues, and the reconciler's one apply is the only status write.

Keeping a resource per work item now costs more than it buys:

- A `SubtitleRequest` is one file's state in a second object. The SubtitleProfile controller
  names it after its MediaFile and makes the MediaFile its owner, and catalogarr copies its
  results back onto the file (`sidecarsFromSubtitleRequest`).
- catalogarr summarises `TranscodeJob` into `MediaFile.status.transcode`, and squasharr keeps a
  Succeeded job for 24 hours so catalogarr can read its result before the swap is incorporated.
- Per-file state has five writers: `catalogarr` and `catalogarr-markers` on MediaFile,
  `captionarr` and `captionarr-worker` split per leaf on SubtitleRequest items, and squasharr's
  reconciler and results consumer on TranscodeJob, kept apart only by compare-and-swap.

## Decision

**Per-file work is a block of MediaFile status.** `status.subtitles` replaces SubtitleRequest,
`status.transcode` grows to carry what TranscodeJob carried, and `status.graft` replaces
AudioGraft (which names an item but rewrites that item's file), beside the probe, naming and
markers already there. The three kinds and their controllers are removed.

**One remediation loop in the manager is the only writer of MediaFile status.** It is keyed by
MediaFile, writes under one field manager, and makes every status write through
`k8s.PatchStatusCAS`. importarr keeps creating MediaFiles and owning their spec; the loop keeps
the spec fields catalogarr takes over after a transcode swap. No worker and no other reconciler
writes MediaFile status, so `catalogarr-markers` goes as well. ADR-0004's
one-writer-per-resource rule stands, and covers more: the state that lived in three other kinds
now has the same single writer as the rest of the file.

**Workers report through records, never through status.** Every remediation follows the probe
protocol. The worker writes a compare-and-swap record in a Durable KV bucket keyed by file UID
and remediation, fenced by a sequence number the loop issued with the task. A raw source
enqueues the file, and the loop incorporates the record in its one apply. Transcode progress
lives only in `clustarr-progress`; nothing writes progress to etcd.

**Admission is the leader's in-memory state, not objects.** The per-profile window, the
per-class slots and the GPU-only wait are decisions across files. The leader keeps them in a
ledger rebuilt at start from a cache index on `status.transcode.phase`, and dispatches each task
once by JetStream Msg-Id. Status is authoritative over the ledger: after a crash the ledger is
rebuilt, never trusted.

**User intent is annotations on the MediaFile.** Forcing a subtitle search, and cancelling,
retrying, prioritising, suspending or choosing the hardware of a transcode, are annotations
carrying a nonce; status records the last nonce handled. The loop never writes the annotation.

**The loop renders every block in one apply.** Each remediation is a planner: it takes a
read-only view of the file and returns its block and the tasks to publish. A planner that fails
or panics leaves its last block in place and sets its own condition, and the others proceed.
The loop never returns early with a partial status. Time-based work (a subtitle's next search,
markers falling due) is `RequeueAfter`, not a periodic resync. An item's rollup is a second key
type in the same queue: a multi-episode file belongs to several Episodes, and a loop keyed by
item would put two reconciles on one file.

**`kubectl get mediafiles` is the queue view.** Each block's phase is a print column. Filtering
on a status path needs a CRD selectable field there, and every selectable field in `api/` is on
spec today, so the design proves the apiserver accepts one on status before relying on it, and
otherwise falls back to labels the loop maintains.

**These stay resources:** Download, because it targets an item rather than a file, exists before
any file, may become many files or none, keeps seeding after import, and gates data removal on
its engine's finalizer; and Search, Episode, Issue and the configuration kinds. Sub-minute tasks
stay messages.

This ADR supersedes ADR-0004. It refines ADR-0009 without superseding it: pools,
the JetStream queue and the task lease stand, but the loop admits work from MediaFile status
rather than from TranscodeJobs, and is the one writer squasharr was.

## Alternatives considered

**Keep the kinds, with one reconciler per kind in the one manager.** The manager design as
written. One process, but the mirrors, the 24-hour retention, the two write paths and three
objects for one file's state all remain.

**Fold, with each service writing its own block under its own field manager.** ADR-0004's
rejected alternative, and the shape its revisit trigger planned for SubtitleRequest. It needs
per-leaf ownership declarations, a predicate on each of the thirteen watchers so one service's
writes do not wake the rest, and `managedFields` tests for every manager, because `PatchStatus`
forces ownership and a double claim is silent. One loop makes all of that unnecessary.

**Fold SubtitleRequest only, and keep TranscodeJob.** Smaller, and needs no admission ledger.
Rejected as the end state, because TranscodeJob carries the most mirrored state: the swap, the
retention and the two write paths. The design may still land subtitles first.

**Put the state on the item.** Movie and Episode status instead of MediaFile. Remediations act
on a file, and a multi-episode file would have two owners.

**Fold Download.** It is not per-file work; see the Decision.

**Keep results only in KV, with nothing in status.** Cheapest for etcd, and the opacity
ADR-0004 rejected: `kubectl` is the interface.

## Consequences

**Gains:**

- One writer and compare-and-swap for all per-file state. The per-file write paths of
  `captionarr`, `captionarr-worker`, `catalogarr-markers` and squasharr's results consumer
  disappear, and workers hold no status RBAC.
- The sidecar and transcode mirrors, the 24-hour retention of Succeeded jobs, job names keyed by
  profile hash, and the SubtitleRequest create-and-collect path are deleted.
- About 3,000 fewer objects at today's size, which matters less than the above.

**Costs:**

- Every remediation write rewrites the whole MediaFile, about 4 KB today and about 6 KB with the
  new blocks, and sends it to every MediaFile informer. A size test that fills every list to its
  cap guards the budget.
- One loop is one blast radius. Planner isolation is what keeps a fault in one remediation from
  stalling the rest.
- `kubectl get transcodejobs` and `kubectl get subtitlerequests` go away. Per-attempt history
  moves to Events on the MediaFile and to the history sink.
- The admission ledger is new state held in one process, and must rebuild correctly after a
  crash.
- Migration needs a release that copies each SubtitleRequest's and TranscodeJob's status onto
  its MediaFile and then deletes the object, before the CRDs are removed. Rebuilding instead
  would lose the provider, score and subtitle ID the upgrade decision reads.
- The spec, its amendment, CLAUDE.md and the ui's projection describe the three kinds as built,
  and are rewritten as each kind is removed.

## Revisit triggers

- The manager runs more than one active leader, or shards its reconcilers: the admission ledger
  assumes one.
- MediaFile status writes become the apiserver's or etcd's bottleneck, or the object outgrows
  its size budget.
- A remediation appears that is not per file (per season, per series). It gets its own resource.
- The apiserver refuses selectable fields on status, and maintaining labels proves too costly.
