# Architecture decision records

Each ADR records one decision the design of record
(`docs/superpowers/specs/2026-09-18-clustarr-design.md`, §19) rests on: the
context, the options weighed, the decision and its consequences. The spec says
*what* Clustarr does; an ADR says *why this and not the alternative*, so a
later change can tell whether its premise still holds.

## Index

Wave U (2026-10-07) renamed the internal *arr names to domain names, cold turkey; ADRs keep the names they were written with -- read them through docs/superpowers/specs/2026-10-07-unbranded-names-design.md §6.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-nats-jetstream-for-events-and-work-queues.md) | NATS JetStream is the event bus and the distributed work queue | Accepted, 2026-09-18 |
| [0002](0002-gpl-3-0-licence.md) | The project is licensed GPL-3.0 | Accepted, 2026-09-18 |
| [0003](0003-release-index-sqlite-fts5.md) | The release index is SQLite FTS5 on an RWO volume, behind a Store interface | Superseded by ADR-0010, 2026-09-24 |
| [0004](0004-one-cr-per-human-visible-unit.md) | One custom resource per human-visible unit, one controller-writer per resource | Superseded by ADR-0016, 2026-10-06 |
| [0005](0005-transcodes-as-batch-jobs.md) | Transcodes run as batch/v1 Jobs gated by suspend and slot budgets | Superseded by ADR-0009, 2026-09-24 |
| [0006](0006-single-rwx-data-volume.md) | Storage is one RWX volume at `/data`, TRaSH layout, hardlink-else-copy | Accepted, 2026-09-18 |
| [0007](0007-single-replica-metadata-gateway.md) | The metadata gateway is a single replica that owns all outbound provider clients | Accepted, 2026-09-18 |
| [0008](0008-delay-profile-grab-semantics.md) | Grab semantics are Radarr-shaped delay profiles on scheduled messages, with a keep-best pending record and a grab lease | Accepted, 2026-09-18 |
| [0009](0009-transcode-worker-pools-over-jetstream.md) | Transcodes run on per-profile-per-hardware-class worker-pool Jobs fed by a JetStream work queue (supersedes 0005) | Accepted, 2026-09-24 |
| [0010](0010-release-index-engine-selectable.md) | The release index engine is selectable: SQLite FTS5 by default, Postgres FTS via CloudNativePG for a multi-replica index domain (supersedes 0003) | Accepted, 2026-09-24 |
| [0011](0011-artwork-in-jetstream-object-store.md) | Artwork lives in a JetStream object store, one bucket, two writers split by variant | Accepted, 2026-09-24 |
| [0012](0012-plex-provider-on-the-ui-service.md) | The Plex Metadata Provider is served read-only by the ui service, unauthenticated per protocol, and must not be publicly exposed | Accepted, 2026-09-24 |
| [0013](0013-per-service-controllers-until-production.md) | Controllers stay one Deployment per service until production ready, then unify into one manager (design recorded, deferred) | Superseded by ADR-0018, 2026-10-06 |
| [0014](0014-download-working-areas-on-the-shared-volume.md) | Download working areas live on the shared data volume (`scratch.path`, `publishDir`), not on node-local scratch; the single shared volume stays the pattern for both engines | Accepted, 2026-09-24; amended 2026-10-15 |
| [0015](0015-no-cuda-image.md) | There is no CUDA image: nvidia pools run the transcoder image, the NVIDIA container runtime injecting the driver's libraries (NVENC, NVDEC, CUDA) from the host | Accepted, 2026-10-01 |
| [0016](0016-per-file-work-is-mediafile-status.md) | Per-file work (subtitles, transcode, graft) is MediaFile status written by one remediation loop; SubtitleRequest, TranscodeJob and AudioGraft go, Download stays (supersedes 0004) | Accepted, 2026-10-06 |
| [0017](0017-no-external-media-programs.md) | No image ships or runs ffmpeg, ffprobe or par2: probing, decoding, subtitle extraction and par2 run as libraries; par2 in a re-exec'd child of the agent (the par2 half waits on a par2go release) | Accepted, 2026-10-06 |
| [0018](0018-manager-agents-and-ui.md) | One manager runs every reconciler; workers run as one agent per domain (cmd/agent), with the ui, markers and transcode as their own binaries; agents autoscale without KEDA (supersedes 0013) | Accepted, 2026-10-06 |
| [0019](0019-agents-never-write-kubernetes.md) | The manager is the control plane: every lifecycle is a state machine in its reconcile loop, it routes and admits every task, and it is the only writer of every CR; agents execute tasks, report over NATS and never write the Kubernetes API; the Download kind is removed, a grab is an entry in its owner's status and converges with its transfer, and the blocklist lives in the release index (reverses 0016's "Download stays") | Accepted, 2026-10-07 |
| [0020](0020-nats-client-resilience.md) | NATS client resilience: graceful drain (`Bus.Drain`, responders drained, `busconn.Shutdown` then `nc.Drain`), explicit pending limits on every core subscription, slow consumers shed and alerted (not readiness), request errors classified, object store chunk size per store and orphan purge over every store, reconnect backoff with jitter; implemented in Wave N (2026-10-07) | Accepted, 2026-10-07 |
| [0021](0021-object-stores-self-describing-no-links.md) | Object stores hold self-describing objects under deterministic names, never links: one metadata builder per store, the complete map on every `Put` and `SetMeta`, readers decide from metadata and `Get` only for bytes; fingerprints gain metadata (built in Wave N, 2026-10-07) | Accepted, 2026-10-07 |

