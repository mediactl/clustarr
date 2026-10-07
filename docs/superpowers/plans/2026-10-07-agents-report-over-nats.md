# Agents report over NATS; the manager is the control plane; the Download kind is removed (ADR-0019) -- Implementation Plan, waves A1-A9

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or
> superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. **Execution mode: implement
> only.** The owner's instruction for this branch is "implement everything, then add all tests in
> one batch": no task here writes a test. Each task lists the tests it needs under **Tests
> (deferred to the batch)**, by Go test name with a one-line assertion; the batch writes them later.
> A task never weakens or deletes an existing test or guard; where its change breaks one, the
> task names it in the deferred list ("rewrite") and leaves the file as it is, compiling or not,
> as Waves 4f-F4 did (the ledger lists the packages whose tests no longer compile). A test file
> whose subject the task deletes goes with it, and the commit message names the deferred tests
> that carry its intent.

**Goal:** No code linked into `cmd/agent`, `cmd/markers` or `cmd/transcode` writes the Kubernetes
API, and no agent makes a choice that changes desired state. Every lifecycle (a grab and its
transfer, an import, a search, a metadata refresh, a library scan, a list sync, an indexer's
health, a DownloadClient's engines) is a state machine in `cmd/manager`'s reconcile loop,
persisted in status; agents execute tasks the manager routes and admits, and report through
compare-and-swap records and intake messages. The `Download` kind becomes an entry in its owning
item's `status.downloads[]` (release N adopts every live Download; release N+1 removes the CRD).

**Architecture:** The remediation loop's item keys (Movie, Episode, Album, Book, Audiobook, Issue
from F4, plus Series and Comic from A3) gain *item stages* (search, grab, downloads, metadata,
artwork, overlay) that read the cache, records and the intake inbox, decide through pure planners
(`app/catalog/grabplan`, `app/catalog/searchplan`, `app/grab/lifecycle`, `app/import/importplan`),
and land one compare-and-swap apply per field manager per pass (`catalogarr` through each kind's
own renderer, then `catalogarr-grab`, `catalogarr-metadata`, `catalogarr-artwork`), followed by
the effects (task publishes admitted by `app/dispatch`, engine commands, the release-index
blocklist RPC, MediaFile materialisation, payload removal). Agents bind durables the manager
created, check a task is current, do the I/O and computation, and answer in one of six new
Durable records buckets or on the Durable `CLUSTARR_INTAKE` stream; the manager's leader-only
consumers (`app/intake`, `app/intake/advisory`, the moved DLQ projector and history sink) turn
answers, candidates, observations and JetStream nak/term advisories into wake-ups and status.
Agent roles become read-only, the agent client is wrapped read-only at runtime, and guards hold
both.

**Spec:** `docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md` ("spec", cited
`§n`), amended with the owner's answers at `69311068`, status Accepted. Read §1-§2 and §4-§9 in
full before any wave; each task cites the sections it implements. **ADR:**
`docs/adr/0019-agents-never-write-kubernetes.md` (Accepted). **Also binding:** the loop spec
(`docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md`, "loop §n"), the split
spec (`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`, "split §n"), the split
plan's **Global Constraints** (`docs/superpowers/plans/2026-10-06-manager-agent-split.md`), the
rulings in `.superpowers/unify/rulings.md`, and CLAUDE.md's invariants and gotchas.

**Basis.** Branch `unify-manager-agent` in `/home/appkins/src/mediactl/clustarr-unify` at
`1598a803` (F4.3 committed; F4.4, the F4 gate, and W6.2-W6.8 in progress in other sessions).
Every path, name and line below was read on that tree; where the spec and the tree disagree, the
tree wins and the disagreement is a ruling in **Rulings**.

---

## Global Constraints

**Inherited, verbatim in force:** the split plan's Global Constraints (branch hygiene R14;
path-scoped commits, `git add` of every new file first, `git rm` for deletions, never
`git add -A`, never `git stash`, never `git reset --hard`, never `git push`; never `go get` or
`go mod tidy` -- no task here adds a module, and one that seems to need one stops and reports;
GPL header from `hack/boilerplate.go.txt` on every new `.go` file, then a blank line, then
`package` -- snippets below omit it; `$(go env GOPATH)/bin/gofumpt -w` and
`$(go env GOPATH)/bin/goimports -local github.com/mediactl/clustarr -w` on every touched `.go`
file; RBAC markers move with the code that needs the grant, and every task that moves, adds or
deletes a marker or an API type runs `make generate manifests` and syncs the chart's copy of each
changed role into `charts/clustarr/templates/rbac.yaml`; R7 names never change unless a task says
so and cites the spec; build, don't deploy; no live-cluster action of any kind).

**Specific to these waves:**

- **Other sessions commit in this worktree.** Before a commit whose pathspec names a directory,
  run `git status --short -- <dir>` and stop if a file you did not touch is modified there.
- **Implement-only gates.** Every task ends with `go build ./...` green, `make build` green (five
  binaries), and `make generate manifests` leaving `git status --porcelain -- api config charts`
  empty after the task's own commit. `go vet ./...` compiles test files, some of which the
  implement-only waves leave not compiling (the ledger lists them), so it runs in the test batch;
  a task that breaks a test file's compilation names that file in its deferred tests.
- **Linkage (split R6, spec §4.4).** `cmd/manager` never links ffgo, purego, onnx, anacrolix,
  `pkg/download/usenet` or `pkg/relindex`; check with
  `go list -deps ./cmd/manager | grep -E 'ffgo|purego|onnx|anacrolix|pkg/download/usenet|pkg/relindex'`
  printing nothing. No agent root (`app/catalog/agent/{catalog,events,metadata}`,
  `app/import/agent`, `app/indexer/agent`, `app/caption/agent`, `app/grab/agent/{torrent,usenet}`,
  `cmd/markers`, `cmd/transcode`) links `app/catalog/grabplan`, `app/catalog/searchplan`,
  `app/grab/lifecycle`, `app/import/importplan`, `app/import/manager/...`, `app/intake`,
  `app/dispatch` or `app/remediation`.

### Exact values (spec §4.3, §5, §6, §8; copy these, do not re-derive)

**Records buckets** (`pkg/events`, beside `recordBuckets()`; every one `Durable: true`,
`Records: true`, `History: 1`, `Storage: StorageFile`, `Replicas: 3` (1 under `ForSingleNode`),
`LimitMarkerTTL: 5m`, `TTL: RecordsTTL` (7 d); KV streams discard new):

| Constant | Name | Key | MaxBytes | MaxValueSize | One writer | Read by |
|---|---|---|---|---|---|---|
| `BucketTransfers` | `clustarr-transfers` | `RecordKey(entry uid)` | 256 MiB | 512 KiB | the engine pod holding the transfer | owner item key |
| `BucketEngines` | `clustarr-engines` | `RecordSubKey(DownloadClient uid, "<ordinal>")` | 8 MiB | 16 KiB | that engine pod | DownloadClient controller |
| `BucketImports` | `clustarr-imports` | `RecordSubKey(entry uid, "inspect"\|"execute")` | 128 MiB | 512 KiB | the import agent | owner item key |
| `BucketSearches` | `clustarr-searches` | `RecordKey(task uid)`: the Search UID (interactive) or the item UID (automatic) | 128 MiB | 512 KiB | the catalog agent (search) | Search controller; item keys |
| `BucketItemMetadata` | `clustarr-item-metadata` | `RecordKey(item uid)` | 512 MiB | 512 KiB | the metadata gateway (and its artwork fetcher, same process) | item keys; Artist, Author reconcilers |
| `BucketIndexerHealth` | `clustarr-indexer-health` | `RecordKey(indexer uid)` | 8 MiB | 8 KiB | the index agent (fan-out and RSS share one in-process CAS) | Indexer controller |

**Streams:**

| Constant | Name | Subjects | Retention | Storage | Durable | MaxBytes | MaxAge | Discard | Duplicates |
|---|---|---|---|---|---|---|---|---|---|
| `StreamIntake` | `CLUSTARR_INTAKE` | `clustarr.intake.>` | WorkQueue | File | **true** (full size under `ForSingleNode`) | 256 MiB | 0 | **New** | 1 h |
| `StreamWorkEngine` | `CLUSTARR_WORK_ENGINE` | `clustarr.work.engine.>` | WorkQueue | File | **true** | 64 MiB | 0 | **New** | 1 h |
| `StreamTaskEvents` | `CLUSTARR_TASK_EVENTS` | `$JS.EVENT.ADVISORY.CONSUMER.MSG_NAKED.<stream>.<durable>` and `…MSG_TERMINATED.<stream>.<durable>` for every `Dispatched` consumer, rendered from the topology (`…<stream>.*` for `CLUSTARR_WORK_SQUASHARR` and `CLUSTARR_WORK_ENGINE`, whose durables are dynamic); never `>` | WorkQueue | File (memory under `ForSingleNode`, 1 MiB floor) | false | 8 MiB | 1 h | Old | 0 |

**Durables** (all created by the manager's topology or its controllers, never by an agent):

| Constant | Name | Stream / filter | AckWait | MaxDeliver | BackOff | Slots / MaxAckPending | Other |
|---|---|---|---|---|---|---|---|
| `ConsumerIntakeCandidate` | `catalogarr-intake-candidate` | INTAKE / `clustarr.intake.candidate.>` | 30 s | 20 | 10 s | 64 / 64 | `HandlerTimeout` 60 s, `Heartbeat` 10 s; leader-only in the manager |
| `ConsumerIntakeScan` | `importarr-intake-scan` | INTAKE / `clustarr.intake.scan.>` | 30 s | 10 | 5 s, 30 s, 2 m | 8 / 64 | `HandlerTimeout` 60 s, `Heartbeat` 10 s; leader-only |
| `ConsumerTaskEvents` | `clustarr-task-events` | TASK_EVENTS / every subject | 30 s | 3 | 5 s | 16 / 256 | leader-only |
| `EngineConsumerName(client, ordinal)` | `grabarr-engine-<KVKeyToken(client)>-<ordinal>` | WORK_ENGINE / `clustarr.work.engine.<KVKeyToken(client)>.<ordinal>.>` | 2 m | 20 | 10 s, 1 m, 5 m, 15 m | 4 / 4 | `Heartbeat` 30 s, `SampleFrequency` `100%`, `Dispatched`; ensured by the DownloadClient controller per rendered ordinal |
| (existing) the §5.1 dispatched durables | `catalogarr-search-high`, `catalogarr-search-normal`, `catalogarr-metadata`, `catalogarr-artwork-fetch`, `catalogarr-artwork-render`, `importarr-probe-high`, `importarr-probe-low`, `captionarr-fetch-high`, `captionarr-fetch-normal`, `catalogarr-markers`, `catalogarr-segments-plan`, `segmentarr-analyze`, `importarr-scan`, `importarr-fileimport`, `importarr-list`, `importarr-recycle`, `indexarr-rss`, and the dynamic `squasharr-transcode-*` | unchanged | unchanged | unchanged | unchanged | unchanged | gain `Dispatched: true` and `SampleFrequency: "10%"` (A2.4) |

**Subjects, keys and Msg-Ids:**

- Candidate intake: `clustarr.intake.candidate.<lower(owner kind)>.<KVKeyToken(owner uid)>`;
  Msg-Id `rss/<owner uid>/<sha1(indexer:guid)[:16]>` (RSS) or `pick/<search uid>/<sha1(indexer:guid)[:16]>`
  (a Search pick or `grabBest`).
- Scan intake: `clustarr.intake.scan.<namespace>.<KVKeyToken(scan uid)>`; Msg-Id
  `scan/<scan uid>/<sha1(path)[:16]>/<fingerprint>`.
- Engine command: `clustarr.work.engine.<KVKeyToken(client)>.<ordinal>.<KVKeyToken(entry uid)>`;
  a resync uses the last token `_resync`, a removal of an unidentified transfer `_id-<KVKeyToken(downloadID)>`.
  Msg-Id `engine/<entry uid>/<seq>`, after an engine restart `engine/<entry uid>/<seq>/<bootID>`;
  resync `engine/<client uid>/<ordinal>/resync/<resyncSeq>`; unidentified removal
  `engine/<client uid>/<ordinal>/id/<downloadID>`.
- Import tasks: `clustarr.work.importarr.fileimport.inspect.<KVKeyToken(entry uid)>` and
  `…fileimport.execute.<KVKeyToken(entry uid)>` (the existing `importarr-fileimport` filter
  `clustarr.work.importarr.fileimport.>` covers both); Msg-Id `import/<entry uid>/<phase>/<seq>`.
- Recycle of named files (A6): `clustarr.work.importarr.recycle.files.<KVKeyToken(id)>` under
  the existing `importarr-recycle`.
- Search task (A4): `WorkSearchSubject(PriorityNormal|PriorityLow|PriorityHigh, mediaKey)`
  unchanged; Msg-Id `search/<task uid>/<seq>`.
- Metadata task v2 (A5): `WorkMetadataSubject` unchanged; Msg-Id `metadata/<item uid>/<inputsHash>`.
- RPC: `RPCIndexBlocklist = "clustarr.rpc.indexarr.blocklist"`, queue group `indexarr`, ops
  `block`, `unblock`, `list`; caller timeout 5 s.
- Agent presence: `clustarr-progress` key `agent.<domain>.<KVKeyToken(pod)>`; written at start
  and every 30 s; the bucket's 10 min TTL retires it; the manager reads it through a 5 s cache.
- Transfer progress: `clustarr-progress` key `download.<KVKeyToken(entry uid)>`
  (`app/grab/engine.ProgressKey`, unchanged), 1 Hz.
- Advisories: `$JS.EVENT.ADVISORY.CONSUMER.MSG_NAKED.<stream>.<durable>`,
  `$JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED.<stream>.<durable>`, metrics
  `$JS.EVENT.METRIC.CONSUMER.ACK.<stream>.<durable>` (core subscription, no stream).

**Caps** (every list `+listType=atomic`, every string capped, no `+kubebuilder:default` on any
new field):

| Field | Cap |
|---|---|
| `status.downloads` | MaxItems 8 (Movie, Album, Book, Audiobook); 24 (Series, Comic) -- **lowered to 16 on Series and Comic, before any string cap, if `TestItemAtEveryCapFitsTheBudget` measures over** |
| `status.pendingGrabs` (Series, Comic) | MaxItems 24 |
| `entry.episodes` | MaxItems 200 |
| `entry.issues` | MaxItems 200, items MaxLength 16 |
| `entry.id` / `client` / `engine` / `qualityProfileRef` | 253 / 253 / 263 / 253 |
| `entry.uid` | 36 |
| `entry.outputPath` | 4096 |
| `entry.message` | 1024 (the transfer record keeps 2048) |
| `release.title` (clamped on a rune boundary) / `guid` (refused over, never clamped) / `indexerRef` / `indexerName` / `releaseGroup` / `edition` | 512 / 1024 / 253 / 253 / 256 / 256 |
| `release.languages` | MaxItems 16, items MaxLength 35 |
| `release.infoHash` | Pattern `^([0-9a-f]{40}\|[0-9a-f]{64})$` |
| `entry.import.message` | 1024 |
| `downloadNonces.{remove,resume,import,unblock}` | 63 each |
| `Dispatch.destination` / `delivery.reason` / `delivery.message` | 253 / 64 / 256 |
| `status.legacyDownloads.adopted` (release N) | MaxItems 64 |
| `PendingGrab.releaseTitle` (existing, uncapped today) | 512 (ratcheting keeps any stored longer value valid) |
| MediaFile `spec.importedFrom.infoHash` | 64 |
| engine record `unidentified` | 64 entries, then a count |
| transfer record `files` | clamped to a 384 KiB budget, `filesTruncated` set |

**Budgets** (`pkg/crdcheck/budget.go`): `ItemStatusBudgetBytes = 512 << 10`,
`ContainerStatusBudgetBytes = 1 << 20` (Series, Comic), item object ≤ 1.25 MiB
(`ItemObjectBudgetBytes = 1280 << 10`); worst-case entry ≈ 20.6 KB (§6.3).

**Admission (§5.4):** `Budget(durable) = 2 × MaxAckPending` for every dispatched durable but the
transcode pools (the fold's ledger) and the per-object tasks (one open scan per RootFolder, one
sync per list, one poll per indexer, today's controllers); engine commands are not admitted
through the ledger (§5.1: engine readiness gates them). Pacer class `search` (live automatic
searches) 20/min per namespace (`--search-live-per-minute`, default 20); index-only searches are
not paced. Waiting reasons: `Budget`, `Paced`, `NoAgent`, `NoCapableAgent`, `EngineNotReady`,
`Rebuilding`, and `Nak` (delivery). Unattended: `Pending > 0`, `Waiting == 0` and the lag
unchanged for **2 min**, or no presence key for the durable's domain.

**Timeouts and windows:**

| Name (Go) | Value | Meaning |
|---|---|---|
| `lifecycle.BlockQuarantine` | 1 h | a confirmed `Blocklisted` entry stays as a tombstone (§6.14) |
| `lifecycle.UnclaimedGrace` | 10 min | after an engine boot or a transfer's add, before `claimed: false` means anything (§6.7) |
| `lifecycle.UnidentifiedGrace` | 10 min | a transfer with no claim at all, before its removal with bytes kept (§6.7) |
| `lifecycle.EngineTeardownTimeout` | 10 min | R-6: an entry in `Removing` whose engine is gone (§6.8) |
| `lifecycle.ResyncSettle` | 2 min | after `reattached` and `resyncSeq` reach, before an entry with no record is re-added (§6.7) |
| `lifecycle.BlocklistTTL` | 90 d | every blocklist row's `until` (§6.14) |
| `lifecycle.BlockCallTimeout` | 5 s | the owed `clustarr.rpc.indexarr.blocklist` call |
| `lifecycle.ImportHoldRetention` | 24 h | a held import fails `importExpired` (today's `downloadv1alpha1.ImportHoldRetention`) |
| `lifecycle.RedownloadWindow` | 24 h | a failure older than this starts no redownload (P24) |
| `intake.HandlerBudget` | 60 s | a candidate held for its owner's decision; then nak 10 s (§8.4) |
| `intake.CopyWait` | 60 s | a resolved `MSG_TERMINATED` with no DLQ copy is annotated by the projector's code (§8.5) |
| `grabplan.CandidateWindow` | 30 min | a search answer is grab input while younger than this (ruling R7; < `BlockQuarantine`) |
| `presence.Interval` / reader cache | 30 s / 5 s | §5.3 |
| engine record cadence / freshness | 60 s / 2 min | `EngineReady` needs a record younger than 2 min |
| transfer record cadence | on every state or stage change and command; counters at most 1/min; at least 1/day | §6.7 |
| index agent local suppression | ≤ 30 s | after a failure, before the manager's verdict (§7.5, P87) |
| facade key re-read | 30 s | the index agent fails closed until the Secret exists (§7.5) |
| Trakt refresh | within 24 h of `tokenExpiresAt` | §7.6 |

**Field managers (R7: names kept; after these waves every one is written only from
`cmd/manager`, §7.0):** `catalogarr` (item rollups, `downloads`, `downloadPhase`,
`downloadNonces`, `legacyDownloads`; MediaFile status; the loop's MediaFile main resource),
`catalogarr-grab` (`pendingGrab`, `pendingGrabs`, `lastSearchedAt`, `searchAttempts`,
`donorSearchAttempts`, `searchDispatch`), `catalogarr-metadata` (`status.metadata`,
`status.artwork`), `catalogarr-artwork` (`status.overlay`, Movie and Series), `catalogarr-worker`
(Search `finishedAt`, `indexerOutcomes`, `results`), `catalogarr-series`, `catalogarr-fanout`,
`catalogarr-classify` (unchanged), `importarr-worker` (MediaFile spec; spec of scan- and
list-added items; AudioGraft until F7.1), `importarr` (`catalog.clustarr.io/observed-fingerprint`;
the Trakt token Secret; LibraryScan and ImportList status), `indexarr-worker` (Indexer
`WorkerFields`), `indexarr` (Indexer controller fields; the session Secret; the facade key
Secret), `grabarr` (DownloadClient status and workloads), `clustarr-dlq-projector` (the two
dead-letter annotations), `clustarr` (`k8s.DefaultFieldOwner`: the item finalizer
`download.clustarr.io/transfers`), `clustarr-legacy-fold` (release N), `grabarr-engine`
(**retired**: `k8s.RetiredFieldManagers()` in N, deleted in N+1).

**Event recorders and reasons (§7.7):** `clustarr-dlq-projector` `DeadLettered`;
`catalogarr-history` (one per domain event); `metadata-gateway` `ArtworkFetchFailed`;
`grabarr-engine` `ProxyUDPUnavailable`, `TransferOwnerGone`, `UnidentifiedTransferRemoved`;
`downloads` `DownloadAddFailed`, `EngineGone`, `SeedingLost`, `CommandRetrying`,
`InvalidAnnotation`, `NothingToRemove`, `NothingToResume`; the manager's Pod, recorder
`clustarr-dispatch`, `DispatchUnattended` (one per durable per transition). No agent records an
Event.

**Metrics (all in `pkg/obs/metrics`, all in `wantSeries`):** `clustarr_dispatch_waiting{consumer}`,
`clustarr_dispatch_unattended{consumer}` (exported by the QueueGauge, W4.72),
`clustarr_task_events_total{consumer,kind,outcome}` (`outcome` ∈ `recorded`, `stale`,
`unresolved`, `metricsOnly`), `clustarr_task_ack_delay_seconds{consumer}` (histogram),
`clustarr_task_deliveries{consumer}` (histogram), `clustarr_agent_write_refused_total{verb}`,
`clustarr_intake_candidates_total{outcome}` (`incorporated`, `pended`, `refused`, `timedOut`),
`clustarr_intake_scan_total{kind,outcome}`, `clustarr_blocklist_calls_total{op,outcome}`,
`clustarr_engine_commands_total{desired,outcome}`. Never a title, path or release label.

**Annotations (user intent on the owner, §6.10; never written by the loop):**
`download.clustarr.io/paused` (standing, `<id>[,<id>…]`), `download.clustarr.io/priority`
(standing, `<id>=high|normal|low[,…]`), `download.clustarr.io/remove` (one-shot,
`<nonce> <id> [data] [blocklist]`), `download.clustarr.io/resume` (one-shot, `<nonce> <id>`),
`download.clustarr.io/import` (one-shot, `<nonce> <id> [target=<kind>/<name>[/<key>]] [override]`),
`download.clustarr.io/unblock` (one-shot, `<nonce> <infoHash>` or `<nonce> <indexer>/<guid>`, `[global]`).
Nonce grammar is `catalogv1alpha1.ValidNonce` (`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`); an invalid
one is recorded through `catalogv1alpha1.HandledNonce` with a Warning `InvalidAnnotation`.
Ids are separated by commas and spaces. **Finalizer:** `download.clustarr.io/transfers` on the
owner while it has entries (field manager `clustarr`, `k8s.EnsureFinalizer`).

---

## Review Focus

1. **A NATS data loss is never a data removal (D7, §6.7 "Data safety").** Nothing removes a
   transfer, payload or MediaFile because a record, KV key, command or bucket is absent. A
   recreated `clustarr-transfers` triggers a resync before any entry is judged to be missing its
   transfer (`ResyncSettle` after `reattached` and the asked `resyncSeq`); a recreated
   `clustarr-imports` re-issues the inspect, never a `releaseFault`. Check every branch in
   `app/grab/lifecycle` and `app/remediation/downloads` that issues `absent` or `SafeRemove`: its
   evidence must be the owner's state machine, the apiserver's NotFound through the APIReader, or
   an unclaimed-with-no-claim transfer past `UnidentifiedGrace` (bytes kept). A cache NotFound is
   not evidence.
2. **A lost advisory, a leader change, a lost wake.** `delivery` on a dispatch block is history,
   not state: a missed `MSG_NAKED` leaves it stale until the next transition, and correctness
   rests on records and status (§8.4). The dispatch ledger rebuilds from status and refuses with
   `Rebuilding` until it has; the candidate inbox and the block book are leader-local and lost
   with the leader, and both are safe to lose (the server redelivers the candidate; the blocklist
   call is idempotent and re-owed). Check that no planner reads the ledger, inbox or book as the
   truth of what was published.
3. **An etcd restore with new UIDs.** A transfer's claim names its owner's UID and provider id.
   The evidence table of §6.7 decides in order: same UID; a different UID with the same name and
   an equal (or absent) provider id is the same owner (the next command writes the new UID into
   the journal); a different provider id is a different item (owner gone); only the APIReader's
   NotFound removes; any other apiserver error does nothing. An older restored status must not
   read a recent upgrade as "no longer wanted": "still an upgrade over the current file" and "the
   seeding obligation not met" both keep the transfer.
4. **A Series at its entry cap, and the complete declaration.** A Series holds every episode
   grab: at 24 live entries the grab planner pends or refuses (never truncates), adoption holds
   `TooMany` and reports, and the budget test proves the worst case under 1 MiB. Every
   `catalogarr` apply on an item (the main path, each early return, the deletion path) must send
   `downloads`, `downloadPhase` and `downloadNonces`, or server-side apply releases them -- the
   CLAUDE.md gotcha that gutted rollups three times. Each other manager's set
   (`catalogarr-grab`, `-metadata`, `-artwork`) is a complete declaration in its own CAS-chained
   apply, skipped only when equal at exactly its own paths.
5. **A stale import plan.** Between inspect and execute the item can change (a new file, a
   deleted file, a transcode swap). The execute task carries the plan's basis (each replaced
   MediaFile's UID and resourceVersion); the import agent re-reads each through the APIReader
   and refuses a stale plan with `stale`, and the planner re-inspects; replaced MediaFiles are
   deleted by the manager with a UID precondition (W22 had none), and the entry reads `Imported`
   only when `Files` MediaFiles name its `id`, so the item never reads Wanted between a placed
   file and its MediaFile.

---

## Order and dependencies

**New overall order** (the owner's answer to Q3, with A6 after A3 beside A4 and A5 as amended at
`69311068`): F3 → F4 → **A1 → A2 → A3 → A4 / A5 / A6** → F5 → F6 → F7 → **F8 with A8** → **A7** →
W6 (W6.2-W6.8 already in progress early, a recorded ruling) → W6b → W9 → W10 → **F9 with A9**.
Waves 0-5, 4a-4f, 7 and 8 are built.

| Wave | Tasks | Depends on | Parallel with |
|---|---|---|---|
| A1 API and topology | A1.1-A1.8 (8) | F4.4 green | nothing (serial, one owner: `api/`, `pkg/events`, `pkg/records` are shared) |
| A2 manager intake and acks | A2.1-A2.6 (6) | A1.8 | A2.1 beside A2.2; A2.3-A2.5 serial |
| A3 downloads | A3.1-A3.10 (10) | A2.6 | A3.1 beside A3.2; then serial A3.3 → A3.4 → A3.5; A3.6 and A3.7 serial after A3.5 (both `app/grab`); A3.8 after A3.5; A3.9 after A3.6-A3.8 |
| A4 grab and search policy | A4.1-A4.5 (5) | A3.10 | A5 and A6 (disjoint packages; see "Shared files" below) |
| A5 metadata and artwork | A5.1-A5.4 (4) | A3.10 | A4, A6 |
| A6 import, lists, indexers | A6.1-A6.5 (5) | A3.10 | A4, A5 |
| A8 release N migration | A8.1-A8.5 (5) | A4.5, A5.4, A6.5; F8.3, F8.4, F8.5 as each lands | runs inside F8, in this sequence: F8.3 → A8.2 → F8.4 → A8.1 → F8.5 → A8.3 → A8.4 → F8.6 … F8.11 → F8.12 with A8.5 |
| A7 RBAC and guards | A7.1-A7.3 (3) | F8.12 (with A8) | nothing; before W6.1 |
| A9 release N+1 removal | A9.1-A9.5 (5) | W10.8; runs with F9 | F9 (A9.n after F9.n) |

**51 tasks.** Shared files when A4, A5 and A6 run beside each other: `pkg/events/schema`
(each adds only the types named in its tasks, already declared by A1.4 -- so no wave edits it),
`app/catalog/history/target.go` (each adds its resolvers; commit immediately after each edit),
`pkg/agentdomain/agentdomain.go` (A4.4 only), `internal/cli/manager/run.go` (A4.2, A5.2, A6.1
each add one registration line; commit immediately), `app/remediation/manager/register.go`
(A4.1, A4.2, A5.1, A5.2, A5.3 each append one stage; commit immediately).

**Between waves the system is not runnable end to end, by design.** Between A3.10 and A4.4 no
automatic grab reaches an entry (the grab worker still creates Downloads nobody reconciles); no
commit between W4.13 and F8.12 is deployed (split plan, Global Constraints), so nothing observes
it. Each task's gate is the build, the generators and its greps.

---

## Rulings

Where the spec and the tree disagree, or the spec leaves a decision open, the tree wins or the
ruling below decides. Each names its cost if wrong.

- **R1.** The spec's `GrabbedBy` enum is `downloadv1alpha1.GrabSource` on the tree
  (`download_types.go:217`, values `rss;search;interactive;push;redownload`); it moves as
  `commonv1.GrabSource`, and the entry's field is `GrabbedBy commonv1.GrabSource
  json:"grabbedBy"`. -- Cost if wrong: one type rename.
- **R2.** The enums and `DownloadSource` (with `IndexerDownload`, which it references) move to
  `api/common/v1alpha1`; `api/download/v1alpha1` keeps Go type aliases and re-declared constants
  until N+1, and the Download types' own fields name the `commonv1alpha1` types directly, so
  controller-gen never resolves an alias. If controller-gen's object generator emits methods for
  an alias, mark the alias block `// +kubebuilder:object:generate=false`; if it still refuses,
  drop the aliases and rewrite the call sites (`grep -rln 'downloadv1alpha1\.<T>\b'`, about 100
  files) to `commonv1alpha1` in the same task. The Download CRD YAML must not change except the
  new `payloadUnavailable` enum value and descriptions. -- Cost if wrong: the fallback rewrite.
- **R3.** `downloadv1alpha1.ImportPhase` (`pending;blocked;importing;imported;ignored`) stays in
  `api/download`; the entry's summary uses a new `catalogv1alpha1.ImportPhase`
  (`pending;inspected;approved;imported;blocked;held;expired`), as §6.2 names it. -- Cost: none;
  two types share a name in two packages until N+1.
- **R4.** Entry ids keep today's argument order: `k8s.ChildName(target.Name, guid)`, or
  `k8s.ChildName(target.Name, string(purpose), guid)` for a donor (`perform.go:158-163`), not
  §6.2's `(target, guid[, purpose])`, so adopted entries keep their Downloads' names. -- Cost if
  wrong: none for adoption; a doc fix.
- **R5.** *Item stages.* The item path stays `remediation.ItemReconciler` calling each kind's
  `ReconcileItem` (F4.2). A3.3 adds `remediation.ItemStage` (planners beside the rollups) and the
  contract package `app/catalog/controller/itempass`, which the item packages import (they never
  import `app/remediation`, loop §3.12). The `catalogarr` contribution (entries, phase, nonces,
  the Episode/Issue views) is rendered by each kind's own renderer into its one `catalogarr`
  apply; every other manager's set is a generic `itempass.Set` (status JSON field names and
  values) applied by `ItemReconciler` through `itemstatus.ApplySet` (server-side apply of an
  unstructured status, CAS on the previous apply's resourceVersion), so no kind needs a typed
  renderer per set. The spec's "loop's adapter in `app/remediation/downloads`" is that stage. --
  Cost if wrong: a typed set renderer per kind instead (about 20 small functions).
- **R6.** *Records written by agents.* The six new buckets have one writer, the agent; the
  manager never writes `requested` there (unlike the fold's buckets) and reads through
  `records.Reader`. Agents write through `records.Writer`, a compare-and-swap that refuses a
  record whose `Seq` is below the stored one and allows repeated writes at one `Seq` (a transfer
  rewrites counters at the same command seq). `records.Spec` gains `KeyOf`, since these records'
  keys are entry, task, item, indexer and client UIDs, not `MediaFile.UID`. -- Cost if wrong: if
  adoption-style request records are wanted later, a `Requester` per bucket is additive.
- **R7.** *Search answers as grab input are level-triggered.* The grab planner considers an
  item's search record while it answers the item's current `searchDispatch.seq` and is younger
  than `grabplan.CandidateWindow` (30 min, below `BlockQuarantine`); a Series or Comic reads the
  records of its covered Episodes or Issues the same way. Re-considering an answer decides the
  same (the queue preference rejects a release already an entry), so a crash between the
  Episode's bookkeeping apply and the Series' grab pass loses nothing. -- Cost: one KV `Get` per
  covered episode with a recent answer, per container pass.
- **R8.** *Delivery state.* The advisory intake never writes a CR (the loop is the only writer
  of item status): it records each resolved nak or term in the leader-local
  `dispatch.DeliveryBook` and wakes the owning key; planners render `Dispatch.delivery` from the
  book. Fold planners (F5-F7, unchanged by §11.1) do not render `delivery` in release N; the
  fields stay empty on MediaFile blocks, and their naks show in metrics only. -- Cost: a
  MediaFile task's retries are not on the CR until a follow-up wires the book into those planners.
- **R9.** *Blocklist confirmations.* The owed `blocklist` call runs as an effect after the
  apply; its reply is recorded in the leader-local `downloads.BlockBook`, and the next pass
  renders `entry.block.confirmedAt` (or records the unblock nonce). A lost book re-calls; the
  row write is idempotent and seq-fenced. -- Cost: one extra pass per block.
- **R10.** *Term reasons.* natsbus already terms with `TermWithReason` (`natsbus/message.go:146`);
  A2.4 prefixes the reason with the envelope's `Clustarr-Id` and one space
  (`"<id> <reason>"`), and the advisory intake reads the first field. -- Cost: any other reader of
  the advisory's reason sees the prefix.
- **R11.** Comic gains `status.downloadingIssueCount` for its `Queue` print column (§6.2 names
  only Series' `downloadingEpisodeCount`). -- Cost: a field rename.
- **R12.** `status.searchDispatch` is added to the searchable kinds only: Movie, Episode, Album,
  Book, Audiobook, Issue (a Series or Comic is never searched; CLAUDE.md, "a Series is not a
  Search fan-out kind"). -- Cost: none.
- **R13.** `k8s.ReadOnly` is built in A1.7 and wired into `AgentManagerOptions` in A7.1, after
  A3-A6 removed every agent write; wiring it in A1 would fail every agent write still in the tree
  at runtime. -- Cost: none.
- **R14.** Agent presence writers are `cmd/agent` (every domain, engines included) and
  `cmd/markers`; `cmd/transcode` reports through its encoder limits, which the router already
  reads. -- Cost: a transcode pool with no presence key reads as attended by its limits.
- **R15.** `StreamAdmin.EnsureConsumer(ctx, ConsumerSpec) error` is added (creating the durable and
  its dead-letter watcher); the spec's `DeleteConsumer` is the existing
  `StreamAdmin.DeleteSubscription(ctx, stream, durable)`, which already deletes the watcher. --
  Cost: a name.
- **R16.** The advisory intake needs the naked message's subject and headers: `StreamAdmin` gains
  `Message(ctx, stream string, seq uint64) (subject string, env *events.Envelope, err error)`
  (`jetstream.Stream.GetMsg`), and `events.CoreSubscriber` (optional, natsbus only) serves the
  ack-metric subscription. -- Cost: two bus methods.
- **R17.** The spec's import-list snapshot lives in `clustarr-importlist` as the spec says, at a
  new key `snapshot.<KVKeyToken(list uid)>` (chunks `….<n>` past 512 KiB), written by the import
  agent; the remembered `StoredItem` keys (`importlist.ItemsKey`) become the ImportList
  controller's alone. Today's result value is in `clustarr-progress` (`importlist.<uid>`,
  `importliststate.ResultKey`), not in `clustarr-importlist` as §7.6 says; it retires, its
  counts computed by the controller. -- Cost: one key layout.
- **R18.** `status.nextPollAt` does not exist on Indexer; RSS scheduling stays
  `rssschedule.NextPollAt(idx, now)` plus a scheduled `RssTask` (`WithScheduleAt`) published by
  the Indexer controller after each poll's health record, as `seedRSSSchedule`
  (`controller/indexer/controller.go:447`) already does at start. -- Cost: none.
- **R19.** Indexer health is incorporated by timestamps, not a new status counter: a record
  whose `lastFailureAt` is after `status.lastFailureAt` applies `RecordFailure`, one whose
  `lastSuccessAt` is after both applies `RecordSuccess`. Several failures between two passes
  escalate one rung. -- Cost: a slower ladder under a burst; Prowlarr's per-failure rungs would
  need a `status.healthSeq`.
- **R20.** The engine durables' timing (AckWait 2 m, Heartbeat 30 s, MaxDeliver 20, BackOff
  10 s/1 m/5 m/15 m) and the intake durables' (table above) are this plan's; the spec gives only
  their MaxAckPending and budgets. -- Cost: tuning constants.
- **R21.** A usenet `downloadTimeout` (opt-in) is the manager's judgement from `startedAt`
  (`Blocklisted`, `timeout`, item scope), as the stall is from `lastProgressAt`; `timeout` keeps
  `IsReleaseFault()` true and is item-scoped (§6.14's table does not list it). -- Cost: the
  engine's own timeout path (`pkg/download/usenet`) becomes a fact the manager ignores.
- **R22.** Delay-held grabs and scheduled grab messages from the previous release are not
  adopted: `clustarr-pending` values and messages on `catalogarr-grab` are left to drain and
  are deleted in N+1 (A9.2); the next search or RSS match finds the release again. The two
  retired durables have no home in N: `agentdomain.Draining()` lists them, exempt from the
  one-home guard until A9.2 retires them. -- Cost: a delay-profile hold restarts after the
  upgrade.
- **R23.** Metadata, artwork-fetch and overlay-render dispatches have no status dispatch block
  (§6.2 names none); the ledger admits them and forgets them on the answer or after the task's
  `AckWait × MaxDeliver`, and does not rebuild them after a leader change, so a new leader may
  over-admit them by at most one budget. A metadata record is matched by its inputs (§7.1), so no
  sequence in status is needed. -- Cost: one budget of extra tasks after a failover.
- **R24.** The Search controller applies `decision.ItemStateRejections` against the target
  item's fresh state when it renders an interactive Search's `status.results`, so a person
  still sees queue, existing-file and `TranscodedFinal` rejections now that the search agent
  evaluates release rules only (§3.5 P8). -- Cost: none; it is a cheap comparison.
- **R25.** A redownload is level-triggered: the search planner treats an item as due at once
  while its owner holds a `Failed` (not a local fault, not `importExpired`) or `Blocklisted`
  entry covering it whose failure (the entry's `dispatch.dispatchedAt`, set by the `absent`
  command at the transition) is newer than the item's `searchDispatch.dispatchedAt` and younger
  than `RedownloadWindow`; lifecycle keeps such an entry until every covered item's search was
  dispatched after it. -- Cost: a failed entry can outlive its transfer by one search dispatch.
- **R26.** Between A3 and F7.1 the manager applies the donor's AudioGraft (W23's replacement is
  F7.1's); A3.8's materialise effect applies it under `importarr-worker` from the execute
  record's donor path, and F7.1's amendment deletes that effect. -- Cost: none.

---
## Wave A1: API and topology (A1.1-A1.8)

**Spec:** §4.3, §5.3, §6.2, §6.3, §6.5, §6.7 (the command, record and claim shapes), §6.9, §6.10,
§6.14, §7.1, §7.4, §7.5, §7.6, §8.2, §8.3, §9.2 (`k8s.ReadOnly`), §11.2 row A1.

**Depends on:** F4.4 green. **Order:** serial, one owner, A1.1 → A1.8: every task edits `api/`,
`pkg/events`, `pkg/records` or their generated files, and each later wave consumes exactly the
names this wave produces.

**What A1 delivers.** Every type, constant, bucket, stream, durable, subject and builder the later
waves name, so A2-A9 tasks written in parallel agree; nothing here changes behaviour except the
new enum value `payloadUnavailable`, the new printer columns and selectable fields, and new empty
NATS objects.

---

### Task A1.1: The download enums and `DownloadSource` move to `api/common/v1alpha1`; `payloadUnavailable`

**Spec:** §6.2 (first paragraph), §6.7 ("payloadUnavailable (new)"), §6.14 (scope by reason).

**Files:**

- Create: `api/common/v1alpha1/download_enums.go`
- Modify: `api/download/v1alpha1/download_types.go` (definitions removed, aliases added, the
  Download types' fields retyped to `commonv1alpha1`), `api/common/v1alpha1/zz_generated.deepcopy.go`
  and `api/download/v1alpha1/zz_generated.deepcopy.go` (generated), `config/crd/bases/download.clustarr.io_downloads.yaml`
  (generated: one enum value), the chart CRD copy if the chart vendors CRDs (`charts/clustarr/crds/`)

**Interfaces:**

- Consumes: nothing new.
- Produces (package `commonv1alpha1`, imported as `commonv1` under `api/catalog` and
  `commonv1alpha1` elsewhere): `DownloadPhase` and its eleven constants
  (`DownloadPhasePending` … `DownloadPhaseRemoving`), `DownloadStage` and its eight
  (`DownloadStageFetchingMetadata` … `DownloadStageDone`), `DownloadFailureReason` and its
  constants plus the new `DownloadFailurePayloadUnavailable = "payloadUnavailable"`, with methods
  `IsReleaseFault() bool`, `IsFailure() bool` and the new `IsLocalFault() bool` (`diskFull`,
  `writeError`); `DownloadPurpose` (`DownloadPurposeAudioDonor`); `GrabSource` and its five
  constants (ruling R1); `ImportRejectionClass` and `ImportClassTransient`,
  `ImportClassItemState`, `ImportClassNeedsPerson`, `ImportClassReleaseFault`; `DownloadSource`
  (with its "exactly one of" CEL rule, verbatim from `download_types.go:306`) and
  `IndexerDownload`; `DownloadPriority` and its three constants. `api/download/v1alpha1` keeps
  aliases of every one (`type DownloadPhase = commonv1alpha1.DownloadPhase`, …) and
  re-declared constants (`DownloadPhasePending = commonv1alpha1.DownloadPhasePending`, …) until
  A9.1.

- [ ] **Step 1: Move the declarations.** Cut each type above, with its doc comment, its
  `+kubebuilder:validation:Enum` marker, its constants and its methods, from
  `api/download/v1alpha1/download_types.go` into `api/common/v1alpha1/download_enums.go`
  (package `v1alpha1`; the common package already carries `+kubebuilder:object:generate=true`
  in `doc.go:29`). Add `payloadUnavailable` to `DownloadFailureReason`'s Enum marker (after
  `payloadMismatch`) and a constant:

  ```go
  // DownloadFailurePayloadUnavailable is an engine's answer when it cannot
  // re-add an entry's transfer because the payload can no longer be fetched
  // from its indexer (ADR-0019 §6.7): before import a release fault, after
  // import a lost seed.
  DownloadFailurePayloadUnavailable DownloadFailureReason = "payloadUnavailable"
  ```

  `IsReleaseFault()` gains `payloadUnavailable` (it already holds missingArticles, encrypted,
  stalled, timeout, importRejected, manual, payloadMismatch). Add:

  ```go
  // IsLocalFault is a failure of this host, not of the release: never
  // blocklisted and never searched again (Radarr raises no DownloadFailedEvent
  // for one; ADR-0019 §6.4).
  func (r DownloadFailureReason) IsLocalFault() bool {
  	return r == DownloadFailureDiskFull || r == DownloadFailureWriteError
  }
  ```

- [ ] **Step 2: Aliases.** In `download_types.go` add one `type (...)` block of aliases and one
  `const (...)` block re-declaring every moved constant, under the comment
  `// Kept until release N+1 (ADR-0019 §6.2, §10.3): the types live in api/common/v1alpha1.`
  Retype every field of `DownloadSpec`, `DownloadStatus` and `ImportState` that named a moved
  type to the `commonv1alpha1` type directly (ruling R2). Leave `ImportPhase`, `ImportState`,
  `ImportedFile`, `DownloadFile`, `UsenetHealth`, `DefaultBlocklistTTL`, `ImportHoldRetention`
  and the `ImportMessage*` constants where they are (A3.8 copies the messages into
  `importplan`; A9.1 deletes the Download-only ones).
- [ ] **Step 3: Generate and prove the CRD did not move.** `make generate manifests`. If
  controller-gen emits a `DeepCopy` for an alias, put `// +kubebuilder:object:generate=false`
  on the alias block and regenerate; if it still fails, take R2's fallback in this task.
  Then `git diff --stat -- config/crd/bases/download.clustarr.io_downloads.yaml` shows only the
  one enum line (and description lines, if a doc comment moved); `git diff -- config/crd/bases/download.clustarr.io_downloadclients.yaml`
  is empty.

**Tests (deferred to the batch):**

- `TestTheDownloadCRDIsUnchangedButPayloadUnavailable` (`pkg/crdcheck`): the generated Download
  schema equals the pre-move one except the enum value.
- `TestReleaseAndLocalFaultsArePartitioned` (`api/common/v1alpha1`): every failure reason is
  exactly one of release fault, local fault, `none`, or `importExpired`.
- `TestPayloadUnavailableIsAReleaseFault` (same package).
- `pkg/crdcheck/download_cel_test.go` and `donor_cel_test.go` still pass (rerun).

- [ ] **Step 4: Commit.**

  ```bash
  git add api/common/v1alpha1/download_enums.go
  git commit -m 'feat(api): the download enums and DownloadSource move to api/common (aliases kept until N+1); payloadUnavailable (ADR-0019 A1.1)' -- api/common/v1alpha1 api/download/v1alpha1 config/crd charts/clustarr/crds
  ```

---

### Task A1.2: Item status: grab entries, intents, pending candidates, the queue view; DownloadClient engine fields

**Spec:** §6.1, §6.2 (all of it), §6.3, §6.5, §6.10, §7.3 (`searchDispatch`), §10.2
(`status.legacyDownloads`), §6.7 (DownloadClient `unidentifiedTransfers`), §7.7
(`ProxyUDPUnavailable`); rulings R3, R11, R12.

**Files:**

- Create: `api/catalog/v1alpha1/download_entry_types.go`, `api/catalog/v1alpha1/download_annotations.go`
- Modify: `api/catalog/v1alpha1/{movie,series,episode,album,book,audiobook,comic,issue}_types.go`,
  `api/catalog/v1alpha1/shared_types.go` (`PendingGrab`), `api/download/v1alpha1/downloadclient_types.go`,
  generated: `api/catalog/v1alpha1/zz_generated.deepcopy.go`, `api/applyconfiguration/catalog/...`,
  `api/applyconfiguration/download/...`, `config/crd/bases/catalog.clustarr.io_{movies,series,episodes,albums,books,audiobooks,comics,issues}.yaml`,
  `config/crd/bases/download.clustarr.io_downloadclients.yaml`, the chart CRD copies

**Interfaces:**

- Consumes: A1.1's `commonv1` download types; F2.1's `Dispatch` (`mediafile_dispatch_types.go`).
- Produces (package `catalogv1alpha1`): `DownloadEntry`, `EntryBlock`, `BlockScope`
  (`BlockScopeItem = "item"`, `BlockScopeGlobal = "global"`), `EpisodeNumber`,
  `DownloadRelease`, `DownloadImportSummary`, `ImportPhase` (R3; constants
  `ImportPhasePending`, `…Inspected`, `…Approved`, `…Imported`, `…Blocked`, `…Held`,
  `…Expired`), `DownloadNonces`, `GrabCandidate`, `LegacyDownloads`, `LegacyDownload`,
  `LegacyOutcome` (`adopted;blocklisted;dismissed;removing`); fields on the owner kinds
  (`Downloads`, `DownloadPhase`, `DownloadNonces`, `LegacyDownloads`), on Series and Comic
  (`PendingGrabs`), on Comic (`DownloadingIssueCount`), on Episode and Issue
  (`DownloadPhase`), on the searchable kinds (`SearchDispatch *Dispatch`), on `PendingGrab`
  (`Candidate *GrabCandidate`, `Episodes []EpisodeNumber`, `Issues []string`, `ReleaseTitle`
  capped at 512); constants `FieldDownloadPhase = "status.downloadPhase"`,
  `FinalizerTransfers = "download.clustarr.io/transfers"`, `AnnotationDownloadPaused`,
  `AnnotationDownloadPriority`, `AnnotationDownloadRemove`, `AnnotationDownloadResume`,
  `AnnotationDownloadImport`, `AnnotationDownloadUnblock`, `DownloadIntentAnnotations() []string`;
  `MaxEntryMessage = 1024`, `MaxReleaseTitle = 512` (reuse `MaxReleaseTitleLength` if equal),
  `MaxOwnerEntries = 8`, `MaxContainerEntries = 24`.
  Package `downloadv1alpha1`: `DownloadClientStatus.UnidentifiedTransfers int32`,
  `DownloadClientStatus.ResyncSeq int64`, `DownloadClientConditionProxyUDPUnavailable = "ProxyUDPUnavailable"`.

- [ ] **Step 1: `download_entry_types.go`.** Write §6.2's block verbatim with these changes from
  the tree: `GrabbedBy commonv1.GrabSource` (R1); `Dispatch Dispatch` is the F2.1 type in this
  package; `Purpose`, `Phase`, `Stage`, `FailureReason` are the `commonv1` types; every new
  list `+listType=atomic`; no `+kubebuilder:default` anywhere. Add the types §6.2 references
  but does not spell:

  ```go
  // BlockScope is where a release is blocked (ADR-0019 §6.14).
  // +kubebuilder:validation:Enum=item;global
  type BlockScope string

  const (
  	BlockScopeItem   BlockScope = "item"
  	BlockScopeGlobal BlockScope = "global"
  )

  // ImportPhase is a grab's import, summarised (§6.9). Not
  // downloadv1alpha1.ImportPhase, which release N+1 removes with Download.
  // +kubebuilder:validation:Enum=pending;inspected;approved;imported;blocked;held;expired
  type ImportPhase string

  // DownloadNonces records the one-shot download intents last handled on
  // this owner (§6.10; loop spec §2.9's rules).
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
  // be made from status alone (§6.2, §6.6).
  type GrabCandidate struct {
  	Release DownloadRelease         `json:"release"`
  	Source  commonv1.DownloadSource `json:"source"`
  	// +optional
  	Score int32 `json:"score,omitempty"`
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

  type LegacyDownload struct {
  	// +kubebuilder:validation:MaxLength=253
  	Name string `json:"name"`
  	// +kubebuilder:validation:MaxLength=36
  	UID types.UID `json:"uid"`
  	Outcome LegacyOutcome `json:"outcome"`
  	At metav1.Time `json:"at"`
  }

  // +kubebuilder:validation:Enum=adopted;blocklisted;dismissed;removing
  type LegacyOutcome string
  ```

  `DownloadImportSummary.Phase` is required (no omitempty); `DownloadEntry.Dispatch` and
  `DownloadImportSummary.Dispatch` are required, so a `Pending` entry carries `seq ≥ 1` from
  creation (A3.4 issues it then; nothing is published until `Assigned`).

- [ ] **Step 2: `download_annotations.go`.** The six annotation constants of §6.10 (Global
  Constraints, "Annotations"), `FinalizerTransfers`, `FieldDownloadPhase`, and
  `func DownloadIntentAnnotations() []string` returning the six in a fixed order (the loop's
  item sources wake on them at `PriorityUser`, A3.3). No parser here: parsing is
  `app/grab/lifecycle`'s (A3.4), pure.
- [ ] **Step 3: Owner kinds.** On `MovieStatus`, `AlbumStatus`, `BookStatus`, `AudiobookStatus`
  add, after `ActiveDownloadRef`:

  ```go
  // Downloads is every grab of this item the system is responsible for, live
  // entries only (ADR-0019 §6.4); written by the remediation loop's item key
  // under catalogarr.
  // +optional
  // +listType=atomic
  // +kubebuilder:validation:MaxItems=8
  Downloads []DownloadEntry `json:"downloads,omitempty"`
  // DownloadPhase is the active entry's phase, "" when none: a print column
  // and a selectable field (§6.2).
  // +optional
  DownloadPhase commonv1.DownloadPhase `json:"downloadPhase,omitempty"`
  // +optional
  DownloadNonces *DownloadNonces `json:"downloadNonces,omitempty"`
  // +optional
  LegacyDownloads *LegacyDownloads `json:"legacyDownloads,omitempty"`
  // SearchDispatch is the outstanding search task (§7.3), catalogarr-grab's.
  // +optional
  SearchDispatch *Dispatch `json:"searchDispatch,omitempty"`
  ```

  `SeriesStatus` and `ComicStatus` get the same four download fields with `MaxItems=24`, no
  `SearchDispatch` (R12), plus `PendingGrabs []PendingGrab` (`+listType=atomic`,
  `MaxItems=24`); `ComicStatus` also gets `DownloadingIssueCount int32
  json:"downloadingIssueCount,omitempty"` (R11). `EpisodeStatus` and `IssueStatus` get
  `DownloadPhase` and `SearchDispatch`. `PendingGrab` (`shared_types.go:306`) gains
  `+kubebuilder:validation:MaxLength=512` on `ReleaseTitle` and:

  ```go
  // Candidate is the release this pending grab will be made from (§6.2).
  // +optional
  Candidate *GrabCandidate `json:"candidate,omitempty"`
  // Episodes and Issues name what a Series' or Comic's pending grab covers.
  // +optional
  // +listType=atomic
  // +kubebuilder:validation:MaxItems=200
  Episodes []EpisodeNumber `json:"episodes,omitempty"`
  // +optional
  // +listType=atomic
  // +kubebuilder:validation:MaxItems=200
  // +kubebuilder:validation:items:MaxLength=16
  Issues []string `json:"issues,omitempty"`
  ```

- [ ] **Step 4: Print columns and selectable fields.** On each of the eight kinds add
  `+kubebuilder:printcolumn:name="Download",type=string,JSONPath=`.status.downloadPhase``
  (before `Age`) and `+kubebuilder:selectablefield:JSONPath=`.status.downloadPhase``. Series
  adds `+kubebuilder:printcolumn:name="Queue",type=integer,JSONPath=`.status.downloadingEpisodeCount``,
  Comic `…JSONPath=`.status.downloadingIssueCount``. Count: each item kind then has 2
  selectable fields (≤ 8, §6.2).
- [ ] **Step 5: DownloadClient.** In `downloadclient_types.go` add the condition constant and,
  to `DownloadClientStatus`:

  ```go
  // UnidentifiedTransfers counts transfers the engines hold with no claim
  // (ADR-0019 §6.7), from the engine records.
  // +optional
  UnidentifiedTransfers int32 `json:"unidentifiedTransfers,omitempty"`
  // ResyncSeq is the last resync the controller asked its engines for after
  // clustarr-transfers was recreated (§6.7, "Resync").
  // +optional
  ResyncSeq int64 `json:"resyncSeq,omitempty"`
  ```

  and a print column `Unidentified` (`.status.unidentifiedTransfers`, `priority=1`).
- [ ] **Step 6: Generate.** `make generate manifests`; `git diff --stat config/crd` lists the
  eight item CRDs and `downloadclients` only; `grep -c 'selectableFields' -A3` on each item CRD
  shows two paths.

**Tests (deferred to the batch):**

- `TestItemAtEveryCapFitsTheBudget` (`pkg/crdcheck`, envtest; §6.3): for each of the eight item
  kinds, build the worst case from the generated schema (every list at MaxItems, every string at
  MaxLength, a 4,096-byte magnet), apply its status through `k8s.PatchStatus` under each owning
  manager (`catalogarr`, `catalogarr-grab`, `catalogarr-metadata`, `catalogarr-artwork`), read it
  back with `managedFields`, and assert status ≤ `ItemStatusBudgetBytes` (Series and Comic ≤
  `ContainerStatusBudgetBytes`) and the object ≤ `ItemObjectBudgetBytes`. **If Series or Comic
  measures over, lower their `Downloads` and `PendingGrabs` MaxItems to 16 before any string cap,
  and say so in the batch's commit.**
- `TestEveryStatusListIsCapped` (existing, `pkg/crdcheck/statuslists_guard_test.go:188`): passes
  with the new lists, its minimum walked-list count raised by the number added.
- `TestNoCRDDefaultIsUnreachableFromGo` (existing): no new default.
- `TestEveryItemKindSelectsByDownloadPhase` (`pkg/crdcheck`, envtest): a field selector
  `status.downloadPhase=Downloading` lists only the matching item of each of the eight kinds.
- `TestDownloadEntryBoundsMatchTheCRD` (`api/catalog/v1alpha1`): `MaxEntryMessage`,
  `MaxOwnerEntries`, `MaxContainerEntries` equal the generated schema's caps.

- [ ] **Step 7: Commit.**

  ```bash
  git add api/catalog/v1alpha1/download_entry_types.go api/catalog/v1alpha1/download_annotations.go api/applyconfiguration
  git commit -m 'feat(api): grab entries on the owning item -- status.downloads, downloadPhase (print column, selectable field), intents, pending candidates, searchDispatch; DownloadClient unidentifiedTransfers and resyncSeq (ADR-0019 A1.2)' -- api/catalog/v1alpha1 api/download/v1alpha1 api/applyconfiguration config/crd charts/clustarr/crds
  ```

---

### Task A1.3: `Dispatch.destination` and `delivery`; MediaFile `spec.importedFrom.infoHash`

**Spec:** §8.3 (the two fields and the lifecycle table), §6.2 ("MediaFile spec gains
`importedFrom.infoHash`"), §10.2 (the backfill, A8.2).

**Files:**

- Modify: `api/catalog/v1alpha1/mediafile_dispatch_types.go`, `api/catalog/v1alpha1/mediafile_types.go`
  (`ImportSource`), `app/import/mediafilespec/spec.go` (`ReassertFrozen` carries the new leaf),
  generated files, `config/crd/bases/catalog.clustarr.io_mediafiles.yaml` and every item CRD
  (the shared `Dispatch` type), chart CRD copies

**Interfaces:**

- Produces: `catalogv1alpha1.DeliveryState{State DeliveryPhase; Reason, Message string; Attempts int32; LastNakAt, DeadLetteredAt *metav1.Time}`,
  `DeliveryPhase` (`DeliveryWaiting`, `DeliveryPublished`, `DeliveryClaimed`,
  `DeliveryRetrying`, `DeliveryDeadLettered`), `Dispatch.Destination string`,
  `Dispatch.Delivery *DeliveryState`, the reason constants `DeliveryReasonBudget = "Budget"`,
  `DeliveryReasonPaced = "Paced"`, `DeliveryReasonNoAgent = "NoAgent"`,
  `DeliveryReasonNoCapableAgent = "NoCapableAgent"`, `DeliveryReasonEngineNotReady = "EngineNotReady"`,
  `DeliveryReasonRebuilding = "Rebuilding"`, `DeliveryReasonNak = "Nak"`;
  `ImportSource.InfoHash string` (`+kubebuilder:validation:MaxLength=64`, `json:"infoHash,omitempty"`).

- [ ] **Step 1: Write §8.3's two fields and `DeliveryState` verbatim** into
  `mediafile_dispatch_types.go` (they are optional, so every stored MediaFile and item stays
  valid). `InFlight()` is unchanged.
- [ ] **Step 2: `ImportSource.InfoHash`**, doc: "the release's info hash, frozen at import; the
  search's current-file check reads it instead of the Download `downloadRef` named (ADR-0019
  §6.2)". `mediafilespec.ReassertFrozen` copies it into the `ImportedFrom` apply configuration so
  a re-apply never releases it (CLAUDE.md, "a re-scan erased a MediaFile's frozen import fields").
- [ ] **Step 3: Generate.** `make generate manifests`.

**Tests (deferred to the batch):**

- `TestMediaFileAtEveryCapFitsTheBudget` (`pkg/crdcheck`, F2.2's, rerun): still within
  `MediaFileStatusBudgetBytes` with `destination` and `delivery` at their caps on every dispatch
  block (the fold's three plus every subtitle item's).
- `TestTheSchemaAcceptsThePreviousReleasesMediaFileApply` (F2.1's, rerun).
- `TestReassertFrozenKeepsTheInfoHash` (`app/import/mediafilespec`).

- [ ] **Step 4: Commit.**

  ```bash
  git commit -m 'feat(api): Dispatch gains destination and delivery; MediaFile importedFrom.infoHash (ADR-0019 A1.3)' -- api/catalog/v1alpha1 api/applyconfiguration app/import/mediafilespec/spec.go config/crd charts/clustarr/crds
  ```

---

### Task A1.4: `schema` -- item refs, records, intake, commands, tasks and RPC payloads

**Spec:** §4.3 (header field, intake payloads), §6.7 (command, transfer record, engine record,
claim), §6.9 (inspect and execute), §6.14 (block state in answers, the RPC), §7.1 (metadata
record), §7.3-§7.4 (search task and record), §7.5 (indexer-health record), §7.6 (scan
observation, list snapshot), §5.3 (presence).

**Files:**

- Create: `pkg/events/schema/transfer.go` (command, claim, transfer and engine records),
  `pkg/events/schema/intake.go` (candidate, scan observation), `pkg/events/schema/imports.go`
  (inspect and execute tasks and the import record), `pkg/events/schema/records_catalog.go`
  (search record, item-metadata record), `pkg/events/schema/records_index.go` (indexer-health
  record, blocklist RPC, release block), `pkg/events/schema/presence.go`,
  `pkg/events/schema/importlist.go` (the list snapshot)
- Modify: `pkg/events/schema/record.go` (`ItemRef`, `RecordHeader.Item`), `pkg/events/schema/catalog.go`
  (`SearchTask` v2, `MetadataTask` v2, `ArtworkFetchTask` v2), `pkg/events/schema/index.go`
  (`Release.Blocked`, `Release.Blocks`, `SearchRequest.Scope`, `QueryRequest.Scope`),
  `pkg/events/schema/schematest/payloads.go` (every new payload)

**Interfaces:**

- Consumes: A1.1-A1.3 types.
- Produces (package `schema`; JSON names are the Go names lower-camel unless shown; every payload
  has `Schema() string`):

  ```go
  // ItemRef names the object whose status incorporates a record or an
  // intake message (ADR-0019 §4.3): an item, a Search, an Indexer, a
  // DownloadClient or an ImportList.
  type ItemRef struct {
  	Kind string `json:"kind"`
  	Ref         // namespace, name, uid, flat
  }
  // RecordHeader gains, after MediaFile:
  //	Item *ItemRef `json:"item,omitempty"`   // set instead of mediaFile by item-keyed records
  type EntryRef struct {
  	ID  string `json:"id"`
  	UID string `json:"uid"`
  }
  // BlockScopeGlobal and BlockScopeOf build the release-index scope string (§6.14).
  const BlockScopeGlobal = "*"
  func BlockScopeOf(item ItemRef) string // "<kind>/<namespace>/<name>/<uid>"
  ```

  | Type | `Schema()` | Fields (summary; exact Go in the step) |
  |---|---|---|
  | `TransferClaim` | — | `Owner ItemRef`, `ProviderID`, `Entry EntryRef`, `InfoHash`, `Indexer`, `GUID`, `Title`, `Purpose`, `RemoveDataOnDelete bool`, `Imported bool` |
  | `TransferSelection` | — | `Episodes []EpisodeNo{Season, Number int32}`, `Absolutes []int32`, `AirDates []string`, `Issues []string` |
  | `EngineCommand` | `download.EngineCommand.v1` | `Seq`, `Owner`, `Entry`, `Desired` (`present`\|`absent`\|`resync`), `RemoveData`, `Protocol`, `Source *commonv1alpha1.DownloadSource`, `ExpectedInfoHash`, `Release CommandRelease{Title, GUID, IndexerRef, SizeBytes}`, `Category`, `Selection *TransferSelection`, `Paused`, `Priority`, `HealthOverride`, `SeedCriteria *commonv1alpha1.SeedCriteria`, `RemoveOnImport`, `Imported`, `ImportedAt *time.Time`, `Claim TransferClaim`, `ResyncSeq`, `DownloadID`, `IssuedAt` |
  | `TransferRecord` | `download.Transfer.v1` | `RecordHeader` (`Item` = owner, `Seq` = last command applied, `State` ∈ `present`\|`removed`\|`failed`), `Entry`, `Claim *TransferClaim`, `Claimed bool`, `Engine`, `BootID`, `DownloadID`, `Stage`, `OutputPath`, `ContentRoot`, `Files []TransferFile{Path, SizeBytes, Skipped}`, `FilesTruncated`, the counters (`TotalBytes`, `RemainingBytes`, `DownloadedBytes`, `UploadedBytes`, `ProgressPercent`, `RatioMilli`, `SeedTimeSeconds`, `Seeders`, `Peers`), `Health *TransferHealth{HealthPercent, CriticalHealthPercent, FailedArticles, TotalArticles}`, `IsEncrypted`, `CanMoveFiles`, `CanBeRemoved`, `SeedGoalReached`, `HealthPaused`, `EngineFailureReason`, `Message` (≤ 2048), `StartedAt`, `CompletedAt`, `LastProgressAt *time.Time`, `AddedAt time.Time`, `DataRemoved` |
  | `EngineRecord` | `download.Engine.v1` | `RecordHeader` (`Item` = the DownloadClient, `Sub` = ordinal, `Seq` monotone per write, `State` `present`), `Client`, `Ordinal int32`, `Pod`, `BootID`, `Version`, `Ready`, `Reattached`, `ResyncSeq`, `Active`, `Queued`, `Seeding int32`, `ScratchFreeBytes`, `PublishFreeBytes int64`, `ProxyUDP` (`available`\|`unavailable`\|`n/a`), `Unidentified []UnidentifiedTransfer{DownloadID, Name, AddedAt, SizeBytes}` (≤ 64), `UnidentifiedCount int32`, `At` |
  | `Candidate` | `catalog.Candidate.v1` | `Owner ItemRef`, `Targets []ItemRef` (matched Episodes or Issues), `Origin` (`rss`\|`search`\|`interactive`), `SearchRef *Ref`, `Manual`, `Override bool`, `GrabbedBy`, `Purpose`, `Release ScoredRelease`, `At` |
  | `ScoredRelease` | — | `Decision commonv1alpha1.ReleaseDecision`, `Parsed ParsedFacts{Seasons, Episodes, Absolute []int32, AirDate, FullSeason, MultiSeason bool, Issue string}`, `Blocked *ReleaseBlock`, `Score int32`, `IndexerPriority int32` |
  | `ScanObservation` | `import.ScanObservation.v1` | `Scan`, `RootFolder Ref`, `Kind` (`present`\|`missing`\|`unmatched`\|`orphanPart`), `Path`, `SizeBytes`, `ModTime`, `Fingerprint`, `Attribution *ScanAttribution{Kind, Name, ProviderID, Keys []string, Track, Evidence string, Create *ItemCreate}`, `Frozen *FrozenFields`, `Basis *MediaFileBasis`, `Unmatched *UnmatchedFact{Code, Reason string; Candidates []string}`, `ProfileTag`, `TranscodeOutputOf string`, `Missing *MissingFact{ENOENT bool; At time.Time}`, `Manual bool`, `SeenAt` |
  | `ItemCreate` | — | `Kind` (`movie`\|`series`), `Name`, `TmdbID`, `TvdbID int64`, `QualityProfileRef`, `RootFolderRef`, `Title`, `Year int32`, `OriginalLanguage` |
  | `FrozenFields` | — | `Quality *commonv1alpha1.Quality`, `Revision *commonv1alpha1.Revision`, `ReleaseType`, `ReleaseGroup`, `Edition *string`, `Languages []string`, `FormatScore *int32`, `MatchedFormats []string`, `ProfileHash`, `Original *bool`, `Track` |
  | `MediaFileBasis` | — | `Name`, `UID`, `ResourceVersion`, `Path`, `SpecHash` |
  | `ImportInspectTask` | `importarr.ImportInspectTask.v1` | `Seq`, `Owner ItemRef`, `Entry EntryRef`, `ContentRoot`, `OutputPath`, `Files []TransferFile`, `Release CommandRelease`, `Protocol`, `Targets []ItemRef`, `Episodes []EpisodeNo`, `Issues []string`, `QualityProfile`, `Purpose`, `GrabbedBy`, `Manual`, `Override bool`, `TargetOverride *ItemRef` |
  | `ImportExecuteTask` | `importarr.ImportExecuteTask.v1` | `Seq`, `Owner`, `Entry`, `Plan ImportPlan{Mode` (`hardlink`\|`move`\|`copy`)`, Moves []ImportMove{Source, Dest string; Target ItemRef; MediaFileName string; Frozen FrozenFields}, Replaces []MediaFileBasis, Donor *DonorPlan{Source, Dir string}, RootFolder, RecycleBin string}` |
  | `ImportRecord` | `importarr.Import.v1` | `RecordHeader` (`Item` = owner, `Sub` = `inspect`\|`execute`, `Seq` = the task's), `Entry`, `Inspect *ImportInspection{Files []InspectedFile{Path, SizeBytes, Parsed ParsedFacts, Probe *ProbeSummary, Fingerprint, Proposed *ItemRef, Frozen FrozenFields, Sample bool, Rejections []InspectRejection{Class, Text, Kind string}}}`, `Execute *ImportExecution{Placed []PlacedFile{Source, Dest string; SizeBytes; ModTime; Fingerprint, ProbeHash string; Target ItemRef; MediaFileName string; Frozen FrozenFields}, Refused string` (`stale`\|`diskFull`\|`overwrite`\|`outsideRoot`), `Recycled []string, DonorPath string}` |
  | `ProbeSummary` | — | `VideoCodec`, `Width`, `Height`, `HDR`, `AudioLanguages []string`, `DurationSeconds`, `Transcoded bool`, `ProfileTag` (enough for `TranscodedFinal`, wrong language, probe-corrected quality, P57-P59) |
  | `SearchTask` (v2) | `catalog.SearchTask.v2` (`SearchTaskV1Schema = "catalog.SearchTask.v1"` kept for history) | the v1 fields plus `Item ItemRef`, `Container *ItemRef`, `Seq int64`, `QualityProfile string`, `Protocols []commonv1alpha1.Protocol`, `IndexerPriorities map[string]int32`, `Scope string`, `Donor *DonorWant{Languages []string; Anchor string; Rejected []string; InFlight []string}` |
  | `SearchRecord` | `catalog.Search.v1` | `RecordHeader` (`Item` = the item, or the Search for an interactive search; `Seq` = the task's), `Task string` (uid), `Container *ItemRef`, `Purpose`, `FinishedAt`, `QueryMode`, `AllPaced bool`, `IndexerOutcomes []SearchOutcome` (≤ 100), `Candidates []ScoredRelease` (≤ 200) |
  | `MetadataTask` (v2) | `catalog.MetadataTask.v2` (`MetadataTaskV1Schema` kept) | v1 fields plus `Item ItemRef`, `Inputs MetadataInputs{ProviderIDs map[string]string; Language string; SchemaVersion int32; Force bool; RefreshEpoch int64}` |
  | `ItemMetadataRecord` | `catalog.ItemMetadata.v1` | `RecordHeader` (`Item`; `Seq` = a per-item counter), `Inputs MetadataInputs`, `RefreshedAt`, `SchemaVersion int32`, `Metadata json.RawMessage` (the kind's `status.metadata` JSON, as `metadata/patch.go` renders it today), `ArtworkFailures []ArtworkFailure{Type, URL, Error string; At time.Time}` (≤ 9) |
  | `ArtworkFetchTask` (v2) | `catalog.ArtworkFetchTask.v2` | `MediaRef`, `Item ItemRef`, `Fetch []ArtworkFetch{Type, URL, Kind, Language string}`, `Drop []string` (types whose override was removed) |
  | `IndexerHealthRecord` | `index.IndexerHealth.v1` | `RecordHeader` (`Item` = the Indexer; `Seq` monotone), `Successes`, `Failures int64`, `LastSuccessAt`, `LastFailureAt *time.Time`, `LastFailure`, `LastFailureClass` (`timeout`\|`rateLimited`\|`error`), `IndexedReleases int64`, `LastRssAt *time.Time`, `LastRssNewCount int32` |
  | `ReleaseBlock` | — | `Scope`, `Reason string`, `Until time.Time` |
  | `BlocklistRequest` | `index.BlocklistRequest.v1` | `Op` (`block`\|`unblock`\|`list`), `Scope`, `InfoHash`, `Indexer`, `GUID`, `Title`, `Protocol`, `Reason`, `EntryID string`, `Seq int64`, `BlockedAt`, `Until time.Time`, `Limit`, `Offset int32` |
  | `BlocklistResponse` | `index.BlocklistResponse.v1` | `Applied`, `Stale bool`, `Rows []BlockRow{Scope, InfoHash, Indexer, GUID, Title, Protocol, Reason, EntryID string; Seq int64; BlockedAt, Until time.Time}`, `Error` |
  | `AgentPresence` | `agent.Presence.v1` | `Domain`, `Pod`, `Node`, `Version string`, `Slots map[string]int`, `Durables []string`, `Capabilities map[string]string`, `At time.Time` |
  | `RecycleFilesTask` | `importarr.RecycleFilesTask.v1` | `RootFolder Ref`, `Paths []string` (A6.2: files a list removal or an orphan part leaves to the recycle bin) |
  | `ListSnapshot` | `importarr.ListSnapshot.v1` | `List ItemRef`, `SyncedAt`, `Error string`, `NeedsToken bool`, `Chunk`, `Chunks int`, `Kinds []ListKindSnapshot{Kind string; Fetched int32; Items []ListedItem{IDs map[string]string; Title string; Year int32; ResolvedID int64; Unresolved bool}}` |
  | `Release` (existing) | unchanged | gains `Blocked *ReleaseBlock json:"blocked,omitempty"` (a search or query answer: the row for the request's scope or the global one) and `Blocks []ReleaseBlock json:"blocks,omitempty"` (the firehose: every scope that blocks it) |
  | `SearchRequest`, `QueryRequest` (existing) | unchanged | gain `Scope string json:"scope,omitempty"` |

- [ ] **Step 1: Write the types** in the files listed, with doc comments that cite the spec
  section each implements, every record type embedding `RecordHeader` flat and returning its
  `Schema()` string, which is also the value written into `RecordHeader.Schema`. Clamp helpers
  belong to the writers (A3.6, A3.8, A4.3, A5.1, A6.3), not here; declare the caps as constants:
  `MaxTransferMessage = 2048`, `MaxTransferFilesBytes = 384 << 10`, `MaxUnidentified = 64`,
  `MaxSearchCandidates = 200`, `MaxIndexerOutcomes = 100`, `MaxArtworkFailures = 9`.
- [ ] **Step 2: Version bumps.** `SearchTask.Schema()` returns `catalog.SearchTask.v2`,
  `MetadataTask.Schema()` `catalog.MetadataTask.v2`, `ArtworkFetchTask.Schema()`
  `catalog.ArtworkFetchTask.v2`; keep `SearchTaskV1Schema`, `MetadataTaskV1Schema`,
  `ArtworkFetchTaskV1Schema` constants and make `Decode` accept the v1 name into the same struct
  (the v1 fields are a subset), so DLQ replays and history keep decoding v1 envelopes. The
  producers switch in A4.2 (search), A5.1 (metadata) and A5.2 (artwork).
- [ ] **Step 3: `schematest.Payloads()`** lists every new payload, so the existing round-trip
  test covers them.

**Tests (deferred to the batch):**

- `TestEveryPayloadRoundTrips` (existing, `pkg/events/schema`): covers the new payloads.
- `TestV1SearchAndMetadataTasksStillDecode` (`pkg/events/schema`): v1 envelopes decode into the
  v2 structs.
- `TestProbeRecordWireFormatIsUnchanged` (F1.1's): an absent `item` leaves the probe record's
  JSON byte-identical.
- `TestBlockScopeOfIsInjective` (`pkg/events/schema`).

- [ ] **Step 4: Commit.**

  ```bash
  git add pkg/events/schema/transfer.go pkg/events/schema/intake.go pkg/events/schema/imports.go pkg/events/schema/records_catalog.go pkg/events/schema/records_index.go pkg/events/schema/presence.go pkg/events/schema/importlist.go
  git commit -m 'feat(schema): item refs and the ADR-0019 payloads -- engine commands and claims, transfer and engine records, candidates, scan observations, import tasks and records, search, metadata and indexer-health records, the blocklist RPC, presence; SearchTask, MetadataTask and ArtworkFetchTask v2 (A1.4)' -- pkg/events/schema
  ```

---

### Task A1.5: `pkg/records` for agent-written buckets; item records in `recordsource`; the `search` pacing class

**Spec:** §4.3 (one writer per key), §8.1 (item-keyed sources S25, S27-S29), §5.4 (Pacer class
`search`); rulings R6, R7.

**Files:**

- Create: `pkg/records/reader.go`, `pkg/records/writer.go`
- Modify: `pkg/records/records.go` (`Spec.KeyOf`, the key check in `get` and the writer's),
  `pkg/records/pacer.go`, `pkg/records/recordsource/recordsource.go`

**Interfaces:**

- Consumes: A1.4's `schema.ItemRef`, `RecordHeader.Item`.
- Produces:

  ```go
  // Spec gains:
  //	// KeyOf names the key tokens a record must carry, for buckets keyed by
  //	// something other than the MediaFile UID; nil is (Header().MediaFile.UID, Header().Sub).
  //	KeyOf func(R) (uid, sub string)

  // Reader is the manager's read half of an agent-written bucket (ADR-0019 §4.3).
  type Reader[R Record] struct{ /* store[R] */ }
  func NewReader[R Record](kv events.KV, spec Spec[R]) *Reader[R]
  func (r *Reader[R]) Get(ctx context.Context, key string) (R, uint64, bool, error)
  func (r *Reader[R]) Keys(ctx context.Context, prefix string) ([]string, error)

  // Writer is the one writer of an agent-written bucket: a compare-and-swap
  // after a fresh read that drops a record whose Seq is below the stored
  // one (Dropped) and allows a repeated Seq (a transfer rewrites its
  // counters at one command's Seq).
  type Writer[R Record] struct{ /* store[R], writer, version */ }
  func NewWriter[R Record](kv events.KV, spec Spec[R], writer, writerVersion string) *Writer[R]
  func (w *Writer[R]) Write(ctx context.Context, key string, rec R) (Verdict, error)
  func (w *Writer[R]) Update(ctx context.Context, key string, mutate func(cur R, ok bool) (R, bool)) (Verdict, error)

  // pacer.go:
  const PaceSearch = "search" // live automatic searches, per namespace: class "search/<namespace>"
  // DefaultRates gains PaceSearch: 20.
  // Reserve and Consume resolve a class's rate by its exact name first, then by
  // the part before its first '/', so "search/media" paces at the "search" rate.

  // recordsource:
  type ItemDecoder func(value []byte) (item schema.ItemRef, state string, ok bool)
  var ItemHeaderDecoder ItemDecoder // decodes only "item" and "state"
  func NewItems[K comparable](bus Bus, bucket string, toKeys func(item schema.ItemRef, value []byte) []K, opts ...Option[K]) *Source[K]
  ```

- [ ] **Step 1: `Spec.KeyOf`.** In `store.get` (`records.go:194`) and the write-side check
  (`records.go:218`) replace `h.MediaFile.UID` / `h.Sub` with `s.keyOf(rec)`, which calls
  `spec.KeyOf` when set. The fold's specs set none, so their behaviour is unchanged.
- [ ] **Step 2: `Reader` and `Writer`.** `Writer.Write` stamps `Writer`, `WriterVersion`,
  `Schema`; clips `Failure` to `MaxFailure`; refuses over `MaxValue` with `ErrTooLarge`; loops at
  most `CASAttempts` times on `ErrKeyExists`/`ErrRevisionMismatch`, re-reading each time; returns
  `Dropped` without writing when the stored record (decoded through the spec) has a higher `Seq`.
  `Update` is the read-modify-write form the index agent's two writers share (§7.5). Neither
  half writes `requested` or `withdrawn`, and neither deletes: the bucket TTL retires records.
  Package doc gains a paragraph: the fold's buckets use Requester/Answerer; ADR-0019's
  agent-written buckets use Reader/Writer, one writer per key.
- [ ] **Step 3: `NewItems`.** Share `Source[K]`'s implementation (first open `UpdatesOnly`, skip
  deletes and the `requested`/`withdrawn` states, resume from `lastRev+1`, `OnRecreated` on a
  recreated bucket) and replace only the decode-to-keys step: decode with `ItemHeaderDecoder`
  (or `WithItemDecoder`), then enqueue every key `toKeys` returns. A record with no `item`
  enqueues nothing.
- [ ] **Step 4: Pacer.** Add `PaceSearch` to `DefaultRates()`; implement the prefix rule in the
  rate lookup only (reservations stay per full class name and key).

**Tests (deferred to the batch):**

- `TestKeyOfGovernsTheKeyCheck` (`pkg/records`): a transfer record keyed by its entry UID reads
  present; under another key it reads absent and counts `decode`.
- `TestWriterDropsALowerSeqAndAllowsARepeatedOne` (`pkg/records`).
- `TestWriterRereadsAfterACASMiss` (`pkg/records`, interleaving a second writer).
- `TestNewItemsEnqueuesEveryKeyAndSkipsDeletes` (`pkg/records/recordsource`).
- `TestTheSearchClassPacesPerNamespace` (`pkg/records`): `search/a` and `search/b` each take
  20/min; `probe/low` keeps its exact rate.
- `TestRecordsLinksNoKubernetes` (existing): still holds.

- [ ] **Step 5: Commit.**

  ```bash
  git add pkg/records/reader.go pkg/records/writer.go
  git commit -m 'feat(records): Reader and Writer for agent-written buckets, Spec.KeyOf, item-keyed recordsource (NewItems), and the search pacing class (ADR-0019 A1.5)' -- pkg/records
  ```

---

### Task A1.6: Topology -- six buckets, three streams, the intake and task-events durables, engine durables, subjects; `SampleFrequency`; `StreamAdmin.EnsureConsumer`

**Spec:** §4.3 (buckets, intake, commands, single-node fit), §5.1 (routing table), §6.7
(command subject and Msg-Id), §8.2 (`CLUSTARR_TASK_EVENTS`, ack sampling), §8.5 (the manager
creates every durable); rulings R15, R20.

**Files:**

- Create: `pkg/events/agents.go` (bucket, stream and consumer constants and builders of this
  design), `pkg/events/natsbus/ensure_consumer.go`
- Modify: `pkg/events/topology.go` (`ConsumerSpec.SampleFrequency`, `ConsumerSpec.Dispatched`,
  `Default()` gains the new objects, `Validate`), `pkg/events/records.go` (the six buckets beside
  `recordBuckets()`), `pkg/events/topology_nats.go` (`ConsumerConfig` maps `SampleFrequency`),
  `pkg/events/subjects.go` (subject and Msg-Id builders), `pkg/events/bus.go`
  (`StreamAdmin.EnsureConsumer`), `pkg/events/membus/admin.go`, `pkg/events/natsbus/admin.go`

**Interfaces:**

- Consumes: A1.4 nothing at compile time (subjects are strings).
- Produces (package `events`): the bucket constants of the Global Constraints;
  `StreamIntake = "CLUSTARR_INTAKE"`, `StreamWorkEngine = "CLUSTARR_WORK_ENGINE"`,
  `StreamTaskEvents = "CLUSTARR_TASK_EVENTS"`; `ConsumerIntakeCandidate`,
  `ConsumerIntakeScan`, `ConsumerTaskEvents`; `FilterIntakeCandidates = "clustarr.intake.candidate.>"`,
  `FilterIntakeScans = "clustarr.intake.scan.>"`; and:

  ```go
  func IntakeCandidateSubject(ownerKind, ownerUID string) string
  func IntakeScanSubject(namespace, scanUID string) string
  func WorkEngineSubject(client string, ordinal int32, entryUID string) string
  func WorkEngineResyncSubject(client string, ordinal int32) string                 // last token "_resync"
  func WorkEngineIDSubject(client string, ordinal int32, downloadID string) string  // last token "_id-<tok>"
  func EngineConsumerName(client string, ordinal int32) string                      // "grabarr-engine-<tok>-<n>"
  func EngineConsumer(client string, ordinal int32) ConsumerSpec                    // R20's timing
  func WorkImportInspectSubject(entryUID string) string
  func WorkImportExecuteSubject(entryUID string) string
  func SubjectImportRecycleFiles(id string) string
  func MsgIDForEngineCommand(entryUID string, seq int64) string
  func MsgIDForEngineCommandAfterBoot(entryUID string, seq int64, bootID string) string
  func MsgIDForEngineResync(clientUID string, ordinal int32, resyncSeq int64) string
  func MsgIDForEngineRemoveID(clientUID string, ordinal int32, downloadID string) string
  func MsgIDForImport(entryUID, phase string, seq int64) string
  func MsgIDForSearch(taskUID string, seq int64) string
  func MsgIDForCandidate(origin, scopeUID, indexer, guid string) string
  func MsgIDForScanObservation(scanUID, path, fingerprint string) string
  func PresenceKey(domain, pod string) string                 // "agent.<domain>.<KVKeyToken(pod)>"
  const RPCIndexBlocklist = "clustarr.rpc.indexarr.blocklist"
  const (
  	SubjectNakAdvisoryPrefix  = "$JS.EVENT.ADVISORY.CONSUMER.MSG_NAKED"
  	SubjectTermAdvisoryPrefix = "$JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED"
  	SubjectAckMetricPrefix    = "$JS.EVENT.METRIC.CONSUMER.ACK"
  )
  func TaskEventSubjects(t Topology) []string
  // ConsumerSpec gains:
  //	SampleFrequency string // "", "10%" or "100%": JetStream ack sampling (§8.2); metrics only
  //	Dispatched bool        // a task the manager dispatches and tracks (§5.1)
  // StreamAdmin gains:
  //	EnsureConsumer(ctx context.Context, c ConsumerSpec) error // creates or updates the durable and its dead-letter watcher
  ```

- [ ] **Step 1: Buckets.** In `records.go` add `agentRecordBuckets()` with the six specs of the
  Global Constraints (same helper shape as `recordBuckets()`), and have `Default()` append it.
  Cap constants beside them (`TransfersMaxBytes = 256 * MiB`, `TransfersMaxValueSize = 512 * KiB`,
  …).
- [ ] **Step 2: Streams and durables.** In `agents.go` declare the three streams of the Global
  Constraints (`CLUSTARR_INTAKE` and `CLUSTARR_WORK_ENGINE` `Durable: true`, `Discard: DiscardNew`,
  `Duplicates: time.Hour`; `CLUSTARR_TASK_EVENTS` not Durable, `Discard: DiscardOld`,
  `MaxAge: time.Hour`, `Duplicates: 0`, its `Subjects` from `TaskEventSubjects(t)` over the
  consumers marked `Dispatched` -- empty until A2.4 marks them, so in this task the stream's
  subjects are only the two `<prefix>.CLUSTARR_WORK_ENGINE.*` wildcards) and the three static
  durables. `Default()` appends them; `withDeadLetterWatchers` derives their watchers as for
  every other consumer.
- [ ] **Step 3: `SampleFrequency`.** `ConsumerConfig` (`topology_nats.go:114`) sets
  `jetstream.ConsumerConfig.SampleFrequency`; `Validate` accepts `""`, `"<n>%"` with
  `0 < n ≤ 100`; membus ignores it.
- [ ] **Step 4: `EnsureConsumer`.** natsbus: create-or-update the consumer from
  `ConsumerConfig(c)` on `c.Stream`, then its dead-letter watcher
  (`DeadLetterWatcherSpec(c)`), idempotent; membus: register the durable in its in-memory
  topology. Only the manager calls it (the DownloadClient controller, A3.7):
  `TestOnlyTheManagerEnsuresTopology` (split §5.15) gains it among its banned-in-agents calls
  (deferred).
- [ ] **Step 5: Single-node fit.** `ForSingleNode` keeps Durable streams on file at full size
  (it already does for `Durable: true`); `CLUSTARR_TASK_EVENTS` takes the 1 MiB memory floor.
  New file reservations: 1,040 MiB of buckets + 256 MiB + 64 MiB ≈ 1.33 GiB (§4.3).

**Tests (deferred to the batch):**

- `TestAgentRecordBucketsAreDurableHistoryOneAndBounded` (`pkg/events`): the six specs equal the
  table.
- `TestIntakeAndCommandStreamsAreDurableAndDiscardNew` (`pkg/events`).
- `TestTaskEventSubjectsListOnlyDispatchedConsumersAndNeverGt` (`pkg/events`).
- `TestEngineConsumerNamesAreInjectiveAndKVSafe` (`pkg/events`; client names that differ only
  in illegal bytes get different names).
- `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` (existing, `natsbus/singlenode_limits_test.go:74`,
  F1's 20 GiB server): passes with the new reservations.
- contract `EnsureConsumerIsIdempotentAndBindsTheWatcher` (`pkg/events/contracttest`, both buses).
- `TestSampleFrequencyReachesTheConsumerConfig` (`pkg/events/natsbus`).

- [ ] **Step 6: Commit.**

  ```bash
  git add pkg/events/agents.go pkg/events/natsbus/ensure_consumer.go
  git commit -m 'feat(events): ADR-0019 topology -- six agent-written records buckets, CLUSTARR_INTAKE, CLUSTARR_WORK_ENGINE and CLUSTARR_TASK_EVENTS, the intake and task-events durables, engine durables, subjects and Msg-Ids; ConsumerSpec.SampleFrequency and Dispatched; StreamAdmin.EnsureConsumer (A1.6)' -- pkg/events
  ```

---

### Task A1.7: `k8s.ReadOnly`, the agent-write metric, and the finalizer's field owner

**Spec:** §9.2 (`TestAgentClientIsReadOnly`'s wrapper), §6.8 (the item finalizer under
`clustarr`); ruling R13.

**Files:**

- Create: `pkg/k8s/readonly.go`
- Modify: `pkg/obs/metrics/domain.go` (`AgentWriteRefusedTotal`), `pkg/obs/metrics/metrics_test.go`'s
  `wantSeries` is a test file (deferred)

**Interfaces:**

- Produces:

  ```go
  // ErrAgentWrite is every write through a ReadOnly client (ADR-0019 §9.2).
  var ErrAgentWrite = errors.New("k8s: agents never write the Kubernetes API (ADR-0019)")
  // ReadOnly wraps c: reads pass through; Create, Update, Patch, Apply,
  // Delete, DeleteAllOf, Status() and SubResource(...) writers return
  // ErrAgentWrite and count clustarr_agent_write_refused_total{verb}.
  func ReadOnly(c client.Client) client.Client
  // DiscardingRecorder is an events.k8s.io recorder that records nothing.
  type DiscardingRecorder struct{}
  func (DiscardingRecorder) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...any)
  // metrics:
  var AgentWriteRefusedTotal *prometheus.CounterVec // clustarr_agent_write_refused_total{verb}
  ```

- [ ] **Step 1: Write `readonly.go`.** A struct embedding `client.Client` overriding every
  `client.Writer` method, `Status()` and `SubResource()` (returning a `client.SubResourceClient`
  whose `Get` passes through and whose `Create`, `Update`, `Patch` refuse), and `Apply`. It is
  not wired anywhere until A7.1 (R13). The `forbidigo` rule against `.Status().Patch` outside
  `pkg/k8s` is unaffected: this file is in `pkg/k8s`.
- [ ] **Step 2: Metric.** Register `AgentWriteRefusedTotal` in `pkg/obs/metrics/domain.go` with
  help "Kubernetes writes an agent attempted and the read-only client refused (ADR-0019); any
  value above zero is a bug".

**Tests (deferred to the batch):**

- `TestReadOnlyRefusesEveryWriterAndCounts` (`pkg/k8s`): each writer method, `Status().Patch`,
  `SubResource("scale").Update` and `Apply` return `ErrAgentWrite` and add one to the counter;
  `Get` and `List` pass through.
- `TestCatalogueMatchesTheAmendmentTable` and `wantSeries` (existing, `pkg/obs/metrics`): gain
  the new family.

- [ ] **Step 3: Commit.**

  ```bash
  git add pkg/k8s/readonly.go
  git commit -m 'feat(k8s): ReadOnly client and ErrAgentWrite, counted by clustarr_agent_write_refused_total; not wired until A7.1 (ADR-0019 A1.7)' -- pkg/k8s/readonly.go pkg/obs/metrics/domain.go
  ```

---

### Task A1.8: Wave A1 gate

- [ ] **Step 1: Build and generate.**

  ```bash
  cd /home/appkins/src/mediactl/clustarr-unify
  go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
  ```

- [ ] **Step 2: Wave greps.** Each must print what its comment says.

  ```bash
  grep -c 'selectablefield:JSONPath=`.status.downloadPhase`' api/catalog/v1alpha1/*_types.go | grep -v ':0' | wc -l   # 8
  grep -n 'payloadUnavailable' config/crd/bases/download.clustarr.io_downloads.yaml | wc -l                        # ≥ 1
  grep -rn 'clustarr-transfers\|clustarr-engines\|clustarr-imports\|clustarr-searches\|clustarr-item-metadata\|clustarr-indexer-health' pkg/events/*.go | grep -c 'Bucket'   # 6
  grep -rn '"CLUSTARR_INTAKE"\|"CLUSTARR_WORK_ENGINE"\|"CLUSTARR_TASK_EVENTS"' pkg/events/agents.go | wc -l       # 3
  grep -rn 'k8s.ReadOnly(' --include='*.go' . | grep -v _test.go | wc -l                                           # 0 (wired in A7.1)
  go list -deps ./cmd/manager | grep -cE 'ffgo|purego|onnx|anacrolix|pkg/download/usenet|pkg/relindex'               # 0
  ```

- [ ] **Step 3: Record in the ledger** (`.superpowers/unify/progress.md`): the A1 commits, the
  deferred tests by task, and any R2 fallback taken. The ledger is the controlling session's
  file: append only, and commit it path-scoped.

---
## Wave A2: manager intake and acks (A2.1-A2.6)

**Spec:** §4.2, §4.3 (intake), §5.3-§5.5 (capacity, admission, no agent), §8 (all of it), §7.7
(the projector and sink move), §11.2 row A2; rulings R8, R10, R14, R16.

**Depends on:** A1.8. **Order:** A2.1 and A2.2 may run beside each other (disjoint packages:
`pkg/presence` + the two writers, and `app/dispatch`); then A2.3 → A2.4 → A2.5 serially
(`app/intake`, `pkg/events`, `app/catalog/history`); A2.6 is the gate.

**What A2 delivers.** The manager's half of every report path the later waves use: who is
running (presence), what may be published (the dispatch ledger), the candidate inbox with
ack-after-decision, the scan intake's skeleton, delivery state from nak and term advisories, and
the DLQ projector and history sink as leader-only consumers in the manager. Nothing consumes the
inbox or the ledger until A3-A6.

---

### Task A2.1: Agent presence -- `pkg/presence`, a writer in every agent and in `cmd/markers`, a reader for the manager

**Spec:** §5.3 (the presence row), §5.5 (`NoAgent`, `NoCapableAgent`); ruling R14.

**Files:**

- Create: `pkg/presence/presence.go`
- Modify: `internal/cli/agent/run.go` (start the writer after `register`), `cmd/markers/main.go`
  (start the writer once the bus exists)

**Interfaces:**

- Consumes: A1.4 `schema.AgentPresence`; A1.6 `events.PresenceKey`; `events.BucketProgress`.
- Produces:

  ```go
  package presence // imports pkg/events and its schema only: cmd/markers links it

  const Interval = 30 * time.Second

  // Writer reports one agent process (ADR-0019 §5.3).
  type Writer struct {
  	KV           events.KV // clustarr-progress
  	Domain, Pod  string
  	Node         string // $NODE_NAME, "" when the installer does not set it
  	Version      string
  	Slots        map[string]int // per bound durable (events.SlotsFor)
  	Durables     []string
  	Capabilities func() map[string]string // e.g. "ffgo": "9.0.1", "data": "mounted", "providers": "tmdb,tvdb"
  	Now          func() time.Time
  }
  func (w *Writer) Run(ctx context.Context) error // writes now and every Interval; deletes its key when ctx ends (best effort)

  // Reader is the manager's 5 s cached view.
  type Reader struct {
  	KV  events.KV
  	TTL time.Duration // 0 is 5 s
  	Now func() time.Time
  }
  func (r *Reader) Domain(ctx context.Context, domain string) ([]schema.AgentPresence, error)
  func (r *Reader) Capable(ctx context.Context, domain, capability string) (bool, error)
  ```

- [ ] **Step 1: `pkg/presence`.** `Writer.Run` puts `schema.AgentPresence` JSON at
  `events.PresenceKey(Domain, Pod)` with a plain `Put` (one writer per key; the bucket's 10 min
  TTL retires a dead pod's key). `Reader` lists keys with prefix `agent.<domain>.` through
  `KV.Keys` and decodes each, caching the whole bucket for `TTL`.
- [ ] **Step 2: Agent writer.** In `internal/cli/agent/run.go`, after `register`, add an
  `k8s.EveryReplica` runnable running `presence.Writer{KV: bus.KV(events.BucketProgress),
  Domain: o.Domain, Pod: <PodName as command.go:138 derives it>, Node: os.Getenv("NODE_NAME"),
  Version: version.String(), Slots: <SlotsFor each consumer of agentdomain.Lookup(o.Domain)>,
  Durables: <those consumers>, Capabilities: <the domain's self-check facts: FFmpeg 9 present
  (ffruntime), /data mounted, configured providers>}`. Engines report domain `torrent-engine` or
  `usenet-engine` with no durables (their engine record is their capacity report).
- [ ] **Step 3: Markers writer.** In `cmd/markers/main.go`, after `busRef.Store(bus)`, start
  `presence.Writer{Domain: "markers", Durables: []string{events.ConsumerSegmentarrAnalyze}, …}`
  in a goroutine tied to `ctx`. `cmd/markers` gains no Kubernetes import
  (`TestBinaryImportsNoKubernetesClient`-style guards stay green).

**Tests (deferred to the batch):**

- `TestPresenceWritesAtStartAndEveryInterval` (`pkg/presence`, membus, fake clock).
- `TestThePresenceReaderCachesForFiveSeconds` (`pkg/presence`).
- `TestPresenceLinksNoKubernetes` (`pkg/presence`, `go list -deps`).
- `TestEveryAgentDomainWritesPresence` (`internal/cli/agent`): each domain's run adds the writer.

- [ ] **Step 4: Commit.**

  ```bash
  git add pkg/presence/presence.go
  git commit -m 'feat(presence): every agent and markers report presence in clustarr-progress; the manager reads it through a 5 s cache (ADR-0019 A2.1)' -- pkg/presence internal/cli/agent/run.go cmd/markers/main.go
  ```

---

### Task A2.2: `app/dispatch` -- the ledger, `Admit`, budgets, unattended detection, the delivery book

**Spec:** §5.4 (the ledger, the rule, pacing stays the Pacer's), §5.5 (unattended, `NoAgent`),
§8.3 (the delivery table), §8.4 ("Ledgers are rebuilt, never trusted"); rulings R8, R23.

**Files:**

- Create: `app/dispatch/{ledger.go,admit.go,unattended.go,delivery.go,rbac.go,doc.go}`
- Modify: `internal/cli/manager/run.go` (`register` builds one `*dispatch.Ledger` and passes it
  to the registrations that dispatch), `app/autoscale/extmetrics/gauge.go` (`QueueGauge` gains
  `Dispatch DispatchStats` and sets the two gauges), `app/autoscale/register.go` (passes the
  ledger), `pkg/obs/metrics/domain.go` (`DispatchWaiting`, `DispatchUnattended`), `Makefile`
  (`RBAC_PATHS_catalogarr` gains `./app/dispatch/...`; W6.9 moves it to `RBAC_PATHS_manager`)

**Interfaces:**

- Consumes: A1.3 `catalogv1alpha1.{Dispatch, DeliveryState, DeliveryReason*}`; A1.6
  `ConsumerSpec.Dispatched`; A2.1 `presence.Reader`; `extmetrics.StateCache` (W4.69, W4.100);
  `agentdomain.Domains()`.
- Produces:

  ```go
  package dispatch

  type Reason = string // catalogv1alpha1.DeliveryReason* values

  // Key is a dispatching CR and, for a CR with several dispatches, the sub
  // (an entry uid, "inspect"/"execute", "search").
  type Key struct{ Kind, Namespace, Name, Sub string }

  type Decision struct {
  	Admitted   bool
  	Reason     Reason // when !Admitted
  	Message    string // "waiting for catalogarr-search-normal: 16 in flight"
  	RetryAfter time.Duration
  }

  // Outstanding is one dispatch a CR's status says is in flight (seq > answeredSeq).
  type Outstanding struct {
  	Key Key
  	Seq int64
  }

  // Source rebuilds one durable's outstanding dispatches from the cache.
  type Source interface {
  	Durable() string
  	Rebuild(ctx context.Context, r client.Reader) ([]Outstanding, error)
  }

  type Options struct {
  	Topology events.Topology
  	States   ConsumerStater   // extmetrics.StateCache's Get, adapted
  	Presence *presence.Reader
  	Reader   client.Reader    // the manager's cache
  	Recorder k8sevents.EventRecorder // "clustarr-dispatch", on the manager's Pod
  	Pod      types.NamespacedName    // $POD_NAMESPACE/$POD_NAME
  	Now      func() time.Time
  }
  func New(o Options) *Ledger
  func (l *Ledger) Register(s Source)                     // before Start; one per rebuildable durable
  func (l *Ledger) Start(ctx context.Context) error        // leader-only: rebuild, then every 10 s unattended detection and metrics
  func (l *Ledger) NeedLeaderElection() bool              // true
  func (l *Ledger) Budget(durable string) int             // 2 × MaxAckPending (§5.4)
  func (l *Ledger) Admit(durable string, k Key, seq int64) Decision // reserves on admission; Rebuilding until rebuilt
  func (l *Ledger) Published(durable string, k Key, seq int64)      // the publish effect landed
  func (l *Ledger) Answered(durable string, k Key, seq int64)       // the planner incorporated (or closed) it
  func (l *Ledger) Waiting(durable string, k Key, reason Reason)    // the planner left the block waiting
  func (l *Ledger) Forget(k Key)                                    // the CR is gone
  func (l *Ledger) Unattended(durable string) bool
  // DispatchStats is what QueueGauge reads.
  func (l *Ledger) WaitingCount(durable string) int

  // DeliveryBook is the leader-local record of nak and term advisories (R8).
  type DeliveryBook struct{ /* map[Key]deliveryEntry, mutex */ }
  func NewDeliveryBook() *DeliveryBook
  func (b *DeliveryBook) Retrying(k Key, seq int64, deliveries int32, at time.Time, last bool)
  func (b *DeliveryBook) DeadLettered(k Key, seq int64, at time.Time)
  func (b *DeliveryBook) Lookup(k Key, seq int64) (catalogv1alpha1.DeliveryState, bool)
  func (b *DeliveryBook) Forget(k Key, seq int64)

  // Render sets d's destination and delivery from the planner's decision and
  // the book: waiting (with the reason), published, retrying, deadLettered;
  // nil delivery once answered (§8.3's table).
  func Render(d *catalogv1alpha1.Dispatch, destination string, dec *Decision, book *DeliveryBook, k Key)
  ```

- [ ] **Step 1: The ledger.** Per durable a set of `(Key, seq)` with a state (`reserved` with an
  expiry of 60 s, or `published`). `Admit` refuses `Rebuilding` until `Start` has rebuilt every
  registered source through the cache (R23: durables with no `Source` start empty);
  `NoAgent` when `Unattended(durable)` holds and the durable's outstanding count is at its
  budget; `NoCapableAgent` is the caller's (it passes the capability check's result through
  `Waiting`); otherwise admits while `outstanding < Budget(durable)`, else `Budget` with the
  message `"waiting for <durable>: <n> in flight"`. A reservation not confirmed by `Published`
  within 60 s lapses (the apply failed and the next pass decides again). `Answered` removes;
  R23's unrebuilt kinds also lapse after `AckWait × MaxDeliver` of their spec.
- [ ] **Step 2: Unattended detection** (`unattended.go`), every 10 s on the leader, for every
  consumer marked `Dispatched` in the topology: unattended when its `ConsumerState` has
  `Pending > 0`, `Waiting == 0` and an unchanged `Lag()` for 2 min, or when the presence reader
  finds no key for the consumer's domain (`agentdomain` lookup; engines and the transcode pools
  are skipped: their health is the engine records' and the encoder limits'). On each transition
  set `metrics.DispatchUnattended{consumer}` and record one Warning `DispatchUnattended` on the
  manager's Pod.
- [ ] **Step 3: `DeliveryBook` and `Render`** as in §8.3's table: the first nak and the nak at
  `MaxDeliver - 1` set `retrying` with `attempts` and `lastNakAt`; a term sets `deadLettered`;
  `Render` clamps `Message` to 256 and `Reason` to 64.
- [ ] **Step 4: Gauge and wiring.** `metrics.DispatchWaiting` and `DispatchUnattended` (gauges,
  label `consumer`); `QueueGauge.Update` sets both from `Dispatch` when it is set (W4.72's
  amendment). `internal/cli/manager/run.go`'s `register` builds the ledger with
  `Recorder: mgr.GetEventRecorder("clustarr-dispatch")` and `Pod` from `POD_NAMESPACE`/`POD_NAME`,
  `mgr.Add(ledger)`, and passes it to `remediationmanager.Options.Dispatch` (A3.3), the Search
  controller (A4.3), the ImportList controller (A6.2) and `autoscale.Options.Dispatch`.
  `rbac.go` carries `// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch`
  and `// +kubebuilder:rbac:groups="",resources=pods,verbs=get`.

**Tests (deferred to the batch):**

- `TestAdmitHoldsTheBudgetAndReportsTheReason` (`app/dispatch`): 16 admitted for an 8-MAP
  durable, the 17th `Budget` with the message.
- `TestAdmitRefusesUntilRebuilt` (`app/dispatch`): `Rebuilding` before `Start`'s rebuild.
- `TestTheLedgerIsRebuiltFromStatusNeverTrusted` (`app/dispatch`, envtest): a leader with stale
  in-memory state rebuilds from items' `searchDispatch` blocks.
- `TestAnUnconfirmedReservationLapses` (`app/dispatch`, fake clock).
- `TestUnattendedNeedsTwoQuietMinutesOrNoPresence` (`app/dispatch`).
- `TestRenderFollowsTheDeliveryTable` (`app/dispatch`, every row of §8.3).
- `TestQueueGaugeExportsDispatchWaitingAndUnattended` (`app/autoscale/extmetrics`).
- `wantSeries` (`pkg/obs/metrics`): the two families.

- [ ] **Step 5: Commit.**

  ```bash
  git add app/dispatch
  git commit -m 'feat(dispatch): the manager admits only what a destination can take -- ledger, budgets, unattended detection, the delivery book, clustarr_dispatch_waiting and _unattended (ADR-0019 A2.2)' -- app/dispatch internal/cli/manager/run.go app/autoscale pkg/obs/metrics/domain.go Makefile config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A2.3: `app/intake` -- the candidate inbox with ack-after-decision; the scan intake's skeleton

**Spec:** §4.3 (intake), §8.1 (S30), §8.4 ("Intake is acked only after its decision lands").

**Files:**

- Create: `app/intake/{inbox.go,candidates.go,scans.go,doc.go}`
- Modify: `internal/cli/manager/run.go` (`register` builds the inbox and adds both consumers as
  `k8s.LeaderOnly`), `pkg/obs/metrics/domain.go` (`IntakeCandidatesTotal`, `IntakeScanTotal`),
  `Makefile` (`RBAC_PATHS_catalogarr` gains `./app/intake/...`)

**Interfaces:**

- Consumes: A1.4 `schema.{Candidate, ScanObservation, ItemRef}`; A1.6
  `events.{ConsumerIntakeCandidate, ConsumerIntakeScan}`; `events.Bus.Subscribe` (bind-only,
  W4.90).
- Produces:

  ```go
  package intake

  const HandlerBudget = 60 * time.Second
  type Outcome string
  const (Incorporated Outcome = "incorporated"; Pended Outcome = "pended"; Refused Outcome = "refused")

  type OwnerKey struct{ Kind, Namespace, Name string }

  // Parked is one candidate held for its owner's next pass.
  type Parked struct {
  	MsgID     string
  	Candidate schema.Candidate
  	At        time.Time
  }

  // Inbox is leader-local: a leader crash loses it and the server redelivers (§8.4).
  type Inbox struct{ /* … */ }
  func NewInbox(max int) *Inbox                           // max = the consumer's MaxAckPending (64)
  func (in *Inbox) Take(owner OwnerKey) []Parked         // every unsettled candidate of owner, oldest first
  func (in *Inbox) Settle(msgID string, o Outcome)       // releases the handler waiting on msgID
  func (in *Inbox) Wakes() <-chan schema.ItemRef         // S30: the owner to enqueue at PriorityUser

  // CandidateConsumer is the leader-only catalogarr-intake-candidate consumer.
  type CandidateConsumer struct {
  	Bus      events.Bus
  	Inbox    *Inbox
  	Topology events.Topology
  }
  func (c *CandidateConsumer) Start(ctx context.Context) error
  func (c *CandidateConsumer) NeedLeaderElection() bool // true

  // ScanApplier decides one observation and writes; A6.1 implements it.
  type ScanApplier interface {
  	Apply(ctx context.Context, obs schema.ScanObservation) error
  }
  type ScanConsumer struct {
  	Bus      events.Bus
  	Applier  ScanApplier // nil: every observation is nak'd with 30 s (until A6.1 wires one)
  	Topology events.Topology
  }
  func (c *ScanConsumer) Start(ctx context.Context) error
  func (c *ScanConsumer) NeedLeaderElection() bool // true
  ```

- [ ] **Step 1: The inbox and the candidate handler.** The handler decodes `schema.Candidate`
  (a decode failure is `events.Discard`), parks it under its owner key
  (`OwnerKey{Kind: c.Owner.Kind, …}`) with the envelope's ID, sends `c.Owner` on `Wakes()`, and
  waits for `Settle` or `HandlerBudget`: settled returns nil (ack) and counts the outcome;
  the budget's lapse returns `events.Retry(10*time.Second, …)` and counts `timedOut`. The
  subscription's `Heartbeat` (10 s, A1.6) keeps the message in progress. A parked candidate whose
  handler gave up is dropped from the inbox. The inbox refuses more than `max` (the handler naks
  with 10 s), which `MaxAckPending` already bounds.
- [ ] **Step 2: The scan consumer.** Decode `schema.ScanObservation`; with a nil `Applier` nak 30 s;
  else `Applier.Apply`, then ack (§8.4: write, then ack; a crash re-applies an idempotent
  create-or-update). An `Apply` error marked transient (`remediation.IsTransient`'s rules,
  restated here as `intake.Transient(err)`) naks on the consumer's backoff; anything else is
  `events.Discard` with the error as the reason.
- [ ] **Step 3: Wiring.** `register` builds `intake.NewInbox(64)` and adds both consumers
  (`mgr.Add`, leader-only); `remediationmanager.Options.Inbox` (A3.3) takes the inbox. The scan
  consumer's `Applier` stays nil until A6.1.

**Tests (deferred to the batch):**

- `TestACandidateIsAckedOnlyAfterItsOwnerSettlesIt` (`app/intake`, membus).
- `TestAnUnsettledCandidateIsNakedAfterTheBudget` (`app/intake`, fake clock: nak with 10 s).
- `TestALeaderCrashRedeliversTheParkedCandidate` (`app/intake`, NATS contract).
- `TestTheScanConsumerWritesBeforeItAcks` (`app/intake`).
- `TestIntakeConsumersRunOnlyOnTheLeader` (`internal/cli/manager`).

- [ ] **Step 4: Commit.**

  ```bash
  git add app/intake
  git commit -m 'feat(intake): the leader-only candidate inbox acks after its owner decides; the scan intake skeleton (ADR-0019 A2.3)' -- app/intake internal/cli/manager/run.go pkg/obs/metrics/domain.go Makefile config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A2.4: The task-events intake -- nak and term advisories to delivery state; ack sampling; term reasons carry the `Clustarr-Id`

**Spec:** §8.2 (all), §8.3, §8.5 (the second net); rulings R8, R10, R16.

**Files:**

- Create: `app/intake/advisory/{intake.go,resolve.go,metrics.go,doc.go}`
- Modify: `pkg/events/bus.go` (`StreamAdmin.Message`, `CoreSubscriber`), `pkg/events/natsbus/admin.go`
  (`Message` through `jetstream.Stream.GetMsg`), `pkg/events/natsbus/natsbus.go`
  (`SubscribeCore`), `pkg/events/natsbus/message.go` (`Term` prefixes the `Clustarr-Id`),
  `pkg/events/membus/admin.go` (`Message` from its in-memory stream), `pkg/events/topology.go`
  (`Dispatched: true` and `SampleFrequency: "10%"` on the §5.1 durables, so
  `CLUSTARR_TASK_EVENTS`'s subjects fill in), `app/catalog/history/target.go`
  (`ResolveDispatch`), `app/catalog/worker/history/dlq.go` (exported `Annotate`, `Seen`),
  `internal/cli/manager/run.go`, `pkg/obs/metrics/domain.go`

**Interfaces:**

- Consumes: A1.6 (`StreamTaskEvents`, `ConsumerTaskEvents`, the advisory prefixes,
  `TaskEventSubjects`), A2.2 `DeliveryBook`, A2.3 `Inbox`'s pattern for wakes.
- Produces:

  ```go
  // pkg/events:
  //	StreamAdmin gains Message(ctx context.Context, stream string, seq uint64) (subject string, env *Envelope, err error)
  type CoreSubscriber interface {
  	SubscribeCore(ctx context.Context, subject string, h func(subject string, data []byte)) (stop func(), err error)
  }

  // app/catalog/history:
  // ResolveDispatch maps a task envelope to its CR and the dispatch seq and
  // sub its payload carries; ok false for a payload with no dispatch.
  func ResolveDispatch(subject string, env *events.Envelope) (t Target, seq int64, sub string, ok bool)

  // app/catalog/worker/history:
  func (p *DLQProjector) Annotate(ctx context.Context, t cataloghistory.Target, seq int64) error
  func (p *DLQProjector) Seen() <-chan string // Clustarr-Ids of DLQ copies it handled

  // app/intake/advisory:
  type Intake struct {
  	Bus       events.Bus
  	Admin     events.StreamAdmin
  	Book      *dispatch.DeliveryBook
  	Topology  events.Topology
  	Projector *historyworker.DLQProjector
  	Now       func() time.Time
  }
  func (i *Intake) Start(ctx context.Context) error // leader-only: the clustarr-task-events consumer and the ack-metric subscriptions
  func (i *Intake) NeedLeaderElection() bool        // true
  func (i *Intake) Wakes() <-chan cataloghistory.Target
  const CopyWait = 60 * time.Second
  ```

- [ ] **Step 1: Bus additions.** `Message` (natsbus: `js.Stream(ctx, stream)` then `GetMsg(ctx,
  seq)`, building the envelope with `EnvelopeFromHeaders`; `ErrKeyNotFound`-style
  `events.ErrMessageNotFound` when gone); `SubscribeCore` (natsbus `nc.Subscribe`; membus does not
  implement it). In `natsbus/message.go:146`'s `Term`, when the envelope's `Clustarr-Id` header
  is set, pass `id + " " + reason` (R10).
- [ ] **Step 2: Mark the dispatched durables.** In `topology.go`'s `defaultConsumers()` and
  `probe.go`'s `probeConsumers()`, set `Dispatched: true, SampleFrequency: "10%"` on every
  durable of the Global Constraints' "§5.1 dispatched" row; `TranscodeTaskConsumer` sets them
  too; `EngineConsumer` already sets `100%`. `TaskEventSubjects` now yields two subjects per
  static durable plus the two wildcards per dynamic stream.
- [ ] **Step 3: The intake.** For each `CLUSTARR_TASK_EVENTS` message decode the advisory JSON
  (`stream`, `consumer`, `stream_seq`, `deliveries`, `reason`):
  - **naked**: `Admin.Message(stream, stream_seq)` → `history.ResolveDispatch(subject, env)` →
    `Book.Retrying(key, seq, deliveries, now, last)` where `last` is `deliveries ==
    spec.MaxDeliver-1`, recorded only on the first nak and the last (other naks count
    `metricsOnly`); send the target on `Wakes()`.
  - **terminated**: the message is gone; read the Clustarr-Id from the reason's first field (R10)
    and resolve through the history resolvers' Msg-Id index (`ResolveDispatch` falls back to
    parsing the A1.6 Msg-Id shapes: `search/<uid>/<seq>`, `engine/<uid>/<seq>…`,
    `import/<uid>/<phase>/<seq>`); `Book.DeadLettered`; wake; then start a `CopyWait` timer: if
    `Projector.Seen()` has not reported that Clustarr-Id within 60 s, call
    `Projector.Annotate(ctx, target, seq)` (the second net, §8.5).
  - **stale** (the advisory's seq is below the CR's current dispatch seq) and **unresolvable**:
    ack, count `stale` / `unresolved`.
- [ ] **Step 4: Ack sampling.** For each `Dispatched` consumer, `SubscribeCore` on
  `$JS.EVENT.METRIC.CONSUMER.ACK.<stream>.<durable>` (dynamic streams with `*`) and observe
  `clustarr_task_ack_delay_seconds{consumer}` (from `ack_time`) and
  `clustarr_task_deliveries{consumer}`. Metrics only; nothing reaches a CR.
- [ ] **Step 5: Wiring.** `register` adds the intake leader-only, gives the projector to it, and
  passes `Intake.Wakes()` to `remediationmanager.Options.DeliveryWakes` (A3.3 maps a target to a
  `remediation.Key`).

**Tests (deferred to the batch):**

- `TestANakAdvisoryBecomesRetryingOnTheFirstAndLastNakOnly` (`app/intake/advisory`, NATS
  contract with a real server).
- `TestATermAdvisoryResolvesFromTheReasonsClustarrID` (`app/intake/advisory`).
- `TestATermWithNoDLQCopyIsAnnotatedAfterSixtySeconds` (`app/intake/advisory`, fake clock).
- `TestAStaleAdvisoryIsAckedAndDropped` (`app/intake/advisory`).
- `TestTermWithReasonCarriesTheClustarrID` (`pkg/events/natsbus`).
- `TestMessageReadsANakedMessageByStreamSeq` (contract, both buses).
- `TestAckSamplingIsMetricsOnly` (`app/intake/advisory`).
- `TestEveryDispatchedSubjectHasAResolver` (`app/catalog/history`): every subject builder of a
  `Dispatched` consumer resolves through `ResolveDispatch`.

- [ ] **Step 6: Commit.**

  ```bash
  git add app/intake/advisory
  git commit -m 'feat(intake): nak and term advisories become delivery state through CLUSTARR_TASK_EVENTS; ack sampling for metrics; terms carry the Clustarr-Id; a term with no DLQ copy is annotated (ADR-0019 A2.4)' -- app/intake/advisory pkg/events app/catalog/history/target.go app/catalog/worker/history/dlq.go internal/cli/manager/run.go pkg/obs/metrics/domain.go
  ```

---

### Task A2.5: The DLQ projector and the history sink move into the manager

**Spec:** §7.7 (the `events` domain keeps only the RSS matcher), §8.5, D12; split §3.5.3's
`events` row (superseded; F8.11's amendment records it).

**Files:**

- Modify: `app/catalog/worker/history/{dlq.go,sink.go}` (each gains `SetupLeaderOnly(mgr, bus)`;
  `SetupWithManager`'s `EveryReplica` path is deleted), `app/catalog/agent/events/register.go`
  (registers only the RSS matcher and, until A4.4, the redownload consumer),
  `app/catalog/manager/register.go` (a new `registerHistory(mgr, bus, inbox)` registering both,
  leader-only, recorders `catalogarr-history` and `clustarr-dlq-projector`, DLQ reader
  `history.DLQReaderFor(bus)`), `pkg/agentdomain/agentdomain.go` (`events` domain loses
  `ConsumerCatalogHistory` and `ConsumerDLQProjector`; `Fixed()` gains both, homed `manager`)

**Interfaces:**

- Consumes: W2.11's `historyworker.{NewSink, NewDLQProjector}`; A2.4's `Annotate`, `Seen`.
- Produces: `(*historyworker.DLQProjector).SetupLeaderOnly(mgr ctrl.Manager, bus events.Bus) error`,
  `(*historyworker.Sink).SetupLeaderOnly(mgr ctrl.Manager, bus events.Bus) error`. Durables,
  field manager and recorders unchanged (R7).

- [ ] **Step 1:** Move the two registrations; keep every RBAC marker in `app/catalog/worker/history`
  (the package is now linked by the manager only; A7.2 checks no agent root reaches it). The
  catalog agent's `Registration.Indexes` keeps `search.FieldIndexes()` and the matcher's until
  A3.9/A4.4.
- [ ] **Step 2:** `make manifests` and prove the roles did not move:
  `git diff --exit-code -- config/rbac charts/clustarr/templates/rbac.yaml` (the package stays in
  `RBAC_PATHS_catalogarr`; W6.9 puts it in `RBAC_PATHS_manager`).

**Tests (deferred to the batch):**

- `TestEveryConsumerHasExactlyOneHome` (W6.2's; amended): `catalogarr-history` and
  `clustarr-dlq-projector` are homed in the manager.
- `TestTheEventsDomainRunsOnlyTheRSSMatcher` (`app/catalog/agent/events`), after A4.4.
- `TestDLQProjectorAnnotatesAMediaFile` (F3.5's, rerun from its new home).

- [ ] **Step 3: Commit.**

  ```bash
  git commit -m 'refactor(history): the DLQ projector and the history sink run leader-only in the manager; the events domain keeps the RSS matcher (ADR-0019 A2.5)' -- app/catalog/worker/history app/catalog/agent/events/register.go app/catalog/manager/register.go pkg/agentdomain/agentdomain.go
  ```

---

### Task A2.6: Wave A2 gate

```bash
cd /home/appkins/src/mediactl/clustarr-unify
go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
grep -rn 'historyworker.NewSink\|historyworker.NewDLQProjector' app/catalog/agent/ | wc -l                 # 0
grep -rn 'Dispatched: *true' pkg/events/topology.go pkg/events/probe.go pkg/events/agents.go | wc -l       # ≥ 18
go list -deps ./cmd/markers | grep -c 'k8s.io/client-go\|controller-runtime'                               # 0
for r in app/catalog/agent/catalog app/catalog/agent/events app/catalog/agent/metadata app/import/agent app/indexer/agent app/caption/agent app/grab/agent/torrent app/grab/agent/usenet; do go list -deps ./$r | grep -cE 'app/(dispatch|intake)'; done   # all 0
```

Record in the ledger as A1.8 does.

---
## Wave A3: downloads (A3.1-A3.10)

**Spec:** §6 (all of it), §3.3, §3.4, §5.1 (client and engine choice), §5.2, §5.5 (engine not
Ready), §7.0, §7.7, §8.1 (S24, S27, S28, S30), §9.1 (`grabarr-engine` role), §11.1 (F4's three
notes), §11.2 row A3; rulings R4-R6, R9, R21, R25, R26.

**Depends on:** A2.6. **Order:** A3.1 and A3.2 may run beside each other (`pkg/relindex` +
`app/indexer`, and `app/catalog/controller/{series,comic}` + `app/remediation`; they share no
file). Then A3.3 → A3.4 → A3.5 serially. A3.6 and A3.7 serially after A3.5 (both under
`app/grab`). A3.8 after A3.5 (it shares `app/remediation/downloads`; run it after A3.7 when one
session owns both). A3.9 after A3.6-A3.8. A3.10 is the gate.

**What A3 delivers.** The Download kind stops being written or read by any controller or agent:
grabs are entries on their owners, run by the downloads stage's state machine; engines execute
commands and report records; imports run in two phases with the decision in the manager; the
blocklist lives in the release index. Automatic grabs reach entries only from A4 (Order and
dependencies, last paragraph).

---

### Task A3.1: The blocklist in the release index -- the table, `Store` methods, the `blocklist` RPC verb, block state in every answer

**Spec:** §6.14 (all of it), §6.11 (the search worker's and the RSS matcher's blocklist rows),
§3.5 P8, P22.

**Files:**

- Create: `pkg/relindex/blocklist.go` (SQLite), `pkg/relindex/postgres_blocklist.go`,
  `app/indexer/blocklist/{service.go,doc.go}`
- Modify: `pkg/relindex/store.go` (`Store` gains four methods, `Block` type),
  `pkg/relindex/schema.go` (`schemaVersion = 2`, `ddlV2`), `pkg/relindex/postgres_schema.go`
  (`pgSchemaVersion = 2`, `pgDDLV2`), `app/indexer/agent/sweeper.go` (a second pass, `PruneBlocks`),
  `app/indexer/search/service.go` (the verbs table gains `RPCIndexBlocklist`; `Search` marks block
  state), `app/indexer/search/fanout.go` (passes `req.Scope`), `app/indexer/query/service.go`
  (marks block state), `app/indexer/worker/rss/{worker.go,publish.go}` (each firehose release
  carries `Blocks`), `pkg/decision/types.go` (doc of `Target.Blocklist`: a lookup over the
  answer's block state), `app/catalog/worker/search/{worker.go,blocklist.go}` and
  `app/catalog/worker/rssmatcher/{handler.go,resolve.go}` (the Download-label blocklist replaced
  by the answers' block state)

**Interfaces:**

- Consumes: A1.4 (`schema.{ReleaseBlock, BlocklistRequest, BlocklistResponse, BlockRow,
  BlockScopeOf, BlockScopeGlobal}`, `Release.Blocked`, `Release.Blocks`, `SearchRequest.Scope`,
  `QueryRequest.Scope`); A1.6 `events.RPCIndexBlocklist`.
- Produces:

  ```go
  package relindex
  // Block is one blocklist row (ADR-0019 §6.14). Unblocked rows are
  // tombstones kept until Until so a late block with a lower Seq is a no-op.
  type Block struct {
  	Scope, InfoHash, Indexer, GUID, Title, Protocol, Reason, EntryID string
  	Seq                                                              int64
  	BlockedAt, Until                                                 time.Time
  	Unblocked                                                        bool
  }
  type BlockKey struct{ InfoHash, Indexer, GUID string }
  // Store gains:
  //	Block(ctx context.Context, b Block) (applied bool, err error)   // upsert, no-op when a row for the key has Seq >= b.Seq
  //	Unblock(ctx context.Context, b Block) (applied bool, err error) // tombstone, fenced the same way
  //	BlockState(ctx context.Context, keys []BlockKey, now time.Time) (map[int][]Block, error) // live rows (not unblocked, Until > now), every scope, by input index
  //	ListBlocks(ctx context.Context, scope string, limit, offset int) ([]Block, error)
  //	PruneBlocks(ctx context.Context, now time.Time) (int, error)

  package blocklist // app/indexer/blocklist
  type Service struct {
  	Store relindex.Store
  	Now   func() time.Time
  }
  func (s *Service) Handle(ctx context.Context, req schema.BlocklistRequest) schema.BlocklistResponse
  ```

- [ ] **Step 1: The table, both engines.** SQLite `ddlV2` (the migration from 1 runs `ddlV2` only):

  ```sql
  CREATE TABLE blocklist (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    scope      TEXT    NOT NULL,
    info_hash  TEXT    NOT NULL DEFAULT '',
    indexer    TEXT    NOT NULL DEFAULT '',
    guid       TEXT    NOT NULL DEFAULT '',
    title      TEXT    NOT NULL DEFAULT '',
    protocol   TEXT    NOT NULL DEFAULT '',
    reason     TEXT    NOT NULL,
    entry_id   TEXT    NOT NULL DEFAULT '',
    seq        INTEGER NOT NULL,
    blocked_at INTEGER NOT NULL,
    until      INTEGER NOT NULL,
    unblocked  INTEGER NOT NULL DEFAULT 0
  );
  CREATE UNIQUE INDEX blocklist_scope_hash ON blocklist(scope, info_hash) WHERE info_hash <> '';
  CREATE UNIQUE INDEX blocklist_scope_guid ON blocklist(scope, indexer, guid) WHERE guid <> '';
  CREATE INDEX blocklist_until ON blocklist(until);
  ```

  Postgres `pgDDLV2`: the same columns as `bigserial`, `text`, `bigint`, `timestamptz`,
  `boolean`, with the same partial unique indexes; migrated under `pgAdvisoryLockKey`. The table
  sits outside the 72 h release retention (`Prune` never touches it).
- [ ] **Step 2: `Block`, `Unblock`.** Upsert on whichever unique key the row has (the hash
  when known, else indexer and guid), in one transaction: when an existing row for the key has
  `seq >= b.Seq`, return `applied=false` and write nothing (a late block cannot undo a later
  unblock); `Until` is `BlockedAt + 90 d` when zero. `Unblock` writes the same row with
  `unblocked=1` and its own `seq`, `until = now + 90 d`. `BlockState` queries both keys for the
  input releases and returns live rows; `PruneBlocks` deletes rows with `until <= now`.
  `storetest.Run` gains the cases (deferred).
- [ ] **Step 3: The RPC verb.** `app/indexer/blocklist.Service.Handle`: `block` → `Store.Block`,
  `unblock` → `Store.Unblock`, `list` → `Store.ListBlocks`; `Applied` and `Stale` (`!applied`)
  in the reply; a store error is the reply's `Error`. Register it in `search.Serve`'s verbs table
  as `{events.RPCIndexBlocklist, "indexarr.rpc.serve.blocklist", …}` with the service built in
  `app/indexer/agent/workers.go` beside `query.Service`. The verb never decides: it persists what
  the manager asked.
- [ ] **Step 4: Block state in every answer.** `search.Service.Search` (after merge) and
  `query.Service.Handle` call `BlockState` for the answer's releases and set `Release.Blocked`
  to the row for `req.Scope`, else the global (`*`) row; the RSS worker, before
  `PublishReleases`, sets `Release.Blocks` to every live row of each release (almost always
  none). A `BlockState` error leaves the fields empty and logs at Warn (the manager re-checks
  its own tombstones, R7).
- [ ] **Step 5: The agents read block state, not Downloads.** In `app/catalog/worker/search`
  delete `LoadBlocklist` and the `Blocklist` type (`blocklist.go:111-160`); `Target.Blocklist`
  becomes `func(infohash, title string) bool` over a map built from the answer's
  `Release.Blocked` rows (the search request's `Scope` is
  `schema.BlockScopeOf(<the item's ItemRef>)`). In the RSS matcher drop `search.LoadBlocklist`
  (`handler.go:226`) and build the lookup from `rel.Blocks` filtered to the matched item's scope
  or `*`. `IndexDownloadTarget` and `FieldIndexes` stay until A3.9 (the queue still reads
  Downloads until then).
- [ ] **Step 6: Sweeper.** `indexSweeper.pruneOnce` also calls `PruneBlocks(now)`.

**Tests (deferred to the batch):**

- `storetest.Run` cases (both engines): `BlockIsFencedBySeq`, `UnblockLeavesATombstoneALateBlockCannotUndo`,
  `BlockStateMatchesByHashOrByIndexerAndGUID`, `BlockStateIgnoresExpiredAndUnblockedRows`,
  `PruneBlocksDeletesOnlyExpiredRows`, `PruneLeavesTheBlocklistAlone`.
- `TestTheMigrationFromVersionOneAddsTheBlocklist` (`pkg/relindex`, both engines).
- `TestTheBlocklistVerbPersistsWhatItIsAsked` (`app/indexer/blocklist`).
- `TestASearchAnswerMarksABlockedRelease` / `TestAQueryAnswerMarksABlockedRelease`
  (`app/indexer/search`, `app/indexer/query`).
- `TestTheFirehoseCarriesEveryScopeThatBlocksARelease` (`app/indexer/worker/rss`).
- `TestTheSearchWorkerRejectsABlockedReleaseFromTheAnswer` (`app/catalog/worker/search`;
  `decision.ReasonBlocklisted`).
- `TestTheRSSMatcherKeepsOnlyItsItemsAndGlobalBlocks` (`app/catalog/worker/rssmatcher`).
- e2e `TestABlockedReleaseShowsBlockedInASearch` (§6.13; Phase H).

- [ ] **Step 7: Commit.**

  ```bash
  git add pkg/relindex/blocklist.go pkg/relindex/postgres_blocklist.go app/indexer/blocklist
  git commit -m 'feat(relindex): the blocklist lives in the release index -- a seq-fenced table in both engines, the clustarr.rpc.indexarr.blocklist verb, block state in every search, query and firehose answer (ADR-0019 A3.1)' -- pkg/relindex app/indexer app/catalog/worker/search app/catalog/worker/rssmatcher pkg/decision/types.go
  ```

---

### Task A3.2: Series and Comic are loop item keys

**Spec:** §6.1 ("Series and Comic join the loop as item keys, moved as F4.2 moved Movie and
Episode"), §11.2 row A3; loop §3.12 (the item path), F4.2's pattern (`ReconcileItem`,
`Watches()`, `itemstatus.Apply`).

**Files:**

- Modify: `app/remediation/itemkeys.go` (`KindSeries`, `KindComic`; `ItemKind` maps
  `MediaKindSeries`, `MediaKindComic`), `app/remediation/indexes.go` (`RegisterIndexes` calls
  `series.RegisterIndexes` and the new `comic.RegisterIndexes`; F4.1's deviation 5 no longer
  holds, since the Series controller is gone), `app/remediation/manager/items.go` (two entries),
  `app/catalog/controller/series/reconciler.go`, `app/catalog/controller/comic/reconciler.go`,
  `app/catalog/manager/register.go` (`registerControllers` and `registerNonVideoControllers` drop
  the two), `test/guards/mediafilewatch_test.go` (untouched: neither kind watches MediaFile; a
  test file anyway)

**Interfaces:**

- Consumes: F4.2 `rollup.{Item, Watch, Self}`, `itemstatus.{Apply, Requeue}`; F4.1
  `remediation.ItemKind`.
- Produces: `remediation.KindSeries KeyKind = "Series"`, `remediation.KindComic KeyKind =
  "Comic"`; `series.(*Reconciler).{ReconcileItem, Watches}`, `comic.(*Reconciler).{ReconcileItem,
  Watches}`; `comic.RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error`
  (the Issue-by-`spec.comicRef` index, today inline in `comic.SetupWithManager:125`). The
  controller names `series` and `comic` are gone (loop §9 D29's rule, extended: record it in
  F8.11's amendments); recorders stay `series` and `comic`.

- [ ] **Step 1: Series.** Rename `Reconcile(ctx, req)` to `ReconcileItem(ctx, nn)`; delete
  `SetupWithManager`; add `Watches()` returning `{Series, rollup.Self, seriesPredicate() ∪
  k8s.AnnotationsChanged(catalogv1alpha1.DownloadIntentAnnotations()...)}`,
  `{Episode, mapEpisodeToSeries (spec.seriesRef), episodeRollupChanged()}` and
  `{RootFolder, r.mapRootFolder, k8s.GenerationChanged()}`. Convert the three
  `k8s.PatchStatus(..., ManagerCatalogarr, Series…)` sites (`reconciler.go:372,400,500`) to
  `itemstatus.Apply` with `reassertKnownStatus` on the two early returns, as F4.2 did for Movie.
  The `catalogarr-series` Episode writes and the `catalogarr-classify` spec patch stay as they
  are.
- [ ] **Step 2: Comic.** The same: `Watches()` returns `{Comic, rollup.Self, comicPredicate() ∪
  intent annotations}` and `{Issue, mapIssueToComic (spec.comicRef), the HasFile predicate}`;
  `comic.RegisterIndexes` registers the Issue index under its existing name
  (`issueByComicRefIndexKey`); the three status sites (`:280,300,350`) go through
  `itemstatus.Apply`; the `catalogarr-fanout` Issue writes stay.
- [ ] **Step 3: Register.** Add `KindSeries: &series.Reconciler{…, Recorder: recorder("series")}`
  and `KindComic: &comic.Reconciler{…}` to `items()`; remove both from
  `app/catalog/manager/register.go`; `remediation.RegisterIndexes` registers both kinds' indexes
  once (split §3.5.2 step 9: one registrar per process).

**Tests (deferred to the batch):**

- `TestSeriesAndComicAreItemKeys` (`app/remediation`): `ItemKind(MediaKindSeries)` is
  `KindSeries`; `items()` holds both.
- `TestManagerFieldIndexes` (W5.7's; amended `want`): `.spec.seriesRef` and the Issue-by-comic
  index registered once, by the loop.
- `TestAnEpisodeHasFileFlipWakesItsSeriesKey` (`app/remediation`, envtest).
- `TestControllerNamesAreUniqueAcrossTheBinary` (W5.15's): the `series` and `comic` names gone,
  its floor re-checked.
- The `series` and `comic` packages' existing reconciler tests: `Reconcile` → `ReconcileItem`
  (rewrite).

- [ ] **Step 4: Commit.**

  ```bash
  git commit -m 'feat(remediation): Series and Comic are the loop'"'"'s item keys -- ReconcileItem and Watches replace their controllers, item status by compare-and-swap (ADR-0019 A3.2)' -- app/remediation app/catalog/controller/series app/catalog/controller/comic app/catalog/manager/register.go
  ```

---

### Task A3.3: Item stages -- `itempass`, `remediation.ItemStage`, one apply per manager per pass, the item effects, `remediation.item.download`, S24

**Spec:** §7.0 (one pass, one apply per manager, CAS-chained, skipped only when equal at its own
paths), §6.11 (the index `remediation.item.download`, S24), §8.1 (S24-S30), §6.8 (the
finalizer), §6.7 ("An item key that is NotFound in the cache…"); ruling R5.

**Files:**

- Create: `app/catalog/controller/itempass/{itempass.go,doc.go}`,
  `app/remediation/{itemstage.go,itemeffects.go,itemsources.go}`,
  `app/remediation/dlindex/{dlindex.go,doc.go}`, `pkg/k8s/patch_unstructured.go`
- Modify: `app/remediation/items.go` (`ItemReconciler` runs stages around `ReconcileItem`;
  `Watch` adds S24 and the channel sources), `app/remediation/indexes.go` (registers `dlindex`),
  `app/remediation/manager/register.go` (`Options` gains `Dispatch`, `Inbox`, `DeliveryWakes`,
  `Stages`; `Register` builds `ItemReconciler{Items, Stages, Env, Dispatch, Book}`),
  `app/catalog/controller/itemstatus/apply.go` (`ApplySet`, `SetEqual`; `Apply` reports the
  landed resourceVersion to the pass), `internal/cli/manager/run.go`

**Interfaces:**

- Consumes: A2.2 `dispatch.{Ledger, DeliveryBook, Render}`; A2.3 `intake.Inbox`; A2.4
  `advisory.Intake.Wakes()`; F4.2 `ItemReconciler`, `rollup.Item`, `itemstatus.Apply`.
- Produces:

  ```go
  package itempass // app/catalog/controller/itempass: imported by the item packages; imports api types and pkg/k8s only

  // Contribution is what the item stages decided for the catalogarr apply,
  // which each kind's own renderer folds into its one catalogarr status
  // (ADR-0019 §7.0). nil fields are "this pass decided nothing: re-send the
  // stored value".
  type Contribution struct {
  	Downloads       *[]catalogv1alpha1.DownloadEntry
  	DownloadPhase   *commonv1.DownloadPhase
  	DownloadNonces  *catalogv1alpha1.DownloadNonces
  	LegacyDownloads *catalogv1alpha1.LegacyDownloads // release N (A8.1)
  	// Views on an Episode or Issue, from its container's entries (§6.1):
  	Covering          []catalogv1alpha1.DownloadEntry
  	ActiveDownloadRef *string
  	DonorOpen         bool
  }

  // Set is one other field manager's complete set on the item (R5): status
  // JSON field names to values; a name mapped to nil is sent as absent.
  type Set struct {
  	Manager k8s.FieldManager
  	Fields  map[string]any
  }

  // Pass rides the context of one item pass.
  type Pass struct {
  	Contribution Contribution
  	landedRV     string
  	landed       bool
  }
  func With(ctx context.Context, p *Pass) context.Context
  func From(ctx context.Context) *Pass                // nil outside a staged pass (tests, F4's harness)
  func (p *Pass) Landed(rv string)                    // itemstatus.Apply calls it when the catalogarr apply landed
  func (p *Pass) LandedRV() (string, bool)

  // Renderer helpers every item kind calls on every catalogarr apply site,
  // the deletion path included (Review Focus 4):
  func Downloads(p *Pass, stored []catalogv1alpha1.DownloadEntry) []catalogv1alpha1.DownloadEntry
  func Phase(p *Pass, stored commonv1.DownloadPhase) commonv1.DownloadPhase
  func Nonces(p *Pass, stored *catalogv1alpha1.DownloadNonces) *catalogv1alpha1.DownloadNonces

  package itemstatus // gains:
  func ApplySet(ctx context.Context, c client.Client, item client.Object, rv string, s itempass.Set) (newRV string, conflicted bool, err error)
  func SetEqual(item client.Object, s itempass.Set) bool

  package k8s // gains:
  func PatchStatusUnstructured(ctx context.Context, c client.Client, fm FieldManager, u *unstructured.Unstructured) (*unstructured.Unstructured, error)

  package remediation // gains:
  type ItemStageName string
  const (StageSearch ItemStageName = "search"; StageGrab = "grab"; StageDownloads = "downloads"; StageMetadata = "metadata"; StageArtwork = "artwork"; StageOverlay = "overlay")
  // StageOrder: grab decides new entries from candidates, downloads runs the
  // state machine over stored and new entries, search reads the downloads
  // outcome (redownloads, R25), then metadata, artwork and overlay.
  var StageOrder = []ItemStageName{StageGrab, StageDownloads, StageSearch, StageMetadata, StageArtwork, StageOverlay}

  type ItemView struct {
  	Key        Key
  	Item       client.Object // the cached item, deep-copied; nil when the owner is gone (convergence only)
  	Now        metav1.Time
  	NewEntries []catalogv1alpha1.DownloadEntry // the grab stage's decisions this pass
  	Downloads  *DownloadsOutcome               // the downloads stage's, for search
  }
  type DownloadsOutcome struct {
  	Entries     []catalogv1alpha1.DownloadEntry
  	Redownloads []schema.ItemRef // items a Failed or Blocklisted entry asks to search (R25)
  }
  type ItemResult struct {
  	Contribution itempass.Contribution
  	Sets         []itempass.Set
  	NewEntries   []catalogv1alpha1.DownloadEntry
  	Downloads    *DownloadsOutcome
  	Effects      []Effect
  	Events       []ItemEvent
  	Due          time.Time
  	Again        bool
  }
  type ItemEvent struct{ Recorder, Type, Reason, Action, Message string; On client.Object } // On nil: the item
  type ItemStage interface {
  	Name() ItemStageName
  	Applies(kind KeyKind) bool
  	Plan(ctx context.Context, env *Env, v *ItemView) (ItemResult, error)
  }
  // OwnerGone is implemented by the downloads stage: a key NotFound in the
  // cache that the unclaimed index holds is decided on the APIReader's answer (§6.7).
  type OwnerGone interface {
  	Holds(k Key) bool
  	Gone(ctx context.Context, env *Env, k Key) ([]Effect, []ItemEvent, error)
  }

  // Item effects (itemeffects.go), run in order after every apply landed:
  type EnsureFinalizer struct{ Name string }   // field manager clustarr (k8s.EnsureFinalizer)
  type RemoveFinalizer struct{ Name string }
  type DispatchPublish struct {                 // a Publish whose success confirms the ledger
  	Publish
  	Durable string
  	Ledger  dispatch.Key
  	Seq     int64
  }
  type DispatchAnswered struct{ Durable string; Ledger dispatch.Key; Seq int64 }
  type RPC struct {                             // request and reply, Timeout bounded
  	Subject string
  	Request any
  	Reply   any
  	Timeout time.Duration
  	Done    func(reply any, err error)          // records into a leader-local book (R9)
  }
  type SafeRemove struct{ Path string }        // fsops.SafeRemove(env.DataDir, Path) through env.IO
  type ApplyMediaFileSpec struct {             // specwrite.ApplyFacts under importarr-worker
  	Namespace, Name, RV string
  	Ref                 commonv1.MediaRef
  	Path                string
  	SizeBytes           int64
  	ModTime             time.Time
  	Frozen              mediafilespec.Frozen
  }
  type DeleteObject struct {                   // a Delete with UID (and, when set, resourceVersion) preconditions
  	Object client.Object
  	UID    types.UID
  	RV     string
  }
  type SettleCandidate struct{ MsgID string; Outcome intake.Outcome }
  type CountGrab struct{ Namespace, IndexerRef, Key string; At time.Time } // limits.CountGrabAt keyed by entry uid (§6.11)
  ```

  `remediation.ItemReconciler` gains `Stages []ItemStage`, `Env *Env`, `Dispatch
  *dispatch.Ledger`, `Book *dispatch.DeliveryBook`, `Client client.Client`, `Recorder
  func(string) k8sevents.EventRecorder`. Package `dlindex`: `const Item =
  "remediation.item.download"`, `func Register(ctx, idx client.FieldIndexer) error` (the six
  owner kinds; values: every entry's `id` and `uid`), `func OwnerOf(ctx context.Context, r
  client.Reader, namespace, idOrUID string) (schema.ItemRef, bool, error)`,
  `func PinnedTo(ctx context.Context, r client.Reader, namespace, engine string) ([]schema.ItemRef, error)`
  (a second value per entry, `engine:<entry.engine>`).

- [ ] **Step 1: `itempass`.** Write the package as above. The three helpers return the pass's
  decision when set, else the stored value, so a kind renders `downloads`, `downloadPhase` and
  `downloadNonces` on **every** `catalogarr` apply: the main path, every early return
  (`QueueFull`, `RootFolderNotFound`, …) and the deletion path (A3.5 adds one), whether or not a
  stage ran -- the complete-declaration rule (CLAUDE.md; Review Focus 4).
- [ ] **Step 2: `itemstatus`.** `Apply` keeps its signature and, when it lands, calls
  `itempass.From(ctx).Landed(<the applied object's resourceVersion>)` (`k8s.PatchStatus` returns
  the applied configuration; read `ResourceVersion` from it). `ApplySet` builds an
  `unstructured.Unstructured` with the item's `apiVersion`, `kind`, `name`, `namespace`,
  `resourceVersion: rv` and `status: s.Fields`, applies it through
  `k8s.PatchStatusUnstructured(ctx, c, s.Manager, u)` (forced ownership, as every apply in
  `pkg/k8s`), returns the new resourceVersion, and maps a Conflict to `conflicted`. `SetEqual`
  converts the cached item with `runtime.DefaultUnstructuredConverter.ToUnstructured`, takes
  `status`, and compares each of `s.Fields`' names: absent equals nil; values compare after a
  JSON round trip (so typed and map forms compare equal).
- [ ] **Step 3: The staged pass** (`items.go`, `itemstage.go`). `ItemReconciler.Reconcile(ctx,
  k)`:
  1. Read the item from the cache. NotFound: if a stage implementing `OwnerGone` `Holds(k)`, run
     `Gone` (its APIReader evidence and effects) and return; else return (F4.2's behaviour).
  2. Build `ItemView{Key: k, Item: <deep copy>, Now: <UTC, whole seconds>}`; run each stage of
     `StageOrder` that `Applies(k.Kind)` in isolation (a panic or error marks that stage failed
     for this pass, keeps the stored values through nil contribution fields, and requeues after
     `plannerFailedRetry`, loop §3.6); thread `NewEntries` from grab to downloads and
     `Downloads` from downloads to search.
  3. Merge the contributions (each field from at most one stage) and the sets (by manager:
     `catalogarr-grab` gets the grab stage's pending fields and the search stage's bookkeeping
     merged into one `Set`; two stages writing the same field name is a programming error:
     panic in isolation).
  4. `ctx = itempass.With(ctx, &itempass.Pass{Contribution: merged})`; call
     `Items[k.Kind].ReconcileItem(ctx, nn)`. If the pass did not land (a conflict, an error, a
     path that returned before applying), stop: no set, no effect; return the kind's result.
  5. For each merged `Set` in manager order (`catalogarr-grab`, `catalogarr-metadata`,
     `catalogarr-artwork`): skip when `itemstatus.SetEqual(cached, set)`; else
     `ApplySet(…, rv, set)` with `rv` the previous apply's; a conflict ends the pass with a 1 s
     requeue at priority 0 (the next pass re-renders every set from a fresh view, §7.0).
  6. Run every stage's effects in `StageOrder`, each stage's in its own order, first failure
     stops that stage's batch; `Publish`/`History` as the file path's `runEffect` does
     (`effects.go`); the new kinds as declared. `DispatchPublish` calls
     `Dispatch.Published` on success. Then emit the `ItemEvent`s (recorder named per event, the
     kind's recorder when empty).
  7. Requeue at the earliest of the kind's result, every stage's `Due` (floored at 1 s,
     `PriorityTimed`), `Again` (1 s, priority 0), and a failed effect's transient backoff.
- [ ] **Step 4: Sources** (`itemsources.go`, added in `ItemReconciler.Watch`):
  - **S24** a `TypedFuncs` handler on Series and on Comic whose `UpdateFunc` compares
    `DownloadsSignature(old)` with `DownloadsSignature(new)` (`DownloadsSignature` hashes each
    entry's `id`, `phase` and covered numbers, and each pending grab's covered numbers) and
    enqueues every covered Episode (listing the series' Episodes through `.spec.seriesRef` and
    matching `spec.seasonNumber`/`spec.episodeNumber`, plus an entry's single-episode target by
    name) or Issue (`spec.comicRef` and `spec.number`) of old and new.
  - **S30** `source.Channel` over `Inbox.Wakes()`, mapping the `ItemRef` to a `Key` and adding
    at `PriorityUser`.
  - **Delivery wakes**: `source.Channel` over `DeliveryWakes`, mapping a `history.Target` of an
    item kind to its `Key`.
  - Each stage that is a `remediation.Watcher` adds its own sources (records wakers, the
    objindex) through the existing `Sources(mgr)` hook.
  - The item kinds' Self watches gain `k8s.AnnotationsChanged(DownloadIntentAnnotations()...)`
    (A3.2 did Series and Comic; this task does the six F4 kinds), enqueued at `PriorityUser`.
- [ ] **Step 5: `dlindex`** registered by `remediation.RegisterIndexes`.
- [ ] **Step 6: Wiring.** `remediationmanager.Options` gains `Dispatch *dispatch.Ledger`,
  `Inbox *intake.Inbox`, `DeliveryWakes <-chan cataloghistory.Target`, `Book
  *dispatch.DeliveryBook`; `Register` builds `ItemReconciler{Items: items(…), Stages: Stages(d),
  Env: env, …}` where `Stages(d Deps) []remediation.ItemStage` returns nothing yet (A3.5 adds the
  downloads stage, A4 grab and search, A5 metadata, artwork and overlay).
  `internal/cli/manager/run.go` passes them.

**Tests (deferred to the batch):**

- `TestEveryCatalogarrApplySendsTheDownloads` (`app/remediation`, envtest, per item kind, against
  an item **already in its steady state** with entries): drive each early-return path and the
  deletion path; `managedFields` for `catalogarr` still own `status.downloads`, and the values
  are unchanged.
- `TestOneApplyPerManagerCASChained` (`app/remediation`, envtest): a pass with a grab set and a
  metadata set makes three applies, each preconditioned on the previous one's resourceVersion;
  an interleaved write by another manager makes the second a Conflict and the next pass completes
  it (§7.0).
- `TestASetEqualAtItsPathsIsSkipped` (`app/remediation`).
- `TestAStageThatPanicsKeepsItsStoredValues` (`app/remediation`).
- `TestApplySetReleasesAFieldItStopsSending` (`app/catalog/controller/itemstatus`, envtest:
  `pendingGrab` cleared by omission under `catalogarr-grab`, the other managers' fields kept).
- `TestS24WakesOnlyTheCoveredEpisodes` (`app/remediation`).
- `TestDlindexFindsTheOwnerByEntryIDOrUID` (`app/remediation/dlindex`).
- `TestManagerFieldIndexes` (W5.7's; amended `want`: `remediation.item.download`).

- [ ] **Step 7: Commit.**

  ```bash
  git add app/catalog/controller/itempass app/remediation/itemstage.go app/remediation/itemeffects.go app/remediation/itemsources.go app/remediation/dlindex pkg/k8s/patch_unstructured.go
  git commit -m 'feat(remediation): item stages -- planners beside the rollups, one apply per field manager per pass chained by resourceVersion, item effects, the remediation.item.download index and S24 (ADR-0019 A3.3)' -- app/catalog/controller/itempass app/catalog/controller/itemstatus app/remediation pkg/k8s/patch_unstructured.go internal/cli/manager/run.go
  ```

---

### Task A3.4: `app/grab/lifecycle` -- the grab state machine, pure

**Spec:** §6.4 (every row), §6.7 (convergence, both ways; data safety), §6.8 (deletion, R-6),
§6.10 (intents), §6.14 (scope by reason, quarantine), §5.1 (choosing the client and the
ordinal); rulings R4, R21, R25.

**Files:**

- Create: `app/grab/lifecycle/{doc.go,view.go,plan.go,transitions.go,converge.go,intents.go,choose.go,scope.go,constants.go}`

**Interfaces:**

- Consumes: A1.2 and A1.4 types only; `pkg/names` (`HashOrdinal`); `pkg/records` (`NextSeq`).
  It imports no client, no bus and no clock (`TestLifecycleIsPure`, a `go list -deps` guard).
- Produces:

  ```go
  package lifecycle

  const (
  	BlockQuarantine       = time.Hour
  	UnclaimedGrace        = 10 * time.Minute
  	UnidentifiedGrace     = 10 * time.Minute
  	EngineTeardownTimeout = 10 * time.Minute
  	ResyncSettle          = 2 * time.Minute
  	BlocklistTTL          = 90 * 24 * time.Hour
  	BlockCallTimeout      = 5 * time.Second
  	ImportHoldRetention   = 24 * time.Hour
  	RedownloadWindow      = 24 * time.Hour
  	EngineRecordFresh     = 2 * time.Minute
  )

  type Client struct { // a DownloadClient, as the stage reads it
  	Name           string
  	UID            types.UID
  	Protocol       commonv1.Protocol
  	Enabled        bool
  	Priority       int32
  	Replicas       int32
  	RemoveCompleted bool                  // torrent spec.torrent.removeCompleted, default true
  	StallTimeout   time.Duration          // torrent default 24 h; usenet 0 = never
  	DownloadTimeout time.Duration         // usenet, opt-in (R21)
  	HealthAction   downloadv1alpha1.HealthAction
  	Seed           *commonv1.SeedCriteria // the client's defaults
  	Categories     map[string]string
  	ResyncSeq      int64                  // status.resyncSeq
  }
  type Engine struct { // an engine instance, from its record
  	Name                  string // "<client>-<ordinal>"
  	Client                string
  	Ordinal               int32
  	Ready, Reattached     bool
  	BootID                string
  	ResyncSeq             int64
  	Active, Queued        int32
  	At                    time.Time
  	ReattachedAt          time.Time // when this bootID first reported reattached
  }
  type Transfer struct { Record schema.TransferRecord; Revision uint64 }
  type Intents struct { // parsed from the owner's annotations
  	Paused   map[string]bool
  	Priority map[string]string
  	Remove   *RemoveIntent  // Nonce, ID, Data, Blocklist
  	Resume   *ResumeIntent  // Nonce, ID
  	Import   *ImportIntent  // Nonce, ID, Target *schema.ItemRef, Override
  	Unblock  *UnblockIntent // Nonce, InfoHash or Indexer+GUID, Global
  	Invalid  []InvalidIntent // annotation, value, reason: Warning InvalidAnnotation, nonce recorded through HandledNonce
  }
  func ParseIntents(annotations map[string]string) Intents

  type View struct {
  	Owner        schema.ItemRef
  	ProviderID   string
  	Kind         commonv1.MediaKind
  	Now          time.Time
  	Deleting     bool
  	Monitored    bool
  	EntryCap     int
  	Stored       []catalogv1alpha1.DownloadEntry
  	Nonces       catalogv1alpha1.DownloadNonces
  	Intents      Intents
  	NewEntries   []catalogv1alpha1.DownloadEntry   // Pending, from the grab stage (A4) or a manual pick
  	Transfers    map[types.UID]Transfer            // by entry uid
  	Unclaimed    []Transfer                        // records naming this owner whose entry is not in Stored
  	Clients      []Client
  	Engines      map[string]Engine
  	Files        map[string]int                    // MediaFiles naming each entry id in spec.importedFrom.downloadRef
  	Import       map[types.UID]ImportDecision      // importplan's per Completed entry (A3.8)
  	Blocks       map[types.UID]BlockReply          // the block book's replies by entry uid (R9)
  	Unblocked    map[string]bool                   // the book's confirmed unblock nonces
  	Searched     map[schema.ItemRef]time.Time      // each covered item's searchDispatch.dispatchedAt (R25)
  	Removed      map[string]bool                   // outputPaths the stage's SafeRemove already removed
  	StillWants   func(schema.TransferClaim) (bool, string) // the grab planner's (A4); A3's default below
  	IndexerClient map[string]string                // Indexer name → spec.downloadClientRef
  }
  type Command struct {
  	Engine  string
  	Client  string
  	Ordinal int32
  	Subject string
  	MsgID   string
  	Cmd     schema.EngineCommand
  }
  type BlockCall struct{ EntryUID types.UID; Req schema.BlocklistRequest }
  type Plan struct {
  	Entries     []catalogv1alpha1.DownloadEntry
  	Phase       commonv1.DownloadPhase
  	Nonces      catalogv1alpha1.DownloadNonces
  	Commands    []Command
  	Blocks      []BlockCall
  	Removals    []string                 // outputPath, removed before the entry is dropped
  	Imports     []ImportDispatch         // inspect or execute tasks owed (filled from View.Import, A3.8)
  	Materialise []Materialise            // MediaFile applies and deletes owed (A3.8)
  	History     []schema.DownloadEvent   // published on clustarr.evt.download.download.<action>.<entry uid>
  	Events      []Event                  // Recorder, Type, Reason, Message; On: the owner or a DownloadClient
  	Redownloads []schema.ItemRef         // R25
  	DirectGrabs []DirectGrab             // IndexerRef, entry uid, at: the Assigned edge of a direct source (§6.11)
  	Finalizer   *bool                    // true: ensure FinalizerTransfers; false: remove; nil: leave
  	Due         time.Time
  }
  func Decide(v View) Plan
  func ChooseClient(clients []Client, protocol commonv1.Protocol, indexerClientRef string) (Client, bool)
  func ChooseOrdinal(c Client, engines map[string]Engine, infoHash, guid string, now time.Time) (int32, bool)
  func ScopeFor(reason commonv1.DownloadFailureReason) catalogv1alpha1.BlockScope
  func EntryID(target string, purpose commonv1.DownloadPurpose, guid string) string // R4's order

  // Declared here so importplan (A3.8) can produce them without lifecycle
  // importing it (importplan imports lifecycle, never the reverse):
  type ImportVerdict string // "none" | "imported" | "releaseFault" | "held" | "expired" | "retry"
  type ImportDispatch struct {
  	Phase   string // "inspect" | "execute"
  	Seq     int64
  	Subject string
  	MsgID   string
  	Task    any    // *schema.ImportInspectTask or *schema.ImportExecuteTask
  }
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
  type AudioGraftApply struct{ Owner schema.ItemRef; DonorPath, Anchor, Default string } // R26, until F7.1
  type Materialise struct {
  	Apply      *ApplyMediaFile
  	Delete     *schema.MediaFileBasis
  	AudioGraft *AudioGraftApply
  }
  type ImportDecision struct {
  	Summary     catalogv1alpha1.DownloadImportSummary
  	Dispatch    *ImportDispatch
  	Materialise []Materialise
  	Verdict     ImportVerdict
  	Class       commonv1.ImportRejectionClass
  }
  type ImportIntent struct{ Nonce, ID string; Target *schema.ItemRef; Override bool }
  type RemoveIntent struct{ Nonce, ID string; Data, Blocklist bool }
  type ResumeIntent struct{ Nonce, ID string }
  type UnblockIntent struct{ Nonce, InfoHash, Indexer, GUID string; Global bool }
  type InvalidIntent struct{ Annotation, Value, Reason string }
  type BlockReply struct{ Seq int64; Applied, Stale bool; At time.Time }
  type DirectGrab struct{ IndexerRef string; EntryUID types.UID; At time.Time }
  type Event struct {
  	Recorder, Type, Reason, Message string
  	On      *schema.ItemRef // nil: the owner; a DownloadClient ref for TransferOwnerGone
  }
  ```

- [ ] **Step 1: Transitions** (`transitions.go`): one function per row of §6.4's table, each
  taking the entry, its transfer (if any), the client, the engine and the view, returning the next
  entry and its effects. Fix these decisions the table implies:
  - **Issuing a command** always moves `dispatch.seq` to `records.NextSeq(rec.Seq,
    entry.dispatch.seq, now)` and `dispatchedAt` to `now`; the command's desired state is the
    entry's whole desired state at that seq (§6.7); its Msg-Id is
    `MsgIDForEngineCommand(uid, seq)`, or `…AfterBoot(uid, seq, bootID)` when the engine's
    `bootID` differs from the one the record carries. **An owed command** (record seq below
    `dispatch.seq`) is republished every pass with the same Msg-Id while `now - dispatchedAt <
    records.RepublishWindow`, and with a new seq past it.
  - **Pending → Assigned** needs `ChooseClient` and `ChooseOrdinal` to succeed and the engine
    Ready (fresh record ≤ `EngineRecordFresh`, `ready`); else the entry stays `Pending` with
    `message` naming the engine and `dispatch.delivery` waiting `EngineNotReady`. `client` and
    `engine` are pinned once. The edge emits `History` `grabbed` (`evt.catalog.release.grabbed`
    is the grab stage's, A4) and a `DirectGrab` when `dl.Source` is a magnet, torrent URL or NZB
    URL (`directgrab.IsDirectGrab`'s rule moves here).
  - **Stage map** from the record: `fetchingMetadata` → `Queued`; `transferring`, `verifying`,
    `repairing`, `extracting`, `publishing` → `Downloading`; `done`, `seeding` → `Completed`
    (then the import, A3.8); `healthPaused` → `Paused` with the health floor in `message`.
  - **Failure reasons from the record:** a release fault (`IsReleaseFault`) → `Blocklisted`
    with `block{scope: ScopeFor(reason), reason, seq: NextSeq(...)}`, command `absent` with
    `removeData: removeDataOnDelete`, `History` `failed`, and a `Redownloads` entry for each
    covered item (R25); a local fault → `Failed`, `absent`, nothing else.
  - **Stall** (torrent; usenet when `StallTimeout > 0`): `Downloading` and
    `now - lastProgressAt > StallTimeout` → `Blocklisted`, `stalled`. **Download timeout**
    (usenet, R21): `now - startedAt > DownloadTimeout` → `Blocklisted`, `timeout`.
    **Health action delete** with `healthPaused` → `Blocklisted`, `missingArticles`.
  - **Seeding and removal:** `Completed` and the import `Imported` with `Files` MediaFiles naming
    the id → `Imported` (usenet) or `Seeding` (torrent), command `imported: true`; `Seeding`
    with `seedGoalReached` and (`removeOnImport` or the client's `removeCompleted`) →
    `Removing`, `absent`, `removeData: removeDataOnDelete` (Q6: the library holds its own copy);
    usenet `Imported` with `removeOnImport` → `Removing`.
  - **Dropping:** `Removing` or `Failed` with the record `removed` at the command's seq → when the
    command asked `removeData` and `outputPath` is set, kept with `Removals = [outputPath]` until
    `View.Removed[outputPath]` (the stage's leader-local removal book, set when the `SafeRemove`
    effect succeeded; a lost book removes again, a no-op on a missing path), then dropped, with a
    `History` `removed`; `Blocklisted` likewise, but only once `block.confirmedAt` is set, and then only
    after `BlockQuarantine`; a `Failed` or `Blocklisted` entry that owes a redownload
    (`Searched[item]` older than the failure, R25) is kept until the search was dispatched. An
    entry whose engine is gone (DownloadClient missing, not `EngineReady`, or the ordinal ≥
    `max(replicas, 1)`, as `teardown.go:70-88`) is dropped `EngineTeardownTimeout` after its
    `Removing` transition, removing the data itself when asked, with Event `EngineGone`.
  - **No transfer:** a live entry before `Removing` whose engine has `reattached`, whose
    `resyncSeq` reached the client's `ResyncSeq`, and whose record is absent or older than the
    engine's boot `ResyncSettle` after that point → command `present` at a new seq (re-add,
    Msg-Id with the bootID). A record `failed` with `payloadUnavailable`: before import, the
    release fault path (item scope); after import (`Imported`, `Seeding`) → dropped with Event
    `SeedingLost`, nothing blocked or searched.
  - **The owner deleting:** every live entry → `Removing` with `removeData:
    removeDataOnDelete`; `Finalizer` false once no entry is left; true whenever an entry exists.
  - **Phase:** `Plan.Phase` is the active entry's phase (`rollup.ActiveEntry`'s rule, A3.5),
    `""` when none.
- [ ] **Step 2: Intents** (`intents.go`): parse §6.10's grammar; `paused` and `priority` are
  standing (rendered into every command of the named entries); `remove` (`Removing`, or
  `Blocklisted` with scope item, reason `manual`, when `blocklist` is given; `data` sets
  `removeData`), `resume` (clears a health pause: the command's `healthOverride` = the nonce),
  `import` (re-issues the inspect with the target and override, A3.8) and `unblock` (a
  `BlockCall` with `op: unblock`, scope the owner's or `*` with `global`; the nonce is recorded
  only once `Unblocked[nonce]` says the index confirmed, R9) are one-shot; every one-shot is
  recorded in `Nonces` on the pass that handles it (`NothingToRemove`/`NothingToResume` Events
  when the id names no entry); invalid values are recorded through `HandledNonce` with a
  Warning `InvalidAnnotation`.
- [ ] **Step 3: Convergence** (`converge.go`), §6.7's evidence table for each `Unclaimed`
  transfer whose record reads `claimed: false` and whose claim names this owner:
  same UID → `StillWants(claim)`: wanted → the entry is re-created under the claim's `id` and
  `uid` in the phase the record implies (`Pending` → … mapping above), command `present` at a new
  seq; not wanted → command `absent`, `removeData` from the claim. A different UID with the same
  name and an equal or empty provider id is decided the same (the command carries the new UID).
  A different provider id → the owner-gone row (the stage's `OwnerGone`, A3.5). A claim with no
  owner at all never reaches a key (the DownloadClient controller's, A3.7). **A3's default
  `StillWants`** until A4.1 replaces it: wanted iff the owner is monitored and either no live
  entry of the claim's purpose exists and the owner has no file, or the claim is `imported` with
  its seed goal not reached.
- [ ] **Step 4: Choice** (`choose.go`): `ChooseClient` is today's `pickClient`
  (`app/grab/controller/download/controller.go:666`: enabled, same protocol, lowest priority,
  then name) with the Indexer's `spec.downloadClientRef` winning when it names an enabled client
  of the protocol. `ChooseOrdinal`: among Ready engines of the client with a fresh record, the
  fewest `active + queued`, ties by `names.HashOrdinal(replicas, infoHash, guid)`; a usenet client
  has ordinal 0.
- [ ] **Step 5: Scope** (`scope.go`): `encrypted`, `payloadMismatch`, `missingArticles` → global;
  `importRejected`, `stalled`, `payloadUnavailable`, `timeout` (R21), `manual` → item.

**Tests (deferred to the batch):**

- `TestEveryRowOfTheStateMachine` (`app/grab/lifecycle`): one table case per row of §6.4,
  including "any live → Removing on the owner deleting".
- `TestNothingRemovesOnAbsence` (`app/grab/lifecycle`): for every combination of absent record,
  absent engine record, absent bucket and absent command, `Decide` issues no `absent` and no
  removal (Review Focus 1).
- `TestAnEntryIsNotJudgedMissingBeforeResync` (`app/grab/lifecycle`).
- `TestTheClaimEvidenceOrder` (`app/grab/lifecycle`): the four rows of §6.7, the restored-UID case
  included (Review Focus 3).
- `TestABlocklistedEntryIsDroppedOnlyAfterConfirmationAndQuarantine` (`app/grab/lifecycle`).
- `TestAnOwedCommandIsRepublishedWithTheSameMsgIDInsideTheWindow` (`app/grab/lifecycle`).
- `TestEngineGoneDropsAfterTenMinutes` (`app/grab/lifecycle`).
- `TestIntentsParseAndRecordEveryNonce` (`app/grab/lifecycle`), invalid values included.
- `TestChooseClientPrefersTheIndexersClient` / `TestChooseOrdinalBalancesThenHashes`.
- `TestScopeByReason` (`app/grab/lifecycle`): §6.14's table plus `timeout`.
- `TestAFailedEntryWaitsForItsRedownloadSearch` (`app/grab/lifecycle`, R25).
- `TestLifecycleIsPure` (`test/guards`): no client, bus, clock or I/O import.

- [ ] **Step 6: Commit.**

  ```bash
  git add app/grab/lifecycle
  git commit -m 'feat(grab): app/grab/lifecycle, the grab state machine as a pure planner -- transitions, convergence both ways, intents, client and engine choice, block scope (ADR-0019 A3.4)' -- app/grab/lifecycle
  ```

---

### Task A3.5: The downloads stage; the item kinds render entries; rollups over entries; the item finalizer; deletion and owner-gone convergence

**Spec:** §6.1 (owners; Episode and Issue views), §6.4-§6.8, §6.10, §6.11 (rollups, indexes,
`grabsource.Covers`, history resolution), §8.1 (S27, S28), §11.1 (F4's notes: S9 and the
`.spec.target.<kind>` indexes go here); rulings R5, R9, R25.

**Files:**

- Create: `app/remediation/downloads/{stage.go,gather.go,effects.go,books.go,owner_gone.go,sources.go,doc.go,rbac.go}`,
  `app/catalog/controller/rollup/entries.go`
- Modify: the eight item packages' reconcilers (`movie`, `episode`, `album`, `book`, `audiobook`,
  `issue`, `series`, `comic`: every `catalogarr` apply site renders `itempass.Downloads/Phase/Nonces`;
  the deletion path applies status when the pass carries a contribution and leaves the item
  finalizer while `FinalizerTransfers` is present; the phase overlay and `activeDownloadRef`
  read entries; the Download watch, `mapDownload`, `downloadByMovieIndexKey` and siblings
  (`.spec.target.<kind>` indexes) and every `downloadv1alpha1` import go),
  `app/catalog/controller/rollup/{activedownload.go,downloadoverlay.go,downloadpredicate.go}`
  (no item kind calls them any more; they stay until A4.4, because the grab worker's double-grab
  guard and the search worker's queue read `DownloadNonTerminal` until then; their entry forms
  are in `entries.go`), `app/catalog/controller/episode/downloadoverlay.go`
  and siblings (entry forms), `app/catalog/grabsource/grabsource.go` (`CoversEntry`),
  `app/remediation/manager/register.go` (`Stages` returns the downloads stage),
  `app/catalog/history/target.go` (`DownloadEvent`, v1 `ImportTask` → owner)

**Interfaces:**

- Consumes: A3.3 (all of it), A3.4 `lifecycle.*`, A1.5 `records.Reader`, `recordsource.NewItems`;
  A1.6 bucket and subject builders; A2.2 ledger; `limits.CountGrabAt`
  (`app/indexer/limits/ring.go:183`).
- Produces:

  ```go
  package downloads // app/remediation/downloads

  type Options struct {
  	Reader    client.Reader
  	APIReader client.Reader
  	Bus       events.Bus
  	Dispatch  *dispatch.Ledger
  	Book      *dispatch.DeliveryBook
  	Recorder  func(string) k8sevents.EventRecorder
  }
  func New(o Options) *Stage                     // implements remediation.ItemStage, Watcher, OwnerGone
  func (s *Stage) Name() remediation.ItemStageName // StageDownloads
  func (s *Stage) Applies(k remediation.KeyKind) bool // the eight item kinds
  // BlockBook records blocklist replies by entry uid and unblock nonces (R9).
  type BlockBook struct{ /* leader-local */ }
  // Unclaimed is S27's leader-local index of transfer records whose entry no
  // owner holds, keyed by the claim's owner key.
  type Unclaimed struct{ /* … */ }

  package rollup // entries.go
  func EntryNonTerminal(e *catalogv1alpha1.DownloadEntry) bool            // DownloadNonTerminal's rule over an entry: not Imported, Failed, Blocklisted or Removing; a held import (import.phase held) is terminal
  func ActiveEntry(entries []catalogv1alpha1.DownloadEntry, covers func(*catalogv1alpha1.DownloadEntry) bool) *catalogv1alpha1.DownloadEntry // oldest grabbedAt, then id; donors skipped
  func DonorEntryOpen(entries []catalogv1alpha1.DownloadEntry, covers func(*catalogv1alpha1.DownloadEntry) bool) bool
  func EntryOverlay(e *catalogv1alpha1.DownloadEntry) (Overlay, bool)   // DownloadOverlay's table over entry phases
  func CoversEpisode(e *catalogv1alpha1.DownloadEntry, episodeName string, season, number int32) bool
  func CoversIssue(e *catalogv1alpha1.DownloadEntry, issueName, number string) bool
  func DownloadsSignature(entries []catalogv1alpha1.DownloadEntry, pending []catalogv1alpha1.PendingGrab) uint64

  package grabsource // gains:
  func CoversEntry(e *catalogv1alpha1.DownloadEntry, kind commonv1.MediaKind, name string, season, number int32) bool
  ```

- [ ] **Step 1: Gather** (`gather.go`) for an owner key (Movie, Album, Book, Audiobook, Series,
  Comic): the cached owner's `status.downloads`, `downloadNonces` and annotations
  (`lifecycle.ParseIntents`); for every stored entry its transfer record
  (`records.NewReader[*schema.TransferRecord](bus.KV(BucketTransfers), transferSpec)`, key
  `RecordKey(entry.uid)`) and import records (A3.8); the owner's `Unclaimed` transfers; the
  namespace's DownloadClients from the cache (`lifecycle.Client` built with today's defaults:
  `removeCompleted` true, torrent `stallTimeout` 24 h); for each pinned and each candidate
  engine its engine record (`RecordSubKey(client uid, ordinal)`); the MediaFiles naming each
  entry id (the owner's files through `mfindex.Item`, filtered on
  `spec.importedFrom.downloadRef`); for Series and Comic the covered Episodes' or Issues'
  `searchDispatch.dispatchedAt` (R25); the `BlockBook`'s replies; the Indexers'
  `spec.downloadClientRef`. Every read is the cache or KV: no apiserver read on the hot path.
- [ ] **Step 2: Plan** (`stage.go`): build `lifecycle.View` (`EntryCap` 8 or 24; `StillWants` the
  A3.4 default until A4.1) and call `lifecycle.Decide`; translate the plan into an
  `ItemResult`: `Contribution{Downloads, DownloadPhase, DownloadNonces}`;
  effects in this order: `EnsureFinalizer(FinalizerTransfers)` (when wanted and absent),
  each `RPC` blocklist call (`Subject: events.RPCIndexBlocklist`, `Timeout:
  lifecycle.BlockCallTimeout`, `Done` recording into `BlockBook`), each engine command as a
  `Publish` (no ledger: R20/§5.1), each import dispatch as a `DispatchPublish` on
  `importarr-fileimport` (A3.8), each `SafeRemove`, each materialise effect (A3.8), each
  `CountGrab` (keyed by entry uid), each `History` on
  `events.DownloadEventSubject(action, entryUID)` with `schema.DownloadEvent{DownloadRef:
  {Namespace, Name: entry.id, UID: entry.uid}, Media: <the owner's or covered item's ref>, …}`
  and Msg-Id `<entry uid>:<action>:<seq>`, then `RemoveFinalizer(FinalizerTransfers)` when no
  entry is left; Events with recorder `downloads` (`EngineGone`, `SeedingLost`,
  `DownloadAddFailed` on a record `failed` before any stage, `CommandRetrying` when a command is
  republished past the window, `InvalidAnnotation`, `NothingToRemove`, `NothingToResume`).
  `Due` is the earliest of: an owed command's republish (30 s), a stall deadline, a hold's
  expiry, `EngineTeardownTimeout`, `BlockQuarantine`'s end, `UnclaimedGrace`'s end.
- [ ] **Step 3: Episode and Issue views.** For `KindEpisode` and `KindIssue` the stage reads the
  container (Series by `spec.seriesRef`, Comic by `spec.comicRef`) from the cache and returns a
  view-only `Contribution{Covering, ActiveDownloadRef, DownloadPhase, DonorOpen}` built with
  `rollup.CoversEpisode`/`CoversIssue`; it issues no effect.
- [ ] **Step 4: The kinds render entries.** In each of the eight packages: every `catalogarr`
  apply passes `WithDownloads(itempass.Downloads(p, stored)...)`,
  `WithDownloadPhase(itempass.Phase(p, stored))` and `WithDownloadNonces(…)` (owners), or
  `WithDownloadPhase` (Episode, Issue); `activeDownloadRef` is the active entry's `id` (owners:
  `rollup.ActiveEntry(entries, nil)`; Episode/Issue: `Contribution.ActiveDownloadRef`); the
  phase overlay is `rollup.EntryOverlay` of that entry; the donor state reads
  `rollup.DonorEntryOpen`; Series keeps computing `downloadingEpisodeCount` from its Episodes'
  phases (an Episode covered by a non-terminal entry reads `Downloading`), and Comic computes the
  new `downloadingIssueCount` the same way from its Issues' `downloadPhase` (non-empty and
  non-terminal). `reconcileDelete` applies a status with
  `reassertKnownStatus` plus the contribution when `itempass.From(ctx)` carries one, and removes
  the item's own finalizer only when `FinalizerTransfers` is absent. Delete each package's
  Download watch, `mapDownload`, `downloadByMovieIndexKey`-style index registration and
  `activeDownload` Download List, and the `rollup.DownloadPredicate` (S9) watches (F4's notes).
- [ ] **Step 5: Sources** (`sources.go`): **S27** `recordsource.NewItems` on `clustarr-transfers`:
  a record whose `item` names an owner kind enqueues that owner; a record with `claimed: false`
  is also put into `Unclaimed` under its claim's owner key. **S28** on `clustarr-imports`
  (A3.8 decodes it). A DownloadClient's new engine `bootID` enqueues every owner with an entry
  pinned to that engine (`dlindex.PinnedTo`), which republishes desired state (the resync, §6.7):
  the stage watches `clustarr-engines` through `recordsource.NewItems` with a `toKeys` that reads
  the bootID transition from a leader-local map and lists `PinnedTo`.
- [ ] **Step 6: Owner gone** (`owner_gone.go`): `Holds(k)` is `Unclaimed` holding a transfer for
  `k`; `Gone` makes **one** `APIReader.Get` of the claim's kind, namespace and name: NotFound →
  each transfer gets command `absent` with `removeData` from its claim's `removeDataOnDelete`,
  and Event `TransferOwnerGone` (recorder `grabarr-engine`) on its DownloadClient; found with a
  different provider id → the same; found with the same provider id (a restored owner the cache
  has not seen) → nothing, requeue in 5 s; any other error → nothing (no evidence, no action).
- [ ] **Step 7: Readers.** `grabsource.CoversEntry` over entries (the Download form stays until
  A4.4 deletes the grab worker). `history.Resolve` maps a `DownloadEvent` to its `Media` owner
  and a v1 `ImportTask` (Download-keyed) through `dlindex.OwnerOf` when the resolver has a reader
  (the projector, in the manager, A2.5). RBAC (`rbac.go`): the eight item kinds `get;list;watch`,
  their `/status` `get;update;patch`, their `/finalizers` `update`, `downloadclients`
  `get;list;watch`, `indexers` `get;list;watch`, `mediafiles` `get;list;watch;create;update;patch;delete`
  (A3.8's materialise), `transcode.clustarr.io` `audiografts` `get;list;watch;create;patch;update`
  (R26), `events.k8s.io` `events` `create;patch`.

**Tests (deferred to the batch):**

- `TestTheDownloadsStageRunsTheTableOnAnOwnerAlreadyInItsSteadyState` (`app/remediation/downloads`,
  envtest).
- `TestAnEpisodeReadsItsSeriesEntries` (`app/remediation/downloads`): `downloadPhase` and
  `activeDownloadRef` from the container's covering entry (the pending-grab view is A4.1's).
- `TestTheFinalizerHoldsDeletionUntilEveryTransferIsRemoved` (envtest).
- `TestOwnerGoneNeedsTheAPIReadersNotFound` (envtest: a cache NotFound with the object present
  removes nothing).
- `TestAnUnclaimedTransferIsResumedUnderItsOwner` (envtest, A3's default `StillWants`).
- `TestARefusedImportOverATranscodedFileStopsReadingDownloading` (F4's, rewritten over entries:
  a held import is not active).
- `TestEntryRollupsMatchTheDownloadRollups` (`app/catalog/controller/rollup`): every row of
  `DownloadOverlay`'s table over the equivalent entry phase.
- `TestHistoryResolvesADownloadEventToItsOwner` (`app/catalog/history`).
- `TestOnlyTheLoopWatchesMediaFile` (F0.3's): unaffected.

- [ ] **Step 8: Commit.**

  ```bash
  git add app/remediation/downloads app/catalog/controller/rollup/entries.go
  git commit -m 'feat(downloads): the downloads stage runs every grab on its owner -- entries rendered by each item kind, Episode and Issue views, the item finalizer, owner-gone convergence on the APIReader'"'"'s answer; the Download watches and target indexes go (ADR-0019 A3.5)' -- app/remediation app/catalog/controller app/catalog/grabsource/grabsource.go app/catalog/history/target.go
  ```

---
### Task A3.6: Engines execute commands -- journal claims and seqs, transfer and engine records, resync; no Download reconciler, reaper or finalizer

**Spec:** §6.7 (what an engine does, commands, records, re-attach and the journal, claims,
convergence's engine half, resync, data safety), §3.5 P111-P123, §3.1 W37-W44, §9.1
(`grabarr-engine` reads `downloadclients` and `get` on Secrets only), §10.2 (the first
release-N boot); ruling R20.

**Files:**

- Create: `app/grab/engine/command.go` (the shared command handler), `app/grab/engine/journal.go`
  (the journal fields both engines share), `app/grab/engine/report.go` (transfer and engine record
  writers, claimed tracking), `app/grab/engine/torrent/commands.go`,
  `app/grab/engine/usenet/commands.go`
- Modify: `app/grab/engine/engine.go` (`Stopped`, `Finalizer`, `OrphanClock` deleted),
  `app/grab/engine/progress.go` (`ProgressPublisher` keyed from the journal, no Download List),
  `app/grab/engine/torrent/{engine.go,state.go,selection.go,resolve.go}`,
  `app/grab/engine/usenet/{engine.go,payload.go,config.go}`, `pkg/download/usenet/client.go`
  (the manifest gains the journal fields), `pkg/download/torrent/client.go` (`MarkImported`
  persisted through the descriptor, not memory only: `:555-567`), `app/grab/agent/torrent/{register.go,proxy.go}`,
  `app/grab/agent/usenet/register.go`, `app/grab/engine/torrent/doc.go` and
  `app/grab/engine/usenet/doc.go` (RBAC markers), `pkg/k8s/fieldmanager.go`
  (`ManagerGrabarrEngine` into `RetiredFieldManagers()`), `Makefile`
  (`RBAC_PATHS_grabarr-engine := ./app/grab/engine/... ./app/grab/agent/...`, §9.1)
- Delete: `app/grab/engine/torrent/{reconciler.go,reaper.go}`, `app/grab/engine/usenet/reaper.go`,
  the `Reconciler` half of `app/grab/engine/usenet/engine.go` (its `Reconcile`,
  `SetupWithManager`, `patchTelemetry`, `reconcileImported`, `reconcileStopped`,
  `reconcileDeleting`)

**Interfaces:**

- Consumes: A1.4 (`schema.{EngineCommand, TransferClaim, TransferRecord, EngineRecord,
  TransferSelection}`), A1.5 (`records.Writer`), A1.6 (`EngineConsumer`, `BucketTransfers`,
  `BucketEngines`), A2.1 presence; `pkg/download.Client` (unchanged interface).
- Produces:

  ```go
  package engine // app/grab/engine
  // Journal is what an engine remembers per transfer beyond the payload
  // (§6.7): the torrent descriptor and the usenet manifest both embed it.
  type Journal struct {
  	EntryUID      string               `json:"entryUID,omitempty"` // "" for a pre-journal transfer (release N's first boot)
  	Seq           int64                `json:"seq,omitempty"`
  	Claim         *schema.TransferClaim `json:"claim,omitempty"`
  	Imported      bool                 `json:"imported,omitempty"`
  	ImportedAt    *time.Time           `json:"importedAt,omitempty"`
  	FailureReason string               `json:"failureReason,omitempty"`
  	Desired       string               `json:"desired,omitempty"` // the last applied: present|absent
  }
  // Transfers is what a command handler drives: one engine's client and journal.
  type Transfers interface {
  	List(ctx context.Context) ([]Held, error)                        // every transfer with its journal and client item
  	Apply(ctx context.Context, cmd schema.EngineCommand) (Held, error) // add, update or remove to match cmd
  	RemoveByID(ctx context.Context, downloadID string, removeData bool) error
  }
  type Held struct {
  	Name    string // the Download name (pre-journal) or the entry id
  	Journal Journal
  	Item    download.Item
  	Found   bool
  }
  type Handler struct {
  	Transfers Transfers
  	Reporter  *Reporter
  	Engine    string // "<client>-<ordinal>"
  	Now       func() time.Time
  }
  func (h *Handler) Handle(ctx context.Context, m events.Message) error
  type Reporter struct {
  	Transfers *records.Writer[*schema.TransferRecord]
  	Engines   *records.Writer[*schema.EngineRecord]
  	Client    schema.ItemRef // the DownloadClient
  	Ordinal   int32
  	Pod, BootID, Version string
  	Proxy     func() string // "available" | "unavailable" | "n/a"
  	Free      func() (scratch, publish int64)
  	Now       func() time.Time
  }
  func (r *Reporter) Start(ctx context.Context) error // re-attach report, then the engine record every 60 s and on change; transfer counters at most 1/min, a refresh at least daily
  func (r *Reporter) Ready() bool                       // after every transfer record of this boot is written
  ```

- [ ] **Step 1: The journal.** Embed `engine.Journal` in the torrent `descriptor`
  (`state.go:40`) and the usenet `manifest` (`pkg/download/usenet/client.go:312`; its existing
  `Imported` and `Reason` map onto `Journal.Imported` and `FailureReason` -- keep the JSON names
  of the existing fields and add the rest). `pkg/download/torrent`'s `MarkImported` stops being
  memory-only: the engine writes `Imported` into the descriptor and passes it on re-attach, so
  `CanBeRemoved` survives a restart (`session.go:161`).
- [ ] **Step 2: The command handler** (`command.go`), bound by `bus.Subscribe(ctx,
  events.EngineConsumer(client, ordinal).Subscription(), h.Handle)` (bind-only: the manager's
  DownloadClient controller created the durable, A3.7). For each `EngineCommand`:
  - `Desired: resync`: rewrite every transfer record, then write the engine record with
    `ResyncSeq = cmd.ResyncSeq`; ack.
  - `DownloadID` set (an unidentified transfer, A3.7): `RemoveByID(id, removeData)`, ack.
  - otherwise find the held transfer by `Journal.EntryUID == cmd.Entry.UID`, else by name
    (`Held.Name == cmd.Entry.ID`, a pre-journal transfer: fill `EntryUID`, `Claim`, `Seq`);
    drop and ack when `cmd.Seq <= Journal.Seq`; else `Apply`: `present` adds (resolving the
    payload through `clustarr.rpc.indexarr.download` as `resolve.go`/`payload.go` do; the usenet
    client dedupes on `AddRequest.Name`, CLAUDE.md) or updates pause, priority (`SetPriority`),
    seed criteria (the command's, the client's defaults applied to unset fields), selection
    (`cmd.Selection` replaces the Episode reads of `selection.go:75`, which goes), `imported`
    (`MarkImported`), `healthOverride`; `absent` removes with `removeData`. Write the journal,
    then the transfer record at `cmd.Seq` (`claimed: true`), then ack. A payload RPC failure or a
    full disk naks with a delay (`events.Retry(30*time.Second, …)`, P116); a payload that can no
    longer be fetched for a re-add answers the record `failed`,
    `engineFailureReason: payloadUnavailable`, and acks.
- [ ] **Step 3: Records** (`report.go`). The transfer record (`RecordKey(entry uid)`): every
  field of A1.4's `TransferRecord` from `download.Item` and the journal; `files` clamped to
  `schema.MaxTransferFilesBytes` with `filesTruncated`; `message` through `pkg/download`'s
  `clampMessage` (2048); `claimed` true when a command named the entry since this boot, false
  `lifecycle.UnclaimedGrace` after the boot or the add otherwise. Written on every state or stage
  change, on every command, at most once a minute for counters, and at least once a day. A
  pre-journal transfer has no entry UID, so it gets no transfer record until a command names it:
  it is listed in the engine record's `unidentified` with its name. The engine record
  (`RecordSubKey(client uid, ordinal)`): `ready` = re-attach done and every transfer record of
  this boot written; `reattached`; `resyncSeq`; counts from `Client.Info`; free bytes by
  `statfs` of the scratch and publish directories; `proxyUDP` from the torrent proxy's state
  (the `warn` callback of `agent/torrent/proxy.go` sets it to `unavailable`; usenet `n/a`);
  `unidentified` (transfers with no claim, ≤ 64, then the count); written at start (after the
  transfer records, with a new `bootID` = `uuid.NewUUID()` from `k8s.io/apimachinery/pkg/util/uuid`),
  on change and every 60 s.
- [ ] **Step 4: Decisions leave the engine** (§3.5 P111-P123): delete the stall verdict
  (`torrent/engine.go:38` `StallTimeout` stays only as the manager-side default constant, moved to
  `lifecycle`), the removal on `CanBeRemoved && removeOnImport && removeCompleted`
  (`torrent/reconciler.go:355-367`), the usenet removal on `removeOnImport`
  (`usenet/engine.go:356-370`), `engine.Stopped`'s removal of failed transfers, and both reapers.
  Health pause stays in `pkg/download/usenet` (a mechanism: it stops spending quota) and is
  reported; the usenet `downloadTimeout` failure is no longer raised by the engine (R21). The
  payload-mismatch check stays and is reported as `engineFailureReason: payloadMismatch`.
- [ ] **Step 5: Progress.** `ProgressPublisher` lists the client's transfers and maps each
  `DownloadID` to its journal's `EntryUID` (skipping pre-journal ones until a command names them),
  writing `ProgressKey(entryUID)` as today; it no longer lists Downloads.
- [ ] **Step 6: Registration.** `app/grab/agent/torrent/register.go` and `…/usenet/register.go`:
  keep the APIReader reads of the DownloadClient and its Secrets at start (W4's rolling hash
  still restarts the pod on a change); build the client and re-attach as today; register the
  `Reporter` (`k8s.EveryReplica`), the command `Handler` (`k8s.EveryReplica` subscribing the
  durable) and the `ProgressPublisher`; no recorder (the proxy warning becomes the engine
  record's `proxyUDP`), no reconciler, no reaper, no Episode reader. Readiness check
  `torrent-engine.reattach` reads `Reporter.Ready`. The usenet `directClient` stays for the
  Secret reads only.
- [ ] **Step 7: RBAC.** `torrent/doc.go` and `usenet/doc.go` keep only
  `downloadclients get;list;watch` and `secrets get`; `make manifests` and sync the chart
  (`grabarr_engine_role.yaml` loses every write, `episodes` and `events`). Move
  `ManagerGrabarrEngine` from `FieldManagers()` into `RetiredFieldManagers()` (its `Validate`
  still accepts it in N).

**Tests (deferred to the batch):**

- `TestACommandAtOrBelowTheJournalSeqIsDropped` (`app/grab/engine`).
- `TestAPreJournalTransferIsMatchedByNameAndAdopted` (`app/grab/engine`).
- `TestTheEngineAcksOnlyAfterTheRecordShowsTheSeq` (`app/grab/engine`, membus).
- `TestResyncRewritesEveryRecordThenReportsTheSeq` (`app/grab/engine`).
- `TestAnUnclaimedTransferReadsClaimedFalseAfterTheGrace` (`app/grab/engine`, fake clock).
- `TestTheEngineRemovesNothingWithoutAnAbsentCommand` (`app/grab/engine/torrent` and `usenet`:
  a seed goal met, an import recorded, a failure: nothing removed).
- `TestPayloadUnavailableIsReportedNotRetriedForever` (`app/grab/engine/torrent`).
- `TestImportedSurvivesARestart` (`pkg/download/torrent`).
- `TestTransferRecordFilesAreClampedToTheBudget` (`app/grab/engine`).
- `TestGrabarrEngineRoleIsReadOnly` (`test/guards`, folded into A7's `TestNoAgentRoleGrantsAWriteVerb`).
- e2e `TestEngineRestartKeepsSeeding` (§6.13; Phase H).
- The deleted files' tests (`reconciler_test.go`, `reaper_test.go`, both engines) are deleted
  with them; their intents are the tests above (named in the commit).

- [ ] **Step 8: Commit.**

  ```bash
  git add app/grab/engine/command.go app/grab/engine/journal.go app/grab/engine/report.go app/grab/engine/torrent/commands.go app/grab/engine/usenet/commands.go
  git rm app/grab/engine/torrent/reconciler.go app/grab/engine/torrent/reaper.go app/grab/engine/usenet/reaper.go
  git commit -m 'feat(grab)!: engines execute seq-fenced desired-state commands and report transfer and engine records; claims in the journal; no Download reconciler, reaper, finalizer or Event; grabarr-engine retired (ADR-0019 A3.6)' -- app/grab/engine app/grab/agent pkg/download pkg/k8s/fieldmanager.go Makefile config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A3.7: The DownloadClient controller reads engine records; per-engine durables; resync; unidentified transfers; the Download controller, the blocklist sweeper and `directgrab` go

**Spec:** §6.7 ("What the manager does with them", "Resync", unidentified transfers), §5.1
(per-engine durables), §6.11 (`BlocklistSweeper`, DownloadClient Active/Queued, `directgrab`),
§3.3 (the manager-side code that goes), §7.7 (`ProxyUDPUnavailable`,
`UnidentifiedTransferRemoved`); ruling R15.

**Files:**

- Modify: `app/grab/controller/downloadclient/{controller.go,doc.go,diskspace.go}` (engine
  records instead of `aggregateDownloads`; `DiskSpaceOK` from the engines' free bytes; the new
  condition and Events; `EnsureConsumer`/`DeleteSubscription` per ordinal; resync;
  unidentified removal), `app/grab/manager/register.go` (registers only the DownloadClient
  controller), `app/indexer/manager/register.go` (no `directgrab`), `Makefile`
  (`RBAC_PATHS_grabarr := ./app/grab/manager ./app/grab/controller/...`)
- Delete: `app/grab/controller/download/` (the package), `app/grab/controller/downloadclient/blocklist.go`,
  `app/grab/status/` (the package), `app/indexer/controller/directgrab/` (the package)

**Interfaces:**

- Consumes: A1.2 (`DownloadClientStatus.{UnidentifiedTransfers, ResyncSeq}`, the condition),
  A1.5 (`records.Reader`, `recordsource.NewItems`, `OnRecreated`), A1.6 (`EngineConsumer`,
  `EnsureConsumer`, `WorkEngineResyncSubject`, `WorkEngineIDSubject`), A3.3 `dlindex.PinnedTo`.
- Produces: `downloadclient.Reconciler` gains `Bus events.Bus`, `Admin events.StreamAdmin`,
  `Engines *records.Reader[*schema.EngineRecord]`, `Now func() time.Time`,
  `UnidentifiedGate func(ctx context.Context, dc *downloadv1alpha1.DownloadClient) bool` (nil:
  always open; A8.1 sets release N's adoption gate); `downloadclient.EngineRecordSource(bus)`.

- [ ] **Step 1: Status from engine records.** For each ordinal `< replicas`, read the engine
  record at `RecordSubKey(dc.UID, ordinal)`; `active`, `queued`, `seeding`, rates and
  `freeBytes` are their sums (`freeBytes` the minimum of scratch and publish);
  `EngineReady` is True when the workload's ready replicas equal the desired **and** every
  ordinal has a record younger than `lifecycle.EngineRecordFresh` with `ready: true`;
  `DiskSpaceOK` compares the engines' own free bytes with `MinFreeBytes` (the manager's statfs of
  its own mount, `controller.go:204-208`, goes, with `DiskUsage`); `ProxyUDPUnavailable` is True
  while any record says `unavailable`, with one Warning `ProxyUDPUnavailable` (recorder
  `grabarr-engine`) on the transition; `unidentifiedTransfers` is the sum of the records'
  counts. The Download watch (`controller.go:528`) goes; a records source on `clustarr-engines`
  (`recordsource.NewItems`, the record's `item` is the DownloadClient) wakes the controller.
- [ ] **Step 2: Per-engine durables.** On every reconcile, for each ordinal `< replicas`,
  `Admin.EnsureConsumer(events.EngineConsumer(dc.Name, ordinal))` (idempotent); for an ordinal
  `>= replicas` whose durable exists, `DeleteSubscription` once `dlindex.PinnedTo(ns,
  "<client>-<ordinal>")` is empty. On the DownloadClient's deletion (its own finalizer, which
  exists today for the workload), delete every engine durable once nothing is pinned.
- [ ] **Step 3: Resync.** The engine-records source's `OnRecreated` for **`clustarr-transfers`**
  (a second `recordsource` on that bucket, decoding nothing, used only for the recreation signal)
  enqueues every DownloadClient; the reconcile then bumps `status.resyncSeq` to
  `records.NextSeq(status.resyncSeq, 0, now)` and, after the apply, publishes
  `schema.EngineCommand{Desired: "resync", ResyncSeq}` to `WorkEngineResyncSubject(client,
  ordinal)` for each ordinal with `MsgIDForEngineResync`, republishing until each engine record's
  `resyncSeq` reaches it. Until then `lifecycle` judges no entry missing (it reads
  `Client.ResyncSeq`, A3.4).
- [ ] **Step 4: Unidentified transfers.** For each record's `unidentified` entry, remember the
  first time this leader saw it (leader-local); once `lifecycle.UnidentifiedGrace` has passed and
  `UnidentifiedGate(dc)` holds, publish `EngineCommand{DownloadID: id, Desired: "absent",
  RemoveData: false}` to `WorkEngineIDSubject` with `MsgIDForEngineRemoveID`, and record Event
  `UnidentifiedTransferRemoved` (recorder `grabarr-engine`). The usenet engine also removes the
  job's scratch directory, as `usenet/reaper.go:251-285` did.
- [ ] **Step 5: Delete** the Download controller (its finalizer, phase derivation, import
  publish, blocklisting, teardown: all `lifecycle`'s now), the blocklist sweeper (the release
  index sweeps, A3.1), `app/grab/status`, and `directgrab` (A3.5's `CountGrab` effect). RBAC:
  `downloadclient/doc.go` drops `downloads get;list;watch;delete`; `make manifests`; sync the
  chart. Downloads' RBAC for the manager in release N is A8.2's (`get;list;watch;update;delete`
  for adoption and the Migrator), not this task's.

**Tests (deferred to the batch):**

- `TestEngineReadyNeedsAFreshEngineRecord` (`app/grab/controller/downloadclient`).
- `TestDiskSpaceOKReadsTheEnginesFreeBytes`.
- `TestProxyUDPUnavailableIsAConditionAndOneEvent`.
- `TestEveryRenderedOrdinalHasItsDurable` and `TestAScaledDownOrdinalsDurableGoesOnlyWhenNothingIsPinned`
  (membus `StreamAdmin`).
- `TestARecreatedTransfersBucketStartsAResync` (envtest + membus).
- `TestAnUnidentifiedTransferIsRemovedAfterTheGraceWithItsBytesKept`.
- `TestTheGateHoldsUnidentifiedRemovalUntilAdoptionCompletes` (A8.1 supplies the gate).
- `TestOnlyTheManagerEnsuresTopology` (split §5.15): `EnsureConsumer` is called only from the
  manager.

- [ ] **Step 6: Commit.**

  ```bash
  git rm -r app/grab/controller/download app/grab/status app/indexer/controller/directgrab app/grab/controller/downloadclient/blocklist.go
  git commit -m 'feat(grab)!: the DownloadClient controller reads engine records, owns the per-engine durables, resyncs after a transfers-bucket loss and removes unidentified transfers; the Download controller, blocklist sweeper and directgrab go (ADR-0019 A3.7)' -- app/grab app/indexer/manager/register.go app/indexer/controller Makefile config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A3.8: Two-phase imports -- `app/import/importplan`, inspect and execute tasks, materialisation in the manager

**Spec:** §6.9 (all), §3.5 P51-P69, §3.1 W21, W22, W24, §6.10 (the import intent), §6.11
(retrigger; fileimport's Download read); Review Focus 5; rulings R6, R26.

**Files:**

- Create: `app/import/importplan/{doc.go,plan.go,classify.go,messages.go,mapping.go,naming.go}`,
  `app/import/worker/fileimport/{inspect.go,execute.go}`, `app/remediation/downloads/imports.go`
- Modify: `app/import/worker/fileimport/{worker.go,process.go,episode_import.go,nonvideo.go,donor.go,remediation.go,order.go,transcoded.go,wronglanguage.go,place.go}`
  (the decide halves move out; the I/O halves stay as the execute task's steps),
  `app/import/mediafilespec/{spec.go,rename.go}` (the Kubernetes writers -- `Apply`, the new
  `ApplyFacts`, `RenameFile` with `recordRename` and `applyRenamed` -- move to a new manager-only
  package `app/import/mediafilespec/specwrite`; `mediafilespec` keeps the pure half: `Frozen`,
  `FrozenFromSchema`, `ReassertFrozen`, `SameFingerprint`, `Renameable`, `RenameOutcome`,
  `StaleReadError`, and A6.1's `SpecHash`; every caller re-imports -- the rescan until A6.1, the
  rename actuator, the downloads stage), `app/import/agent/{register.go,workers.go}`,
  `app/import/manager/register.go` (no `retrigger`), `app/remediation/downloads/{stage.go,sources.go}`
  (S28, the import decision per Completed entry), `app/grab/lifecycle` (`ImportDecision` fed by
  importplan)
- Delete: `app/import/controller/retrigger/` (the package), `app/import/worker/fileimport/dedup.go`
  (its fast path is the records' seq fence now; `BucketDedup` stays in the topology until a later
  cleanup)

**Interfaces:**

- Consumes: A1.4 (`schema.{ImportInspectTask, ImportExecuteTask, ImportRecord, ImportPlan,
  ImportMove, MediaFileBasis, FrozenFields, PlacedFile, InspectedFile, ProbeSummary}`), A1.5
  `records.{Reader, Writer}`, A1.6 import subjects and `MsgIDForImport`, A3.4 `lifecycle`,
  `pkg/quality`, `pkg/decision` (`LacksLanguage`, `TranscodedFinal` rules), `pkg/naming`.
- Produces:

  ```go
  package importplan // pure: no client, bus, clock or filesystem

  // The ImportMessage* texts move here from api/download (§6.9).
  const (
  	MessageEveryFileRejected = "every candidate file was rejected"
  	MessageExistingFileFinal = "the existing file is transcoded, and a transcoded file is final: import this download by hand to replace it"
  )
  type Input struct {
  	Owner      schema.ItemRef
  	Entry      catalogv1alpha1.DownloadEntry
  	Inspect    *schema.ImportRecord // at entry.import.dispatch.seq, or nil
  	Execute    *schema.ImportRecord
  	Existing   []catalogv1alpha1.MediaFile // the owner's (and covered items') current files
  	Covered    []CoveredItem               // episodes or issues the entry covers, with monitoring and files
  	Profile    quality.Profile
  	OriginalLanguage string
  	RootFolder catalogv1alpha1.RootFolder
  	Naming     NamingContext                // what pkg/naming needs per target
  	Intent     *lifecycle.ImportIntent
  	Files      int                          // MediaFiles naming entry.id
  	Now        time.Time
  	Admit      func(durable string, seq int64) dispatch.Decision // the ledger's, bound by the stage
  }
  func Decide(in Input) lifecycle.ImportDecision
  // classify and conclude move here from fileimport/remediation.go, unchanged in rules:
  func Classify(rs []Rejection) commonv1.ImportRejectionClass
  ```

  `lifecycle.ImportDecision` (declared by A3.4) carries `Summary catalogv1alpha1.DownloadImportSummary`,
  `Dispatch *ImportDispatch{Phase, Subject, MsgID string; Task any}`, `Materialise
  []Materialise{Apply *ApplyMediaFile; Delete *MediaFileBasis; AudioGraft *AudioGraftApply}`,
  `Verdict` (`none`, `imported`, `releaseFault`, `held`, `expired`, `retry`).

  ```go
  package specwrite // app/import/mediafilespec/specwrite, manager-only: FieldManager is mediafilespec.FieldManager
  func Apply(ctx context.Context, c client.Client, namespace, name, rv string, ref commonv1.MediaRef,
  	path string, info os.FileInfo, f mediafilespec.Frozen) (types.UID, error) // moved unchanged
  func ApplyFacts(ctx context.Context, c client.Client, namespace, name, rv string, ref commonv1.MediaRef,
  	path string, sizeBytes int64, modTime time.Time, f mediafilespec.Frozen) (types.UID, error) // Apply calls it
  func RenameFile(ctx context.Context, c client.Client, api client.Reader, mf *catalogv1alpha1.MediaFile, dryRun, keepFolder bool) (mediafilespec.RenameOutcome, error) // moved unchanged
  package mediafilespec // gains:
  func FrozenFromSchema(f schema.FrozenFields, from catalogv1alpha1.ImportSource) Frozen
  ```

- [ ] **Step 1: `importplan`.** Move the decisions of §3.5 P51-P69 out of `fileimport` into pure
  functions over `Input`: only `Completed` entries import (P51); the import intent redirects the
  target and marks `override` (P52); a container kind needs a person (P53); the quality profile
  comes from the entry (P54); suspected samples (P55, from the inspection's size class); best
  file per single-file item (`order.go`'s rank, P56); a probe-corrected quality the profile does
  not allow is `releaseFault` (P57); `TranscodedFinal` refusal
  (`MessageExistingFileFinal`, exempt for a manual import, P58); upgrade or replace each
  existing file and `replacesWrongLanguage` (P59); episode, series-root and scene mapping from
  the inspection (P63's mapping stays in the agent; fill, upgrade and the named-episode
  override here); multi-file items with existing files need a person (P64); all-or-nothing album
  and audiobook replacement (P65); the class precedence and the retry, hold and fail rules
  (P67, P68): `transient` retries at 1 m, 10 m, 45 m by `summary.attempts` and
  `nextAttemptAt` (status, not stream state), then holds; `releaseFault` walks once more, then
  `lifecycle` blocklists (`importRejected`, item scope); `itemState` and `needsPerson` hold at
  once with `heldSince`; a hold older than `ImportHoldRetention` expires (`Failed`,
  `importExpired`, never blocklisted or searched again). Destinations are computed here with
  `pkg/naming` (today's `MovieFilePath` and the episode/non-video folder rules), so the execute
  task carries the final paths. The plan's `Replaces` name each replaced MediaFile's UID,
  resourceVersion and path (the basis). **A missing record is never a verdict** (Review Focus 1):
  an inspect or execute dispatched and unanswered is republished under its Msg-Id inside
  `records.RepublishWindow`, then re-issued at a new seq; a summary that says `inspected` or
  `approved` whose record is gone (a recreated `clustarr-imports`) re-issues the inspect.
- [ ] **Step 2: The inspect handler** (`inspect.go`). `ImportInspectTask` → check the record at
  `RecordSubKey(entry, "inspect")`: drop when its `Seq ≥ task.Seq` (superseded); read the
  payload from the task's `ContentRoot` and `Files`; for each file parse
  (`release.Parse`), probe (`prober`, summarised into `ProbeSummary`), fingerprint, classify the
  size (sample), map to the task's targets (episodes, scene names, absolute numbers, issues),
  freeze the import fields (`freshVideoSpec`, P49) into `FrozenFields`, collect every rejection
  with its class and kind; write the record (`records.Writer`) and ack. No Kubernetes read but
  the cache's of the targets' identity (allowed: read), no write.
- [ ] **Step 3: The execute handler** (`execute.go`). `ImportExecuteTask` → drop when the
  record at `…"execute"` has `Seq ≥ task.Seq`; **check the basis**: `APIReader.Get` each
  `Replaces` MediaFile and refuse (`Refused: "stale"`) when its UID or resourceVersion differ
  (Review Focus 5); `EnsureFreeSpace` (`diskFull`, P60); place each move with `placeFile`
  (containment, recycle-link an existing destination, refuse an overwrite, P62); recycle each
  replaced file (`fsops.Recycle`, P61); reduce an audio donor into `Donor.Dir` (F7.1's reduce
  arrives later; today's donor placement path stays); seed each placed file's probe
  (`probestore.Seed`, as `seedProbe` does); write the record with `Placed` and ack. No MediaFile
  write, no Download read or write, no AudioGraft write.
- [ ] **Step 4: Materialise in the manager** (`app/remediation/downloads/imports.go`): S28
  (`recordsource.NewItems` on `clustarr-imports`) wakes the owner; the stage reads both records
  at the entry's `import.dispatch.seq` and calls `importplan.Decide`; it dispatches the inspect
  (on `Completed`, and again on an `import` intent or a `stale` execute) and the execute (on an
  approved plan) as `DispatchPublish` effects on `importarr-fileimport` (admitted by the ledger,
  `dispatch.Render` on `entry.import.dispatch`); after an execute record lands it emits
  `ApplyMediaFileSpec` for each placed file (name `k8s.ChildName(<target name>, "mediafile",
  dest)`, `importedFrom{downloadRef: entry.id, infoHash: entry.release.infoHash, releaseTitle,
  indexerName, protocol, importedAt, manual}`, field manager `importarr-worker` through
  `mediafilespec.ApplyFacts`) and `DeleteObject` for each replaced MediaFile with its UID
  precondition (W22 had none); a donor placement applies the AudioGraft under `importarr-worker`
  as `donor.go:applyAudioGraft` did (R26: until F7.1). The entry reads `Imported` only when
  `Files` MediaFiles name its `id` (`lifecycle`, A3.4).
- [ ] **Step 5: Retire.** Delete the retrigger controller (the `download.clustarr.io/import`
  intent replaces it) and `dedup.go`; the fileimport `Worker.Handle` dispatches on the envelope
  schema (`ImportInspectTask`, `ImportExecuteTask`; a v1 `ImportTask` is
  `events.Discard`ed with reason "superseded by ADR-0019 two-phase imports"); RBAC: the
  fileimport package keeps only reads (`mediafiles`, the item kinds, `rootfolders`,
  `qualityprofiles` `get;list;watch`) -- its `mediafiles create;update;patch;delete` and
  `downloads` and `audiografts` writes go (W21, W22, W23-until-F7.1-in-the-manager, W24);
  `make manifests`; sync the chart.

**Tests (deferred to the batch):**

- `TestImportplanCarriesEveryRuleOfFileimport` (`app/import/importplan`): one case per P51-P69
  rule, built through the real parser and a recorded inspection (CLAUDE.md, "a test built from
  a fixture shaped like the answer cannot fail").
- `TestATransientImportRetriesOnStatusNotStreamState` (`importplan`: 1 m, 10 m, 45 m, then held).
- `TestAHeldImportExpiresAfterTwentyFourHoursAndIsNeverBlocklisted` (`importplan`).
- `TestTheExecuteTaskRefusesAStaleBasis` (`app/import/worker/fileimport`, envtest: the
  MediaFile's resourceVersion moved between inspect and execute).
- `TestInspectAndExecuteWriteNoKubernetesObject` (`app/import/worker/fileimport`, a fake client
  that fails every write).
- `TestMaterialiseDeletesReplacedFilesWithAUIDPrecondition` (`app/remediation/downloads`,
  envtest: a recreated same-name MediaFile is not deleted).
- `TestTheEntryReadsImportedOnlyWhenEveryPlacedFileHasItsMediaFile` (`app/remediation/downloads`).
- `TestTheImportIntentReissuesTheInspect` (`app/remediation/downloads`).
- The existing `fileimport` tests that drove `Handle` with an `ImportTask`: rewritten against
  the two handlers and `importplan` (named in the batch).

- [ ] **Step 6: Commit.**

  ```bash
  git add app/import/importplan app/import/worker/fileimport/inspect.go app/import/worker/fileimport/execute.go app/remediation/downloads/imports.go app/import/mediafilespec/specwrite
  git rm -r app/import/controller/retrigger app/import/worker/fileimport/dedup.go
  git commit -m 'feat(import)!: imports run in two phases -- the agent inspects and executes, app/import/importplan decides in the manager, which materialises MediaFiles and deletes replaced ones with UID preconditions; retrigger retires (ADR-0019 A3.8)' -- app/import app/remediation/downloads app/grab/lifecycle config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A3.9: Readers and the ui -- no Download anywhere but the Migrator's

**Spec:** §6.11 (every row), §6.12 (the ui), §6.10 (the ui's actions), §8.5 (dead letters
resolve to the owner), §3.4 (every reader outside `app/grab`).

**Files:**

- Modify: `app/catalog/worker/search/{snapshot.go,blocklist.go,donor.go,nonvideo.go}`
  (`queue` reads the owner's entries; `CurrentFile` compares
  `spec.importedFrom.infoHash`; `IndexDownloadTarget` and `FieldIndexes` deleted),
  `app/catalog/worker/rssmatcher/resolve.go` (`queueFor`, `currentFile` likewise),
  `app/catalog/agent/catalog/register.go` and `app/catalog/agent/events/register.go`
  (`Indexes` drop `search.FieldIndexes()`), `app/catalog/controller/search/reconciler.go`
  (`inFlightDownload` reads entries through `dlindex`; `resolveGrab`'s Download apply stays until
  A4.3), `app/catalog/history/replay/replay.go` (`ReplayKinds` drops Download; `replay-download`
  is not registered), `pkg/k8s/deadletter.go` (Download stays annotatable until A9.1),
  `ui/projection/{projection.go,index.go}`, `ui/{routes.go,sse.go,server.go}`,
  `ui/views/downloads.templ` (and its generated `_templ.go`), `ui/actions/{downloads.go (create),actions.go}`,
  `pkg/pipeline/{project.go,stage.go}`, `internal/cli/ui/{command.go,bus.go}`,
  `config/rbac/ui_role.yaml` and its chart copy (`downloads` leaves; `patch` on the six owner
  kinds is already granted through `MediaKinds()`)

**Interfaces:**

- Consumes: A3.3 `dlindex`, A3.5 `rollup` entry functions, A1.2 annotations.
- Produces:

  ```go
  package projection // ui/projection
  type DownloadRow struct {
  	Owner     schema.ItemRef
  	OwnerKind commonv1.MediaKind
  	Entry     catalogv1alpha1.DownloadEntry
  	Covered   []string // episode or issue names, for a Series or Comic entry
  }
  func (p *Projection) Downloads(ctx context.Context) []DownloadRow
  func (p *Projection) SubscribeDownloads() (<-chan []DownloadRow, func())

  package ui // ui.Options:
  //	SubscribeDownloads func() (<-chan []projection.DownloadRow, func())
  //	TransferProgress   func(ctx context.Context, entryUID string) (schema.DownloadProgress, bool, error)
  //	ImportDetail       func(ctx context.Context, entryUID string) (schema.ImportRecord, bool, error)

  package actions // ui/actions, merge patches of the owner's annotations under clustarr-ui:
  func PauseDownload(ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string, paused bool) error
  func SetDownloadPriority(ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID, priority string) error
  func RemoveDownload(ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string, data, blocklist bool, now time.Time) error
  func ResumeDownload(ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string, now time.Time) error
  func RetryImport(ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string, target *schema.ItemRef, override bool, now time.Time) error
  func UnblockRelease(ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, infoHashOrIndexerGUID string, global bool, now time.Time) error

  package pipeline // Related.Downloads []Download becomes:
  //	Entries []catalogv1alpha1.DownloadEntry
  ```

- [ ] **Step 1: Agents.** The search worker's `queue` (`snapshot.go:283`) and the RSS matcher's
  `queueFor` read the owner's `status.downloads` (the Series' for an episode, filtered by
  `rollup.CoversEpisode`) from the cache; `CurrentFile` (`snapshot.go:237`) reads the source
  hash from `spec.importedFrom.infoHash` instead of a Download Get. Both stop declaring the
  Download index; the catalog and events domains' `Registration.Indexes` lose it. (A4.3 and
  A4.4 delete these reads entirely; this keeps the agents compiling and correct meanwhile.)
- [ ] **Step 2: Manager readers.** The Search controller's `inFlightDownload` reads the target's
  owner entries (`rollup.ActiveEntry` with `CoversEpisode` for an episode, fixing the pack gap
  noted at `reconciler.go:828`); `replay.ReplayKinds` drops the Download row and `registerReplay`
  no longer builds `replay-download` (§6.11); its RBAC marker (`downloads get;list;watch;patch`,
  `replay.go:60`) goes.
- [ ] **Step 3: The ui.** `ui/projection` stops listing Downloads (`watchedKinds`,
  `projection.go:470-479`; `buildRelatedIndex`'s Download List, `index.go:124`) and builds
  `DownloadRow`s from the six owner kinds it already lists (one row per entry, keyed by entry
  uid); `SubscribeDownloads` fans them out; `pipeline.Related.Entries` replaces `Downloads` and
  `project.go`'s helpers (`failedDownload`, `blockedDownload`, `importedDownload`,
  `importingDownload`, `downloadedDownload`, `downloadingDownload`) read entries.
  `ui/views/downloads.templ` renders the same columns from the entry plus
  `TransferProgress` (1 Hz rate, ETA, peers, ratio) and links each row to its owner's page, whose
  import detail reads `ImportDetail`. `internal/cli/ui` binds `TransferProgress` to the
  read-only bus (`bus.KV(events.BucketProgress).Get(ctx, engine.ProgressKey(uid))` under a 2 s
  deadline) and `ImportDetail` to `bus.KV(events.BucketImports)` (`RecordSubKey(uid, "inspect")`
  and `"execute"`); both are reads (`TestUINeverWrites` unchanged). The Downloads page's
  actions call the new `ui/actions` functions, each a merge patch of one annotation on the owner
  (one-shot values `"<unix time> <id> …"` per §6.10's grammar), routed `POST
  /downloads/{ns}/{kind}/{owner}/{entry}/{pause|resume|remove|retry-import}` and
  `POST /library/{tab}/{ns}/{name}/unblock` with `HX-Target: action-status`.
  `ui_role.yaml` drops `downloads`; `actions.Grants()` is unchanged (`patch` on `MediaKinds()`
  already covers the owners).

**Tests (deferred to the batch):**

- `TestTheSearchWorkersQueueReadsTheOwnersEntries` (`app/catalog/worker/search`).
- `TestCurrentFileReadsTheImportedInfoHash` (`app/catalog/worker/search`).
- `TestTheProjectionBuildsOneRowPerEntry` (`ui/projection`).
- `TestTheDownloadsPageRendersEntriesAndProgress` (`ui`).
- `TestDownloadActionsPatchOnlyTheOwnersAnnotations` (`ui/actions`).
- `TestUIWiresEveryUIOption` (`cmd/ui`, W5.11's; the loop spec's
  `TestBothUICommandsWireEveryUIOption`): `TransferProgress` and `ImportDetail` set.
- `TestUINeverWrites`, `TestUIRoleGrantsOnlyReadsAndActionWrites`, `TestUIRoleChartMatchesConfig`
  (existing, rerun).
- `TestPipelineProjectsEntries` (`pkg/pipeline`, rewriting the Download fixtures).
- `TestNothingButTheMigratorNamesDownload` (`test/guards`): outside `api/download`,
  `app/catalog/legacyfold` (A8), `cmd/manager`'s `downloads export` (A8.3), `pkg/k8s/deadletter.go`
  and `test/e2e`, no production file names `downloadv1alpha1.Download` or `DownloadList`.

- [ ] **Step 4: Commit.**

  ```bash
  git add ui/actions/downloads.go
  git commit -m 'feat(ui)!: the Downloads page and pipeline read grab entries and clustarr-progress; download intents patch the owner; agents and the Search controller read entries; replay-download retires (ADR-0019 A3.9)' -- app/catalog ui pkg/pipeline internal/cli/ui pkg/k8s/deadletter.go config/rbac/ui_role.yaml charts/clustarr/templates/rbac.yaml
  ```

---

### Task A3.10: Wave A3 gate

```bash
cd /home/appkins/src/mediactl/clustarr-unify
go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
test ! -d app/grab/controller/download && test ! -d app/grab/status && test ! -d app/indexer/controller/directgrab && test ! -d app/import/controller/retrigger && echo gone
grep -rln 'downloadv1alpha1\.Download\b\|downloadv1alpha1\.DownloadList\|downloadv1\.Download\b' --include='*.go' . | grep -v _test.go | grep -vE '^\./(api/|test/e2e/|pkg/k8s/deadletter.go|app/catalog/worker/grab/|app/catalog/worker/redownload/|app/catalog/controller/search/|app/catalog/grabsource/)'   # nothing (the four excluded paths are A4's: the grab worker, redownload, resolveGrab's Download apply and grabsource.Covers; the Migrator arrives in A8)
grep -rn 'kubebuilder:rbac' app/grab/engine app/grab/agent | grep -vE 'verbs=get;list;watch|resources=secrets,verbs=get' | wc -l   # 0
grep -rn 'Client\.\(Create\|Update\|Patch\|Delete\)\|k8s\.\(Apply\|PatchStatus\|PatchStatusCAS\|EnsureFinalizer\|RemoveFinalizer\)' app/grab/engine app/grab/agent app/import/worker/fileimport --include='*.go' | grep -v _test.go | wc -l   # 0
go list -deps ./cmd/manager | grep -cE 'ffgo|purego|onnx|anacrolix|pkg/download/usenet|pkg/relindex'   # 0
for r in app/grab/agent/torrent app/grab/agent/usenet app/import/agent; do go list -deps ./$r | grep -cE 'app/grab/lifecycle|app/import/importplan|app/remediation'; done   # 0 0 0
```

---
## Wave A4: grab and search policy (A4.1-A4.5)

**Spec:** §6.6 (all), §7.3, §7.4, §5.1 (the two search rows), §5.4 (the `search` Pacer class),
§3.5 P1-P25, §3.1 W1-W3, W5, W6, §6.11 (the search worker's queue, donor queue and current file;
the grab guard; the RSS matcher; redownload); rulings R7, R12, R13 (search record key), R22,
R24, R25.

**Depends on:** A3.10. **Parallel with:** A5 and A6. **Order inside:** A4.1 → A4.2 → A4.3 →
A4.4 → A4.5 (A4.1 and A4.2 both add stages to `app/remediation/manager/register.go`; A4.3 and A4.4
both edit `app/catalog/worker/search` and `rssmatcher`).

**What A4 delivers.** Every decision to search or grab is an item key's: the search planner
dispatches searches under admission and pacing and does their bookkeeping; the grab planner turns
scored candidates (search answers, RSS matches, a person's pick) into new `Pending` entries or a
pending grab with `grabAt`; agents only evaluate and rank. The KV grab lease, `clustarr-pending`,
scheduled grab messages and the redownload consumer stop being used.

---

### Task A4.1: `app/catalog/grabplan` and the grab stage

**Spec:** §6.6 steps 1-5, §3.5 P8 (split), P13-P20, P23 (narrowing), P24-P25; §6.7 ("whether the
owner still wants the release"); rulings R7, R25.

**Files:**

- Create: `app/catalog/grabplan/{doc.go,plan.go,candidates.go,delay.go,narrow.go,wants.go}`,
  `app/remediation/grab/{stage.go,gather.go,sources.go,doc.go}`
- Modify: `pkg/decision/{evaluate.go,checks.go}` (split: `EvaluateRelease` and
  `ItemStateRejections`, `Evaluate` = both, unchanged behaviour), `app/catalog/delay/resolve.go`
  (gains `Bypasses`, `DelayFor`, `ProtocolEnabled`, moved from `app/catalog/worker/grab/delay.go`),
  `app/remediation/downloads/stage.go` (`StillWants` from `grabplan.StillWants`),
  `app/remediation/manager/register.go` (`Stages` gains the grab stage)

**Interfaces:**

- Consumes: A1.4 `schema.{Candidate, ScoredRelease, SearchRecord}`, A2.3 `intake.{Inbox,
  Parked, Outcome}`, A3.3 stages, A3.4 `lifecycle.{EntryID, ChooseClient}`, `limits.Grabs` (the
  ring read, `app/indexer/limits/ring.go:271`), `app/catalog/delay.Resolve`, `pkg/quality`.
- Produces:

  ```go
  package grabplan // pure

  const CandidateWindow = 30 * time.Minute

  type Candidate struct {
  	MsgID     string // "" for a search answer
  	Release   schema.ScoredRelease
  	Source    commonv1.DownloadSource
  	GrabbedBy commonv1.GrabSource
  	Purpose   commonv1.DownloadPurpose
  	Manual    bool
  	Override  bool
  	Targets   []schema.ItemRef // the covered Episodes or Issues; empty for a single-item owner
  	At        time.Time
  }
  type Covered struct { // one item a candidate may cover
  	Ref       schema.ItemRef
  	Season, Number int32 // episodes
  	Issue     string
  	Monitored bool
  	Current   *decision.Current
  	Wanted    bool // monitored, aired or available, cutoff unmet or no file
  }
  type View struct {
  	Owner      schema.ItemRef
  	Kind       commonv1.MediaKind
  	Now        time.Time
  	Monitored  bool
  	Profile    *quality.Profile
  	Delay      *catalogv1alpha1.DelayProfileSpec
  	Entries    []catalogv1alpha1.DownloadEntry // stored: the queue and the Blocklisted tombstones
  	Pending    []catalogv1alpha1.PendingGrab   // pendingGrab (as a one-element list) or pendingGrabs
  	Covered    []Covered
  	Candidates []Candidate
  	GrabLimited map[string]time.Time           // indexer → retry time, from the ring
  	EntryCap   int
  }
  type Plan struct {
  	NewEntries []catalogv1alpha1.DownloadEntry // Pending, uid issued (types.UID(uuid.NewUUID())), dispatch.seq issued
  	Pending    []catalogv1alpha1.PendingGrab
  	Settle     map[string]intake.Outcome       // by candidate MsgID
  	Due        time.Time                       // the soonest grabAt
  	Events     []lifecycle.Event               // e.g. AtCapacity when a candidate is refused for the cap
  }
  func Decide(v View) Plan
  // StillWants is lifecycle's convergence input (§6.7): monitored, not
  // blocked for the release, and either no live entry of that purpose and no
  // file at or above the release's quality, or imported with its seed goal not
  // met, or still an upgrade over the current file.
  func StillWants(v View, claim schema.TransferClaim) (bool, string)

  package decision // gains:
  func EvaluateRelease(ctx context.Context, t Target, p quality.Profile, cat *catalogue.Catalogue, rels []common.ReleaseInfo, o Options) []Decision
  func ItemStateRejections(t Target, d Decision, o Options) []common.Rejection // queue, blocklist (t.Blocklist), current file and upgrade, TranscodedFinal, donor queue/rejected
  ```

- [ ] **Step 1: `pkg/decision` split.** Move the item-state checks of `evaluate.go:126-145`
  (blocklist and history, queue, transcoded, upgrade, donor queue and rejected) behind
  `ItemStateRejections`; `EvaluateRelease` runs the rest (identity, protocol, availability, size,
  quality, language/audio, sample, donor language). `Evaluate` calls both in today's order, so
  every existing `pkg/decision` test keeps passing.
- [ ] **Step 2: `grabplan.Decide`.** For each candidate, newest first: drop it when its record
  is older than `CandidateWindow` (search answers); re-check with `ItemStateRejections` against
  the fresh `Entries` (queue preference), each covered item's `Current` (cutoff,
  `TranscodedFinal` -- exempt when `Manual`, as `resolveGrab` does), wrong language, monitoring,
  the release's `Blocked` state and the owner's `Blocklisted` tombstones (exempt when
  `Override`); narrow a pack to the wanted covered items (P23: a pack covering no wanted episode
  is refused; a pack covering some is taken as covering those); pick the best approved
  (`decision.Rank`'s order, P13), skipping an indexer in `GrabLimited` for the next one, holding
  the soonest when every approved is limited; apply the delay profile (`delay.Resolve` already
  resolved in gather; `Bypasses` and `DelayFor`, P15) -- never delaying a donor -- by keeping the
  best as a `PendingGrab` with `grabAt` and `candidate` (P16, keep-best by `quality.Compare` then
  score), else grabbing now: a new `Pending` entry with `id` = `lifecycle.EntryID(target,
  purpose, guid)`, `uid` issued, `release` clamped (title to 512 on a rune boundary; a guid over
  1024 refuses the candidate), `source`, `grabbedBy`, `purpose`, `manual`, `qualityProfileRef`,
  `seedCriteria` from the Indexer, effective `removeOnImport` and `removeDataOnDelete` (true,
  today's defaults), `episodes`/`issues` from the narrowing, `grabbedAt`, `phase: Pending`,
  `dispatch.seq = NextSeq(0, 0, now)`. A pending grab whose `grabAt` has passed is grabbed from
  its stored `candidate` (P17-P18 retire). An owner at `EntryCap` refuses (or pends) with a
  Normal Event `AtCapacity`, never truncating. Every intake candidate is settled: `incorporated`
  (grabbed), `pended`, or `refused`.
- [ ] **Step 3: The stage** (`app/remediation/grab`): applies to the six owner kinds, and to
  Episode and Issue in view mode: there it reads the container's `pendingGrabs` from the cache and
  puts the one covering this item (`rollup.CoversEpisode`/`CoversIssue` over its `episodes` or
  `issues`), or nil, into the item's `catalogarr-grab` `Set` as `pendingGrab` (§6.2: a view,
  under the manager that owns `pendingGrab`), merged with the search stage's bookkeeping fields;
  nothing else. For the owners, gather: `Inbox.Take(owner)`; the owner's search record (`RecordKey(owner uid)`) and, for a
  Series or Comic, the records of covered Episodes or Issues whose `searchDispatch` was answered
  inside `CandidateWindow` (R7); the QualityProfile (`quality.FromCRD`), the DelayProfile
  (`delay.Resolve` over the namespace's DelayProfiles and the item's tags); the covered items and
  their current files (`mfindex.Item`, `rollup.PickMediaFile`, `search.CurrentFile`'s logic now
  in `grabplan`); the Indexers' `downloadClientRef` and the grab ring (`limits.Grabs` read from
  `BucketIndexerLimits`, reserving nothing: the index agent's payload fetch reserves, P95). Plan:
  `grabplan.Decide`; output `NewEntries` (to the downloads stage, which admits them through
  `lifecycle`), the `catalogarr-grab` `Set` with `pendingGrab` (Movie, Album, Book, Audiobook) or
  `pendingGrabs` (Series, Comic), `SettleCandidate` effects, a `History` effect publishing
  `CatalogReleaseSubject(ActionGrabbed, ownerUID)` for each grab (as `publishGrabbed`,
  `perform.go:499`), `Due` = the soonest `grabAt`.
- [ ] **Step 4: Convergence input.** The downloads stage's `lifecycle.View.StillWants` becomes
  `grabplan.StillWants` over the same gathered view (A3.4's default retires).

**Tests (deferred to the batch):**

- `TestDecisionSplitIsBehaviourPreserving` (`pkg/decision`): `Evaluate` equals `EvaluateRelease`
  followed by `ItemStateRejections` over the existing corpus.
- `TestGrabplanReChecksAgainstFreshState` (`app/catalog/grabplan`): a candidate approved by the
  agent but now queued, or now below a better imported file, is refused.
- `TestATombstoneRejectsACandidateFromBeforeTheBlock` (R7, §6.14 "No regrab in the gap").
- `TestADelayedGrabIsMadeFromStatusAlone` (`grabplan`).
- `TestAPackIsNarrowedToTheWantedEpisodes` (`grabplan`).
- `TestAManualPickIsExemptFromTranscodedFinalAndOverrideFromBlocklisted` (`grabplan`).
- `TestAnOwnerAtItsCapPendsNeverTruncates` (`grabplan`).
- `TestStillWantsKeepsAnUpgradeAndAnUnmetSeedObligation` (`grabplan`; Review Focus 3).
- `TestEveryIntakeCandidateIsSettled` (`app/remediation/grab`).

- [ ] **Step 5: Commit.**

  ```bash
  git add app/catalog/grabplan app/remediation/grab
  git commit -m 'feat(grab): the grab decision is the owner key'"'"'s -- app/catalog/grabplan re-checks scored candidates against fresh item state, delays, narrows packs and keeps the best pending in status; pkg/decision splits release evaluation from item-state rules (ADR-0019 A4.1)' -- app/catalog/grabplan app/remediation pkg/decision app/catalog/delay
  ```

---

### Task A4.2: The search planner -- due, `IndexOnly`, the donor cap, pacing, admission, bookkeeping; `wantedcron` keeps its timer

**Spec:** §7.3, §5.1 (automatic search row), §5.4 (the `search` class), §3.5 P1-P7, P11, P12;
rulings R12, R13, R25.

**Files:**

- Create: `app/catalog/searchplan/{doc.go,plan.go,donor.go}`, `app/remediation/search/{stage.go,sources.go,doc.go}`
- Modify: `app/catalog/controller/wantedcron/runnable.go` (no `WantedScan` publish: it lists
  candidates and sends their refs on `Wakes()` at `LowPriority`, and refills the donor budget per
  namespace), `app/remediation/manager/register.go` (stage; S29 source; the wantedcron wakes
  channel), `internal/cli/manager/{options.go,command.go,run.go}` (`--search-live-per-minute`,
  default 20, into the Pacer's `search` rate; `run.go` creates the wake channel and hands it to
  both `wantedcron.Runnable` and `remediationmanager.Options.WantedWakes`)

**Interfaces:**

- Consumes: A1.2 `SearchDispatch`; A1.4 `schema.{SearchTask (v2), SearchRecord}`; A1.5
  `PaceSearch`, `records.Reader`, `recordsource.NewItems`; A2.2 ledger, `Render`; A3.3 stages,
  `DownloadsOutcome.Redownloads`; `wantedcron.{Backoff, Eligible, Candidate, ListCandidates}`.
- Produces:

  ```go
  package searchplan // pure
  type View struct {
  	Item         schema.ItemRef
  	Container    *schema.ItemRef
  	Kind         commonv1.MediaKind
  	Now          time.Time
  	Monitored    bool   // an Episode needs its Series monitored too (P2)
  	Searchable   bool   // the kind can be grabbed (P1)
  	Wanted       bool   // Wanted or CutoffUnmet (wantedcron.Candidate's rule)
  	Profile      string // "" → no dispatch (P3)
  	Protocols    []commonv1.Protocol // from the DownloadClients (P7)
  	Priorities   map[string]int32    // Indexer spec.priority (P9's finding)
  	Dispatch     *catalogv1alpha1.Dispatch
  	Record       *schema.SearchRecord // at Dispatch.Seq
  	LastSearched *metav1.Time
  	Attempts, DonorAttempts commonv1.Attempts
  	Donor        *DonorWant           // P4: languages, anchor, rejected, in flight; nil when no donor is wanted
  	DonorBudget  bool                 // the namespace's first-time live donor budget has room
  	Redownload   *time.Time           // R25: the failure time of a covering Failed or Blocklisted entry
  }
  type Plan struct {
  	Dispatch       *catalogv1alpha1.Dispatch // the next searchDispatch
  	Publish        *Task                     // subject, Msg-Id, payload, lane (normal|low), IndexOnly
  	LastSearchedAt *metav1.Time
  	Attempts, DonorAttempts commonv1.Attempts
  	Answered       bool                      // the record at Dispatch.Seq was incorporated
  	Due            time.Time
  }
  func Decide(v View, admit func(durable string, seq int64) dispatch.Decision, pace func() time.Time) Plan
  // DonorBudget is leader-local: at most DonorSearchesPerSweep (20) first-time
  // live donor searches per namespace per wantedcron period (§7.3).
  type DonorBudget struct{ /* … */ }
  func (b *DonorBudget) Refill(namespace string, n int)
  func (b *DonorBudget) Take(namespace string) bool
  ```

- [ ] **Step 1: `searchplan.Decide`.** Due when `Wanted` and monitored and searchable and
  `wantedcron.Eligible(attempts, now)` (6 h · 2^n, capped at 7 d, P11), or at once for a
  `Redownload` newer than `Dispatch.dispatchedAt` and younger than `RedownloadWindow` (R25);
  never while a dispatch is in flight (`Dispatch.InFlight()`), except that a record at its seq is
  first incorporated: `answeredSeq = seq`, `LastSearchedAt = record.FinishedAt`, `Attempts` (or
  `DonorAttempts` for `purpose: audioDonor`) incremented unless `record.AllPaced` (P6), the
  dispatch's `delivery` cleared. A dispatch outstanding past 15 min (`AckWait × MaxDeliver` of
  `catalogarr-search-normal` plus 5 min) closes as timed out without counting. `IndexOnly` when
  the item was searched before (`Attempts.Count > 0 || LastSearched != nil`, P12); a live search
  first reserves `PaceSearch/<namespace>` (`Paced` while the slot is in the future), then
  `admit("catalogarr-search-normal", seq)` (`Budget`, `NoAgent`); index-only searches skip the
  pacer. Donor searches (P4) are due only with `DonorBudget` room for a first-time live one.
  The task (`SearchTask` v2): `Item`, `Container`, `Seq`, `QualityProfile`, `Protocols`,
  `IndexerPriorities`, `Scope: schema.BlockScopeOf(item)`, `IndexOnly`, `Purpose`, `Donor`,
  `MediaRef` and `Keys` as today; subject `WorkSearchSubject(PriorityNormal or PriorityLow,
  mediaKey)` (`low` for `IndexOnly`), Msg-Id `MsgIDForSearch(item uid, seq)`.
- [ ] **Step 2: The stage** (`app/remediation/search`): applies to Movie, Episode, Album, Book,
  Audiobook, Issue (R12). Gather the record (`records.Reader` on `clustarr-searches`, key
  `RecordKey(item uid)`), the DownloadClients' protocols, the Indexers' priorities, the Series'
  monitoring for an Episode, the donor want (`search.donorWant`'s reads, now here), the
  `Redownload` time from the container's or own `Downloads` outcome (A3.3 threads
  `DownloadsOutcome` into the view). Output the `catalogarr-grab` `Set` fields
  `searchDispatch` (with `dispatch.Render`), `lastSearchedAt`, `searchAttempts`,
  `donorSearchAttempts` (Movie, Episode); a `DispatchPublish` effect; a `DispatchAnswered` effect
  on incorporation; `Due`.
- [ ] **Step 3: Sources.** **S29** `recordsource.NewItems` on `clustarr-searches`: a record whose
  `item` is a searchable kind enqueues it, and its `container` (an Episode's Series, an Issue's
  Comic) too (R7). `wantedcron.Runnable` keeps its schedule (`TwelveHourly`), lists
  `ListCandidates` per namespace, `Refill`s the donor budget with 20 per namespace, and sends
  each candidate's ref on a `Wakes()` channel the loop enqueues at `LowPriority`; the
  `WantedScan` publish and its consumer path in the search worker (`wantedscan.go`) retire with
  A4.3.
- [ ] **Step 4: Register.** The ledger registers a `dispatch.Source` for
  `catalogarr-search-normal` that rebuilds from every searchable item's `searchDispatch` (`seq >
  answeredSeq`) through the cache.

**Tests (deferred to the batch):**

- `TestTheSearchPlannerFollowsTheBackoffLadder` (`app/catalog/searchplan`).
- `TestAnIndexOnlySearchIsNotPacedAndALiveOneIs` (`searchplan`).
- `TestNoBurstOfLiveSearchesReachesAnIndexer` (`app/remediation/search`, envtest + membus: 104
  due items in one namespace publish at most 20 live searches in the first minute; CLAUDE.md,
  2026-10-06).
- `TestAPacedAnswerDoesNotCountAnAttempt` (`searchplan`).
- `TestTheDonorBudgetCapsFirstTimeLiveDonorSearchesPerSweep` (`searchplan`).
- `TestABlocklistedEntryMakesItsItemsDueAtOnce` (`searchplan`, R25), a failed pack per episode.
- `TestAnEpisodeSearchRecordWakesTheEpisodeAndItsSeries` (`app/remediation/search`).
- `TestWantedcronOnlyEnqueues` (`app/catalog/controller/wantedcron`).

- [ ] **Step 5: Commit.**

  ```bash
  git add app/catalog/searchplan app/remediation/search
  git commit -m 'feat(search): the item key'"'"'s search planner decides due, IndexOnly, the donor cap and pacing, admits and dispatches, and keeps the bookkeeping; wantedcron keeps only its timer (ADR-0019 A4.2)' -- app/catalog/searchplan app/remediation app/catalog/controller/wantedcron internal/cli/manager
  ```

---

### Task A4.3: Search answers in records; the Search controller on records and candidates

**Spec:** §7.4, §6.6 step 4 (a user's pick), §3.1 W3, §3.5 P8-P10; rulings R13, R24.

**Files:**

- Modify: `app/catalog/worker/search/{worker.go,snapshot.go,donor.go,nonvideo.go,rank.go,request.go,rpc.go}`
  (`handleSearchTask` answers in a record; no `applySearchStatus`, no `Sink`, no
  `RecordSearchAttempt`, no blocklist, queue or current-file reads, no `WantedScan` handling),
  `app/catalog/agent/catalog/register.go` (no `grab.Sink`; the worker gets a
  `records.Writer[*schema.SearchRecord]`), `app/catalog/controller/search/{reconciler.go,grab.go}`
  (incorporate the record into Search status under `catalogarr-worker` by compare-and-swap; picks
  and `grabBest` become `Candidate` intake messages; `status.grabbed` from the owner's entries),
  `app/catalog/history/target.go` (v2 `SearchTask` → its `Item`)
- Delete: `app/catalog/worker/search/wantedscan.go`

**Interfaces:**

- Consumes: A1.4, A1.5 `records.Writer`, A1.6 `IntakeCandidateSubject`, `MsgIDForCandidate`;
  A4.1 `decision.ItemStateRejections`.
- Produces: `search.Worker` gains `Records *records.Writer[*schema.SearchRecord]` and loses
  `Sink`, `Index`, `DonorSearchesPerSweep`; `searchctl.Reconciler` gains `Searches
  *records.Reader[*schema.SearchRecord]`, `Bus events.Bus` (publishes candidates); a
  `recordsource.NewItems` on `clustarr-searches` whose `item` kind `Search` wakes the Search
  controller.

- [ ] **Step 1: The agent answers.** `handleSearchTask` (v2): `Superseded` check against the
  record at the task's key (its `Seq ≥ task.Seq` → ack and drop); build the request with the
  task's `Scope`, protocols and indexer priorities (the agent reads no DownloadClient, P7);
  evaluate with `decision.EvaluateRelease` (the item's identity and runtime from the cache read
  the agent already does; `Target.Blocklist` over the answer's block state, A3.1; no `Queue`, no
  `Current`), rank and cap at 200 (`RankAndCap` with `IndexerPriority` from the task, P9's
  finding); write `SearchRecord{Item, Container, Task, Seq, FinishedAt, QueryMode, AllPaced,
  IndexerOutcomes (≤ 100), Candidates (≤ 200, each with its parsed coverage and `Blocked`)}`
  through the writer; ack. An interactive task (`SearchRef` set) is keyed by the Search's UID,
  an automatic one by the item's (R13). A v1 task is answered the same way with what it
  carries (decoding v1 into v2, A1.4).
- [ ] **Step 2: The Search controller.** It keeps `startSearch` (publishing the v2 task at
  `PriorityHigh` with `SearchRef`, `Item`, `Seq`) and incorporates the record: `finishedAt`,
  `indexerOutcomes`, `results` (each `ReleaseDecision` with the item-state rejections
  `decision.ItemStateRejections` adds against the target's fresh state, R24) under
  `catalogarr-worker` through `k8s.PatchStatusCAS` (W3's fence). `resolveGrab` keeps its
  `TranscodedFinal` exemption logic but, instead of applying a Download (`reconciler.go:804`),
  publishes a `schema.Candidate{Owner: <the target's owner: the item, or the Series for an
  episode>, Targets, Origin: "interactive" (spec.grab) or "search" (grabBest), SearchRef,
  Manual: <spec.grab>, Override: spec.override, GrabbedBy: interactive|search, Release}` to
  `IntakeCandidateSubject` with `MsgIDForCandidate("pick", searchUID, indexer, guid)`.
  `status.grabbed[]` reads each requested guid's entry on the owner (`dlindex`) and reports
  `created`, `pending` or `refused` from the entry's presence and the intake outcome.
- [ ] **Step 3: Retire.** `wantedscan.go` goes (the planner owns the sweep, A4.2); the
  `FilterCatalogWanted` filter stays on `catalogarr-search-normal` (R7: names never change) with
  nothing published to it. RBAC: the search worker's package loses `searches/status` and
  `downloads`; the Search controller's package loses `downloads create;patch`.

**Tests (deferred to the batch):**

- `TestTheSearchAgentAnswersOnlyInARecord` (`app/catalog/worker/search`, a fake client failing
  every write).
- `TestASupersededSearchTaskIsDropped` (`app/catalog/worker/search`).
- `TestTheSearchControllerIncorporatesTheRecordByCAS` (`app/catalog/controller/search`, envtest
  with an interleaved writer).
- `TestInteractiveResultsStillShowItemStateRejections` (R24).
- `TestAPickBecomesAManualCandidateForTheOwner` (`app/catalog/controller/search`).
- `TestTranscodedFinalBindsGrabBestButNotAPick` (`app/catalog/controller/search`, main's
  `22230298` rule kept).

- [ ] **Step 4: Commit.**

  ```bash
  git rm app/catalog/worker/search/wantedscan.go
  git commit -m 'feat(search)!: the search agent answers every search in clustarr-searches; the Search controller incorporates interactive answers and sends picks to the owner as candidates (ADR-0019 A4.3)' -- app/catalog/worker/search app/catalog/agent/catalog/register.go app/catalog/controller/search app/catalog/history/target.go config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A4.4: RSS candidates; the grab worker, leases, pending records, scheduled grabs and redownload retire

**Spec:** §6.6 steps 1, 3, 5, §3.5 P13-P25, §3.1 W1, W5, W6, §6.11 (grab guard, RSS matcher,
redownload); ruling R22.

**Files:**

- Modify: `app/catalog/worker/rssmatcher/{handler.go,resolve.go}` (match, evaluate, publish a
  `Candidate` per matched owner; no `grab.Decide`, no queue or current-file reads, no
  `recordRejections` status write), `app/catalog/agent/catalog/register.go` (no grab handler),
  `app/catalog/agent/events/register.go` (no redownload), `pkg/agentdomain/agentdomain.go`
  (`catalog` loses `ConsumerCatalogGrab`; `events` loses `ConsumerCatalogRedownload`; new
  `Draining()` lists both, R22), `app/catalog/grabsource/grabsource.go` (`Covers` over Downloads
  deleted; `ResolveSource` stays)
- Delete: `app/catalog/worker/grab/` (the package; `delay.go`'s helpers moved in A4.1, `target.go`'s
  `MediaKey`/`StatusTargets` move to `app/catalog/grabsource` where still used),
  `app/catalog/worker/redownload/` (the package), `app/catalog/controller/rollup/{activedownload.go,downloadoverlay.go,downloadpredicate.go}`
  (A3.5 left them for these two)

**Interfaces:**

- Consumes: A1.4 `schema.Candidate`, A1.6 `IntakeCandidateSubject`, `MsgIDForCandidate`; A4.1
  `decision.EvaluateRelease`.
- Produces: `agentdomain.Draining() []string`; the RSS matcher publishes `Candidate`s and writes
  nothing else.

- [ ] **Step 1: The RSS matcher.** Keep `Match` (P21) and the monitored pre-filter (P22, the
  manager re-checks); evaluate each matched item with `EvaluateRelease` (block state from
  `rel.Blocks`, A3.1); for each approved match publish `schema.Candidate{Owner: <the item, or the
  Series for episodes, with Targets = the matched episodes>, Origin: "rss", GrabbedBy: rss,
  Purpose, Release}` to `IntakeCandidateSubject` with `MsgIDForCandidate("rss", ownerUID,
  indexer, guid)`; protocols and indexer priority are the grab planner's now (P22). Rejections
  stay in metrics and logs only (`recordRejections` wrote nothing to Kubernetes; keep it if it
  only logs).
- [ ] **Step 2: Retire.** Delete the grab worker (Decide, Sink, perform, the double-grab guard,
  leases, pending, the scheduled-grab handler, kindops' worker status writes) and the redownload
  handler. Their durables (`catalogarr-grab`, `catalogarr-redownload`) stay in the topology,
  listed by `agentdomain.Draining()` until A9.2 retires them; `clustarr-leases` and
  `clustarr-pending` stay declared, unused, until A9.2. Their RBAC markers go with the code;
  `make manifests`; sync the chart.
- [ ] **Step 3: Sweep for leftovers.** `grep -rn 'BucketLeases\|BucketPending\|WorkGrabSubject\|ConsumerCatalogGrab\|ConsumerCatalogRedownload\|FreeLeases\|casKeepBest' --include='*.go' . | grep -v _test.go`
  prints only `pkg/events` (declarations) and `pkg/agentdomain` (`Draining`).

**Tests (deferred to the batch):**

- `TestTheRSSMatcherSendsCandidatesAndWritesNothing` (`app/catalog/worker/rssmatcher`).
- `TestAnRSSCandidateBecomesAnEntryThroughTheOwner` (`app/remediation/grab`, envtest + membus:
  firehose release → candidate → `Pending` entry → `Assigned` command).
- `TestTheEventsDomainRunsOnlyTheRSSMatcher` (`app/catalog/agent/events`).
- `TestEveryConsumerHasExactlyOneHome` (W6.2's; amended): `Draining()` is exempt.
- The deleted packages' tests go with them; their intents are A4.1's and A4.2's tests (named
  in the commit).

- [ ] **Step 4: Commit.**

  ```bash
  git rm -r app/catalog/worker/grab app/catalog/worker/redownload app/catalog/controller/rollup/activedownload.go app/catalog/controller/rollup/downloadoverlay.go app/catalog/controller/rollup/downloadpredicate.go
  git commit -m 'feat(catalog)!: the RSS matcher sends scored candidates to the owner; the grab worker, KV grab leases, clustarr-pending, scheduled grabs and the redownload consumer retire (ADR-0019 A4.4)' -- app/catalog pkg/agentdomain/agentdomain.go config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A4.5: Wave A4 gate

```bash
cd /home/appkins/src/mediactl/clustarr-unify
go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
test ! -d app/catalog/worker/grab && test ! -d app/catalog/worker/redownload && echo gone
grep -rn 'applySearchStatus\|RecordSearchAttempt\|RecordDonorSearchAttempt\|updateWorkerStatus' --include='*.go' app | grep -v _test.go | wc -l   # 0
grep -rn 'catalogarr-grab\b' --include='*.go' app internal | grep -v _test.go | grep -v 'k8s.ManagerCatalogarrGrab' | wc -l                        # 0 (the field manager stays; the durable is only in pkg/events and agentdomain.Draining)
for r in app/catalog/agent/catalog app/catalog/agent/events; do go list -deps ./$r | grep -cE 'app/catalog/(grabplan|searchplan)|app/remediation'; done   # 0 0
grep -rln 'downloadv1alpha1\.Download\b\|downloadv1alpha1\.DownloadList\|downloadv1\.Download\b' --include='*.go' . | grep -v _test.go | grep -vE '^\./(api/|test/e2e/|pkg/k8s/deadletter.go)'   # nothing: A3.10's exclusions are gone
```

---

## Wave A5: metadata and artwork (A5.1-A5.4)

**Spec:** §7.1, §7.2, §3.1 W4, W10, W11, W14, §3.5 P27-P31, §7.7 (`ArtworkFetchFailed`), §8.1
(S25, S26); ruling R23.

**Depends on:** A3.10. **Parallel with:** A4, A6. **Order inside:** A5.1 → A5.2 → A5.3 → A5.4.

**What A5 delivers.** The metadata gateway, the artwork fetcher and the overlay renderer write
no Kubernetes object: the gateway answers in `clustarr-item-metadata`, the fetcher and the
renderer write only their object-store variants (with versioned metadata, W4.104), and the item
keys (Artist and Author in their reconcilers) render `status.metadata`, `status.artwork` and
`status.overlay` under their unchanged field managers.

---

### Task A5.1: Metadata records -- the gateway answers in `clustarr-item-metadata`; the metadata stage incorporates

**Spec:** §7.1 (bullets 1, 2, 5-7), §3.1 W10, W11, §3.5 P28, P30, P31.

**Files:**

- Create: `app/remediation/metadata/{stage.go,sources.go,doc.go}`, `app/catalog/metadatainputs/{inputs.go,doc.go}`
- Modify: `app/catalog/metadata/{worker.go,patch.go}` (the handler renders the kind's metadata
  block to JSON and writes the record; no `Pass.Run` status apply),
  `app/catalog/metadata/artwork/handler.go` (`Pass.Run` and `ExtractGatewayStatus` lose their
  status apply; A5.2 rewrites the rest), the eight metadata-carrying item packages' metadata
  publish (`MetadataTask` v2 with `Item` and `Inputs`), `app/catalog/controller/{artist,author}/reconciler.go`
  (incorporate their record under `catalogarr-metadata`), `app/catalog/agent/metadata/register.go`
  (the handler gets a `records.Writer[*schema.ItemMetadataRecord]`), `app/catalog/status/artwork.go`
  (`GatewayManager` stays the name; nothing in an agent calls a status write)

**Interfaces:**

- Consumes: A1.4 `schema.{MetadataTask v2, MetadataInputs, ItemMetadataRecord}`, A1.5
  `records.{Reader, Writer}`, `recordsource.NewItems`, A3.3 stages and `itempass.Set`.
- Produces: `metadata.Handler.Records *records.Writer[*schema.ItemMetadataRecord]`;
  package `app/catalog/metadatainputs` (pure: api types and `schema` only) with
  `metadatainputs.For(obj client.Object) schema.MetadataInputs` (provider ids, language, schema
  version, `force` from the refresh annotation's epoch) -- used by the item kinds' publish and the
  stage's incorporation, so the manager's item packages need not import the gateway's package;
  `app/remediation/metadata.Stage`.

- [ ] **Step 1: The gateway answers.** In `worker.go`'s `Handle`, after the provider I/O (P30
  unchanged: cache bypass, TTL by state, provider order, ratings carry-forward, retries, token
  buckets), render the kind's metadata apply configuration exactly as `patch.go` does today
  (`buildMovieMetadataAC` and siblings), `json.Marshal` it, and write
  `ItemMetadataRecord{Item, Seq: <the previous record's + 1>, Inputs: task.Inputs, RefreshedAt,
  SchemaVersion: metadata.SchemaVersion, Metadata: <the JSON>}` through the writer (keyed
  `RecordKey(item uid)`). It still writes `clustarr-metadata-extended` and the L2 cache. A
  provider failure writes the record with the header's `Failure` and `Transient` set. Album's
  `selectedReleaseID` stays the Album reconciler's leaf (not in the record's block).
- [ ] **Step 2: Publish v2.** Each item kind's existing "stale or outdated → publish a
  `MetadataTask`" path (e.g. `movie/reconciler.go`'s, and `metadatarefresh.Refresher`) publishes
  v2 with `Item` and `Inputs: metadatainputs.For(obj)`, Msg-Id `metadata/<uid>/<inputsHash>`,
  admitted by the ledger (`catalogarr-metadata`, R23: no status block).
- [ ] **Step 3: The metadata stage** (Movie, Series, Album, Book, Audiobook, Comic): read the
  record; incorporate it when `record.Inputs == metadatainputs.For(item)` and
  `record.RefreshedAt` is after `status.metadata.refreshedAt` (§7.1 "Ordering"); output the
  `catalogarr-metadata` `Set{Fields: {"metadata": <the record's JSON>, "artwork": <A5.2's
  entries, or the stored value until A5.2>}}` -- one complete declaration of that manager's
  two fields. **S25**: `recordsource.NewItems` on `clustarr-item-metadata` enqueues the item.
  The `MetadataReady` condition stays the kind's (`catalogarr`), read from `status.metadata`
  as today.
- [ ] **Step 4: Artist and Author.** Their reconcilers (not loop keys) gain a raw source on
  `clustarr-item-metadata` (`recordsource.NewItems` with `toKeys` keeping `Artist`/`Author`
  items) and, after their own `catalogarr` apply, apply the same set through
  `itemstatus.ApplySet` when not equal.
- [ ] **Step 5: RBAC.** The metadata gateway's package loses every `*/status` write marker (the
  eight metadata kinds keep `get;list;watch`); `make manifests`; sync the chart.

**Tests (deferred to the batch):**

- `TestTheGatewayAnswersOnlyInARecord` (`app/catalog/metadata`, a client failing every write).
- `TestAMetadataRecordIsIncorporatedOnlyForTheCurrentInputs` (`app/remediation/metadata`).
- `TestAnOlderRefreshNeverRollsBackANewerOne` (`app/remediation/metadata`).
- `TestTheMetadataSetDeclaresMetadataAndArtworkTogether` (envtest: `managedFields` of
  `catalogarr-metadata` own both and nothing else).
- `TestArtistAndAuthorIncorporateTheirRecords` (envtest).
- `TestMetadataJSONRoundTripsEveryKindsBlock` (`app/catalog/metadata`: the record's JSON decodes
  into the kind's `*Metadata` type and back unchanged).

- [ ] **Step 6: Commit.**

  ```bash
  git add app/remediation/metadata app/catalog/metadatainputs
  git commit -m 'feat(metadata): the gateway answers in clustarr-item-metadata; item keys and the Artist and Author reconcilers apply status.metadata under catalogarr-metadata (ADR-0019 A5.1)' -- app/catalog app/remediation config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A5.2: The manager's `objindex`; the artwork fetch plan; `status.artwork` from object metadata

**Spec:** §7.1 (bullets 3-4, 6), §3.5 P29, §7.7 (`ArtworkFetchFailed`), §8.1 (S26); W4.104's
`artwork.ObjectMeta`.

**Files:**

- Create: `app/remediation/artwork/{stage.go,plan.go,sources.go,doc.go}`
- Modify: `app/catalog/metadata/artwork/{handler.go,fetcher.go}` (the fetch task names its URLs;
  the fetcher writes objects only; a failure goes into the item's metadata record's
  `ArtworkFailures` through the same writer; no `publishRenders`), `app/catalog/artwork/{meta.go,sources.go}`
  (`Facts`/`ObjectMeta` carry `updatedAt` if absent), `internal/cli/manager/run.go` (the
  manager's `objindex` over `clustarr-artwork`, leader-only)

**Interfaces:**

- Consumes: A1.4 `schema.ArtworkFetchTask` v2, `ItemMetadataRecord.ArtworkFailures`;
  `pkg/events/objindex.{New, Index, Lookup, Subscribe, Run, Synced}`; `artwork.{ResolveSources,
  ObjectMeta, MetaCurrent}`.
- Produces: `app/remediation/artwork.Stage` (Movie, Series, Album, Book, Audiobook, Comic);
  `artwork.EntriesFromIndex(idx *objindex.Index, kind commonv1.MediaKind, uid types.UID) []catalogv1alpha1.ArtworkEntry`;
  Artist and Author render their own through the same function.

- [ ] **Step 1: The manager's index.** `register` builds `objindex.New(bus.ObjectStore(events.BucketArtwork))`
  and adds its `Run` leader-only (with the creation-time check, loop §4.9 amendment); **S26**:
  `Subscribe()`'s changes enqueue the item named by the object's `item` metadata.
- [ ] **Step 2: The plan** (`plan.go`, pure): `artwork.ResolveSources(spec overrides, metadata
  images)` per type; fetch when the type has no `original` object, the source URL differs from
  the object's `sourceURL`, or the object's `updatedAt` is past the refetch age; keep on a
  failure; drop a removed override (`Drop`). The stage publishes `ArtworkFetchTask` v2 with the
  URLs, admitted on `catalogarr-artwork-fetch` (R23), Msg-Id
  `schema.MsgIDForArtworkFetch(uid, <hash of the fetch list>)`.
- [ ] **Step 3: `status.artwork`** is rendered from the index's `original` objects of the item
  (`EntriesFromIndex`: type, digest, size, source, updatedAt) into the `catalogarr-metadata` set
  A5.1 builds. A lost bucket reads as no artwork at once and is refetched when due.
- [ ] **Step 4: The fetcher.** The fetch handler fetches exactly the task's URLs, `Put`s each
  `original` object with its complete `ObjectMeta` (W4.104; `updatedAt` added to the metadata if
  `ObjectMeta` lacks it -- the fetcher is that variant's only writer, ADR-0011), deletes the
  `Drop` types' objects, records failures in the item's metadata record (`Update` through the
  shared writer, one process), and publishes no render. **`ArtworkFetchFailed`** (recorder
  `metadata-gateway`, kept) is emitted by the stage on a failure's first appearance in the
  record. The fetcher's `Recorder` (`fetcher.go:494`) goes.

**Tests (deferred to the batch):**

- `TestArtworkStatusIsRenderedFromObjectMetadata` (`app/remediation/artwork`, membus object
  store).
- `TestALostBucketReadsAsNoArtworkAndRefetches` (`app/remediation/artwork`).
- `TestTheFetcherWritesObjectsOnlyAndRecordsFailures` (`app/catalog/metadata/artwork`).
- `TestArtworkFetchFailedIsOneEventPerNewFailure` (`app/remediation/artwork`).
- `TestNoObjectLinks` (existing guard, rerun).

- [ ] **Step 5: Commit.**

  ```bash
  git add app/remediation/artwork
  git commit -m 'feat(artwork): the item key plans artwork fetches and renders status.artwork from the manager'"'"'s objindex; the fetcher writes objects and failures only (ADR-0019 A5.2)' -- app/remediation app/catalog/metadata/artwork app/catalog/artwork internal/cli/manager/run.go
  ```

---

### Task A5.3: Overlays planned by the item key; `status.overlay` from the overlay object's metadata

**Spec:** §7.2, §3.1 W4, §3.5 P27; W4.82 superseded.

**Files:**

- Modify: `app/remediation/artwork/stage.go` (the overlay half, Movie and Series), the renderer
  `app/catalog/worker/artwork/{handler.go,doc.go}` (plan given by the task; draws and `Put`s the
  `overlay` object; deletes a stale overlay object; no `record`, no `PatchOverlay`, no
  `overlayCASAttempts`), `app/catalog/status/artwork.go` (`PatchOverlay` deleted;
  `RendererManager` stays as the name of the set's manager)

**Interfaces:**

- Consumes: `overlayplan.{ItemOf, Plan, Want, InputsDigest, Publish}`, A5.2's index.
- Produces: the overlay half of `app/remediation/artwork.Stage`: the `catalogarr-artwork` `Set{Fields:
  {"overlay": <OverlayEntry from the overlay object's metadata, or nil>}}`.

- [ ] **Step 1:** In the stage, for Movie and Series: compute `overlayplan.Plan(item, profiles,
  <the poster original's digest from the index>)`; when the `overlay` object's metadata (its
  inputs digest and profile, W4.104) differs from the plan's `InputsDigest`, publish
  `RenderOverlayTask` with the digest, admitted on `catalogarr-artwork-render`; render
  `status.overlay` from the overlay object's metadata (nil when there is none, or when no profile
  selects the item: then the renderer's task deletes the object).
- [ ] **Step 2:** The renderer decodes, draws, `Put`s the `overlay` object with its metadata and
  acks; it reads the item through the APIReader as today but writes no status. RBAC: the
  renderer's package loses `movies/status` and `series/status`.

**Tests (deferred to the batch):**

- `TestOverlayStatusIsRenderedFromTheOverlayObject` (`app/remediation/artwork`).
- `TestAnUnchangedDigestPublishesNoRender` (`app/remediation/artwork`).
- `TestARendererReplicaNeverRollsBackAnotherReplicasOverlay` (W4.82's): retired with its
  subject, its intent carried by the one-writer object and the stage's single apply (named in
  the commit).
- `TestTheRendererWritesNoStatus` (`app/catalog/worker/artwork`).

- [ ] **Step 3: Commit.**

  ```bash
  git commit -m 'feat(artwork): the item key plans overlays and renders status.overlay from the overlay object'"'"'s metadata; PatchOverlay retires (supersedes W4.82; ADR-0019 A5.3)' -- app/remediation/artwork app/catalog/worker/artwork app/catalog/status/artwork.go config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A5.4: Wave A5 gate

```bash
cd /home/appkins/src/mediactl/clustarr-unify
go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
grep -rn 'PatchOverlay\|ExtractGatewayStatus\|publishRenders' --include='*.go' app | grep -v _test.go | wc -l    # 0
grep -rn 'kubebuilder:rbac.*/status' app/catalog/metadata app/catalog/worker/artwork | wc -l                  # 0
go list -deps ./app/catalog/agent/metadata | grep -c 'app/remediation'                                        # 0
```

---

## Wave A6: import, lists, indexers (A6.1-A6.5)

**Spec:** §7.5, §7.6, §3.1 W15-W20, W25-W35, §3.5 P35-P50, P72-P81, P82-P99; rulings R17-R19.

**Depends on:** A3.10 (it shares `app/indexer` with A3.1's verb). **Parallel with:** A4, A5.
**Order inside:** A6.1 → A6.2 (both `app/import`); A6.3 → A6.4 (both `app/indexer`); the two
pairs may run beside each other; A6.5 is the gate.

---

### Task A6.1: Scan observations and `app/import/manager/scanapply`; the rescan writes nothing; the rename pass retires

**Spec:** §7.6 "Library scans" (every bullet), §3.1 W15-W20, §3.5 P35-P50, §4.4 ("Throughput": the
25/s limiter).

**Files:**

- Create: `app/import/manager/scanapply/{applier.go,create.go,prune.go,orphans.go,rbac.go,doc.go}`
- Modify: `app/import/worker/rescan/{worker.go,mediafile.go,series.go,nonvideo_scan.go,manual.go,prune.go,orphanpart.go,missingfolder.go,keptoutput.go}`
  (each write becomes an observation; the decisions P36-P38, P41, P43-P48 move out),
  `app/import/agent/register.go`, `internal/cli/manager/run.go` (the scan consumer's `Applier`),
  `app/import/mediafilespec/spec.go` (`SpecHash(spec)`), `app/import/scanprogress/progress.go`
  (unchanged keys; the tally's `HandedOver`/`ItemsCreated` counts become the applier's, reported
  back through the observation counters the LibraryScan controller already aggregates)
- Delete: `app/import/worker/rescan/renamepass.go`

**Interfaces:**

- Consumes: A1.4 `schema.{ScanObservation, ScanAttribution, ItemCreate, FrozenFields,
  MediaFileBasis, UnmatchedFact, MissingFact}`, A1.6 `IntakeScanSubject`,
  `MsgIDForScanObservation`, A2.3 `intake.ScanApplier`, the loop's `remediation.WriteLimiter`
  (`DefaultBulkWritesPerSecond` 25), `specwrite.ApplyFacts` (A3.8).
- Produces:

  ```go
  package scanapply
  type Applier struct {
  	Client    client.Client
  	Reader    client.Reader
  	APIReader client.Reader
  	Bus       events.Publisher // the recycle-files task for orphan parts
  	Limiter   *remediation.WriteLimiter
  	Now       func() time.Time
  }
  func (a *Applier) Apply(ctx context.Context, obs schema.ScanObservation) error // implements intake.ScanApplier
  package mediafilespec // gains:
  func SpecHash(s catalogv1alpha1.MediaFileSpec) string // sha256 over importarr-worker's set, the basis of an observation
  ```

- [ ] **Step 1: The rescan reports.** Walking, parsing, fingerprinting and attribution stay
  (P35, P39's attribution, P40, P42's diff, P43's size class, P44's tag and output relation,
  P49): for every file whose facts differ from the MediaFile the agent read (or that has none),
  publish a `ScanObservation` (`present` with the proposed `Attribution` and its evidence, the
  `Frozen` fields, the `Basis` -- UID, resourceVersion, `SpecHash` -- of the MediaFile read;
  `unmatched` with the code and reason; `missing` with the `Lstat` fact; `orphanPart` with the
  path and age) to `IntakeScanSubject(ns, scanUID)` under `MsgIDForScanObservation`. A new item
  is an `Attribution.Create` (`movie` from `MatchMovie`'s TMDB id and the RootFolder's default
  profile, `series` from a `{tvdb}` folder), never an apply. The tally in `clustarr-progress`
  is unchanged. No Kubernetes write remains: `applyMovie`, `applySeries`, `handOver`,
  `applyObserved`, `prunePass`'s Delete and `sweepOrphanPart`'s `os.Remove` go (the last is the
  applier's decision and a recycle task's removal, P47).
- [ ] **Step 2: The applier.** `present`: recompute the name `k8s.ChildName(<item name>,
  "mediafile", path)` and compare; when the existing MediaFile's `SpecHash` differs from
  `Basis.SpecHash` (another write moved it), ack and ask for a re-observation of that folder
  (publish a `ScanTask` for the folder's subpath, deduped) instead of applying (§7.6 "stale
  basis"); refuse to re-point a MediaFile to another item (P41: `unmatched`,
  `nameCollision`); create the attributed Movie or Series when absent (`names.Movie`/`names.Series`,
  under `importarr-worker`, `addOptions.monitor: none`, as `applyMovie`/`applySeries` render it
  today) -- an existing object whose provider id differs is never overwritten (`unmatched`,
  `nameCollision`); refuse an excluded item (P37, `BucketImportExclusions`) and one with no
  default profile (P38); validate a manual target (P45); apply the MediaFile spec through
  `specwrite.ApplyFacts` under `importarr-worker` and the observed fingerprint annotation
  under `importarr` (`mediafile.AnnotationObservedFingerprint`, W17); hand a post-transcode
  change to the loop by the annotation (P42). `missing`: delete the MediaFile with UID and
  resourceVersion preconditions once it has read `FileMissing` for 10 min (`prune.go:40`'s
  `missingGrace`, P46). `orphanPart`: when no live transcode of the owning MediaFile
  (`status.transcode` not in flight) and the age ≥ 72 h, publish a recycle-files task (A6.2's
  subject) naming the path (P47). Creates and deletes pass the 25/s limiter (`Limiter.Admit`,
  waiting rather than failing). The scan's missing folder (P48) and the suspected-sample verdict
  (P43) are decided here and counted in the observation's tally entry.
- [ ] **Step 3: Renames.** Delete `renamepass.go` and its call (`worker.go`); renames are the
  loop's rename actuator's (F3.3), gated on no open scan of the folder (loop §3.9).
- [ ] **Step 4: RBAC.** The rescan package keeps reads only; the applier's `rbac.go` carries
  `mediafiles get;list;watch;create;update;patch;delete`, `movies;series
  get;list;watch;create;update;patch`, `libraryscans get;list;watch`. `Makefile`:
  `RBAC_PATHS_importarr` already covers `./app/import/...`. `make manifests`; sync the chart.

**Tests (deferred to the batch):**

- `TestTheRescanPublishesObservationsAndWritesNothing` (`app/import/worker/rescan`).
- `TestAStaleBasisIsReobservedNotApplied` (`app/import/manager/scanapply`, envtest).
- `TestAnExistingItemWithAnotherProviderIDIsNeverOverwritten` (`scanapply`).
- `TestAMissingFileIsDeletedAfterTenMinutesWithPreconditions` (`scanapply`).
- `TestCreatesAndDeletesArePaced` (`scanapply`: 12,000 observations at 25/s).
- `TestTheScannerNeverGuesses` (`scanapply`): an ambiguous attribution is `unmatched`.
- `TestRenamesAreTheLoopsAlone` (`test/guards`: no `specwrite.RenameFile` caller outside
  `app/remediation/rename`).

- [ ] **Step 5: Commit.**

  ```bash
  git add app/import/manager/scanapply
  git rm app/import/worker/rescan/renamepass.go
  git commit -m 'feat(import)!: library scans report observations; the manager'"'"'s scan applier creates and deletes items and MediaFiles with preconditions and pacing; the rescan rename pass retires (ADR-0019 A6.1)' -- app/import internal/cli/manager/run.go config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A6.2: Import lists -- the agent reports a snapshot; the ImportList controller diffs and applies; Trakt tokens in the manager; recycling named files

**Spec:** §7.6 "Lists" and "Trakt tokens", §3.1 W25-W30, §3.5 P72-P81; ruling R17.

**Files:**

- Create: `app/import/controller/importlist/{diff.go,apply.go,snapshot.go,tokens.go}`,
  `app/import/worker/fileimport/recyclefiles.go`
- Modify: `app/import/worker/importlist/{worker.go,sync.go,catalogitem.go,delete.go,items.go}`
  (fetch, resolve, snapshot; nothing else), `app/import/controller/importlist/controller.go`
  (reads the snapshot through a KV watch; the diff, applies, deletes, recycle tasks; status
  `syncedAt` from the snapshot; token refresh), `app/import/importliststate/tokenstore.go`
  (split: the Secret writers -- `applyAll`, `Save`, `SaveDeviceCode`, `ClearDeviceCode` -- move to
  `app/import/controller/importlist/tokens.go`, manager-only; `importliststate` keeps a
  `TokenReader` with `Load` and `LoadDeviceCode`, which the list agent links), `app/import/agent/register.go` (the recycle handler also
  serves `recycle.files.*`)

**Interfaces:**

- Consumes: A1.4 `schema.ListSnapshot`, A1.6 `SubjectImportRecycleFiles`, the existing
  `importlist.{Dedupe, ApplySyncLevel}` (`pkg/importlist`), `StoredItem`, `ItemsKey`;
  A1.4 `schema.RecycleFilesTask`.
- Produces: `importlistctrl.Reconciler.Snapshots events.KV`; `importlist.SnapshotKey(listUID string, chunk int) string`
  (`"snapshot."+KVKeyToken(uid)` and `"…."+n`); `fileimport.RecycleFiles` (handler).

- [ ] **Step 1: The agent reports.** `Worker.Handle` fetches the provider's list, resolves ids
  through the metadata RPC (P77's resolution), and writes the `ListSnapshot` chunks
  (`Put`, one writer per key) -- no `StampSynced` (W25), no item applies or deletes (W26-W29),
  no Secret write (W30): on a Trakt 401 the snapshot says `NeedsToken: true`. The
  `clustarr-progress` `importlist.<uid>` result is no longer written (R17).
- [ ] **Step 2: The controller diffs.** A KV watch on `clustarr-importlist` (`UpdatesOnly`,
  prefix `snapshot.`) enqueues the list; the reconcile reads every chunk (refusing a partial set:
  all `Chunks` present with one `SyncedAt`), and applies today's rules in the manager: refuse a
  kind the list cannot yield (P72), `Dedupe` (P73), drop excluded entries (P74, failing open as
  today), `automaticAdd` off → listed only (P75), leave an item another source added (P76), keep
  an unresolvable id known (P77), create or update from the list's defaults keeping the anime
  classification (P78, today's `seriesSpecFor`), what fell off the list per `syncLevel` (P79),
  keep an item another list still has (P80); it owns the `StoredItem` keys (`ItemsKey`), applies
  items under `importarr-worker`, deletes with a UID precondition (W28), and for
  `removeAndDelete` publishes a `RecycleFilesTask` for the item's files, then deletes their
  MediaFiles with UID preconditions (W29, P81). Status `syncedAt` comes from the snapshot
  (`importlist-synced-at` retires).
- [ ] **Step 3: Trakt tokens.** The controller refreshes a token within 24 h of
  `tokenExpiresAt`, and at once on `NeedsToken` (then re-issues the sync); it is the Secret's only
  writer (`SecretTokenStore`, `importarr`). The agent reads the Secret (`get`) only.
- [ ] **Step 4: Recycling named files.** The recycle handler (`importarr-recycle`) also handles
  `recycle.files.*`: `fsops.Recycle` each path inside the root folder's recycle bin
  (`ErrOutsideRootFolder`, `ErrNoRecycleBin` as `delete.go` refuses today), then
  `PruneEmptyDirs`.
- [ ] **Step 5: RBAC.** The list worker's package keeps `importlists`, the item kinds,
  `mediafiles`, `rootfolders`, `episodes` `get;list;watch` and `secrets;configmaps get`; the
  controller's gains `movies;series create;update;patch;delete`, `mediafiles delete`, `secrets
  get;create;update;patch`. `make manifests`; sync the chart.

**Tests (deferred to the batch):**

- `TestTheListAgentWritesOnlyASnapshot` (`app/import/worker/importlist`).
- `TestTheDiffWritesOnlyTheItemsTheListAddedItself` (`app/import/controller/importlist`, envtest,
  CLAUDE.md's Radarr rule).
- `TestAPartialSnapshotIsNotDiffed`.
- `TestRemoveAndDeleteRecyclesThenDeletesWithPreconditions`.
- `TestOneRefresherRotatesTheTraktToken` (envtest: a 401 snapshot triggers one refresh and one
  re-sync).
- `TestRecycleFilesRefusesAPathOutsideTheRootFolder` (`app/import/worker/fileimport`).

- [ ] **Step 6: Commit.**

  ```bash
  git add app/import/controller/importlist/diff.go app/import/controller/importlist/apply.go app/import/controller/importlist/snapshot.go app/import/controller/importlist/tokens.go app/import/worker/fileimport/recyclefiles.go
  git commit -m 'feat(import)!: import lists report a snapshot; the ImportList controller diffs, applies and deletes with preconditions, recycles through a task and refreshes Trakt tokens alone (ADR-0019 A6.2)' -- app/import config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A6.3: Indexer health -- outcome records from the index agent; the ladder and RSS scheduling in the Indexer controller

**Spec:** §7.5 "Health and backoff", §3.1 W34, W35, §3.5 P87, P91; rulings R18, R19.

**Files:**

- Create: `app/indexer/health/{writer.go,suppress.go,doc.go}` (agent side),
  `app/indexer/controller/indexer/health.go` (manager side), `app/indexer/status/statuspatch/{patch.go,doc.go}`
  (`Patch`, `PatchCAS`, `casAttempts` and `seedFor` moved out of `app/indexer/status/status.go`,
  which the index agent links for `Healthy` and `SupportsMode`: the package an agent reaches holds
  no Kubernetes writer, A7.1's guard)
- Modify: `app/indexer/search/fanout.go` (`recordOutcome` writes the record and arms local
  suppression; no `idxstatus.PatchCAS`), `app/indexer/search/select.go` (skip an indexer
  suppressed locally), `app/indexer/worker/rss/worker.go` (writes the record; no `PatchCAS`, no
  `ScheduleNext` at `:257,288,355`), `app/indexer/controller/indexer/controller.go` (records
  source; incorporation under `indexarr-worker`; schedules the next poll), `app/indexer/agent/workers.go`
  (one `health.Writer` shared by the fan-out and the RSS worker), `app/indexer/worker/rss/doc.go`
  and the search package's markers (no `indexers/status`)

**Interfaces:**

- Consumes: A1.4 `schema.IndexerHealthRecord`, A1.5 `records.Writer.Update`, `recordsource.NewItems`;
  `idxstatus.{RecordFailure, RecordSuccess, ApplyEscalation, PatchCAS, PublishTransitions}`;
  `rssschedule.{NextPollAt, ScheduleNext, TaskMsgID}`.
- Produces: `statuspatch.Patch(ctx, c client.Client, mgr k8s.FieldManager, idx *indexv1alpha1.Indexer, mutate func(*indexac.IndexerStatusApplyConfiguration)) error`
  and `statuspatch.PatchCAS(ctx, r client.Reader, c client.Client, mgr k8s.FieldManager, key client.ObjectKey, mutate func(fresh *indexv1alpha1.Indexer, ac *indexac.IndexerStatusApplyConfiguration) (skip bool)) (*indexv1alpha1.Indexer, bool, error)`
  (signatures unchanged from `idxstatus`; every manager caller re-imports), and:

  ```go
  package health // app/indexer/health, agent side
  type Writer struct{ Records *records.Writer[*schema.IndexerHealthRecord]; Now func() time.Time }
  func (w *Writer) Outcome(ctx context.Context, idx schema.ItemRef, ok bool, class, reason string, indexed int64) error
  func (w *Writer) RSS(ctx context.Context, idx schema.ItemRef, at time.Time, newCount int32, ok bool, class, reason string) error
  // Suppress is the ≤ 30 s local mechanism between a failure and the manager's verdict (P87).
  type Suppress struct{ /* … */ }
  func (s *Suppress) Fail(uid types.UID, now time.Time)
  func (s *Suppress) Suppressed(uid types.UID, now time.Time) bool
  const SuppressFor = 30 * time.Second
  ```

- [ ] **Step 1: The agent reports.** `recordOutcome` (`fanout.go:636`) and the RSS worker's status
  write (`worker.go:326`) become `health.Writer` calls: `Update` read-modify-write the record
  (counters monotone; `LastSuccessAt`/`LastFailureAt`, `LastFailure` clamped to 1024,
  `LastFailureClass` from `classify`, `IndexedReleases` added, `LastRssAt`, `LastRssNewCount`).
  A failure arms `Suppress` for 30 s; `selectCandidates` skips a suppressed indexer (as
  `unhealthy`). The agent reads `disabledUntil` from its cache as today (P82).
- [ ] **Step 2: The controller decides.** A records source on `clustarr-indexer-health`
  (`item` = the Indexer) enqueues it; incorporate per R19: a record whose `lastFailureAt` is after
  `status.lastFailureAt` applies `RecordFailure(status, now, record.LastFailure)`, one whose
  `lastSuccessAt` is after both applies `RecordSuccess`; then `ApplyEscalation` and the record's
  `IndexedReleases`, `LastRssAt`, `LastRssNewCount` into `WorkerFields` under `indexarr-worker`
  through `idxstatus.PatchCAS` (now manager-only); `PublishTransitions` on a change.
- [ ] **Step 3: RSS scheduling.** The controller publishes each poll (R18): after a record's
  `LastRssAt` advances past the last scheduled slot, `ScheduleNext(ctx, bus, idx, at)` with `at`
  = `NextPollAt(idx, now)`, or `disabledUntil` when unhealthy, or the query-limit retry; the
  agent's three self-chaining publishes go (P91).

**Tests (deferred to the batch):**

- `TestTheIndexAgentWritesOutcomesNotStatus` (`app/indexer/search`, a client failing every
  write).
- `TestTheLadderRunsInTheController` (`app/indexer/controller/indexer`, envtest: the ten rungs).
- `TestLocalSuppressionLastsAtMostThirtySeconds` (`app/indexer/health`).
- `TestTheControllerSchedulesTheNextPollAfterAnOutcome` (`app/indexer/controller/indexer`).
- `TestNoPollChainsItself` (`app/indexer/worker/rss`).

- [ ] **Step 4: Commit.**

  ```bash
  git add app/indexer/health app/indexer/controller/indexer/health.go app/indexer/status/statuspatch
  git commit -m 'feat(indexer)!: the index agent reports query outcomes in clustarr-indexer-health; the Indexer controller runs the backoff ladder and schedules every RSS poll (ADR-0019 A6.3)' -- app/indexer config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A6.4: Sessions mirrored by the manager; the facade key created by the manager

**Spec:** §7.5 "Sessions" and "Facade key", §3.1 W31-W33; W4.20-W4.24 (amended).

**Files:**

- Create: `app/indexer/controller/indexer/sessionsecret.go` (the Secret half of the session
  store, moved out of `app/indexer/clients`: `sessionSecretAC`, `dropSecret`, the Secret apply of
  `Save`; manager-only, so no agent root links a Secret writer -- A7.1's type-checked guard)
- Modify: `app/indexer/clients/session.go` (`SessionStore` is KV only: `Save` → `KV.Put`;
  `Drop` → `dropKV`'s `DeleteRevision`; `Load` reads the KV, then the Secret through a reader;
  the `Client` and `Manager` fields and `NewSessionStore`'s third parameter (W4.20) go),
  `app/indexer/agent/clients.go`, `app/indexer/clientcache/clientcache.go` (the relogin hook takes
  the KV-only store), `app/indexer/controller/indexer/{controller.go,controller_definition.go}`
  (`sessionsSource` also mirrors: on a KV put, apply the Secret from the KV session under
  `indexarr`; on a delete, empty the Secret by compare-and-swap, as `dropSecret` did; the login
  path saves the KV and mirrors), `app/indexer/agent/facadekey.go` (read-only:
  `get` by name, fail closed, re-read every 30 s), `app/indexer/manager/register.go` (a
  leader-only runnable creates the facade key Secret if absent, create-only, `indexarr`),
  `app/indexer/agent/doc.go` and `facadekey.go` markers (`secrets get` only)

**Interfaces:**

- Consumes: W4.20's session store, W4.21's `Drop` CAS, W4.24's `sessionsSource`.
- Produces: `clients.NewSessionStore(r client.Reader, bus events.Bus) *SessionStore` (KV writes,
  Secret reads); `indexer.mirrorSession(ctx, c client.Client, idx *indexv1alpha1.Indexer, sess *cardigann.Session) error`
  and `indexer.dropSessionSecret(ctx, c client.Client, idx *indexv1alpha1.Indexer, failed *cardigann.Session) error`;
  `indexermanager.EnsureFacadeKey(ctx, c client.Client, namespace, name string) error`
  (leader-only); `facadekey.Wait(ctx, r client.Reader, namespace, name string) ([]string, error)`
  (agent: polls every 30 s until the Secret exists).

- [ ] **Step 1:** The agent's relogin path (`clientcache.go:352-366`) writes only the KV
  (`Save`) or deletes there by revision (`Drop`, `DeleteRevision`); the session Secret's only
  writer is the Indexer controller's mirror. The manager's own login path
  (`controller_definition.go:131-140`) keeps writing both.
- [ ] **Step 2:** Move `ensureFacadeAPIKeys`' create (`facadekey.go:117`) into the index manager
  registration (`indexarr`, create-only); the agent's facade waits for it and serves 503 until
  then.
- [ ] **Step 3: RBAC.** The agent packages keep `secrets get`; the controller keeps `secrets
  get;create;patch`. `make manifests`; sync the chart.

**Tests (deferred to the batch):**

- `TestSessionDropDoesNotWipeANewerSave` (W4.21's; keeps its KV case, loses its Secret case).
- `TestTheControllerMirrorsTheKVSessionIntoTheSecret` (envtest).
- `TestTheAgentWritesNoSecret` (`app/indexer/clients`, a client failing every write).
- `TestTheFacadeFailsClosedUntilTheManagerCreatesItsKey` (`app/indexer/agent`).

- [ ] **Step 4: Commit.**

  ```bash
  git add app/indexer/controller/indexer/sessionsecret.go
  git commit -m 'feat(indexer)!: the index agent keeps sessions in KV only and the Indexer controller mirrors them into the Secret; the manager creates the facade key (ADR-0019 A6.4)' -- app/indexer config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A6.5: Wave A6 gate

```bash
cd /home/appkins/src/mediactl/clustarr-unify
go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
grep -rn 'k8s\.\(Apply\|PatchStatus\|PatchStatusCAS\)\|Client\.\(Create\|Update\|Patch\|Delete\)\|StampSynced\|ScheduleNext' app/import/worker app/indexer/agent app/indexer/search app/indexer/worker --include='*.go' | grep -v _test.go | wc -l   # 0 (app/indexer/clients keeps its Secret writes, reached only through the manager's store with a client; A7's type-checked guard proves no agent root calls them)
test ! -f app/import/worker/rescan/renamepass.go && echo gone
```

---
## Wave A7: RBAC and guards (A7.1-A7.3)

**Spec:** §9 (all), D1; ruling R13.

**Depends on:** F8.12 (with A8). **Before:** W6.1. **Order:** A7.1 → A7.2 → A7.3.

---

### Task A7.1: The agent client is read-only at runtime; no agent package calls a Kubernetes writer

**Spec:** §9.2 (`TestAgentClientIsReadOnly`, `TestAgentsCallNoKubernetesWriter`).

**Files:**

- Modify: `pkg/k8s/manager.go` (`AgentManagerOptions` sets `NewClient` to wrap the client in
  `ReadOnly`), `internal/cli/agent/run.go` (the manager's event recorder provider is the
  `DiscardingRecorder`); the packages the sweep below names (each split so its writer moves to a
  manager-only package)

- [ ] **Step 1: Wire `ReadOnly`.** `AgentManagerOptions` sets
  `ctrl.Options.NewClient = func(cfg *rest.Config, o client.Options) (client.Client, error) {
  c, err := client.New(cfg, o); if err != nil { return nil, err }; return ReadOnly(c), nil }`
  (keeping today's cache options), and the agent run hands controller-runtime an event broadcaster
  whose recorder is `DiscardingRecorder`. Any write left in an agent fails with `ErrAgentWrite`
  and counts.
- [ ] **Step 2: The writer sweep.** List every module package any agent root reaches
  (`go list -deps` of the ten roots in Global Constraints, module packages only, `pkg/k8s`
  excluded as the definitions' home) and, type-checked (`go/packages` with `NeedTypes`), every
  call in them to `k8s.{PatchStatus, PatchStatusCAS, Apply, EnsureFinalizer, RemoveFinalizer,
  ScaleTo, PatchStatusUnstructured}`, to `Create`, `Update`, `Patch`, `Delete`, `DeleteAllOf`,
  `Status` or `SubResource` on a value implementing controller-runtime's `client.Writer` or a
  client-go typed client, and to `GetEventRecorder`/`GetEventRecorderFor` or a
  `record.EventRecorder`. Keep the scan script under the scratchpad; it becomes
  `TestAgentsCallNoKubernetesWriter` in the batch. For each hit, move the writer into a
  manager-only package (as A3.8, A6.2-A6.4 did for `mediafilespec`, `importliststate`,
  `app/indexer/status` and `app/indexer/clients`). Expected after A3-A6 and F5: nothing, or
  `app/caption/status`'s `Patch*` if F5 left the package linked by the caption agent -- then split
  it the same way.
- [ ] **Step 3: Publishes.** List every `Publish` in the same packages whose subject comes from a
  `Work*Subject` builder or `WorkEngine*Subject`; expected: none (agents publish only
  `clustarr.intake.>`, `clustarr.rel.>` and `clustarr.evt.>`).

**Tests (deferred to the batch):**

- `TestAgentClientIsReadOnly` (`internal/cli/agent`): `AgentManagerOptions` wraps the client and
  discards Events.
- `TestAgentsCallNoKubernetesWriter` (`test/guards`, type-checked, §9.2).
- `TestAgentsPublishNoTask` (`test/guards`, §9.2).
- `TestAgentLinksNoPlanner` (`test/guards`, extends split §4.5.3's
  `TestAgentLinksNoManagerRegistration`): no agent root links `app/catalog/grabplan`,
  `app/catalog/searchplan`, `app/grab/lifecycle`, `app/import/importplan`,
  `app/import/manager/...`, `app/intake`, `app/dispatch`, `app/remediation`,
  `app/import/mediafilespec/specwrite`, `app/indexer/status/statuspatch`.
- `TestManagerLinksNoAgentCode` (W5.14's): `AllowApp` admits the new manager packages.

- [ ] **Step 4: Commit.**

  ```bash
  git commit -m 'feat(agent): the agent'"'"'s Kubernetes client is read-only and its Events discarded; no package an agent links calls a Kubernetes writer (ADR-0019 A7.1)' -- pkg/k8s/manager.go internal/cli/agent/run.go app pkg
  ```

---

### Task A7.2: Markers, roles and field-manager homes

**Spec:** §9.1 (the roles table), §9.2 (`TestEveryFieldManagerHasItsHome`, `TestRecordsWritersAreTheirAgents`,
`starttest.AssertWroteOnlyAs`).

**Files:**

- Modify: every `+kubebuilder:rbac` marker left with a write verb in a package an agent root
  reaches (moved with its code into the manager package that now needs it), `Makefile`
  (`RBAC_PATHS_grabarr-engine` without `./app/grab/status` -- A3.6 did it; the legacy
  `RBAC_PATHS_*` of the roles the manager binds gain the new manager packages:
  `./app/intake/... ./app/dispatch/... ./app/remediation/...` under catalogarr (already);
  `./app/grab/lifecycle` (no marker); `./app/import/importplan` (no marker),
  `./app/import/manager/scanapply` under importarr (already `./app/import/...`)), `config/rbac`
  and the chart (generated)

- [ ] **Step 1:** `grep -rn 'kubebuilder:rbac' $(cat <the agent package list>) | grep -vE
  'verbs=(get;list;watch|get;list|get|list;watch|watch)$'` prints nothing; fix each hit by moving
  the marker to the manager package whose code needs the grant.
- [ ] **Step 2:** `make manifests`; sync the chart; `git diff --stat config/rbac` shows only
  removals of write verbs from roles agents bind today (`grabarr-engine`) and the moves within the
  legacy roles. The per-identity read-only agent roles are W6.9's (amended): A7's role guard runs
  with it.

**Tests (deferred to the batch):**

- `TestNoAgentRoleGrantsAWriteVerb` (`test/guards`; runs once W6.9 generates the per-identity
  roles): verbs ⊆ {get, list, watch}; no `*/status`, `*/finalizers`, `*/scale`, `events`,
  `leases`; `secrets` only `get`; the markers SA unbound; pool and graft pods mount no token.
- `TestEveryFieldManagerHasItsHome` (W5.19's; amended): every `k8s.Manager*` but `ManagerUI`
  homed in `cmd/manager` alone; the multi-home allow-list empty; `grabarr-engine` in
  `RetiredFieldManagers()`.
- `TestRecordsWritersAreTheirAgents` (`test/guards`, extends F7.4's
  `TestRecordsBucketsOpenOnlyThroughTheirStores`): each new bucket's `records.Writer` is
  constructed only in its writer's package (`app/grab/engine`, `app/import/worker/fileimport`,
  `app/catalog/worker/search`, `app/catalog/metadata`, `app/indexer/health`).
- `starttest.AssertWroteOnlyAs` per agent identity (W5.4's harness): an RBAC-enforced start
  writes nothing.

- [ ] **Step 3: Commit.**

  ```bash
  git commit -m 'chore(rbac): no package an agent links carries a write marker; roles regenerated (ADR-0019 A7.2)' -- app Makefile config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A7.3: Wave A7 gate

```bash
cd /home/appkins/src/mediactl/clustarr-unify
go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
grep -n 'ReadOnly(' pkg/k8s/manager.go | wc -l                                              # 1
grep -A12 'name: clustarr-grabarr-engine' config/rbac/grabarr_engine_role.yaml | grep -cE 'create|update|patch|delete'   # 0
```

plus the A7.1 sweep script printing nothing. Record in the ledger.

---

## Wave A8: release N migration, inside F8 (A8.1-A8.5)

**Spec:** §10.1, §10.2 (all of it), §10.4 (rollback; `downloads export`), §6.13 (e2e), D14; loop
§7 (the fold's Migrator, report, flags and runbook this extends).

**Depends on:** A4.5, A5.4, A6.5, and the F8 tasks named per task. **Runs inside F8**, in this
sequence: F8.3 → A8.2 → F8.4 → A8.1 → F8.5 → A8.3 → A8.4 → F8.6 … F8.11 → F8.12 with A8.5 (the
numbering follows §10.2's order of topics, not the run order). One Migrator, one report, one flag
pair (§11.1 rows F8.3-F8.5).

**What A8 delivers.** Release N adopts every live Download into its owner's `status.downloads[]`
(or the release index's blocklist, or a dismissal) without stopping a seed, rehashing a torrent,
deleting a payload Q6 does not decide to remove, forgetting a block, restarting a held import's
24-hour clock, or dropping a user's pause; the Download CRD stays readable and its objects frozen
for rollback; `manager downloads export` writes them back for a rollback.

---

### Task A8.1: Adoption in the downloads stage; `status.legacyDownloads`; the unidentified-removal gate

**Spec:** §10.2 "Adoption in the owner key" (every bullet), "The engines re-attach without
Downloads", §6.7 (pre-journal transfers), §6.3 (an owner over its cap is `Held: TooMany`).

**Files:**

- Create: `app/remediation/adopt/downloads.go` (beside F8.4's adopter), `app/remediation/downloads/legacy.go`
- Modify: `app/remediation/downloads/{stage.go,gather.go}` (release N: the adoption runs first
  in the stage's plan, behind `remediationmanager.Options.LegacyFold`), `app/catalog/legacyfold/indexes.go`
  (an index of Downloads by controlling owner UID, `legacyfold.IndexDownloadOwner`),
  `app/grab/controller/downloadclient/controller.go` (`UnidentifiedGate` = "every Download whose
  `spec.clientRef` names this client is adopted or dismissed"), `app/remediation/downloads/rbac.go`
  (`downloads get;list;watch` -- release N only, removed by A9.1)

**Interfaces:**

- Consumes: F8.4's `adopt` package and `remediationmanager.Options.LegacyFold`; A1.2
  `LegacyDownloads`; A3.4 `lifecycle`; A3.1's blocklist RPC.
- Produces: `adopt.Downloads(ctx context.Context, r client.Reader, owner client.Object, entries []catalogv1alpha1.DownloadEntry, legacy *catalogv1alpha1.LegacyDownloads, mode legacyfold.Mode, now time.Time) (DownloadsAdoption, error)`
  with `DownloadsAdoption{Entries []catalogv1alpha1.DownloadEntry; Legacy catalogv1alpha1.LegacyDownloads; Blocks []lifecycle.BlockCall; Report []adopt.ReportObject}`;
  `legacyfold.IndexDownloadOwner = "legacyfold.download.owner"`.

- [ ] **Step 1: The rules** (`adopt/downloads.go`, pure over the Downloads the index returns for
  the owner's UID, a pack's controlling owner being the Series): skip a Download whose UID is in
  `status.downloads[].uid` or `legacyDownloads.adopted`; **live** (`Pending` through `Seeding`, or
  `Imported` and still seeding): an entry with `id` = the Download's name, `uid` = its UID,
  `client` = `spec.clientRef`, `engine` = `status.engine`, the release clamped from
  `spec.release` (§6.2's caps; a guid over 1024 is `Held` with a message, never clamped), `source`,
  `purpose`, `episodes`/`issues` from `spec.target.keys` resolved through the cache, seed criteria,
  the effective removal flags (`ptr.Deref(…, true)`), `phase`, `stage`, `failureReason`,
  `outputPath`, the timestamps, the import summary from `status.import` (`Blocked` with
  `heldSince` → `held`, keeping `heldSince`), and `dispatch.seq = records.NextSeq(0, 0, now)`
  with `answeredSeq` 0 (the first command is owed); outcome `adopted`. **Blocklisted** (the
  label, or `phase: Blocklisted`): a `BlockCall{op: block, scope: ScopeFor(failureReason)}`
  (no reason, an operator's label → `manual`, item scope), `until` = `blocklistedUntil`; the
  outcome `blocklisted` is recorded only after the reply (R9's book). **Terminal with no transfer
  and no payload** (`Failed`, or `Imported` with its transfer removed and nothing at
  `outputPath`, by an `Lstat` through the I/O executor): `dismissed`. **`Imported`, transfer
  removed, payload on disk:** in `apply` mode only, an entry in `Removing` with `removeData:
  removeDataOnDelete` (Q6), outcome `removing`; in `hold` mode, nothing. An owner whose live
  Downloads exceed its cap: `Held: TooMany`, nothing adopted, reported.
- [ ] **Step 2: In the stage.** In release N (`LegacyFold != nil`), the downloads stage runs the
  adoption before `lifecycle.Decide`, feeds the adopted entries in as stored, renders
  `Contribution.LegacyDownloads`, and in `hold` mode adopts nothing (it only reports). The
  engines' first commands for adopted entries fill their journals (A3.6's name match).
- [ ] **Step 3: The gate.** `downloadclient.Reconciler.UnidentifiedGate` is set in release N to
  "no Download with `spec.clientRef == dc.Name` lacks an adoption outcome on its owner", read
  through `legacyfold.IndexDownloadOwner` and the owners' `legacyDownloads`; so a pre-journal
  transfer is never removed before its Download had the chance to adopt it (§6.7, §10.2).

**Tests (deferred to the batch):**

- `TestAdoptionRules` (`app/remediation/adopt`): one case per bullet, cut from the live dump
  shapes (the 122 Downloads of 2026-10-06; the dump is an owner-gated read, loop §7.1).
- `TestAdoptionIsIdempotent` (envtest: a second pass adopts nothing).
- `TestAHeldImportKeepsItsClock` (`heldSince` carried).
- `TestAnOwnerOverItsCapIsHeldNotTruncated`.
- `TestAPreJournalTransferIsClaimedByItsAdoptedEntry` (`app/grab/engine` + the stage).
- `TestTheUnidentifiedGateWaitsForAdoption` (`app/grab/controller/downloadclient`).
- `TestHoldModeAdoptsNothing`.

- [ ] **Step 4: Commit.**

  ```bash
  git add app/remediation/adopt/downloads.go app/remediation/downloads/legacy.go
  git commit -m 'feat(legacyfold): release N adopts every live Download into its owner'"'"'s status.downloads, blocklisted ones into the release index, and gates unidentified-transfer removal on adoption (ADR-0019 A8.1)' -- app/remediation app/catalog/legacyfold app/grab/controller/downloadclient config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A8.2: The Migrator's Download part -- intent, held imports, source hashes, finalizers, census and deletion

**Spec:** §10.2 "The Migrator" steps 1-5; loop §7.3.5, §7.3.7 (the fold's order and retention).

**Files:**

- Create: `app/catalog/legacyfold/downloads.go`
- Modify: `app/catalog/legacyfold/{migrator.go,census.go,rbac.go}` (F8.3's), `pkg/obs/metrics/legacyfold.go`
  (`LegacyFoldObjects` gains `kind="Download"` states `unadopted`, `adopted`, `dismissed`,
  `blocklisted`, `deleted`)

**Interfaces:**

- Consumes: F8.3's `legacyfold.{Migrator, Options, DefaultRetain, DefaultInterval}`; A1.2 annotations;
  A1.3 `ImportSource.InfoHash`; A3.8 `specwrite.ApplyFacts`'s compare-and-swap form; A1.5
  `records.Writer` for `clustarr-imports` (the Migrator seeds as the import agent would, once,
  before adoption: a documented exception, the record's writer being the Migrator for those keys
  only while no import task has run for them).
- Produces: `legacyfold.(*Migrator).downloadsPass(ctx) error`, called from `Pass` after the fold's
  preparation.

- [ ] **Step 1: Intent first** (before adoption, the fold's §7.3.5 order): a non-default
  `spec.paused` or `spec.priority` → the owner's `download.clustarr.io/paused` (append the id) or
  `priority` (`<id>=<p>`); `catalog.clustarr.io/import-target` or
  `catalog.clustarr.io/import-override` → `download.clustarr.io/import` with nonce
  `fold-<generation>`; merge patches of the owner's annotations under `clustarr-legacy-fold`.
- [ ] **Step 2: Held imports.** For a Download whose `status.import` is `Blocked` (or `pending`
  with `nextAttemptAt`), write `clustarr-imports` at `RecordSubKey(uid, "inspect")` once
  (`Create`, never over an existing record), so the hold keeps `heldSince` and the attempts.
- [ ] **Step 3: Source hashes.** For every MediaFile whose `spec.importedFrom.downloadRef` names a
  live or retained Download with `spec.release.infoHash` and whose `importedFrom.infoHash` is
  empty, backfill it with `specwrite.ApplyFacts`-style compare-and-swap under `importarr-worker`
  (re-sending the whole set: `mediafilespec.ReassertFrozen` plus the hash).
- [ ] **Step 4: Finalizers.** Once the owner records an outcome for a Download, remove
  `download.clustarr.io/download` and `download.clustarr.io/engine` from it (`k8s.RemoveFinalizer`,
  default owner), so deleting it later runs nothing.
- [ ] **Step 5: Census and deletion.** Count Downloads by state into
  `clustarr_legacy_fold_objects{kind="Download"}`; delete an adopted or dismissed Download only
  after `retain` (72 h; 0 s on `config/e2e`) and only with both finalizers gone, with UID and
  resourceVersion preconditions, paced by F8.3's deletes-per-second. RBAC (`legacyfold/rbac.go`,
  release N): `downloads get;list;watch;update;delete`, `downloads/finalizers update`.

**Tests (deferred to the batch):**

- `TestTheMigratorCarriesIntentBeforeAdoption` (envtest).
- `TestAHeldImportIsSeededOnce` (membus).
- `TestTheInfoHashIsBackfilledByCAS` (envtest, an interleaved writer).
- `TestFinalizersAreStrippedOnlyAfterAnOutcome`.
- `TestAnAdoptedDownloadIsDeletedOnlyAfterRetention`.
- `TestManagersOfCoverEveryFieldManager` (W5.4's): unchanged (`clustarr-legacy-fold` is F8.3's).

- [ ] **Step 6: Commit.**

  ```bash
  git add app/catalog/legacyfold/downloads.go
  git commit -m 'feat(legacyfold): the Migrator carries Download intent and held imports, backfills importedFrom.infoHash, strips Download finalizers after adoption and deletes after retention (ADR-0019 A8.2)' -- app/catalog/legacyfold pkg/obs/metrics/legacyfold.go config/rbac charts/clustarr/templates/rbac.yaml
  ```

---

### Task A8.3: `manager legacy-fold report` gains Downloads; `manager downloads export`

**Spec:** §10.2 "Report", §10.4 (the rollback rows, `downloads export`), §6.2 (Q7: `manager
downloads list` stays optional -- not built).

**Files:**

- Create: `internal/cli/manager/downloads.go` (`manager downloads export`)
- Modify: `app/remediation/adopt/report.go` (F8.5's: Downloads), `internal/cli/manager/legacyfold.go`
  (F8.5's), `internal/cli/manager/command.go` (`root.AddCommand(newDownloadsCommand(config.GetConfig))`)

**Interfaces:**

- Consumes: A8.1 `adopt.Downloads`; F8.5's `adopt.{Report, BuildReport}`.
- Produces: `manager downloads export --namespace <ns> [--dry-run]`.

- [ ] **Step 1: Report.** Counts by phase; blocklisted Downloads and the release-index rows they
  become, by scope; held imports; intents carried; the adoption plan per owner; owners over cap;
  transfers no Download names (from the engine records' `unidentified`: removed with bytes kept
  once adoption completes); and Q6's leftover payloads with their bytes (`Lstat` and size of each
  `outputPath`). Table and JSON (`-o`).
- [ ] **Step 2: `downloads export`.** Refuses while a manager holds `manager.clustarr.io` (reads
  the Lease); for every entry of every owner with no Download of the same name, creates one
  (spec from the entry; `status.phase`, `engine`, `outputPath` and `import` from the entry and its
  records) under `catalogarr-grab` (the previous release's creator); exports the release index's
  blocklist (`op: list` over the RPC, through `--nats-url`) as labelled Downloads with
  `blocklistedUntil`. It is the only code in N or N+1 that writes a Download, and runs only from
  an operator's shell.

**Tests (deferred to the batch):**

- `TestTheReportCountsDownloadsAndTheirPlan` (envtest).
- `TestExportRefusesWhileAManagerLeads` (envtest).
- `TestExportRecreatesEveryEntryAndEveryBlock` (envtest + membus RPC).
- e2e the upgrade-and-rollback scenario (F8.9's) covers export (Phase H).

- [ ] **Step 3: Commit.**

  ```bash
  git add internal/cli/manager/downloads.go
  git commit -m 'feat(manager): the legacy-fold report counts Downloads and their adoption; manager downloads export writes them back for a rollback (ADR-0019 A8.3)' -- internal/cli/manager app/remediation/adopt/report.go
  ```

---

### Task A8.4: The runbook and the e2e scenarios' text

**Spec:** §10.2 "Runbook", §6.13; loop §7.5.

**Files:**

- Modify: `docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md` §7.5 (the
  runbook: step 5's quiesce stops grabs and searches under the old release and lets running
  imports finish; engines keep seeding until the DownloadClient controller re-renders them; step
  10 checks `clustarr_legacy_fold_objects{kind="Download",state="unadopted"}` is 0 and every
  torrent re-attached without a hash check), `docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md`
  (§10.2 "as built" pointers only)

- [ ] **Step 1:** Edit the runbook as above; add a "Downloads" row to its pre-flight report
  checklist. No code.
- [ ] **Step 2: Commit.**

  ```bash
  git commit -m 'docs(runbook): release N adopts Downloads -- quiesce grabs and searches, keep engines seeding, check unadopted is zero and no torrent rehashed (ADR-0019 A8.4)' -- docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md
  ```

**Tests (deferred to the batch; e2e, Phase H):** scenarios 1-4, 6, 14 and 15 change to a Search
with `spec.grab` naming a fixture release; the wait helpers (`waitForDownloadPhaseAtLeast`,
`waitForDownloadPhaseExactly`, `describeDownload`, `test/e2e/helpers_test.go:1310-1367`) read
`status.downloads[?(@.id=="…")].phase` and the transfer record; `patchDownloadLabel` (`:1393`)
becomes the `remove … blocklist` intent; `newTorrentDownloadE2E` and `newUsenetDownloadE2E`
(`:1244`, `:1272`) become a Search pick; new `TestEngineRestartKeepsSeeding`,
`TestAnUnclaimedTransferIsResumedOrRemoved`, `TestABlockedReleaseShowsBlockedInASearch`.

---

### Task A8.5: Gate additions to F8.12

F8.12 runs these beside its own:

```bash
cd /home/appkins/src/mediactl/clustarr-unify
grep -rln 'downloadv1alpha1\.Download\b\|DownloadList' --include='*.go' . | grep -v _test.go | grep -vE '^\./(api/|test/e2e/|pkg/k8s/deadletter.go|app/catalog/legacyfold/|app/remediation/adopt/|internal/cli/manager/downloads.go)'   # nothing
go run ./cmd/manager legacy-fold report --help >/dev/null && go run ./cmd/manager downloads export --help >/dev/null && echo cli
```

and records the deferred tests of A8.1-A8.4 in the ledger.

---

## Wave A9: release N+1 removal, with F9 (A9.1-A9.5)

**Spec:** §10.3, §10.4 (row N+1 → N); loop §7.4 (the fold's N+1, which F9 implements).

**Depends on:** W10.8. **Runs with F9:** A9.n after F9.n. Nothing here is deployed without the
owner's OK (the CRD deletion by hand, §10.3).

---

### Task A9.1 (with F9.1, F9.2): Remove the Download type and the Migrator's Download part

**Files:**

- Delete: the `Download` and `DownloadList` types and every Download-only declaration in
  `api/download/v1alpha1/download_types.go` (`ImportState`, `ImportPhase`, `ImportedFile`,
  `DownloadFile`, `UsenetHealth`, `IndexerDownload` alias, the label constants, `DefaultBlocklistTTL`,
  `ImportHoldRetention`, `ImportMessage*`, the aliases of A1.1); their applyconfigurations
  (`git rm api/applyconfiguration/download/download/v1alpha1/{download,downloadspec,downloadstatus,importstate,importedfile,downloadfile,usenethealth,…}.go`
  -- the generator does not delete); `status.legacyDownloads` (A1.2) on the six owner kinds and
  its types; `app/remediation/adopt/downloads.go`, `app/remediation/downloads/legacy.go`,
  `app/catalog/legacyfold/downloads.go`, the `IndexDownloadOwner` index, the `UnidentifiedGate`
  wiring; `internal/cli/manager/downloads.go`; the v1 `ImportTask` decoder (`schema.ImportTask`)
  and its resolver; the Download kind in `pkg/k8s/deadletter.go`; `pkg/crdcheck/download_cel_test.go`
  (its rules move to a `DownloadSource` CEL test in `api/common/v1alpha1`, deferred)
- Modify: `pkg/k8s/fieldmanager.go` (`ManagerGrabarrEngine` deleted from `RetiredFieldManagers()`
  and the constants), the RBAC markers granting `downloads` (A8.1's, A8.2's)

- [ ] **Step 1:** Delete as above; `make generate manifests`; `go build ./...`.
- [ ] **Step 2: Commit** (after F9.2's commit; `git rm` each deleted file first).

  ```bash
  git commit -m 'feat(api)!: the Download kind is gone -- its types, applyconfigurations, status.legacyDownloads, the Migrator'"'"'s Download part, the v1 ImportTask and grabarr-engine (ADR-0019 A9.1)' -- api app internal/cli/manager pkg/events/schema pkg/k8s pkg/crdcheck config/rbac charts/clustarr/templates/rbac.yaml
  ```

**Tests (deferred):** `TestDownloadSourceAllowsExactlyOne` (moved CEL cases);
`TestNothingButTheMigratorNamesDownload` becomes `TestNothingNamesDownload`.

---

### Task A9.2 (with F9.4): Retire the NATS state of the old grab path

**Files:**

- Modify: `pkg/events/topology.go` (`Retired` gains `{Stream: StreamWorkCatalogarr, Durable:
  ConsumerCatalogGrab, Purge: FilterCatalogGrab}` and `{Stream: StreamEvents, Durable:
  ConsumerCatalogRedownload}`; the two consumers leave `defaultConsumers()`; `BucketLeases` and
  `BucketPending` leave `defaultBuckets()` and are deleted at `Ensure` through a retired-bucket
  list beside `Retired`, after the drain gate F9.4 applies to the fold's durables),
  `pkg/agentdomain/agentdomain.go` (`Draining()` deleted), `pkg/events/subjects.go`
  (`WorkGrabSubject`, `LeaseKey`, `PendingKey`, `FilterDownloadFailed/Blocklisted` deleted when
  unused)

- [ ] **Step 1:** As above; the retired-bucket deletion reports what it deleted
  (`RetiredReport`-style) for the N+1 runbook.
- [ ] **Step 2: Commit** after F9.4's.

  ```bash
  git commit -m 'feat(events)!: catalogarr-grab and catalogarr-redownload retire through Topology.Retired; clustarr-leases and clustarr-pending are deleted after the drain gate (ADR-0019 A9.2)' -- pkg/events pkg/agentdomain/agentdomain.go
  ```

**Tests (deferred):** `TestValidateChecksRetiredConsumers` (F1.2's, gains the two);
`TestRetiredBucketsAreDeletedAfterTheDrainGate`.

---

### Task A9.3 (with F9.3): Installers without `downloads`

**Files:** `config/crd/kustomization.yaml` (the Download base, `:40`), `config/crd/bases/download.clustarr.io_downloads.yaml`
(`git rm`), the chart's CRD copy, every role and the chart's RBAC (`downloads` leaves),
`test/e2e/main_test.go` (`expectedCRDCount` 29 → 28 after F9's own decrements; a test file:
deferred), `crdbases_test.go` (deferred).

- [ ] **Step 1:** Remove; `make manifests`; sync the chart.
- [ ] **Step 2: Commit** after F9.3's.

  ```bash
  git rm config/crd/bases/download.clustarr.io_downloads.yaml
  git commit -m 'build(installers)!: downloads leaves the CRDs, every role and the chart (ADR-0019 A9.3)' -- config charts
  ```

---

### Task A9.4 (with F9.5): Docs -- the Download CRD removal runbook

**Files:** the loop spec's N+1 runbook (loop §7.5) and `charts/clustarr/crds/README.md`: after the
census reads 0, `kubectl delete crd downloads.download.clustarr.io` (Helm never deletes CRDs);
rollback N+1 → N re-applies N's CRDs (`git show`) then `helm rollback` (§10.4).

- [ ] **Step 1:** Edit.
- [ ] **Step 2: Commit** after F9.5's.

  ```bash
  git commit -m 'docs(runbook): release N+1 deletes the downloads CRD by hand after the census reads zero; rolling back re-applies it (ADR-0019 A9.4)' -- docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md charts/clustarr/crds/README.md
  ```

---

### Task A9.5: Release N+1 gate (beside F9.5's)

```bash
cd /home/appkins/src/mediactl/clustarr-unify
go build ./... && make build && make generate manifests && test -z "$(git status --porcelain -- api config charts)"
grep -rn 'downloadv1alpha1\.Download\b\|kind: Download$\|downloads\.download\.clustarr\.io' --include='*.go' --include='*.yaml' . | grep -v '^./docs/' | wc -l   # 0
grep -rn 'BucketLeases\|BucketPending\|ConsumerCatalogGrab\|ConsumerCatalogRedownload\|ManagerGrabarrEngine' --include='*.go' . | grep -v _test.go | grep -v 'Retired' | wc -l   # 0
```

---
## Amendments to the main plan

Spec §11.1, row by row: the exact change each task of
`docs/superpowers/plans/2026-10-06-manager-agent-split.md` takes. Each affected task carries one
line under its heading pointing here (`> **Amended by ADR-0019:** …`); the task's own text is not
edited. Where a row covers a range, the tasks of the range that change are named; the others
take no change. Built tasks (Waves 4b, 4d, 4e) are not re-run: the A-task named in each entry
changes their code, and the entry is the record of it.

**F3.1-F3.6, F5.1-F5.5, F6.1-F6.7.** No change (§11.1). F3 is built; F5 and F6 run after A1-A6
and consume nothing of them; ruling R8 leaves their dispatch blocks' new `delivery` field empty.

**F4.1-F4.4.** No change; their three notes are closed by A3: S9 (the Download source) and
`TestARefusedImportOverATranscodedFileStopsReadingDownloading` are replaced by entries and S24
(A3.5, A3.3); `itemstatus.Apply` stays `catalogarr`-only and the other managers' applies are
`itemstatus.ApplySet` (A3.3); the `.spec.target.<kind>` Download indexes go in A3.5.

**F7.1** (donor on the item). Derive `status.audio.donor` from the owner's `audioDonor` entry
(`status.downloads[]` with `purpose: audioDonor`, phase `Imported` or `Seeding`) and the entry's
execute record (`clustarr-imports`, `Execute.DonorPath`), not from an imported Download; read
the donor's open state through `rollup.DonorEntryOpen` (A3.5), not `rollup.DonorDownloading`.
"fileimport reduces the donor at import" happens inside A3.8's execute handler
(`app/import/worker/fileimport/execute.go`), which reports the reduced `.mka` path; F7.1 then
deletes A3.8's `AudioGraftApply` materialise effect and its `audiografts` RBAC in
`app/remediation/downloads/rbac.go` (R26). `AudioDonor.DownloadRef` holds the entry's `id`.

**F8.3** (Migrator). The Migrator gains A8.2's Download part (`downloadsPass`: intent, held
imports, source hashes, finalizers, census, deletion), its RBAC (`downloads
get;list;watch;update;delete`, `downloads/finalizers update`), and `kind="Download"` in
`clustarr_legacy_fold_objects`. One Migrator, one interval, one retention.

**F8.4** (adoption in the loop). The same `remediationmanager.Options.LegacyFold` also turns on
A8.1's Download adoption in the downloads item stage; `legacyfold.RegisterIndexes` registers
`IndexDownloadOwner`; the DownloadClient controller's `UnidentifiedGate` is set in release N.

**F8.5** (report and flags). The report gains A8.3's Downloads section; the flag pair is shared;
`internal/cli/manager/command.go` also adds `manager downloads export` (A8.3) beside
`legacy-fold`.

**F8.6** (dead letters and replays). The DLQ projector is edited at its manager home: A2.5 made
it a leader-only consumer registered by `registerHistory` in `app/catalog/manager/register.go`
(files unchanged: `app/catalog/worker/history/dlq.go`, `app/catalog/history/replay/replay.go`);
its resolvers also cover the subjects A2.4, A3.5, A3.6 and A3.8 added (engine commands, import
inspect and execute, intake candidates and scan observations); Download already left
`ReplayKinds` in A3.9, so F8.6 removes TranscodeJob and SubtitleRequest only.

**F8.7** (ui projection). Its MediaFile half is unchanged. The Downloads half is A3.9's (rows from
the owners' entries, `TransferProgress`, `ImportDetail`); F8.7 must not re-add a Download List to
`buildRelatedIndex` or `watchedKinds`; `TestUIWiresEveryUIOption` checks both new options.

**F8.9** (e2e). Scenario 1's download legs follow §6.13: a Search with `spec.grab` naming the
fixture release, waits on `status.downloads[?(@.id=="…")].phase` and the transfer record, the
`remove … blocklist` intent in place of `patchDownloadLabel`; add
`TestEngineRestartKeepsSeeding`, `TestAnUnclaimedTransferIsResumedOrRemoved` and
`TestABlockedReleaseShowsBlockedInASearch`; the upgrade-and-rollback scenario runs
`manager downloads export` before starting the previous release.

**F8.11** (split spec amendments). Add ADR-0019's supersessions: split §3.5.3 (the `events`
domain keeps only the RSS matcher; the engine, DLQ-projector and history-sink rows; `catalog`
loses the grab worker), §4.6 and §10.2.4 (no agent write verb; generated agent roles read-only),
§5.2 (every agent row of the "After the split" column), §5.3 (§5.3.1, the agent-side Secret
writes of §5.3.2, §5.3.4), and loop spec §4.14's agent table; and record the controller names
that are gone: `series`, `comic` (A3.2), `download`, `downloadclient-blocklist`,
`indexarr-directgrab`, `torrent-engine`, `usenet-engine` (A3.6, A3.7), `fileimport-retrigger`
(A3.8), `replay-download` (A3.9).

**F8.12** (fold gate). Run A8.5's greps and A7.1's writer sweep script; write §9.2's guards
(`TestAgentsCallNoKubernetesWriter`, `TestAgentsPublishNoTask`, `TestAgentLinksNoPlanner`,
`TestEveryFieldManagerHasItsHome` with an empty multi-home allow-list,
`TestRecordsWritersAreTheirAgents`, `starttest.AssertWroteOnlyAs` per agent identity) into the
test batch's list, expected green from A7.3 on (A7 runs after F8), and re-measure
`bin/manager` against W5.18's size with the new planners linked (§4.4: only `app/catalog/delay`
and `pkg/metadata/scenemap` are new to it beyond the planners).

**F9.1-F9.5** (release N+1). A9 removes Download alongside, each A9 task after its F9 task:
F9.1/F9.2 with A9.1 (the type, applyconfigurations, `status.legacyDownloads`, the Migrator's
Download part, the v1 `ImportTask` decoder, `ManagerGrabarrEngine`, the Download kind in
`pkg/k8s/deadletter.go`, the CEL test moved to `DownloadSource`); F9.4 with A9.2
(`clustarr-leases`, `clustarr-pending`, `catalogarr-grab`, `catalogarr-redownload` retired through
`Topology.Retired` and a retired-bucket list after the drain gate); F9.3 with A9.3 (`downloads`
leaves every role, the chart and `config/crd`; `expectedCRDCount` one lower); F9.5 with A9.4 (the
`kubectl delete crd downloads.download.clustarr.io` step after the census reads 0).

**W4.20-W4.25** (indexer sessions, built). **W4.20:** `NewSessionStore` loses its field-manager
parameter and its `Client`: the session store is KV only (A6.4); the facade key is created by the
manager under `indexarr` (`EnsureFacadeKey`) and only read by the agent. **W4.21:** the `Drop`
compare-and-swap keeps its KV half (`dropKV`, `DeleteRevision`); the Secret half moves to the
Indexer controller (`dropSessionSecret`); `TestSessionDropDoesNotWipeANewerSave` keeps its KV case
and loses its Secret case. **W4.22, W4.23:** no change. **W4.24:** `sessionsSource` also mirrors
the KV session into the Secret under `indexarr` (put → apply; delete → emptied by CAS).
**W4.25:** the agent's `newClientCache` builds the KV-only store; `facadekey.go` becomes the
read-only `Wait`.

**W4.82** (`PatchOverlay` CAS, built). Superseded by A5.3: `PatchOverlay` and
`overlayCASAttempts` are deleted; `status.overlay` is rendered by the Movie and Series item keys
from the overlay object's metadata under `catalogarr-artwork`, one apply per pass;
`TestARendererReplicaNeverRollsBackAnotherReplicasOverlay` retires with its subject.

**W4.90-W4.100** (bus, built). Used as built (§11.1): bind-only `Subscribe` (the manager creates
every durable, the engines' included), `HandlerTimeout` (the candidate intake's 60 s budget), the
`Clustarr-Id` header (now also the term reason's first field, R10), leader-only `ConsumerState`
(capacity). No pointer.

**W4.60-W4.75** (autoscale, built). The HPA metric is unchanged. **W4.60:** `importarr-recycle`
also takes `clustarr.work.importarr.recycle.files.<id>` with `schema.RecycleFilesTask` (A6.2),
with a history resolver. **W4.63:** `agentdomain.Domains()`: `catalog` loses
`catalogarr-grab`; `events` keeps only `catalogarr-rss-matcher` (`catalogarr-history` and
`clustarr-dlq-projector` move to `Fixed()`, homed `manager`, A2.5; `catalogarr-redownload` and
`catalogarr-grab` move to the new `Draining()`, A4.4, until A9.2); the intake and task-events
durables are `Fixed()`, homed `manager`; the per-engine durables belong to no domain (engines
are not HPA domains). **W4.72:** `QueueGauge` gains `Dispatch` and exports
`clustarr_dispatch_waiting{consumer}` and `clustarr_dispatch_unattended{consumer}` (A2.2); they
are dashboards' and alerts', never an HPA metric. **W4.74:** `autoscale.Options` gains
`Dispatch *dispatch.Ledger`, passed to the gauge. W4.61, W4.62, W4.64-W4.71, W4.73, W4.75: no
change.

**W5.19** (`TestEveryFieldManagerHasItsHome`). Its multi-home allow-list (`catalogarr-grab`,
`importarr`, `importarr-worker`, `grabarr-engine`) empties (A7): every `k8s.Manager*` but
`ManagerUI` has the one home `cmd/manager`; `grabarr-engine` is in `RetiredFieldManagers()`
(A3.6) and homeless.

**W6.2** (`TestEveryConsumerHasExactlyOneHome`). It learns the manager-hosted durables
(`catalogarr-intake-candidate`, `importarr-intake-scan`, `clustarr-task-events`,
`clustarr-dlq-projector`, `catalogarr-history`) and the per-engine durable family
(`grabarr-engine-<client>-<ordinal>`, created by the DownloadClient controller, bound by that
engine pod); `agentdomain.Draining()` durables are exempt until A9.2 retires them.

**W6.9** (one ClusterRole per identity). The generated agent roles are read-only (§9.1's table),
since A3-A6 moved every write marker into manager packages; `RBAC_PATHS_manager` gains
`./app/intake/... ./app/dispatch/... ./app/catalog/worker/history ./app/import/manager/...` beside
what it names, `RBAC_PATHS_grabarr-engine` lacks `./app/grab/status` (deleted); the manager role
holds `downloads get;list;watch;update;delete` and `downloads/finalizers update` in release N only
(A8.1, A8.2); A7's `TestNoAgentRoleGrantsAWriteVerb` runs with this task.

**W6.11** (topology switch). The `events` Deployment carries only the RSS matcher (its
`minReplicas 1` now protects only `catalogarr-rss-matcher`); the manager's grace (85 s) already
covers the intake durables' 60 s handler budget plus 25 s (`TestGracePeriodsCoverAckWait` reads
`HandlerBudget()`); agent Deployments set `NODE_NAME` from the downward API (`spec.nodeName`) for
presence (A2.1), and the manager `POD_NAME` and `POD_NAMESPACE` for the dispatch Event (A2.2).

**W6.15** (e2e renames). The download helpers follow §6.13 (A8.4's list: Search picks, entry
phase waits, the transfer record, the remove-blocklist intent).

**W10.2** (ADR-0018). Cite ADR-0019 for agent writes and policy: agents are executors, the
manager the only writer of every CR and Secret; ADR-0019 supersedes the agent rows of split §5.2
and §4.6 that ADR-0018 adopts.

**W10.4, W10.5, W10.7** (design of record, docs, CLAUDE.md). Rewrite the invariants this design
changes: "One controller-writer per resource" and the MediaFile spec/status split (both writers
are now in the manager: `importarr-worker` through the scan applier and the import
materialisation, `catalogarr` through the loop); "Kubernetes watches are the default coupling"
(agents couple through NATS only; the manager through watches, records and intake); the Download,
blocklist and grab-lease text (grabs are `status.downloads[]` entries; the blocklist is the
release index's, seq-fenced, 90 days; leases and `clustarr-pending` are gone); `CLAUDE.md:7`'s
`kubectl get movies,downloads,transcodejobs` becomes `kubectl get movies,series,mediafiles` with
the `--field-selector status.downloadPhase!=` queue view; the ADR index refinements for 0008 (the
delay profile and keep-best pending grab are the item's state machine; the KV lease retires) and
0016 (Download is no longer kept as a resource). **W10.5** adds to `docs/autoscaling.md` the
admission budgets and why `clustarr_dispatch_waiting` is not an HPA metric, and to
`docs/observability.md` the metric families and Event reasons of Global Constraints. **W10.7**
adds the new guards (§9.2) to CLAUDE.md's guard list and rewrites the gotchas that name Download
(the CEL release-identity rule, the usenet re-add dedupe, the engine emptyDir scratch) in terms of
entries, claims and the engine journal.

---

## Under-specified points, and how this plan decides them

The rulings above settle each; this list is for the reviewer.

- **How item planners land beside the rollups** (R5): the spec says "one pass, one apply per
  manager" but F4's item path is each kind's own reconciler; this plan adds item stages and
  generic unstructured sets.
- **Records written by agents** (R6): the fold's request/answer protocol does not fit buckets
  whose one writer is the agent; `records.Reader`/`Writer` and `Spec.KeyOf` are new.
- **When a search answer stops being grab input** (R7, `CandidateWindow`), and how a Series sees
  its episodes' answers.
- **Where delivery state is written** (R8): not by the intake; the fold's MediaFile blocks do not
  render it in release N.
- **Redownload as a level** (R25), since the spec's "the planner decides the next search" is an
  edge on a transition that can be lost.
- **Indexer health without a sequence in status** (R19).
- **Engine and intake durable timing** (R20) and **metadata dispatches without a status block**
  (R23).
- **Previous-release pending grabs** (R22): not adopted.
- **The import-list snapshot's key** (R17), since today's result lives in `clustarr-progress`.
- **Interactive results' item-state rejections** (R24), which moving item-state rules to the
  manager would otherwise drop from the ui.
- **Packages an agent links that hold a writer**: §9.2's type-checked guard fails on any call in
  a reachable package, so `mediafilespec`, `importliststate`, `app/indexer/status` and
  `app/indexer/clients` split (A3.8, A6.2-A6.4) and A7.1 sweeps the rest.
