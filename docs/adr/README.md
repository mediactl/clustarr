# Architecture decision records

Each ADR records one decision the design of record
(`docs/superpowers/specs/2026-09-18-clustarr-design.md`, §19) rests on: the
context, the options weighed, the decision and its consequences. The spec says
*what* Clustarr does; an ADR says *why this and not the alternative*, so a
later change can tell whether its premise still holds.

## Index

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
| [0010](0010-release-index-engine-selectable.md) | The release index engine is selectable: SQLite FTS5 by default, Postgres FTS via CloudNativePG for multi-replica indexarr (supersedes 0003) | Accepted, 2026-09-24 |
| [0011](0011-artwork-in-jetstream-object-store.md) | Artwork lives in a JetStream object store, one bucket, two writers split by variant | Accepted, 2026-09-24 |
| [0012](0012-plex-provider-on-the-ui-service.md) | The Plex Metadata Provider is served read-only by the ui service, unauthenticated per protocol, and must not be publicly exposed | Accepted, 2026-09-24 |
| [0013](0013-per-service-controllers-until-production.md) | Controllers stay one Deployment per service until production ready, then unify into one manager (design recorded, deferred) | Accepted, 2026-09-24 |
| [0014](0014-download-working-areas-on-the-shared-volume.md) | Download working areas live on the shared data volume (`scratch.path`, `publishDir`), not on node-local scratch; the single shared volume stays the pattern for both engines | Accepted, 2026-09-24 |
| [0015](0015-no-cuda-image.md) | There is no CUDA image: nvidia pools run the transcoder image, the NVIDIA container runtime injecting the driver's libraries (NVENC, NVDEC, CUDA) from the host | Accepted, 2026-10-01 |
| [0016](0016-per-file-work-is-mediafile-status.md) | Per-file work (subtitles, transcode, graft) is MediaFile status written by one remediation loop; SubtitleRequest, TranscodeJob and AudioGraft go, Download stays (supersedes 0004) | Accepted, 2026-10-06 |
| [0019](0019-agents-never-write-kubernetes.md) | The manager is the control plane: every lifecycle is a state machine in its reconcile loop, it routes and admits every task, and it is the only writer of every CR; agents execute tasks, report over NATS and never write the Kubernetes API; the Download kind is removed, a grab is an entry in its owner's status and converges with its transfer, and the blocklist lives in the release index (reverses 0016's "Download stays") | Accepted, 2026-10-07 |
| [0020](0020-nats-client-resilience.md) | NATS client resilience: graceful drain (`Bus.Drain`, responders drained, `busconn.Shutdown` then `nc.Drain`), explicit pending limits on every core subscription, slow consumers shed and alerted (not readiness), request errors classified, object store chunk size per store and orphan purge over every store, reconnect backoff with jitter; implemented as Wave N after the current plan | Accepted, 2026-10-07 |

Refinements that did not change a decision are recorded in the spec, not here:

- **0004:** the one sanctioned exception is `MediaFile`, split spec versus
  status between importarr and catalogarr (amendment §A1.3 erratum), and the
  one cross-service status field is `Download.status.import`, owned by
  importarr.
- **0008:** the lease bucket has no TTL. The grab path reclaims a lease whose
  holder Download is terminal, being deleted, or missing for ten minutes, and
  re-enters one it holds itself (gap fix X4a, spec §5 and §8.2).
- **0008:** under ADR-0019 the delay profile and the keep-best pending grab are the owning
  item's state machine in the manager (`status.pendingGrab`); the KV grab lease and
  `clustarr-pending` retire, because one reconcile per owner serialises grabs (2026-10-07).
- **0009:** under ADR-0016 the remediation loop admits transcodes from
  `MediaFile.status.transcode` rather than from TranscodeJobs, and is the one
  writer squasharr was; the pools, the JetStream queue and the task lease are
  unchanged.
- **0011:** 2026-10-07: no object links; objects carry versioned metadata; the
  ui indexes the bucket by watch; the reaper audits and purges orphan chunks
  (`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md` §B,
  amended 2026-10-07).
- **0016:** its "These stay resources: Download" is reversed by ADR-0019 (2026-10-07); the
  rest of 0016 stands. A grab now lives in its owning item's status, not on the MediaFile,
  for the reasons 0016 gave for not folding Download into per-file work.

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