Refinements that did not change a decision are recorded in the spec, not here:

- **0004:** the one sanctioned exception is `MediaFile`, split spec versus
  status between import and catalog (amendment §A1.3 erratum), and the
  one cross-service status field is `Download.status.import`, owned by
  import.
  (As built on the unify branch, 2026-10-07: both MediaFile writers are in the manager, and
  `Download.status.import` went with the Download kind, ADR-0019.)
- **0008:** the lease bucket has no TTL. The grab path reclaims a lease whose
  holder Download is terminal, being deleted, or missing for ten minutes, and
  re-enters one it holds itself (gap fix X4a, spec §5 and §8.2).
- **0008:** under ADR-0019 the delay profile and the keep-best pending grab are the owning
  item's state machine in the manager (`status.pendingGrab`); the KV grab lease and
  `clustarr-pending` retire, because one reconcile per owner serialises grabs (2026-10-07).
- **0005:** TranscodeJob is gone (ADR-0016); ADR-0009's worker pools remain.
- **0009:** under ADR-0016 the remediation loop admits transcodes from
  `MediaFile.status.transcode` rather than from TranscodeJobs, and is the one
  writer the transcode controller was; the pools, the JetStream queue and the task lease are
  unchanged.
- **0009:** pool Jobs run `/usr/bin/transcode` from the `native` image, component
  `transcode` (2026-10-06, ADR-0018).
- **0010:** the index agent stays at one replica with `Recreate` under Postgres too, until
  indexers share a KV limiter (2026-10-06, ADR-0018).
- **0011:** 2026-10-07: no object links; objects carry versioned metadata; the
  ui indexes the bucket by watch; the reaper audits and purges orphan chunks
  (`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md` §B,
  amended 2026-10-07).
- **0015:** the transcoder image is now the `native` image, which also carries the agent and
  markers (2026-10-06, ADR-0018).
- **0016:** as built (2026-10-07): each remediation's records live in one Durable KV bucket;
  segment analysis and markers are fenced by their inputs, not a loop-issued sequence; the
  admission ledger rebuilds through the APIReader with a status selectable field, which the
  apiserver accepts on 1.37, so no label fallback is built; user intent is nonce annotations for
  one-shot actions and value annotations for standing preferences; planner failures are
  per-planner conditions present only while failing, with conditions MaxItems 12.
- **0016:** its "These stay resources: Download" is reversed by ADR-0019 (2026-10-07); the
  rest of 0016 stands. A grab now lives in its owning item's status, not on the MediaFile,
  for the reasons 0016 gave for not folding Download into per-file work.
- **0016:** 2026-10-07, the owner: cold turkey, no migration. The three folded kinds are
  deleted right after the waves that replace them, with no adopting release; the live
  cluster's objects are removed by hand (`docs/runbooks/unify-cold-cutover.md`) and their
  history is not kept. The Consequences bullet about a copying release no longer holds.
- **0019:** 2026-10-07, the owner: cold turkey, no migration. Download is deleted once nothing
  creates or reads it (after waves A4-A6), with no adopting release, export or rollback; the
  live cluster's Downloads and their blocklist labels are an accepted loss
  (`docs/runbooks/unify-cold-cutover.md`).

## Writing one

- Number it with the next free four-digit number and a kebab-case title:
  `0009-short-decision-title.md`. Numbers are never reused, even for a
  rejected ADR.
- Use the sections every existing ADR uses: a **Status** line, then
  **Context**, **Decision**, **Alternatives considered**, **Consequences** and
  **Revisit triggers**.
- Add a row to the index above in the same commit, and a one-line summary to
  the spec's §19 if the decision is architectural.
- Carry the GPL-3.0 posture of ADR-0002 into anything that ports upstream
  code: name the source and the commit it was taken from.

## Lifecycle

An ADR's **Status** line is the only part that changes after it is accepted.
The body is a record of what was known at the time; do not rewrite it to match
later events.

| Status | Meaning |
|---|---|
| Proposed | Written and under review. The code does not rely on it yet. |
| Accepted, *date* | The decision in force. |
| Rejected, *date* | Considered and declined; kept so the question is not reopened blind. |
| Deprecated, *date* | No longer applies and nothing replaces it (the feature went away). |
| Superseded by ADR-NNNN, *date* | A later ADR reverses or replaces the decision. |

To supersede an ADR:

1. Write the new ADR. Its Context names the ADR it replaces and says what
   changed: a premise that no longer holds, or a revisit trigger that fired.
2. In the same commit, change the old ADR's Status line to
   `Superseded by ADR-NNNN, <date>`, leaving its body untouched, and update
   both rows of the index.
3. Update the spec's §19 summary and every passage that cited the old
   decision, so the spec and the ADRs never disagree.

A partial change, one that keeps the decision but alters how it is carried
out, is not a supersession: record it in the spec and, if it is worth
knowing when reading the ADR, as a one-line refinement under the index.
