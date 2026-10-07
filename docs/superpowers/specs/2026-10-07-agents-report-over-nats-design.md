# The manager is the control plane; agents report over NATS; the Download kind is removed (ADR-0019)

**Status:** Proposed, 2026-10-07. For the owner's review; nothing here is implemented, and the
plan is revised only after the review (§11).

**Basis:**

- Worktree `/home/appkins/src/mediactl/clustarr-unify`, branch `unify-manager-agent`, at
  `530b5dfb`. Line references are to that commit unless marked otherwise. Another session was
  building fold waves F1 and F2 (`pkg/records`, the MediaFile types) and the start of F3 while
  this was written; where the working tree differed, the text says so.
- The owner's four decisions of 2026-10-07 (`.superpowers/unify/rulings.md`, "Agents never
  write Kubernetes", and the coordinator's relay of decision 4), §1.
- ADR-0016 and the fold's design (`docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md`,
  "loop spec"), whose records pattern this design generalises; the split design
  (`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`, "split spec"); the NATS
  research notes (`.superpowers/unify/research/{nats-worker-pools,nats-object-store,nats-hpa-metrics}.md`).
- Seven read-only audits of the branch: agent Kubernetes writes per domain, Download's fields
  and readers, RBAC and guards, the bus and records layer, agent policy decisions (two), and
  task routing and capacity. Their findings are §3 and §5.

CLAUDE.md's invariants and gotchas bind throughout: one complete declaration per field manager
per apply; forced ownership makes a double claim silent; a slow read-work-apply path re-reads
before it applies; a typed client cannot send a value a CRD default overrides; every status
list and string is capped; every NATS KV key goes through `events.KVKeyToken`; single-node
work streams are small memory streams that discard.

**ADR number.** The ADR is `docs/adr/0019-agents-never-write-kubernetes.md`. 0017 and 0018 are
free on disk on this branch and on main, but the committed plan reserves them (W10.3 writes
ADR-0017, "no external media programs"; W10.2 writes ADR-0018, "manager, agents and ui"), the
plan's code comments already cite ADR-0017, and the loop spec's D35 fixes both numbers.

---

## 1. Owner decisions, and the frame they set

1. **"I would like to get rid of the downloads crd since it's updated directly from a worker
   (anti-pattern)."** Offered "keep Download, the manager its only writer" or "remove the
   Download CRD", the owner chose removal.
2. **Every agent.** No code that runs in `cmd/agent` (any `--domain`: `catalog`, `events`,
   `metadata`, `import`, `index`, `caption`, `torrent-engine`, `usenet-engine`), `cmd/markers`
   or `cmd/transcode` writes the Kubernetes API: no create, update, patch or delete, no status,
   no finalizers, no Secrets, and no Events unless decided here (decided: none). Agents keep
   read-only RBAC or none and report over NATS.
3. **"We should also be receiving acks and updates for the streams within the manager - which
   will be responsible for updating the CRs."** The manager consumes the workers'
   acknowledgements and updates (records, KV watches, consumer advisories) and is the only
   writer of every CR. `cmd/ui` is not an agent; its `ui/actions` user-intent writes stay.
4. **"The manager should primarily function as a reconciliation loop/state machine. It will
   function as a proper control plane - determining which agent to queue for which task."**

Decision 4 is the frame for the rest. **The manager is the control plane:** every resource's
lifecycle is a state machine in its reconcile loop (§4.1); a task is an effect of a
transition; the manager decides the work is due, chooses the task kind and its destination
(domain and queue, hardware class, DownloadClient engine instance, node-bound pool), admits it
against the destination's capacity, publishes it, and advances on the acks and updates
(§5, §8). **Agents are executors:** they take a task, do it, report, and hold no policy (§4.2).
The audit therefore covers decisions as well as writes (§3.5).

This supersedes ADR-0016's "These stay resources: Download …"
(`docs/adr/0016-per-file-work-is-mediafile-status.md:89`). Search, Episode, Issue and the
configuration kinds stay resources; DownloadClient stays, reconciled by the manager.

---

## 2. Decisions in brief

Each is binding for the implementation unless the owner reverses it; each names what it costs
if wrong.

| # | Decision | Section | Cost if wrong |
|---|---|---|---|
| D1 | The manager is the only writer of every CR and of every Secret clustarr writes; agent roles are read-only; a type-checked AST guard, a role guard and a read-only client hold it | §9 | none in correctness; a missed write fails loudly at runtime |
| D2 | Agents report in two shapes: **records** (CAS, one writer per key) for state the manager reads, **intake** messages (`CLUSTARR_INTAKE`, acked after the decision lands) for candidates and observations it did not request | §4.3 | a third shape later (e.g. bulk uploads) |
| D3 | Every decision that changes desired state moves to the manager; scoring, matching, I/O and observation stay in agents. 136 decisions audited: 51 move, 19 split, 56 stay (19 computation, 34 mechanism, 3 stated exceptions), 3 retire, 5 are the fold's, 2 are already the manager's | §3.5 | a split drawn in the wrong place costs a round trip or a manager CPU hot spot |
| D4 | A grab is an entry in its owner's `status.downloads[]`; a Series owns every episode grab (single episodes too), a Comic every issue grab; Series and Comic become loop item keys | §6.1 | Series status size; moving single-episode grabs later is a migration |
| D5 | Field manager names stay (R7); a loop item pass makes one apply per manager whose set it owns on the kind, each CAS-chained; no ownership migration | §7.0 | up to four applies per pass where sets change together |
| D6 | The grab decision, the delay profile and the keep-best pending candidate are the owner key's; the KV grab lease, `clustarr-pending`, scheduled grab messages and the redownload consumer retire | §6.6 | one more hop per automatic grab |
| D7 | Engines execute whole desired-state commands, seq-fenced, from `CLUSTARR_WORK_ENGINE` (the default kept, for its delivery signals); they report transfer and engine records; they never remove on absence; orphans are reported, not reaped | §6.7 | if KV desired state proves simpler, the command transport changes; the records do not |
| D8 | Imports run in two phases, inspect then execute, with the decision between them in the manager | §6.9 | Q4 |
| D9 | The blocklist is a capped list in the owner's status with today's 90-day TTL | §6.2 | Q5 |
| D10 | Admission bounds every dispatched durable at 2 × `MaxAckPending` outstanding; the rest waits as visible state; the HPA metric is unchanged | §5.4 | a budget too low starves a domain's scale-up; it is one constant |
| D11 | `MSG_NAKED` and `MSG_TERMINATED` go to a new small stream, `CLUSTARR_TASK_EVENTS`; a task's delivery state is written to its CR on transitions only | §8 | stale `delivery` fields after a lost advisory |
| D12 | The DLQ projector and the history sink move into the manager; the `events` domain keeps only the RSS matcher | §7.7 | none expected |
| D13 | `status.artwork` and `status.overlay` are derived from the artwork objects' metadata through a manager-side `objindex` | §7.1 | a field missing from object metadata needs adding (the fetcher owns it) |
| D14 | Two releases: N adopts every live Download into its owner (with the fold's Migrator), N+1 removes the CRD; rollback from N re-exports Downloads | §10 | a rollback path that is rarely exercised |

---

## 3. The audit

Read-only, on the branch at `530b5dfb` (2026-10-07). Paths are relative to the worktree root.
"Domain" is the process that runs the code after the split: an `agent --domain <d>`
(`internal/cli/agent/domains.go:51-57`, `pkg/agentdomain/agentdomain.go`), `markers`,
`transcode`, or the manager. The write helpers are server-side apply with forced ownership
(`k8s.PatchStatus`, `k8s.Apply`, `pkg/k8s/patch.go:70,103`), the same with a resourceVersion
precondition (`k8s.PatchStatusCAS`, `pkg/k8s/patch_cas.go:75`), and whole-object Updates for
finalizers (`pkg/k8s/finalizers.go:83,121`). A write that names no manager records
`k8s.DefaultFieldOwner`, `clustarr` (`pkg/k8s/fieldmanager.go:336`).

### 3.1 Every Kubernetes write agent-side code makes

44 rows. **Fold** marks the four that ADR-0016's fold already removes; they are not designed
again here. The last column names the section that replaces each row.

**Agent `catalog`** (4)

| # | Site | Field manager | Verb; object and fields | Replacement |
|---|---|---|---|---|
| W1 | `app/catalog/worker/grab/kindops.go:187` (Movie), `:252` (Episode); `nonvideo.go:105` (Album), `:167` (Book), `:219` (Audiobook), `:275` (Issue); all through `updateWorkerStatus` (`kindops.go:321-337`); callers `decide.go:210`, `perform.go:243,466`, `kindops.go:371,398` | `catalogarr-grab` | `PatchStatus`, CAS by `RetryOnConflict`; item `status.pendingGrab`, `lastSearchedAt`, `searchAttempts`, and `donorSearchAttempts` on Movie and Episode | The item's grab planner in the manager (§6.6, §7.3) |
| W2 | `app/catalog/worker/grab/perform.go:321` (`createDownload`, `:262-325`) | `catalogarr-grab` | `Apply` creating a Download named `k8s.ChildName(target, guid[, purpose])` (`perform.go:158-163`): spec `protocol`, `source`, `release`, `target`, `grabbedBy`, `purpose`, `qualityProfileRef`, `seedCriteria`; owner reference to the target, or the Series for a pack | A grab entry in the owner's `status.downloads[]` (§6) |
| W3 | `app/catalog/worker/search/worker.go:829` (`applySearchStatus`; callers `:747`, `:790`) | `catalogarr-worker` | `PatchStatus`; Search `status.finishedAt`, `indexerOutcomes` (cap 100), `results` | Record `clustarr-searches`; the Search controller applies under `catalogarr-worker` (§7.4) |
| W4 | `app/catalog/worker/artwork/handler.go:566` → `app/catalog/status/artwork.go:188` (`PatchOverlay`) | `catalogarr-artwork` | `PatchStatus`, CAS ×5; Movie and Series `status.overlay` | Derived from the overlay object's metadata; the loop applies under `catalogarr-artwork` (§7.2) |

**Agent `events`** (5)

| # | Site | Field manager | Verb; object and fields | Replacement |
|---|---|---|---|---|
| W5 | `app/catalog/worker/rssmatcher/handler.go:317` → `grab.Decide` → W1's sites | `catalogarr-grab` | as W1 | RSS candidates on `CLUSTARR_INTAKE`; the manager decides (§6.6) |
| W6 | the RSS matcher's `grab.Sink` → `perform.go:321` | `catalogarr-grab` | as W2 | as W5 |
| W7 | `app/catalog/worker/history/dlq.go:270` (`applyAnnotation`, `:257-280`) | `clustarr-dlq-projector` (`client.FieldOwner`) | JSON merge patch of `clustarr.io/dead-lettered` and `clustarr.io/dead-letter-seq` (`app/catalog/history/annotations.go:29,36`) on every annotatable kind (`history/target.go`; RBAC `dlq.go:75-79`) | The projector moves into the manager, same durable and manager (§8.5) |
| W8 | `app/catalog/worker/history/dlq.go:214` (object), `:234` (Namespace) | recorder `clustarr-dlq-projector` | Warning Event `DeadLettered` | Manager (§8.5) |
| W9 | `app/catalog/worker/history/sink.go:126` | recorder `catalogarr-history` | One Event per domain event (`reasonFor(action)`) | The sink moves into the manager (§7.7) |

**Agent `metadata`** (5)

| # | Site | Field manager | Verb; object and fields | Replacement |
|---|---|---|---|---|
| W10 | `app/catalog/metadata/artwork/handler.go:206` (`Pass.Run`), from `app/catalog/metadata/worker.go:334` | `catalogarr-metadata` (`catalogstatus.GatewayManager`, `app/catalog/status/artwork.go:63`) | `PatchStatus`, no CAS (uncached re-read and merge); `status.metadata` (every leaf `metadata/patch.go` builds, but not Album `selectedReleaseID`) and `status.artwork[]` on Movie, Series, Artist, Album, Author, Book, Audiobook, Comic | Record `clustarr-item-metadata` plus the artwork objects' metadata; the manager applies under `catalogarr-metadata` (§7.1) |
| W11 | `app/catalog/metadata/artwork/handler.go:341-342` (artwork fetch; `ExtractGatewayStatus`, `:365-468`) | `catalogarr-metadata` | `PatchStatus`; `status.artwork` replaced, `status.metadata` re-sent | as W10 |
| W12 | `app/catalog/worker/markers/handler.go:290` → `segmenting.Applier.ApplyMerged` (`app/catalog/segmenting/apply.go:82-109`) | `catalogarr-markers` | `PatchStatus`, CAS ×5; MediaFile `status.markers` | **Fold** (loop spec §4.12, F3.4: `clustarr-markers` records) |
| W13 | `app/catalog/segmenting/setup.go:48`, from `results.go:67` | `catalogarr-markers` | as W12 | **Fold** (F3.4: `clustarr-segments`) |
| W14 | `app/catalog/metadata/artwork/fetcher.go:494` | recorder `metadata-gateway` | Warning Event `ArtworkFetchFailed` | The failure is reported in the metadata record; the manager emits the Event on the transition (§7.7) |

**Agent `import`** (16)

| # | Site | Field manager | Verb; object and fields | Replacement |
|---|---|---|---|---|
| W15 | `app/import/worker/rescan/mediafile.go:536` → `app/import/mediafilespec/spec.go:174` | `importarr-worker` | `Apply` MediaFile spec (`mediaRef`, `path`, `sizeBytes`, `modTime`, the frozen import fields); with the read resourceVersion when the object exists, blind otherwise; name `k8s.ChildName(ref.Name, "mediafile", path)` (`mediafile.go:528`) | A scan observation on `CLUSTARR_INTAKE`; the manager decides and applies (§7.6) |
| W16 | `app/import/worker/rescan/renamepass.go:90` → `mediafilespec/rename.go:265` | `importarr-worker` | `Apply` MediaFile spec (new path), with resourceVersion; moves the file and its sidecars | Deleted: the loop's rename actuator (loop spec §3.9) is the only rename path (§7.6) |
| W17 | `app/import/worker/rescan/mediafile.go:360` (`handOver`) | `importarr` | `Apply` annotation `catalog.clustarr.io/observed-fingerprint` | A field of the scan observation; the manager applies under `importarr` (§7.6) |
| W18 | `app/import/worker/rescan/mediafile.go:403` (`applyMovie`) | `importarr-worker` | `Apply` Movie spec (create): `tmdbID`, `qualityProfileRef`, `rootFolderRef`, `addOptions` | The manager creates the item from the observation's attribution (§7.6) |
| W19 | `app/import/worker/rescan/series.go:279` (`applySeries`) | `importarr-worker` | `Apply` Series spec (create) | as W18 |
| W20 | `app/import/worker/rescan/prune.go:71` | default | `Delete` MediaFile, UID and resourceVersion preconditions | A `missing` observation; the manager deletes with the same preconditions (§7.6) |
| W21 | `app/import/worker/fileimport/worker.go:475` (`applyMediaFile`; callers `process.go:414`, `episode_import.go:412`, `nonvideo.go:365`) | `importarr-worker` | `Apply` MediaFile spec, CAS through the APIReader; name `k8s.ChildName(item, "mediafile", dest)` | The import's execute phase reports placed files; the manager applies (§6.9) |
| W22 | `app/import/worker/fileimport/process.go:435`, `episode_import.go:433`, `nonvideo.go:263,378` | default | `Delete` replaced MediaFiles, **no precondition** | The manager deletes, with a UID precondition, as part of the approved import plan (§6.9) |
| W23 | `app/import/worker/fileimport/donor.go:254` | `importarr-worker` | `Apply` AudioGraft | **Fold** (F7.1) |
| W24 | `app/import/worker/fileimport/worker.go:550` (`patchImport`, re-Get `:537`) | `importarr` | `PatchStatus`, forced, **no CAS**: Download `status.import` (all nine leaves, `download_types.go:527-572`) | Record `clustarr-imports`; the grab entry's import summary (§6.9) |
| W25 | `app/import/worker/importlist/worker.go:218` (`StampSynced`) | `importarr-worker` | `Apply` annotation `catalog.clustarr.io/importlist-synced-at` | Retired; the sync's record carries `syncedAt` (§7.6) |
| W26 | `app/import/worker/importlist/catalogitem.go:189` | `importarr-worker` | `Apply` Movie spec (create, apply, unmonitor), no resourceVersion | The ImportList state machine in the manager diffs and applies (§7.6) |
| W27 | `app/import/worker/importlist/catalogitem.go:229` | `importarr-worker` | `Apply` Series spec, CAS | as W26 |
| W28 | `app/import/worker/importlist/catalogitem.go:336` (Movie), `:346` (Series) | default | `Delete` item (syncLevel remove or removeAndDelete) | as W26, with a UID precondition |
| W29 | `app/import/worker/importlist/delete.go:185` | default | `Delete` MediaFiles after recycling (removeAndDelete) | The manager deletes; the agent recycles on a task (§7.6) |
| W30 | `app/import/importliststate/tokenstore.go:154`, from `importlist/sync.go:103` → `pkg/importlist/trakt/list.go:132` | `importarr` | Secret apply, CAS (Trakt token refresh after a 401) | The manager refreshes tokens; the agent reports `needsToken` (§7.6) |

**Agent `index`** (5)

| # | Site | Field manager | Verb; object and fields | Replacement |
|---|---|---|---|---|
| W31 | `app/indexer/agent/facadekey.go:117`, from `workers.go:181` | `indexarr-worker` | typed `Create` of the facade key Secret (create-only) | The manager creates it; the agent reads it (§7.5) |
| W32 | `app/indexer/clients/session.go:175`, from `clientcache.go:366` | `indexarr-worker` | Secret apply, no resourceVersion (session save after a relogin) | KV `clustarr-indexer-sessions` only; the manager mirrors it into the Secret under `indexarr` (§7.5) |
| W33 | `app/indexer/clients/session.go:246` (`Drop`), from `clientcache.go:359` | `indexarr-worker` | Secret apply, CAS (empties a failed session) | KV `DeleteRevision` only; the manager mirrors (§7.5) |
| W34 | `app/indexer/search/fanout.go:659` → `app/indexer/status/status.go:254` | `indexarr-worker` | `PatchStatusCAS`, Indexer `WorkerFields`: `escalationLevel`, `lastFailure`, `lastFailureAt`, `initialFailureAt`, `disabledUntil`, `indexedReleases` | Outcome record `clustarr-indexer-health`; the Indexer state machine decides backoff (§7.5) |
| W35 | `app/indexer/worker/rss/worker.go:326` | `indexarr-worker` | as W34, plus `lastRssAt`, `lastRssNewCount` | as W34 |

**Agent `caption`** (1)

| # | Site | Field manager | Verb; object and fields | Replacement |
|---|---|---|---|---|
| W36 | `app/caption/worker/fetch/apply.go:301` | `captionarr-worker` | `PatchStatusCAS`, SubtitleRequest `status.items[]` worker leaves | **Fold** (F5.2: `clustarr-subtitles`) |

**Agent `torrent-engine`** (4) and **`usenet-engine`** (4)

| # | Site | Field manager | Verb; object and fields | Replacement |
|---|---|---|---|---|
| W37 | `app/grab/engine/torrent/reconciler.go:160` | default | `EnsureFinalizer` `download.clustarr.io/engine` | Gone with Download; the item finalizer is the manager's (§6.8) |
| W38 | `app/grab/engine/torrent/reconciler.go:237,274,357,369` → `:483` → `app/grab/status/status.go:246` | `grabarr-engine` | `PatchStatus`, Download `EngineFields` (26 leaves, `pkg/download/status.go:84-153`) after a fresh Get | Record `clustarr-transfers` (§6.7) |
| W39 | `app/grab/engine/torrent/reconciler.go:462` | default | `RemoveFinalizer` | Gone (§6.8) |
| W40 | `app/grab/agent/torrent/register.go:108-111` | recorder `grabarr-engine` | Warning Event on the DownloadClient (proxy `Warn`, `ProxyUDPUnavailable`) | Record `clustarr-engines`; DownloadClient condition and Event from the manager (§6.7) |
| W41 | `app/grab/engine/usenet/engine.go:188` | default | `EnsureFinalizer` | as W37 |
| W42 | `app/grab/engine/usenet/engine.go:226` → `:338` | `grabarr-engine` | `PatchStatus`, `EngineFields` | as W38 |
| W43 | `app/grab/engine/usenet/engine.go:423` | default | `RemoveFinalizer` | as W39 |
| W44 | `app/grab/engine/usenet/engine.go:205` | recorder `usenet-engine` | Warning Event `DownloadAddFailed` on the Download | The transfer record's failure; the manager's Event on the owner (§6.7) |

**By domain:** `catalog` 4, `events` 5, `metadata` 5, `import` 16, `index` 5, `caption` 1,
`torrent-engine` 4, `usenet-engine` 4; `markers` and `transcode` 0. The fold removes W12, W13,
W23 and W36, leaving **40** for this design.

### 3.2 What agents do not write

- **Leases.** No agent takes a Kubernetes lease: the agent runs a non-electing manager
  (`internal/cli/agent/run.go:60`). The KV grab lease in `clustarr-leases`
  (`app/catalog/worker/grab/lease.go:125-225`) is NATS, and retires with this design (§6.6).
- **`cmd/markers`, `cmd/transcode`, `app/segments/worker`, `app/squash/worker/**`** import no
  client-go or controller-runtime client (`cmd/transcode/main_test.go:141`
  `TestBinaryImportsNoKubernetesClient`). Pool and graft pods run with
  `AutomountServiceAccountToken: false` (`app/squash/controller/pool/template.go:369`).
- **Already NATS-only:** the probe worker (`clustarr-probes`), probe seeds, the recycle sweep,
  the rescan tally (`clustarr-progress` `scan.<uid>`, `rescan/worker.go:793`, which the
  manager's LibraryScan controller incorporates at `libraryscan_controller.go:318,477`), the
  import-list result (`clustarr-importlist`, `importlist/worker.go:190`, incorporated at
  `controller/importlist/controller.go:285,427`), dedup, the redownload handler (frees leases,
  publishes searches; `redownload/handler.go:44-46,285,455`), the index agent's download verb,
  facade, query and sweeper, and the caption providerset.

### 3.3 Manager-side code the Download removal changes

These are not agent writes, but they write or own Download today and go with it:

| Site | What it does |
|---|---|
| `app/grab/controller/download/controller.go` (`DC:173,204-253,376,407-450,551-578,599-661`) | Finalizer `download.clustarr.io/download`; `spec.clientRef` and the three labels; phase derivation (`phase.go:97-165`); the import task on the Downloaded edge; blocklisting (label, then `blocklistedUntil = now + 90 d`, `download_types.go:66`); `evt failed`; `seedGoalMet`; `removeDataOnDelete` with `fsops.SafeRemove(DataDir, outputPath)` after the engine finalizer goes, waiting `DefaultEngineTeardownTimeout` (10 min, `teardown.go:47`) for a gone engine |
| `app/grab/controller/downloadclient/blocklist.go:96-128` | `BlocklistSweeper` deletes blocklisted Downloads after 90 days, running both finalizers |
| `app/grab/controller/downloadclient/controller.go:375-382,528` | Counts `status.active`/`queued` from a Download watch |
| `app/catalog/controller/search/reconciler.go:795-803` | The Search controller's `resolveGrab` applies a Download under `catalogarr`; `:828-845` lists Downloads uncached for the double-grab check |
| `app/indexer/controller/directgrab/directgrab.go:93-155` | Counts direct grabs into the grab ring (`clustarr-indexer-limits`) on Download create |
| `app/import/controller/retrigger/retrigger.go:83-150` | Re-publishes the ImportTask for a Blocked import when `catalog.clustarr.io/import-target` or `import-override` is set on the Download |
| `app/catalog/history/replay/replay.go:83,134-145` | `replay-download`, one of the `ReplayKinds` |

### 3.4 Every reader of Download outside `app/grab`

| Reader | Site | Reads | Process |
|---|---|---|---|
| Rollups | `app/catalog/controller/rollup/activedownload.go:50` (`DownloadNonTerminal`), `:91` (`ActiveDownload`), `:109` (`DonorDownloading`); `downloadoverlay.go:94` (`DownloadOverlay`) | deletion, the blocklisted label, `status.import` (Blocked with `heldSince`), `status.phase`, `spec.purpose`, creation time | manager (item reconcilers) and agents (search, grab) |
| Coverage | `app/catalog/grabsource/grabsource.go:209` (`Covers`) | owner UID, `spec.target` | agents |
| Item reconcilers | movie `reconciler.go:149,182,260-290`, `report.go:47-53`; episode `:152,231,283,313`; album `:170,195,239,279-284,599`; book `:151,176,213,271-276`; audiobook `:142,177,223,266-271`; issue `:133,148,186,226-231,384` | `.spec.target.<kind>` indexes (the Episode one covers pack keys), phase, deletion | manager |
| Search worker | `app/catalog/worker/search/blocklist.go:47-65` (index `search.clustarr.io/download-target`), `:89-150` (`LoadBlocklist`, label selector), `snapshot.go:237-300` (queue, `CurrentFile` Get by `importedFrom.downloadRef`), `donor.go:75-82`, `nonvideo.go:324` | blocklist, queue, the current file's source hash | agent `catalog` |
| Grab guard | `app/catalog/worker/grab/perform.go:363-445` | uncached List of every Download, `managedFields`, lease holders | agents `catalog`, `events` |
| RSS matcher | `rssmatcher/handler.go:226`, `resolve.go:233,245-260` | blocklist, current file, queue | agent `events` |
| Redownload | `redownload/handler.go:346-373` (`awaitBlocklist`) | blocklisted label, phase | agent `events` |
| History | `app/catalog/history/target.go:332-338,374-380` | maps ImportTask and DownloadEvent to a Download ref for the Event | agent `events` |
| fileimport | `app/import/worker/fileimport/worker.go:244-265`, `dedup.go:113,135` | `status.contentRoot`, `canMoveFiles`, `import.state`, phase; `spec.release`, `qualityProfileRef`, `grabbedBy`, `manual`, `target`, `purpose`; the import annotations | agent `import` |
| ui | `ui/projection/index.go:124-133,195`, `projection.go:475,572`; `ui/routes.go:55-56,205-224`; `ui/sse.go:167,207,573,616-628`; `ui/views/downloads.templ:46-288`; `pkg/pipeline/project.go:69-82,150-170,236-245,296-325,430-480`, `stage.go:112` | everything the Downloads page and the pipeline show | ui |
| Indexes | the six item indexes; `search.clustarr.io/download-target` (`internal/cli/agent/indexes.go:42-60`); the grab controller's `clientRefIndexKey` (`controller.go:783`) | | |
| Printer columns | `api/download/v1alpha1/download_types.go:825-832`; DownloadClient Active/Queued (`downloadclient_types.go:618-619`) | | |
| Tests | `test/e2e/helpers_test.go:1244-1393`, `download_test.go:107,140,229,285`, `import_test.go:76`, `subtitle_test.go:728`, `transcode_test.go:903`, `ui_test.go:168,321-347`; `test/system/nonvideo_envtest_test.go:456-490`; `pkg/crdcheck/download_cel_test.go`, `donor_cel_test.go`; `cmd/manager/start_envtest_test.go` | | |
| Docs | `CLAUDE.md:7`, `README.md`, ADR-0004, `docs/research/naming.md`, the design of record, the split and loop specs and plans | `kubectl get … downloads` | |

No code anywhere has `Owns(&Download{})`: ownership runs from items to Downloads. The ui never
writes a Download (`config/rbac/ui_role.yaml:77-85` grants get, list, watch).

### 3.5 Every policy decision agent-side code makes

Owner decision 4 makes the manager the control plane, so the audit covers decisions as well
as writes. The rule of thumb (§4.2): **pure computation** (parsing, probing, scoring and
ranking candidates, fingerprinting, rendering, provider and indexer I/O) is task work an
agent does and returns; **any choice that changes desired state** (grab this release;
attribute, create or delete a file or item; blocklist; retry, hold or fail; which client,
pool or agent; when to refresh, rescan, sync or search) is the manager's.

**Dispositions.** **Move**: the manager decides; the agent reports the facts. **Split**: the
computation stays, the choice moves. **Stay (computation)**: pure work whose result the
manager accepts or re-checks. **Stay (mechanism)**: a limiter, retry, guard or safety stop
inside the execution of a task the manager issued, whose inputs (limits, thresholds) the
manager or the user declares; each says why it cannot move. **Retire**: the decision has no
successor. **Fold**: ADR-0016's fold already moves it. **Manager**: already in the manager
today, listed because the agent re-runs or depends on it.

**What moving costs.** Linking is not the obstacle. `cmd/manager` already links
`pkg/decision`, `pkg/release`, regexp2 with the TRaSH catalogue, `pkg/quality`, `grabsource`,
`app/indexer/limits`, `overlayplan`, `pkg/segments`, `pkg/subtitles`, `pkg/cardigann` and
every metadata client (`go list -deps ./cmd/manager`, 2026-10-07); it links none of ffgo,
purego, onnx, anacrolix, `pkg/download/usenet` or `pkg/relindex`, and split ruling R6 keeps it
so. Of what moves, only `app/catalog/delay` and `pkg/metadata/scenemap` are new to it, both
small. The costs that decide are CPU on the single leader (regexp2 custom-format scoring of up
to 500 releases per live search and of every matched RSS release), blocking I/O inside a
reconcile (indexer and provider RPCs of up to 45 s), and facts only an agent can observe
(`/data`, probes, live anacrolix and NNTP counters, limiters, sessions). So scoring, matching,
I/O and observation stay in agents; choices move.

Paths: `w/` is `app/catalog/worker/`, `cat/` `app/catalog/agent/catalog/`, `imp/`
`app/import/worker/`, `idx/` `app/indexer/`, `cap/` `app/caption/worker/fetch/`, `eng/`
`app/grab/engine/`, `dl/` `pkg/download/`.

**Catalog: agents `catalog`, `events`, `metadata`** (34)

| # | Site | Decision today | Disposition |
|---|---|---|---|
| P1 | `w/search/worker.go:279,288` | drop automatic tasks for kinds the grab path cannot grab | Move: the search planner never dispatches them |
| P2 | `w/search/worker.go:314`; `snapshot.go:134` | drop an automatic task for an unmonitored item (an episode needs its Series monitored) | Move: the planner checks at dispatch; a task fenced by `searchDispatch.seq` makes a late one stale |
| P3 | `w/search/worker.go:596-616` | dead-letter a task with no profile; retry a missing one | Move: dispatched only with a resolved profile, carried in the task |
| P4 | `w/search/worker.go:337-350`; `donor.go:42` | what a donor search must carry (languages, anchor, rejected releases, donors in flight), or skip it | Move |
| P5 | `w/search/worker.go:357-368` | live search or the local release index, by `task.IndexOnly` | Stay (mechanism): executes the flag; the flag is P12 |
| P6 | `w/search/worker.go:394-402`; `w/grab/kindops.go:364,393` | count a search attempt unless every indexer was paced | Move: rendered from the answer (§7.3) |
| P7 | `w/search/worker.go:668-687` | which protocols are enabled (from DownloadClients) | Move: the task carries them |
| P8 | `w/search/worker.go:416` → `pkg/decision/evaluate.go:44` | approve or reject each release: quality, custom-format score, identity, language **and** blocklist, queue, current file, `TranscodedFinal` | **Split**: release evaluation (parse, quality, regexp2 scoring, profile approval, identity) stays; the item-state rules move to the grab planner (§6.6) |
| P9 | `w/search/rank.go:50` | rank and cap at 200 | Stay (computation). Finding: no `IndexerPriority` is passed, so every indexer ranks at 25; the task carries the priorities |
| P10 | `w/search/worker.go:425-428` | interactive: write Search status; automatic: hand to `grab.Sink` | Move: both answer in a record (§7.4) |
| P11 | `controller/wantedcron/runnable.go:179`, `backoff.go:49` | which namespaces to wake; backoff 6 h · 2^n to 7 d | Manager (its per-item half is P12) |
| P12 | `w/search/wantedscan.go:143-188` | per item: due, `IndexOnly`, the 20-per-sweep donor cap | Move (§7.3) |
| P13 | `w/grab/sink.go:129-221` | best approved release; next indexer when one is at its grab limit; hold the soonest; never delay a donor | Move (§6.6) |
| P14 | `cat/resolve.go:66`; `app/catalog/delay/resolve.go:53`; `w/rssmatcher/resolve.go:298` | which DelayProfile applies | Move (links `app/catalog/delay`) |
| P15 | `w/grab/decide.go:93-111`; `delay.go:37,47` | grab now or hold (delay per protocol, bypass at the top tier or score) | Move |
| P16 | `w/grab/pending.go:96-150` | keep the best pending candidate (`clustarr-pending`, CAS) | Move: `status.pendingGrab` |
| P17 | `w/grab/decide.go:166-217` | schedule the grab (`WithScheduleAt`) and write `pendingGrab` | Move: a `RequeueAfter` |
| P18 | `w/grab/handler.go:146-238` | at the scheduled time: ack, drop, reschedule or retry | Retire with the scheduled grab |
| P19 | `w/grab/lease.go:108-165,187` | take, reclaim, free grab leases | Retire: one reconcile per owner serialises (§6.6) |
| P20 | `w/grab/perform.go:150,196,219,226`; `grabsource.go:93`; `limit.go:70` | source choice, double-grab guard, grab-limit reservation, create the Download | Move |
| P21 | `w/rssmatcher/match.go:66-210`; `index.go:90` | match a release to monitored items (ids first, title+year, ambiguous matches nothing, TheXEM, packs to episodes) | Stay (computation): per firehose release, 13 indexes and TheXEM; the grab planner re-checks identity |
| P22 | `w/rssmatcher/handler.go:265-282`; `resolve.go:268-286` | skip unmonitored items; protocols from the DelayProfile; indexer priority | Stay as a pre-filter (it bounds work; the manager re-checks); protocols and priority Move |
| P23 | `w/rssmatcher/handler.go:300,307-317,381` | evaluate the release per matched item; narrow a pack to wanted episodes; `grab.Decide` | Split: evaluation stays; narrowing and the grab move |
| P24 | `w/redownload/handler.go:208,223,285,296` | free leases; whether to search again (blocklisted always; failed unless a local fault or `importExpired`; under 24 h) | Move: the owner's `Blocklisted` transition (§6.6) |
| P25 | `w/redownload/handler.go:345-403` | wait for the blocklist label; search only live, monitored items; a failed pack per episode; keep the donor purpose | Move, as P24 |
| P26 | `w/history/dlq.go:168-257` | annotate the dead-lettered object | Move (§8.5) |
| P27 | `w/artwork/handler.go:322-396`; `overlayplan/plan.go:185,272` | winning OverlayProfile, badges, inputs digest; delete the overlay; skip an unchanged digest | Split: the plan moves (§7.2); decode and draw stay |
| P28 | `app/catalog/metadata/patch.go:697` (`selectImages`) | which provider images reach `status.metadata.images` | Stay (computation of the document) |
| P29 | `app/catalog/artwork/sources.go:65`; `metadata/artwork/fetcher.go:237,307` | which image to fetch per type; refetch; keep on failure; drop a removed override | Split: the plan moves (§7.1); the fetch stays |
| P30 | `metadata/worker.go:121-129,221-302`; `settle.go:30`; `limiter.go:26`; `registry.go:69`; `enrich.go:81,167` | cache bypass, TTL by state, provider order, ratings carry-forward, retry or discard, token buckets | Stay (mechanism): provider I/O in the one gateway with its in-process limiters (ADR-0007) |
| P31 | `controller/movie/reconciler.go:413-447` and siblings; `controller/metadatarefresh/refresher.go:181-189` | an item's metadata is due | Manager |
| P32 | `w/markers/handler.go:119,220-251` | re-check `markers.Due`; answer NotFound for a multi-episode file or a non-aired order | Fold (loop spec §4.12) |
| P33 | `w/markers/handler.go:126-143,193-215`; `pkg/metadata/clients/theintrodb/theintrodb.go:173-200` | a missing series is absent for 24 h; republish at the allowance reset; rotate keys | Stay (mechanism): provider I/O (the fold keeps it as `Defer`) |
| P34 | `w/markers/handler.go:156-174`; `segmenting/apply.go:82-198`; `pkg/segments/merge.go:38` | classify Found/NotFound/Error, carry `notFoundSince`, merge sources | Fold (the loop's markers planner) |

**Import: agent `import`** (47)

| # | Site | Decision today | Disposition |
|---|---|---|---|
| P35 | `imp/rescan/match.go:161` (`MatchMovie`) | attribute a file to a Movie (tmdb, imdb by RPC, unique title+year) | Stay (computation): reported as the observation's proposed attribution (§7.6) |
| P36 | `imp/rescan/mediafile.go:217-249,393` | create a Movie | Move |
| P37 | `imp/rescan/mediafile.go:220`; `series.go:230` | refuse an item an ImportExclusion blocks | Move |
| P38 | `imp/rescan/mediafile.go:228`; `series.go:240` | no default profile → unmatched | Move |
| P39 | `imp/rescan/series.go:87,229,267` | attribute to a Series; create it from a `{tvdb}` folder | Split: attribution stays, creation moves |
| P40 | `imp/rescan/nonvideo.go:193-512` | non-video attribution (never creates) | Stay (computation) |
| P41 | `imp/rescan/nonvideo_scan.go:283` | refuse to re-point a MediaFile to another item | Move (the scan applier's rule) |
| P42 | `imp/rescan/mediafile.go:321,355` | skip an unchanged fingerprint; hand a post-transcode change to catalogarr | Split: the diff stays (only changes are reported); the hand-over is the manager's |
| P43 | `imp/rescan/worker.go:725` | suspected sample → unmatched | Split: the size classification stays; the verdict moves |
| P44 | `imp/rescan/worker.go:654`; `keptoutput.go:118` | do not adopt a transcode's output | Split: the agent reports the profile tag and output relation; the manager decides |
| P45 | `imp/rescan/manual.go:72,228` | validate a manual import target and bypass matching | Move |
| P46 | `imp/rescan/prune.go:51,88` | delete a MediaFile missing 10 min and `ENOENT` | Move: the agent reports the `Lstat` fact (§7.6) |
| P47 | `imp/rescan/orphanpart.go:49,59` | remove `.part` files of dead transcode jobs (72 h) | Split: the manager decides from `status.transcode`; the agent removes on a task |
| P48 | `imp/rescan/missingfolder.go:42` | a missing subpath ends the scan, or refuses a manual one | Move |
| P49 | `imp/rescan/mediafile.go:425` (`freshVideoSpec`) | freeze the TRaSH score and fallback language | Stay (computation) |
| P50 | `imp/rescan/renamepass.go:71` | rename during the scan | Retire: the loop's rename actuator |
| P51 | `imp/fileimport/worker.go:262` | import only Completed or Seeding | Move: the manager issues the inspect only on `Completed` |
| P52 | `imp/fileimport/worker.go:276-285` | redirect the target by annotation; override is manual | Move: the import intent (§6.10) |
| P53 | `imp/fileimport/worker.go:303` | a container kind → `needsPerson` | Move |
| P54 | `imp/fileimport/worker.go:387` | which quality profile | Move: in the task |
| P55 | `imp/fileimport/process.go:97,219` | suspected-sample handling | Split, as P43 |
| P56 | `imp/fileimport/process.go:158`; `order.go:91,103` | best file per item | Move (§6.9) |
| P57 | `imp/fileimport/process.go:289-297` | probe-corrected quality not allowed → `releaseFault` | Move: the probe is reported |
| P58 | `imp/fileimport/process.go:321`; `transcoded.go:32,53` | `TranscodedFinal` refusal | Move |
| P59 | `imp/fileimport/process.go:328-350`; `wronglanguage.go:33`; `remediation.go:80` | upgrade or replace each existing file; wrong-language override | Move |
| P60 | `imp/fileimport/process.go:365` | free space below the floor → abort | Stay (mechanism): a guard at execution, reported as `diskFull` |
| P61 | `imp/fileimport/process.go:423-441` | recycle every old file and delete their MediaFiles | Split: the approved plan names them; the agent recycles; the manager deletes |
| P62 | `imp/fileimport/place.go:60` | containment, recycle-link an existing destination, refuse an overwrite | Stay (mechanism): a guard at execution |
| P63 | `imp/fileimport/episode_import.go:211,264,285` | scene names; per-episode fill or upgrade; a manual named-episode override | Split: the mapping stays; the fill, upgrade and override move |
| P64 | `imp/fileimport/nonvideo.go:309` | a multi-file item with existing files → `needsPerson` | Move |
| P65 | `imp/fileimport/nonvideo.go:238` | all-or-nothing album or audiobook replacement | Move |
| P66 | `imp/fileimport/dedup.go:113` | exclude the import's own earlier placement | Move: the plan is per entry uid |
| P67 | `imp/fileimport/remediation.go:118,130` | class precedence | Move |
| P68 | `imp/fileimport/remediation.go:184-237` | retry, hold or fail, tied to the JetStream delivery count | Move: attempts and `nextAttemptAt` in the entry (§6.9) |
| P69 | `imp/fileimport/donor.go:71,207,232,270` | donor language check; AudioGraft; remove the old donor | Split: the check moves; the AudioGraft is Fold; the removal is an execute step |
| P70 | `imp/fileimport/recyclebin.go:132,182,214` | longest retention; 0 disables; refuse a bin holding a library | Stay (mechanism): safety guards of the sweep; its cadence is the manager's (`recyclesweep`) |
| P71 | `imp/probe/worker.go:101,130` | skip a superseded probe; refuse a path outside `/data` | Stay (mechanism): the records protocol |
| P72 | `imp/importlist/sync.go:98,127` | refuse a kind the list cannot yield | Move: the ImportList controller |
| P73 | `pkg/importlist/sync.go:154` (`Dedupe`) | dedupe listed items | Move, with the diff |
| P74 | `imp/importlist/sync.go:137,139` | drop excluded entries (fails open) | Move |
| P75 | `imp/importlist/sync.go:160,191` | `automaticAdd` off → listed only | Move |
| P76 | `imp/importlist/sync.go:212`; `catalogitem.go:127` | leave an item another source added | Move |
| P77 | `imp/importlist/sync.go:182,440` | an unresolvable id is skipped but kept known | Split: resolution (metadata RPC) stays; keep-known moves |
| P78 | `imp/importlist/catalogitem.go:166,209,271` | create or update a Movie or Series from defaults, keeping the anime classification | Move |
| P79 | `imp/importlist/sync.go:251`; `pkg/importlist/sync.go:176` | what falls off the list: log, unmonitor, remove, remove and delete | Move |
| P80 | `imp/importlist/sync.go:270` | keep an item another list still has | Move |
| P81 | `imp/importlist/sync.go:375`; `delete.go:70,127` | delete items; recycle their files | Split: the manager deletes; the agent recycles on a task |

**Index: agent `index`** (18)

| # | Site | Decision today | Disposition |
|---|---|---|---|
| P82 | `idx/search/select.go:80` | which indexers a query goes to (enabled, mode, protocol, health, caps, category) | Stay (computation) over manager-decided inputs: `enabled` and `disabledUntil` are the manager's (P87), the protocols and mode come in the task; caps live in the agent's client cache |
| P83 | `idx/search/query.go:140,186`; `select.go:141` | id or text query; skip an indexer with no id parameter; anime categories | Stay (computation) |
| P84 | `idx/search/fanout.go:105,122,131` | budgets and per-query timeout | Stay (mechanism) |
| P85 | `idx/search/fanout.go:342,589`; `limits/ring.go:164,349` | reserve a query in the limit window (fails open) | Stay (mechanism): a limiter whose limits are Indexer spec |
| P86 | `idx/search/fanout.go:352,362,406` | `WaitTurn`; a missed turn is `paced`, not a failure | Stay (mechanism): the per-host limiter (CLAUDE.md, 2026-10-06) |
| P87 | `idx/search/fanout.go:416,637`; `status/health.go:32,41,131,167` | `RecordFailure`/`RecordSuccess`: the escalation ladder and `disabledUntil` | Move: the Indexer controller; the agent reports outcomes and suppresses locally for at most 30 s (§7.5) |
| P88 | `idx/search/fanout.go:165` | classify a failure (timeout, rate-limited, error) | Stay (computation) |
| P89 | `idx/search/fanout.go:446`; `worker/rss/seeders.go:42` | drop torrents below `minimumSeeders` | Stay (computation): a declared filter |
| P90 | `idx/search/merge.go:49,70` | dedupe; the winner by priority, then seeders | Stay (computation) |
| P91 | `idx/worker/rss/worker.go:245,254,276,355,367`; `idx/rssschedule/schedule.go:63,126` | when to poll next; skip disabled; reschedule at `disabledUntil` or the limit | Move: the Indexer controller schedules each poll (it already seeds `NextPollAt`, `controller/indexer/controller.go:471-475`) |
| P92 | `idx/worker/rss/worker.go:417,454` | at most 4 pages; stop at `since` | Stay (mechanism) |
| P93 | `idx/worker/rss/worker.go:515,537,559,578` | what the firehose carries; what relindex stores | Stay (computation): the index agent's own store |
| P94 | `idx/agent/sweeper.go:35,86` | relindex retention 72 h | Stay (mechanism): the agent's store |
| P95 | `idx/download/service.go:256,280`; `limits/ring.go:174,197` | refuse a payload fetch at the grab limit; return an unused slot | Stay (mechanism); the grab planner reads the same ring before it chooses |
| P96 | `idx/download/service.go:305` | reject a 401, 403 or HTML response; redirect an oversize one | Stay (mechanism) |
| P97 | `idx/clientcache.go:352`; `clients/cardigann.go:360` | relogin on an expired session; save or drop it | Stay (mechanism): inside the request, with the engine's session; the manager mirrors the Secret |
| P98 | `idx/clientcache.go:167` | floor `requestDelay` by the definition | Stay (mechanism) |
| P99 | `idx/facade/search.go:101` | every Torznab call is user-invoked | Stay (mechanism) |

**Caption: agent `caption`** (11)

| # | Site | Decision today | Disposition |
|---|---|---|---|
| P100 | `cap/profile.go:45,69` | which SubtitleProfile | Fold (deleted; the task carries the profile, loop spec §2.13) |
| P101 | `cap/worker.go:308` | probe hash drift → superseded | Stay (mechanism): the records protocol |
| P102 | `cap/worker.go:335`; `providerset.go:157` | provider order from the profile | Stay (mechanism): executes the task's profile |
| P103 | `cap/worker.go:348,442` | eligible providers | Stay (computation) |
| P104 | `cap/worker.go:401` | the minimum score | Fold (the margin comes from the task) |
| P105 | `cap/search.go:173,214,311` | local tier first; pool ranking; bench a failing provider | Stay (mechanism) |
| P106 | `cap/search.go:230,379`; `app/caption/throttle/bucket.go:94` | skip throttled providers; token buckets | Stay (mechanism): the shared KV throttle |
| P107 | `cap/search.go:394`; `throttle/state.go:66,74,231`; `pkg/subtitles/throttle.go:152` | throttle durations; five errors in 120 s → a 1 h floor | Stay (mechanism) |
| P108 | `cap/select.go:160,308` | filter and score candidates | Stay (computation) |
| P109 | `cap/apply.go:69,129,341`; `worker.go:75` | download the best; downloaded or upgradable; remove the replaced sidecar | Stay, **an override of the rule of thumb**: choosing and downloading are one provider session against a throttle that refused 274 of the last 283 searches (loop spec §7.1); a choose-then-download round trip would spend a second request per subtitle. The task bounds the choice (languages, minimum score, upgrade margin) and the loop derives the state |
| P110 | `cap/apply.go:243,258` | give up at the last delivery → record `failed` | Stay (mechanism): the fold's last-delivery rule; what `failed` means is the loop's |

**Engines: agents `torrent-engine`, `usenet-engine`** (13)

| # | Site | Decision today | Disposition |
|---|---|---|---|
| P111 | `eng/torrent/engine.go:38`; `dl/torrent/session.go:149,319` | a stall is a failure (`stalled`) | Move: the manager judges `lastProgressAt` against `stallTimeout` |
| P112 | `dl/torrent/session.go:142,355` | the seed goal is met; stop uploading | Stay (computation and mechanism), against the command's effective criteria; removal is P113 |
| P113 | `eng/torrent/reconciler.go:355,520,539,571` | seed-criteria precedence; remove when imported, `removeOnImport` and `removeCompleted` | Move (§6.4); the command carries the effective criteria |
| P114 | `eng/torrent/reconciler.go:409,447` | remove data on delete | Move: the command's `removeData` |
| P115 | `eng/torrent/reconciler.go:228` | a payload whose hash mismatches fails | Split: the engine reports `payloadMismatch`; `Blocklisted` is the manager's |
| P116 | `eng/torrent/reconciler.go:196-211` | requeue an add error; fetch everything on a selection error | Stay (mechanism): a nak with delay; the advisory reaches the manager (§8) |
| P117 | `eng/torrent/selection.go:75,180` | which files to fetch (season and episode, absolute, scene, airdate) | Stay (computation) over the command's selection |
| P118 | `dl/usenet/segment.go:429,621`; `nzb.go:298`; `client.go:~1002` | health below the floor or hopeless: pause or delete | Split: the pause stays as a mechanism (it stops spending quota at once); a delete is reported as `missingArticles`; the verdict is the manager's |
| P119 | `dl/usenet/segment.go:313`; `pool.go:147,151,322,367` | retries, penalties, backup order | Stay (mechanism) |
| P120 | `dl/usenet/segment.go:658`; `client.go:913`; `unpack.go:51` | stall, encryption or disk full → a failure reason | Split: facts stay; the verdict moves |
| P121 | `dl/usenet/priority.go:35,66` | a lower priority waits while a higher one transfers | Stay (mechanism) over the command's priority |
| P122 | `eng/usenet/engine.go:301,356` | keep health-paused jobs paused; remove after import | Split: the pause stays; the removal moves |
| P123 | `eng/torrent/reaper.go:57,255`; `eng/engine.go:118`; `usenet/reaper.go:251-285` | reap transfers no Download names after 10 min | Move: orphans are reported, removed only by intent (§6.7) |

**Markers: `cmd/markers`** (5)

| # | Site | Decision today | Disposition |
|---|---|---|---|
| P124 | `app/segments/worker/worker.go:126` | an existing record for the probe hash and version wins over the task | Stay (mechanism): input fencing (loop spec §4.2 item 8) |
| P125 | `worker.go:155` | amend an intro the season revealed | Stay (computation) |
| P126 | `worker.go:251,288,328` | chapters, then theme, then frame statistics; OCR when confidence is under 70 | Stay (computation) |
| P127 | `worker.go:165`; `cmd/markers/main.go:243` | discard an abandoned decode; restart the pod | Stay (mechanism) |
| P128 | `worker.go:363` | an error over an error moves `analyzedAt` | Fold: the markers planner's `Due` reads it |

**Transcode: `cmd/transcode`** (8)

| # | Site | Decision today | Disposition |
|---|---|---|---|
| P129 | `app/squash/worker/serve.go:245`; `app/squash/task/limits.go:114` | take no work until the device measures; publish limits and `healthy` | Stay (mechanism and report): the manager routes by it (§5) |
| P130 | `app/squash/worker/lease.go:54`; `serve.go:~364` | claim the task lease; a newer attempt wins | Stay (mechanism) |
| P131 | `app/squash/worker/run.go:289,318,337,541` | refuse an output outside the root, a changed source, an unknown HDR, a foreign file | Stay (mechanism): guards, answered as failures the loop judges |
| P132 | `app/squash/worker/run.go:384` | an output above `maxOutputToSourcePercent` fails verification | Stay (mechanism) against the task's limit |
| P133 | `app/squash/worker/ffgo.go:96,104,119` | refuse a task planned for another container or dynamic range | Stay (mechanism) |
| P134 | `app/squash/worker/ffgo.go:322,392`; `run.go:212,217` | report a GPU that cannot encode | Stay (report); `fallbackReason` is already the manager's (`transcodejob/decide.go:151`) |
| P135 | `app/squash/worker/run.go:415,515` | retire the source by `replaceSource` and `recycleBin` | Stay (mechanism) by the task's policy |
| P136 | `app/squash/worker/graft/graft.go:102,220,226` | alignment thresholds; the donor reduce | Stay (computation) |

**Count:** 136 decisions. **Move 51, Split 19, Stay 56** (computation 19, mechanism 34, and
P22's pre-filter, P109's override and P134's report), **Retire 3, Fold 5, Manager 2.** By
domain: catalog 34, import 47, index 18, caption 11, engines 13, markers 5, transcode 8.

The one override of the rule of thumb is P109 (a subtitle is chosen in the session that
downloads it). P22's unmonitored pre-filter and P82's capability match stay in agents because
the manager re-checks or decides their inputs.

---

## 4. The manager is the control plane

### 4.1 Every lifecycle is a state machine in the manager

Each row is one reconcile key, run only on the holder of `manager.clustarr.io`. Its state is
persisted in the resource's status; its tasks are effects of its transitions; its inputs are
the cache, records and intake messages.

| Resource (key) | State (status) | Tasks it issues (effects) | Answers it reads |
|---|---|---|---|
| MediaFile (loop file key) | the fold's blocks: probe, naming, subtitles, transcode, graft, markers | probe, subtitle fetch, transcode, graft Job, TheIntroDB, segment plan | `clustarr-probes`, `-subtitles`, `-transcodes`, `-grafts`, `-markers`, `-segments` (loop spec §4) |
| Movie, Album, Book, Audiobook (loop item keys) | the fold's rollups; **`downloads`, `blocklist`, `downloadPhase`, `downloadNonces`** (`catalogarr`); **`pendingGrab`, `searchDispatch`, attempts** (`catalogarr-grab`); `metadata`, `artwork` (`catalogarr-metadata`); `overlay` on Movie (`catalogarr-artwork`) | search, engine commands, import inspect and execute, metadata, artwork fetch, overlay render (Movie) | `clustarr-searches`, `-transfers`, `-imports`, `-item-metadata`; artwork objects; candidates (intake) |
| Series, Comic (loop item keys, **new**) | as above, for every episode or issue grab; plus the Series and Comic reconcilers' own state | as above, plus Episode and Issue creation (today's) | as above |
| Episode, Issue (loop item keys) | the fold's rollups; `activeDownloadRef`, `downloadPhase` from the container; search state | search | `clustarr-searches` |
| Artist, Author | `metadata`, `artwork` | metadata, artwork fetch | `clustarr-item-metadata`; artwork objects |
| Search | `results`, `indexerOutcomes`, `finishedAt` (`catalogarr-worker`); a pick goes to the owner as a candidate | interactive search | `clustarr-searches` |
| LibraryScan | phase, counters, `unmatched` (today's, `importarr`) | scan | observations (intake); the tally in `clustarr-progress` |
| ImportList | sync status, `auth` (`importarr`) | list sync; recycle for `removeAndDelete` | `clustarr-importlist` snapshot |
| Indexer | controller fields (`indexarr`); `WorkerFields` (`indexarr-worker`); the session Secret | caps probe and login (manager-side today), **RSS poll** | `clustarr-indexer-health`; `clustarr-indexer-sessions` |
| DownloadClient | engines, counts, `EngineReady`, `DiskSpaceOK`, **`ProxyUDPUnavailable`, `OrphansPresent`** (`grabarr`) | engine workloads; per-engine durables; orphan removal | `clustarr-engines` |
| TranscodeProfile, SubtitleProfile | the fold's slim status | pool Jobs | `clustarr-progress` encoder limits |

### 4.2 Agents are executors

An agent:

1. takes a task from its durable;
2. checks the task is still current (`Superseded`, against its record or the CR it reads), and
   drops it when it is not;
3. does the work: I/O, computation, bytes on disk;
4. reports: a record write, or an intake message, and nothing else;
5. acks, or naks with a delay when it cannot work yet, or terms a task it can never do.

It makes no choice that changes desired state. The decision audit (§3.5) moves 51 decisions
and splits 19; the rest stay in agents as computation or as mechanism inside a task (limiters,
retries, guards, safety stops), each with its reason. The rule for a new agent-side branch:
if its outcome could differ from what the manager would decide with the same facts, it is
policy and belongs in the manager; the agent reports the facts.

Two kinds of agent output are not answers to tasks, and both now go only to the manager:
**candidates** (the RSS matcher's scored match of a firehose release to a wanted item) and
**observations** (a scan's file facts). The firehose itself (`CLUSTARR_RELEASES`, index agent
to RSS matcher) stays agent to agent, because it is data, not a task: nothing on it changes
state until the manager accepts a candidate. Every other agent-to-agent path today
(`rssmatcher → grab`, `search → grab`, `wantedscan → search`, `redownload → search`,
`gateway → render`, the RSS poll's self-chaining, the TheIntroDB worker's self-reschedule)
becomes a manager decision (§5.1).

### 4.3 What agents send back, and the NATS state it needs

**Records** (one writer per key, compare-and-swap, `pkg/records`). Every bucket below shares
the fold's rules: `Durable: true`, `History: 1`, `Records: true`, `LimitMarkerTTL: 5m`,
`DiscardNew`, TTL 7 days unless stated (loop spec §4.3). The header gains one field:
`schema.RecordHeader.Item *schema.ItemRef` (`kind`, `namespace`, `name`, `uid`; JSON `item`),
set instead of `mediaFile` by item-keyed records; `recordsource.New` maps either to its key.

| Bucket (`pkg/events` constant) | Key | Writer | MaxBytes / MaxValueSize | Read by |
|---|---|---|---|---|
| `clustarr-transfers` (`BucketTransfers`) | `RecordKey(entry uid)` | the engine holding the transfer | 256 MiB / 512 KiB | owner key |
| `clustarr-engines` (`BucketEngines`) | `RecordSubKey(DownloadClient uid, ordinal)` | that engine pod | 8 MiB / 16 KiB | DownloadClient controller |
| `clustarr-imports` (`BucketImports`) | `RecordSubKey(entry uid, "inspect" \| "execute")` | the import agent | 128 MiB / 512 KiB | owner key |
| `clustarr-searches` (`BucketSearches`) | `RecordKey(search task uid)` | the search agent | 128 MiB / 512 KiB | Search controller; item key |
| `clustarr-item-metadata` (`BucketItemMetadata`) | `RecordKey(item uid)` | the metadata gateway | 512 MiB / 512 KiB | item key; Artist and Author reconcilers |
| `clustarr-indexer-health` (`BucketIndexerHealth`) | `RecordKey(indexer uid)` | the index agent | 8 MiB / 8 KiB | Indexer controller |

**Intake** (proposals and observations the manager did not request):
`CLUSTARR_INTAKE`, subjects `clustarr.intake.>`, WorkQueue, `Durable: true` (file-backed at
full size under `ForSingleNode`: the 2026-10-01 discard is the case this avoids), MaxBytes
256 MiB, `DiscardNew` (a full intake refuses and the publisher retries; nothing is evicted),
`Duplicates: 1h`.

| Subject | Payload | Publisher | Durable (manager, leader-only) |
|---|---|---|---|
| `clustarr.intake.candidate.<owner kind>.<KVKeyToken(owner uid)>` | `schema.Candidate` (`catalog.Candidate.v1`): the release, its source, score, quality, languages, rejections, the matched targets, the origin (RSS, a Search pick) | RSS matcher; the Search controller | `catalogarr-intake-candidate` |
| `clustarr.intake.scan.<ns>.<KVKeyToken(scan uid)>` | `schema.ScanObservation` (`import.ScanObservation.v1`, §7.6) | rescan | `importarr-intake-scan` |

**Commands** (the manager to one engine): `CLUSTARR_WORK_ENGINE`, subjects
`clustarr.work.engine.>`, WorkQueue, `Durable: true`, 64 MiB, `DiscardNew`, `Duplicates: 1h`;
one durable per engine instance (§6.7).

**Existing state, unchanged:** `clustarr-importlist` (gains the snapshot, §7.6),
`clustarr-indexer-sessions`, `clustarr-progress` (gains `agent.<domain>.<pod>` presence,
§5.3), `clustarr-artwork`. **Retired:** `clustarr-leases`, `clustarr-pending` (§6.6), the
`catalogarr-grab` and `catalogarr-redownload` durables (after a drain, through
`Topology.Retired`).

**Single-node fit.** New file reservations: buckets 1,040 MiB, intake 256 MiB, commands
64 MiB, about 1.33 GiB. With the fold's ≈ 7.1 GiB (loop spec §4.3) that is ≈ 8.4 GiB of the
20 GiB `max_file_store` (`config/nats/configmap.yaml:16`). The one new memory-backed stream is
`CLUSTARR_TASK_EVENTS` (§8.2, 8 MiB before scaling), which takes its share of the 64 MiB
single-node budget at the 1 MiB floor like any non-Durable stream. `TestEnsureSingleNodeTopologyFitsTheKindServersLimits`
(`pkg/events/natsbus/singlenode_limits_test.go:74`) runs its server at 10 GiB of file store;
F1's extension to 20 GiB (loop spec §4.15) must land first, or the sum passes 10 GiB once the
transcode and probe buckets are counted at full size.

### 4.4 What moving the decisions costs the manager

- **Linkage.** None of the moved decisions needs ffgo, purego, onnx, anacrolix,
  `pkg/download/usenet` or `pkg/relindex`. The manager gains `app/catalog/delay` and
  `pkg/metadata/scenemap`, and the planners this design names (`app/catalog/grabplan`,
  `app/grab/lifecycle`, `app/import/importplan`, `app/import/manager/scanapply`). The
  `go list -deps` guards of split §4.5.1 keep their lists; A7 adds the planners to the
  manager's allow-list and asserts the agent roots do not link them (§9.2).
- **CPU.** The manager runs only item-state rules (comparisons over parsed facts), never
  regexp2 scoring: release evaluation stays in the search agent and the RSS matcher. The grab
  planner's input is at most 200 ranked candidates per search answer, usually a handful.
- **Blocking.** No moved decision makes an RPC or touches a provider inside a reconcile. The
  import planner's `/data` facts arrive in the inspect record; the only `/data` work the
  manager adds is `fsops.SafeRemove` of a removed grab's payload, through the loop's I/O
  executor (loop spec §3.17), as the Download controller does today.
- **Throughput.** A full library scan or a large list arrives as thousands of intake messages;
  the appliers take the loop's status-write limiter (`--remediation-bulk-writes-per-second`,
  25/s) for creates and deletes, so a 12,000-file first scan writes in about 8 minutes instead
  of as fast as one import pod can apply (the kind control plane shares one disk with etcd,
  CLAUDE.md, 2026-09-24).

---

## 5. Routing and capacity

### 5.1 The routing table

Every task kind after this design, who decides it and where it goes. "Selector" is what the
manager chooses at dispatch and records in the CR's dispatch block (`Dispatch.destination`,
§8.3). "Budget" is the admission bound of §5.4.

| Task | Decided by (key) | Stream; subject | Durable (domain) | Selector | Capacity known from | Budget |
|---|---|---|---|---|---|---|
| interactive search | Search | `CLUSTARR_WORK_CATALOGARR`; `…search.high.<mediaKey>` | `catalogarr-search-high` (catalog) | lane | ConsumerState; presence | 2 × MAP (16) |
| automatic search | item key's search planner (was `wantedscan` and `redownload`) | `…search.normal\|low.<mediaKey>` | `catalogarr-search-normal` (catalog) | lane, `IndexOnly`, protocols, indexer priorities | ConsumerState; Pacer `search` | Pacer `search`, then 2 × MAP |
| metadata | item key; Artist, Author | `…metadata.<lane>.<mediaKey>` | `catalogarr-metadata` (metadata) | lane | ConsumerState | 2 × MAP |
| artwork fetch | item key (the plan, P29) | `…artwork.fetch.<mediaKey>` | `catalogarr-artwork-fetch` (metadata) | URLs in the task | ConsumerState | 2 × MAP |
| overlay render | item key (P27; was the gateway's follow-up) | `…artwork.render.<mediaKey>` | `catalogarr-artwork-render` (catalog) | the plan's digest | ConsumerState | 2 × MAP |
| probe | loop file key (fold) | `CLUSTARR_WORK_PROBE`; `…probe.file.{high,low}.<uid>` | `importarr-probe-high\|low` (import) | lane | ConsumerState; Pacer `probe/low` | Pacer, then 2 × MAP |
| subtitle fetch | loop file key (fold) | `CLUSTARR_WORK_CAPTIONARR`; `…fetch.<lane>.<uid>` | `captionarr-fetch-high\|normal` (caption) | lane | ConsumerState; the KV throttle | Pacer `subtitle` and the fold's backlog gate |
| TheIntroDB markers | loop file key (fold) | `CLUSTARR_WORK_SEGMENTARR`; `…markers.<lane>.<uid>` | `catalogarr-markers` (metadata) | | ConsumerState | Pacer `markers` |
| segment plan, analyze | loop; `segmentplan` (manager) | `…segmentarr.plan\|analyze.*` | `catalogarr-segments-plan` (manager), `segmentarr-analyze` (markers) | | ConsumerState | 2 × MAP |
| transcode | admission ledger (fold) | `CLUSTARR_WORK_SQUASHARR`; `…transcode.task.<profile uid>.<class>.<uid>` | `squasharr-transcode-<puid>-<class>` (pool Job) | profile, **hardware class**, node through the pool's affinity | encoder limits and health (`clustarr-progress` `encoder-limits.<class>`); GPU nodes; `--slots` | the ledger's slots and window (loop spec §5) |
| graft | admission ledger (fold) | a Job per graft | — | node through the pool template | graft slots | `--graft-concurrency` |
| scan | LibraryScan | `CLUSTARR_WORK_IMPORTARR`; `…scan.<uid>` | `importarr-scan` (import) | | ConsumerState | one open scan per RootFolder (today's) |
| import inspect, execute | owner key (§6.9) | `…fileimport.{inspect,execute}.<entry uid>` | `importarr-fileimport` (import) | | ConsumerState | 2 × MAP |
| list sync | ImportList | `…list.<uid>` | `importarr-list` (import) | | ConsumerState | one per list |
| recycle sweep, file removal | `recyclesweep`; ImportList; loop (orphan parts) | `…recycle.{sweep,files}.<id>` | `importarr-recycle` (import) | | ConsumerState | 2 × MAP |
| RSS poll | **Indexer** (P91; was the agent's self-chain) | `CLUSTARR_WORK_INDEXARR`; `…rss.<uid>` | `indexarr-rss` (index) | | ConsumerState | one per indexer |
| engine command | owner key (§6.7) | `CLUSTARR_WORK_ENGINE`; `…engine.<client>.<ordinal>.<entry uid>` | `grabarr-engine-<client>-<ordinal>` (engine instance) | **DownloadClient**, **ordinal** | engine records; `EngineReady` | MAP 4 per engine; the engine queues past `maxActive` |

Choosing a **DownloadClient**: today's `pickClient` (`app/grab/controller/download/controller.go:666`:
same protocol, enabled, lowest `spec.priority`, then name), with one addition: when the
release's Indexer names `spec.downloadClientRef` (`api/index/v1alpha1/indexer_types.go:192`,
which nothing reads today), that client wins if it is enabled and of the right protocol
(Sonarr's per-indexer client). Choosing the **ordinal**: the Ready instance with the fewest
`active + queued` in its engine record, ties broken by today's `HashOrdinal(replicas,
infoHash, guid)` (`pkg/names/names.go:145`); a usenet client has one instance. Both are pinned
in the entry once, as `status.engine` is today.

### 5.2 What a destination is

A destination is a durable, and through it a domain (`pkg/agentdomain.Domains()`), a
Deployment, a pool Job or an engine instance. The manager never addresses a pod. Node binding
is the destination's: a transcode pool's class selects GPU nodes through `applyHardware`
(`app/squash/controller/pool/template.go:408`), an engine instance is a StatefulSet ordinal on
whatever node holds its pod. A task whose destination needs a node that does not exist (no
labelled GPU node, `gpuNodes`, `capacity.go:43`) is not admitted; the fold's
`rerouteUnschedulable` (`pools.go:216`) and `ChooseClass` (`class.go:51`) already do this for
transcode, and the engine choice does the same for engines that are not Ready.

### 5.3 What the manager knows about capacity and capability

| Source | Gives | Freshness |
|---|---|---|
| `StreamAdmin.ConsumerState` (`pkg/events/consumerstate.go:31-50`) through the autoscale `StateCache` | per durable: `Pending`, `AckPending`, `Waiting` (open pulls: the only sign a consumer is alive and idle), `MaxAckPending` | 5 s cache, leader-answered (W4.100) |
| **agent presence**, new: `clustarr-progress` key `agent.<domain>.<KVKeyToken(pod)>` | `domain`, `pod`, `node`, `version`, `slots` per durable, bound durables, capabilities (`ffgo` and its FFmpeg version, configured providers, `/data` mounted), `at` | written at start and every 30 s; the bucket's 10 min TTL retires it |
| encoder limits and health (`app/squash/task/limits.go:114`, `encoder-limits.<class>`) | per class and node: limits, NVDEC, healthy | republished within 10 min |
| engine records (§6.7) | per engine instance: ready, counts, free bytes, proxy UDP, orphans | 60 s |
| Kubernetes | Deployment and StatefulSet ready replicas, HPA state, Node labels and allocatable, pool Job state | informers |
| the manager's own dispatch ledger (§5.4) | per durable: tasks it published and not yet seen answered | exact, leader-local |

The presence key follows the encoder-limits precedent (per-pod reports in `clustarr-progress`)
and is not a records bucket: it is liveness telemetry, rebuilt within 30 s of any loss.
`agent --self-check` already proves the capabilities it lists (split §10.1.1); presence
reports them while running.

### 5.4 Admission: the manager queues only what the destination can take

**The dispatch ledger** (`app/dispatch`, leader-local, the generalisation of the fold's
transcode ledger for every other task kind): per durable, the set of dispatches published and
not yet answered, keyed by (CR key, seq). It is rebuilt at leader start from the CRs'
dispatch blocks (`seq > answeredSeq`) through the cache, never trusted over status, exactly as
the fold's admission ledger is (loop spec §5.6).

**The rule:** a planner that wants to dispatch asks `dispatch.Admit(durable)`. Admission
holds while `ledger.outstanding(durable) < Budget(durable)`, where

- `Budget = 2 × MaxAckPending` for autoscaled and fixed domains: one full fleet in flight and
  one queued behind it;
- the transcode ledger's slots and window for pools (unchanged, fold §5);
- one per key for the per-object tasks (a scan per RootFolder, a sync per list, a poll per
  indexer).

Above it, `Admit` returns the reason and an estimate, the planner leaves its block in a
waiting state with that reason (`Dispatch.delivery.state: waiting`,
`message: "waiting for catalogarr-search-normal: 16 in flight"`), and the key requeues at its
priority (loop spec §3.11) when the ledger frees a slot. Nothing is published, so nothing can
be discarded: a memory work stream on a single node never holds more than `2 × MAP` messages
of one durable, far below its scaled size, which closes the 2026-10-01 class (12,161
discarded messages) for every dispatched task kind.

**Pacing stays the Pacer's** (`pkg/records/pacer.go`): per-key reservations at a class rate,
applied before admission, for work that must be spread in time rather than bounded in flight.
Classes: `probe/low` 600/min, `subtitle` 120/min, `markers` 120/min (the fold's), plus
`search` (new): live automatic searches, default 20/min per namespace
(`--search-live-per-minute`), so the 2026-10-06 burst (104 searches in one second onto one
indexer's 2 s `requestDelay`) cannot recur from the manager; index-only searches are not
paced.

**HPA on consumer lag still works** (split §9). The metric stays
`clustarr_consumer_lag = Pending + AckPending` per durable (research
`nats-hpa-metrics.md`). Admission keeps lag up to `2 × MaxAckPending` rather than letting it
grow without bound, and the HPA's per-pod target is the per-pod slot count, so the desired
replicas `lag / slots` still reach `MaxAckPending / slots`, the ceiling: a backlog scales the
domain to its maximum exactly as before, and a single admitted task (lag 1) still scales a
domain from zero. The QueueGauge additionally exports
`clustarr_dispatch_waiting{consumer}`, the manager's not-yet-admitted backlog, for dashboards
and alerts; it is not an HPA metric, because the HPA must see only work an agent can take.

### 5.5 When no agent of a kind exists

| Case | What happens | What shows |
|---|---|---|
| an autoscaled domain at zero | admission publishes up to its budget; lag wakes the HPA (`render.go:85`; the wake guard `reconciler.go:136`) | the CR's dispatch reads `published`; `Waiting = 0` until a pod pulls |
| a fixed domain down (metadata, index) or a consumer with no live agent | admission publishes up to its budget, then holds. A durable with `Pending > 0`, `Waiting = 0` and no ack for 2 minutes, or no presence key for the domain, is **unattended** | each waiting CR's dispatch: `state: waiting`, reason `NoAgent`, naming the durable; `clustarr_dispatch_unattended{consumer}` = 1; one Warning Event per durable on the manager's Pod per transition |
| an engine instance not Ready | the entry stays `Pending` (never `Assigned`); after R-6's 10 minutes in `Removing`, §6.8 | `entry.message` names the engine; DownloadClient `EngineReady=False` |
| no GPU node, or every GPU class unhealthy | the fold's rules (`gpuOnly` waits; otherwise cpu with `fallbackReason`) | `status.transcode` as the fold has it |
| an agent that cannot do a task kind (a capability missing from presence, e.g. no FFmpeg 9) | the destination is not admitted; the task waits | reason `NoCapableAgent` |

A task is never dropped for want of an agent: it stays a pending state on its CR, and the
CR's next pass after capacity returns admits it.

### 5.6 What routing changes in today's code

- **Gone:** every agent-side publish of a follow-up task (§4.2): `decide.go:196`,
  `handler.go:217` (scheduled grabs), `wantedscan.go:242`, `redownload/handler.go:455`,
  `metadata/artwork/handler.go:253,256` (render after fetch),
  `worker/rss/worker.go:257,288,355` (poll chaining), `worker/markers/handler.go:119` (the
  fold's markers planner owns the reset deferral as `deferredUntil`).
- **New in the manager:** the `search` Pacer class, `app/dispatch` (the ledger and `Admit`),
  the presence reader, the engine-instance chooser, the RSS poll scheduler in the Indexer
  controller (it seeds `NextPollAt` today, `controller/indexer/controller.go:471-475`).
- **Unchanged:** the transcode router (fold §5), the probe lanes, the caption lanes and
  throttle, the per-host indexer limiter inside the index agent.

---

## 6. Downloads: the grab state machine on the item

The Download kind goes. A grab is an entry in its owner's status, and its whole life (chosen,
admitted to an engine, transferring, importing, seeding, removed) is a state machine the
owner's item key runs in the remediation loop. This section says where every Download field
goes, how the state machine runs, what engines do, and how the readers change.

### 6.1 Who owns a grab

| Grab of | Owner (holds `status.downloads[]`) | Loop key | Why |
|---|---|---|---|
| a movie | the Movie | `KindMovie` (F4.2) | one release is one movie |
| one episode, or a pack of episodes | **the Series** | **`KindSeries` (new, A3)** | a pack is one transfer for many Episodes, and today's pack Download is already owned by the Series (`perform.go:276,318-319`); one owner for both shapes means one finalizer, one blocklist, one candidate comparison and one place a single-episode grab and a later pack covering it meet |
| an album | the Album | `KindAlbum` (F4.3) | Lidarr grabs per album |
| a book, an audiobook | the Book, the Audiobook | `KindBook`, `KindAudiobook` (F4.3) | one release each |
| one issue, or a pack of issues | **the Comic** | **`KindComic` (new, A3)** | as Series; an Issue pack today names keys (`api/common/v1alpha1/media_types.go:53-56`) |
| an audio donor | the Movie or the Series | as above | an entry with `purpose: audioDonor` |

**Single-episode grabs live on the Series** (decided, not an owner question). Putting them on
the Episode would give one transfer two possible homes depending on its shape, two
blocklists to consult for one episode, and a cross-object comparison whenever a pack
supersedes queued single episodes. The cost is that every episode grab writes the Series'
status, which is bounded (§6.3), and that Series and Comic join the loop as item keys, moved as
F4.2 moved Movie and Episode (`ReconcileItem`, `Watches()`; the Series reconciler's episode
creation, monitoring cascade and classification stay its own code, now behind the loop's key).

Episode and Issue keep their per-item view: `status.activeDownloadRef` (the covering entry's
`id`) and the new `status.downloadPhase`, rendered by the Episode or Issue key from its
container's entries through the index of §6.11. A Series or Comic status change that alters
which entries cover which episodes, or their phases, wakes the covered keys (source S24, §6.11).

### 6.2 API

New file `api/catalog/v1alpha1/download_entry_types.go`. `DownloadSource` moves from
`api/download/v1alpha1` to `api/common/v1alpha1` with its CEL "exactly one of" rule
(`download_types.go:306`); `api/download/v1alpha1` aliases it until N+1. The enums
`DownloadPhase`, `DownloadStage`, `DownloadFailureReason`, `DownloadPurpose`, `GrabbedBy` and
`ImportRejectionClass` move to `api/common/v1alpha1` the same way. Every list is
`+listType=atomic` (one writer), every string has a cap, and no field has a
`+kubebuilder:default` (the loop always writes concrete values).

```go
// DownloadEntry is one grab of its owner, from the manager's decision to
// grab through seeding and removal. Written only by the owner's item key
// in the remediation loop, under field manager catalogarr (ADR-0019).
type DownloadEntry struct {
	// ID is the grab's stable name: k8s.ChildName(target, guid[, purpose]),
	// the name the Download had (adopted entries keep it, so
	// MediaFile spec.importedFrom.downloadRef, history and ui links still
	// resolve).
	// +kubebuilder:validation:MaxLength=253
	ID string `json:"id"`
	// UID is issued by the manager when it admits the grab (an adopted
	// entry keeps its Download's UID). It keys clustarr-transfers,
	// clustarr-imports, the engine command subject and the clustarr-progress
	// key download.<uid>, so a re-grab under the same ID never reads an old
	// transfer's records.
	// +kubebuilder:validation:MaxLength=36
	UID types.UID `json:"uid"`
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

	Release DownloadRelease        `json:"release"`
	Source  commonv1.DownloadSource `json:"source"`
	// Client is the DownloadClient, Engine the instance holding the
	// transfer (<client>-<ordinal>); both pinned once, at admission.
	// +kubebuilder:validation:MaxLength=253
	Client string `json:"client"`
	// +optional
	// +kubebuilder:validation:MaxLength=263
	Engine string `json:"engine,omitempty"`

	GrabbedBy commonv1.GrabbedBy `json:"grabbedBy"`
	GrabbedAt metav1.Time        `json:"grabbedAt"`
	// +optional
	Manual bool `json:"manual,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`
	// SeedCriteria are the release's (from its Indexer) when it has any;
	// the engine applies its DownloadClient's defaults to what is unset.
	// +optional
	SeedCriteria *commonv1.SeedCriteria `json:"seedCriteria,omitempty"`
	// Effective values, resolved at admission from DownloadClient and
	// RootFolder settings, always written (no default can hide a false).
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
	// (loop spec §2.3, with §8.3's Delivery).
	Dispatch Dispatch `json:"dispatch"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
}

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
	InfoHash string             `json:"infoHash,omitempty"`
	Protocol commonv1.Protocol  `json:"protocol"`
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

// DownloadImportSummary is the grab's import, summarised from its
// clustarr-imports record (§6.9); the per-file detail stays in the record.
type DownloadImportSummary struct {
	// pending | inspected | approved | imported | blocked | held | expired
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
	// Dispatch fences the inspect and execute tasks (§6.9).
	Dispatch Dispatch `json:"dispatch"`
}

// BlocklistEntry is a release this item never grabs again before Until.
type BlocklistEntry struct {
	// +kubebuilder:validation:MaxLength=512
	Title string `json:"title"`
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	GUID string `json:"guid,omitempty"`
	// +optional
	InfoHash string `json:"infoHash,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	IndexerRef string `json:"indexerRef,omitempty"`
	Protocol commonv1.Protocol `json:"protocol"`
	Reason commonv1.DownloadFailureReason `json:"reason"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	EntryID string     `json:"entryID,omitempty"`
	At      metav1.Time `json:"at"`
	Until   metav1.Time `json:"until"`
}
```

**On the owner kinds** (Movie, Series, Album, Book, Audiobook, Comic), under `catalogarr`:

| Field | Type, cap | Notes |
|---|---|---|
| `status.downloads` | `[]DownloadEntry`, MaxItems 8 (Movie, Album, Book, Audiobook), 24 (Series, Comic) | live entries only (§6.4) |
| `status.blocklist` | `[]BlocklistEntry`, MaxItems 32 (Movie, Album, Book, Audiobook), 64 (Series, Comic) | oldest dropped past the cap, with a Normal Event `BlocklistEntryDropped` |
| `status.downloadPhase` | `DownloadPhase` | the active entry's phase, `""` when none; print column and selectable field |
| `status.downloadNonces` | `{remove, resume, import}`, each MaxLength 63 | the one-shot intent last handled (§6.10) |

**On Episode and Issue**, under `catalogarr`: `status.downloadPhase` (new), and
`activeDownloadRef`, whose value becomes the covering entry's `id` on the Series or Comic
(the JSON name stays; until adoption it already holds a Download name, which equals the
adopted entry's `id`).

**Search and pending-grab state**, under `catalogarr-grab` (the set it owns today, plus what
the manager now decides). Searches stay per item (an Episode is searched, not its Series); a
pending candidate lives with the grabs, on the owner:

| Field | On | Change |
|---|---|---|
| `status.lastSearchedAt`, `searchAttempts`, `donorSearchAttempts` | every searchable item, Episode and Issue included | unchanged names; rendered from the manager's own dispatches and the search records |
| `status.searchDispatch` | the same | new `*Dispatch`: the outstanding search task |
| `status.pendingGrab` | Movie, Album, Book, Audiobook | gains `candidate` (`DownloadRelease`, `DownloadSource`, `score`), so a delayed grab can be made from status alone; `releaseTitle`, `protocol`, `grabAt` keep their names |
| `status.pendingGrabs` | Series, Comic (new) | `[]PendingGrab`, MaxItems 24, each with the `episodes` or `issues` it covers |
| `status.pendingGrab` | Episode, Issue | kept as a view: the container's pending candidate covering it, rendered by the Episode or Issue key |

A search record whose `item` is an Episode or Issue wakes both that key (its bookkeeping) and
its container's key (the grab planner), through the `recordsource` mapping of §8.1.

**MediaFile spec** gains `importedFrom.infoHash` (MaxLength 64, importarr-worker, frozen at
import), so the search's current-file check (`snapshot.go:237-279`) stops reading the
Download named by `importedFrom.downloadRef`. Release N backfills it from live Downloads
(§10.2).

**Print columns and selectable fields.** Every item kind gains
`+kubebuilder:printcolumn:name="Download",type=string,JSONPath=`.status.downloadPhase``
and `+kubebuilder:selectablefield:JSONPath=`.status.downloadPhase`` (the fold proved status
selectable fields on 1.37, loop spec §2.10; each kind's count of selectable fields stays at or
under 8, which the A1 task counts). Series and Comic also print
`.status.downloadingEpisodeCount` (Series has it today) as `Queue`. The queue view replaces
`kubectl get downloads`:

```sh
kubectl get movies,series,albums,books,audiobooks,comics -A --field-selector status.downloadPhase!=
kubectl get episodes -n media --field-selector status.downloadPhase=Downloading
kubectl get movie heat-1995 -o jsonpath='{.status.downloads}'
```

### 6.3 Size budget

Worst case per entry at every cap: ids and names ≈ 0.8 KB, release ≈ 2.4 KB, source ≈ 4.4 KB
(a 4,096-byte magnet), 200 episode numbers ≈ 6 KB (an issue list ≈ 4 KB), output path 4.1 KB,
message and import summary ≈ 2.4 KB, dispatches ≈ 0.4 KB: **≈ 20.5 KB**. A blocklist entry is
≈ 2.2 KB.

| Owner | downloads | blocklist | added worst case |
|---|---|---|---|
| Movie, Album, Book, Audiobook | 8 × 20.5 KB | 32 × 2.2 KB | ≈ 235 KB |
| Series, Comic | 24 × 20.5 KB | 64 × 2.2 KB | ≈ 633 KB |

Typical: an entry is about 1.2 KB (a 300-byte magnet, an 80-byte title, a 120-byte path, one
to 24 episodes), so a Series with three live grabs grows by about 4 KB. Against etcd's
1.5 MiB the Series worst case leaves about 900 KB for the rest of its status and spec; the
`seasons` list (200) and `metadata` are the other large parts.

`pkg/crdcheck.TestItemAtEveryCapFitsTheBudget` (envtest), the loop spec's
`TestMediaFileAtEveryCapFitsTheBudget` applied to the eight item kinds: build the worst-case
object from the generated schema, apply its status through `k8s.PatchStatus` under each
owning manager, read it back with `managedFields`, and assert status ≤
`ItemStatusBudgetBytes` (512 KiB; Series and Comic `ContainerStatusBudgetBytes`, 1 MiB) and
the object ≤ 1.25 MiB. If the Series measures over, the A1 task lowers the entry cap to 16
before lowering any string cap. `TestEveryStatusListIsCapped` covers the new lists unchanged.

### 6.4 The state machine

`app/grab/lifecycle` is the planner (pure: a view in, the next entries, blocklist and effects
out), with the loop's adapter in `app/remediation/downloads`. It replaces
`app/grab/controller/download` (phase derivation `phase.go:97-165`, assignment, the import
publish, blocklisting, teardown) and `app/grab/status`. Phases keep today's names
(`download_types.go:71`), so the ui and `pkg/pipeline` mappings carry over; `Removing`, which
nothing writes today, becomes real.

| From | Trigger (source) | To | Effects after the apply |
|---|---|---|---|
| — | the grab planner chooses a candidate (§6.6) | `Pending` | none |
| `Pending` | a client and engine instance are chosen and admitted (§5) | `Assigned` | command `present@seq` to the engine (§6.7); `evt.catalog.release.grabbed`; the direct-grab count (§6.11) |
| `Assigned` | transfer record at `seq`, stage `fetchingMetadata` | `Queued` | |
| `Assigned`, `Queued` | stage `transferring`, `verifying`, `repairing`, `extracting`, `publishing` | `Downloading` | |
| any live | user pause (`download.clustarr.io/paused`, §6.10) | `Paused` | command `present,paused@seq+1` |
| `Downloading` | record `healthPaused` | `Paused`, message names the health floor | none until the user resumes (§6.10) |
| `Downloading` | record stage `done` or `seeding` | `Completed` | import inspect task (§6.9) |
| `Completed` | import record imported, and `Files` MediaFiles name this `id` | `Imported`, or `Seeding` while a torrent seeds | command with `imported: true` |
| `Seeding` (or `Imported`, usenet) | record `seedGoalReached` (torrent) or the import done (usenet), and the effective removal policy (`removeOnImport`, the client's `removeCompleted`) says remove | `Removing` | command `absent@seq+1`, `removeData: removeDataOnDelete`. The library holds its own copy or hard link, so the payload is a leftover; today it stays on disk until the Download is deleted, but an entry is dropped once removed, so the payload goes with it (Q6) |
| `Seeding` | the client's `removeCompleted` is false | stays `Seeding` | none: the entry is kept until a person removes it |
| `Completed` | import verdict `releaseFault` after its last walk (§6.9) | `Blocklisted` | blocklist entry; command `absent`, `removeData: removeDataOnDelete`; `evt download failed`; a redownload search (§6.6) |
| `Completed` | import held past `ImportHoldRetention` (24 h) | `Failed`, `importExpired` | command `absent`, `removeData: removeDataOnDelete`; never blocklisted or searched again (CLAUDE.md, 2026-10-07) |
| `Queued`, `Downloading` | record `engineFailureReason` (a release fault: `missingArticles`, `encrypted`, `payloadMismatch`, `stalled`) | `Blocklisted` | as the import release fault |
| `Queued`, `Downloading` | record `engineFailureReason` `diskFull` or `writeError` (a local fault) | `Failed` | command `absent`; no blocklist, no redownload (Radarr) |
| `Downloading` | no progress for the client's `stallTimeout` (the record's `lastProgressAt`) | `Blocklisted`, `stalled` | the manager decides the stall; the engine only reports progress (§3.5, P111) |
| any | user remove (`download.clustarr.io/remove`) | `Removing` | command `absent`, `removeData` as asked; a blocklist entry if asked |
| any | the owner is being deleted | `Removing` | command `absent`, `removeData: removeDataOnDelete` (§6.8) |
| `Removing` | record state `removed` at the command's seq | entry dropped | `fsops.SafeRemove(DataDir, outputPath)` first when `removeData`; history event |
| `Failed`, `Blocklisted` | record `removed` | entry dropped | as above; the blocklist entry stays |

- **Every effect is owed until the record shows it** (loop spec §3.8): a command whose seq the
  transfer record has not reached is republished with the same Msg-Id while inside the dedupe
  window, and with a new one past it (§6.7). Status is written first, then the command.
- **An entry is dropped only after its transfer record reads `removed`**, or after R-6's ten
  minutes with its engine gone (§6.8). So `status.downloads` holds every transfer the system
  is responsible for, and nothing else.
- **A Failed or Blocklisted entry with no transfer** (it failed before the engine added it) is
  dropped on its next pass.

### 6.5 Where every Download field goes

**Spec** (`download_types.go:353-453`):

| Field | Goes to | Reason |
|---|---|---|
| `protocol` | `entry.release.protocol` | |
| `clientRef` | `entry.client` (pinned at admission) | the manager chooses it (§5) |
| `source` (+`expectedInfoHash`) | `entry.source` | intent: re-adding after an engine loses its journal needs it, so it is in etcd |
| `release` | `entry.release` (capped subset) | `ReleaseInfo` has no string caps |
| `target` | the owner, plus `entry.episodes` or `entry.issues` | a target is the owner or a subset of its children |
| `qualityProfileRef` | `entry.qualityProfileRef` | |
| `priority` | annotation `download.clustarr.io/priority` (§6.10), in force in the command | user intent |
| `paused` | annotation `download.clustarr.io/paused` | user intent |
| `seedCriteria` | `entry.seedCriteria` | |
| `removeOnImport`, `removeDataOnDelete` | `entry.removeOnImport`, `entry.removeDataOnDelete`, effective values | typed-client default gotcha: concrete booleans, always written |
| `grabbedBy`, `manual`, `purpose` | `entry.grabbedBy`, `manual`, `purpose` | |

**Status** (`download_types.go:620-813`):

| Field | Goes to | Reason |
|---|---|---|
| `observedGeneration`, `conditions` (Assigned, Downloaded, SeedGoalMet, Imported, Failed, DeadLettered) | the entry's `phase`; item Events on transitions; `DeadLettered` on the owner | an entry is not a CR |
| `phase` | `entry.phase` | the manager's state |
| `engine` | `entry.engine` | pinned once, as `status.engine` was |
| `failureReason`, `engineFailureReason` | `entry.failureReason`; the engine's in the transfer record | |
| `blocklistedUntil` | `blocklist[].until` | |
| `startedAt`, `completedAt`, `seedGoalMetAt` | the entry | |
| `stage` | `entry.stage` | coarse, changes a few times per grab |
| `downloadID`, `contentRoot`, `files[]`, `isEncrypted`, `canMoveFiles`, `canBeRemoved`, `health`, `healthPaused`, `seedGoalReached` | transfer record (§6.7) | engine-observed; `files` alone can be 820 KB |
| `outputPath` | `entry.outputPath` **and** the transfer record | data removal needs it after a NATS loss |
| `totalBytes`, `remainingBytes`, `downloadedBytes`, `uploadedBytes`, `downloadRateBps`, `uploadRateBps`, `etaSeconds`, `progressPercent`, `seeders`, `peers`, `ratioMilli`, `seedTimeSeconds`, `lastProgressAt` | transfer record (coarse) and `clustarr-progress` `download.<entry uid>` (1 Hz, as today) | nothing writes progress to etcd (loop spec §4.11) |
| `message` | `entry.message`, clamped to 1024 (the record keeps 2048) | |
| `import` | `entry.import` (summary) and the `clustarr-imports` record (detail) | the per-file lists are up to 400 entries |

**Labels and annotations:** `download.clustarr.io/engine` and `client` become `entry.engine`
and `entry.client`; `download.clustarr.io/blocklisted` becomes the blocklist; an operator's
hand-set blocklist is the `remove … blocklist` intent (§6.10).
`catalog.clustarr.io/import-target` and `import-override` become the
`download.clustarr.io/import` intent. `clustarr.io/dead-lettered` lands on the owner, where
every resolver now points (§8.5).

**Finalizers:** `download.clustarr.io/download` and `download.clustarr.io/engine` go. The
owner carries `download.clustarr.io/transfers` while it has entries (§6.8).

### 6.6 The grab planner: candidates in, decisions in the manager

Today the search worker, the grab handler and the RSS matcher each decide to grab, serialised
by a KV lease per target (`lease.go:125-225`), a keep-best pending record in `clustarr-pending`
(`pending.go:114,144`) and scheduled grab messages (`WorkGrabSubject`). Under owner decision 4
the decision is the owner key's:

1. **Candidates come in scored.** A search task (§5) returns its ranked candidates in a
   `clustarr-searches` record; the RSS matcher sends each release it matched and scored to the
   owner as a `Candidate` intake message (§4.3). Agents run the heavy part, `pkg/decision`'s
   release evaluation (parsing, quality and custom-format scoring, the profile's approval) and
   ranking; the result carries the score, quality, languages and every rejection reason.
2. **The owner key decides.** The grab planner (`app/catalog/grabplan`, pure) re-checks each
   approved candidate against the owner's fresh state, with the cheap item-state rules of
   `pkg/decision` only (no regexp2): the blocklist, the live entries (the queue preference),
   the current file and its cutoff, `TranscodedFinal`, wrong language, monitoring, the delay
   profile. It then grabs now (a new `Pending` entry), keeps the best as `status.pendingGrab`
   with `grabAt` (a `RequeueAfter`, replacing the scheduled grab message), or ignores it.
3. **Nothing else serialises.** One reconcile per owner at a time is the lease ADR-0008 kept
   in KV; `clustarr-leases` and the live-read double-grab guard (`perform.go:363-445`) retire,
   and so does `clustarr-pending` (its candidate moves into `status.pendingGrab`).
4. **A user's pick still wins.** Search `spec.grab` is resolved by the Search controller
   (`resolveGrab`, `app/catalog/controller/search/grab.go`), which sends the pick to the owner
   as a `Candidate` marked `manual`; the grab planner exempts it from `TranscodedFinal` exactly
   as `resolveGrab` does today.
5. **Redownload is the planner's.** On an entry's `Blocklisted` transition the planner itself
   decides the next search (the owner's search planner, §5) instead of the
   `catalogarr-redownload` consumer freeing leases and publishing; that consumer retires with
   the leases. `evt download failed` is still published, for history.

Field managers: the entry list and blocklist are `catalogarr`'s; `pendingGrab`,
`lastSearchedAt`, `searchAttempts`, `donorSearchAttempts` and `searchDispatch` stay
`catalogarr-grab`'s (R7), applied by the same item pass as a second apply (§7.0).

### 6.7 Engines

**What an engine does:** it executes desired-state commands for the transfers pinned to it,
reports each transfer in a record, reports itself in an engine record, and publishes 1 Hz
progress as today. It reads no catalog object and writes no Kubernetes object. It keeps
reading its DownloadClient and its proxy or provider Secrets through the APIReader at start
(`app/grab/agent/torrent/register.go:86`, `usenet/register.go:82-89`), and rolls when they
change (the `engine-config-hash`, `workload.go:333`), as today.

**Commands.** Stream `CLUSTARR_WORK_ENGINE` (§4.3), subject
`clustarr.work.engine.<KVKeyToken(client)>.<ordinal>.<KVKeyToken(entry uid)>`, one durable per
engine instance, `grabarr-engine-<KVKeyToken(client)>-<ordinal>`, ensured by the
DownloadClient controller when it renders an ordinal and deleted when no entry is pinned to a
removed ordinal. Only the manager publishes. The payload, `schema.EngineCommand`
(`download.EngineCommand.v1`), is the transfer's **whole desired state** at a seq:

`seq`, `owner` (kind, namespace, name, UID), `entry` (`id`, `uid`), `desired`
(`present` | `absent`), `removeData`, `protocol`, `source`, `expectedInfoHash`, `release`
(title, guid, indexerRef, size), `category` (from the owner kind), `selection` (the season and
episode numbers or issue numbers, which replaces the engine's Episode read through the
APIReader, `register.go:155`), `paused`, `priority`, `healthOverride` (the resume nonce),
`seedCriteria`, `removeOnImport`, `imported` and `importedAt`, `issuedAt`.

The engine applies a command only if its seq is above the seq its journal holds for that
entry; otherwise it acks and drops it. It acks after its transfer record shows the new seq,
and naks with a delay when it cannot apply yet (the payload RPC failed, the disk is full).
Msg-Id `engine/<entry uid>/<seq>`; a resync after an engine restart uses
`engine/<entry uid>/<seq>/<bootID>` so the dedupe window does not swallow it.

**Records.** `clustarr-transfers`, key `RecordKey(entry uid)`, one writer: the engine holding
the transfer. `schema.TransferRecord` (`download.Transfer.v1`) embeds the header (§4.3) with
`item` = the owner, `seq` = the last command applied, and `state`
(`present` | `removed` | `failed`), plus every engine field of §6.5: `downloadID`, `stage`,
`outputPath`, `contentRoot`, `files` (clamped to a 384 KiB budget, with `filesTruncated`),
sizes and counters, `health`, `isEncrypted`, `canMoveFiles`, `canBeRemoved`,
`seedGoalReached`, `healthPaused`, `engineFailureReason`, `message` (2048), `startedAt`,
`completedAt`, `lastProgressAt`, `engine`, `bootID`, `dataRemoved`. The engine writes it on a
state or stage change, on a command, at most once a minute for counters, and at least once a
day while the transfer lives (the bucket's 7-day TTL retires only dead transfers).

`clustarr-engines`, key `RecordSubKey(DownloadClient uid, ordinal)`, one writer per key: that
engine pod. `schema.EngineRecord`: `client`, `ordinal`, `pod`, `bootID`, `version`, `ready`,
`reattached`, counts (`active`, `queued`, `seeding`), `freeBytes` of scratch and publish
directories, `proxyUDP` (`available` | `unavailable` | `n/a`), `orphans` (≤ 32: `downloadID`,
`name`, `addedAt`, `sizeBytes`), `at`; written at start, on change and every 60 s.

**What the manager does with them.** The owner key incorporates a transfer record into its
entry (phase, stage, timestamps, output path, message, `dispatch.answeredSeq`). The
DownloadClient controller renders `status.engine`, `active`, `queued`, `seeding`, `freeBytes`,
`EngineReady` (ready replicas **and** a fresh engine record), `DiskSpaceOK` (from the engine's
own `freeBytes`, not the manager's statfs of its own mount, `controller.go:204-208`), a new
`ProxyUDPUnavailable` condition with its Event (W40), and `OrphansPresent` with the orphan
count. A new `bootID` on an engine record enqueues every owner with an entry pinned to that
engine, which republishes each entry's desired state (the resync).

**Re-attach and the journal.** The torrent engine's `.state` directory already holds, per
transfer, the metainfo and a descriptor naming the Download, its magnet, priority, pause, seed
criteria, selection, `addedAt` and seed counters (`app/grab/engine/torrent/state.go:40-90`,
on the RWX volume at `<DataDir>/torrents/.state/`). It gains `entryUID`, `owner`, the last
applied `seq`, `imported` and the failure verdict, the two things it re-derives from Download
status today (`phase.go:66-69`, `pkg/download/torrent/client.go:555-567`). The usenet
manifest (`pkg/download/usenet/client.go:312-341`) gains the same fields. At start an engine
re-attaches from its journal exactly as today (`torrent/engine.go:90-128`, `usenet
client.go:221-306`), rewrites a transfer record for every transfer, writes its engine record
with a new `bootID`, and only then reports ready. Piece completion stays where R13 keeps it,
so nothing rehashes.

**Data safety.**

1. An engine removes a transfer, or any byte of data, only on a command `absent` at a seq
   above its journal's. Nothing else removes: not a missing record, not a missing command, not
   an expired bucket, not an unknown owner.
2. A transfer the engine holds that no command has named since its boot, after a 10-minute
   grace, is an **orphan**: listed in the engine record, never removed. Today's reapers
   (`torrent/reaper.go:255-296`, `usenet/reaper.go:251-285`) become orphan reporters. An orphan
   is removed only by the DownloadClient's `download.clustarr.io/remove-orphan` intent
   (§6.10), which the DownloadClient controller turns into an `absent` command for that
   transfer (open question Q1).
3. A NATS data loss loses no intent: the entries are in etcd; their records are rebuilt by the
   engines' re-attach; their commands are owed and republished on the next pass of each owner,
   which the recreated-bucket rule of `recordsource` enqueues (loop spec §4.9).
4. The usenet engine with an `emptyDir` scratch (`downloadclient_types.go:347-402`) loses its
   journal with its pod, as it loses its partial data today. The resync re-adds by name
   (`usenet.Client.Add` dedupes on `AddRequest.Name`, CLAUDE.md); a re-fetch of the payload is
   counted by the indexer, as today.

**Engine-side choices that move** (§3.5, P111-P123): the torrent engine's own removal when
`CanBeRemoved && removeOnImport && removeCompleted` (`torrent/reconciler.go:355-367`) and the
usenet engine's removal on `removeOnImport` (`usenet/engine.go:356-370`) become the manager's
`Seeding → Removing` transition; `engine.Stopped`'s removal of a Failed or Blocklisted
transfer (`engine.go:73-81`) becomes the `absent` command; a stall becomes the manager's
judgement on `lastProgressAt`. Health pause stays in the engine as a mechanism (it stops
spending a usenet quota the moment the floor is crossed) and is reported; what follows is the
manager's (Paused, then the user's resume or, with `healthAction: delete`, Blocklisted).

### 6.8 Deleting an item, and R-6

The owner key adds `download.clustarr.io/transfers` (field manager `clustarr`,
`k8s.EnsureFinalizer`) when it admits its first entry, and removes it when it has none.

On deletion every live entry moves to `Removing` with `removeData: removeDataOnDelete`, and
the owner key publishes the commands. An entry is dropped when its record reads `removed` at
that seq; the manager then removes `outputPath` when asked, as the Download controller does
today (`controller.go:599-661`). When the entry's engine is gone (the DownloadClient deleted,
`EngineReady` false, or the ordinal beyond the replicas: `teardown.go:70-88`), the owner key
waits `DefaultEngineTeardownTimeout` (10 minutes, R-6) from the `Removing` transition, then
drops the entry, removes the data itself when asked, and emits `EngineGone`. The finalizer
goes when the list is empty.

Before this design, deleting an item cascaded to its Downloads by owner reference and each
Download's finalizers ran. After it, nothing cascades: the owner's own finalizer does the
same work with the same timeout.

### 6.9 Imports: inspect, decide, execute

Today fileimport decides and acts in one handler: it picks files, decides replacements,
classifies rejections (`remediation.go:96-225`), moves files, creates and deletes MediaFiles
and writes `status.import`. Under decision 4 it splits:

1. **Inspect** (task `ImportInspect` on `importarr-fileimport`, issued on `Completed`): the
   agent reads the payload (the transfer record's `contentRoot` and `files` ride in the task),
   parses, probes and fingerprints each file, matches it to the entry's targets, and answers
   in `clustarr-imports` at `RecordSubKey(entry uid, "inspect")`: per file, the parsed release
   facts, the probe summary, the proposed target, and every reason it could not be imported,
   each with its error kind. Pure computation.
2. **Decide** (the owner key): the import planner (`app/import/importplan`, pure) applies
   today's rules to the inspected facts and the owner's fresh state: best file per
   single-file item, episode and series-root mapping, the upgrade decision,
   `replacesWrongLanguage`, `TranscodedFinal` (`ImportMessageExistingFileFinal`), and the
   remediation classes (`transient`, `releaseFault`, `itemState`, `needsPerson`) with their
   retries, holds and expiry. The result is an approved plan (moves, and the MediaFiles each
   replaces) or a verdict, rendered into `entry.import`.
3. **Execute** (task `ImportExecute`, carrying the approved plan): the agent hard-links or
   moves the files, recycles the replaced ones (`fsops.Recycle`), reduces an audio donor, and
   answers at `RecordSubKey(entry uid, "execute")` with what it placed: per file, the
   destination, size, mtime, fingerprint and the probe it seeded into `clustarr-probes`.
4. **Materialise** (the owner key, as effects after its apply): apply each placed file's
   MediaFile spec through `mediafilespec.Apply` under `importarr-worker`, named
   `k8s.ChildName(item, "mediafile", dest)` as today, and delete each replaced MediaFile with a
   UID precondition (W22 had none). The entry reads `Imported` when `Files` MediaFiles name its
   `id`, so the item never reads Wanted between a placed file and its MediaFile.

`classify` and `conclude` move from `app/import/worker/fileimport/remediation.go` into
`importplan`; `ImportMessage*` constants move with them. The retrigger controller
(`app/import/controller/retrigger`) retires: the `download.clustarr.io/import` intent (§6.10)
re-issues the inspect task, with the target and override it names. The import consumer keeps
its name and backoff for a transient error inside a task; a transient verdict's retry is now
the planner's `nextAttemptAt`, so the 1 m, 10 m, 45 m ladder is status, not stream state.

### 6.10 User intent on the owner

Annotations on the owning item, never written by the loop (loop spec §2.9's rules: a one-shot
nonce is recorded on the pass that sees it; an invalid one is recorded with a Warning Event
`InvalidAnnotation`). Entry ids are DNS subdomains, so commas and spaces separate them.

| Annotation | Kind | Value | Recorded in | Replaces |
|---|---|---|---|---|
| `download.clustarr.io/paused` | standing | `<id>[,<id>…]` | the command's `paused` | Download `spec.paused` |
| `download.clustarr.io/priority` | standing | `<id>=high\|normal\|low[,…]` | the command's `priority` | Download `spec.priority` |
| `download.clustarr.io/remove` | one-shot | `<nonce> <id> [data] [blocklist]` | `downloadNonces.remove` | deleting a Download; labelling it blocklisted |
| `download.clustarr.io/resume` | one-shot | `<nonce> <id>` | `downloadNonces.resume` | toggling `spec.paused` to release a health hold |
| `download.clustarr.io/import` | one-shot | `<nonce> <id> [target=<kind>/<name>[/<key>]] [override]` | `downloadNonces.import` | `catalog.clustarr.io/import-target`, `import-override` and the retrigger controller |

On the DownloadClient: `download.clustarr.io/remove-orphan`, one-shot,
`<nonce> <engine> <downloadID> [data]`, recorded in `status.handledOrphanNonce`.

The ui's Downloads page offers pause, resume, remove (with data and blocklist choices) and
retry-import through these; `ui/actions` gains the patches, and the ui role gains `patch` on
any owner kind it does not already patch (`TestUIRoleGrantsOnlyReadsAndActionWrites` holds the
role to `actions.Grants()`).

### 6.11 Readers, after

| Reader today (§3.4) | After |
|---|---|
| `rollup.DownloadNonTerminal`, `ActiveDownload`, `DonorDownloading`, `DownloadOverlay` | the same rules over `[]DownloadEntry`; Episode and Issue keys read their container's entries |
| the six `.spec.target.<kind>` indexes; S9 (loop spec §3.3) | a new index `remediation.item.download` on the six owner kinds (values: entry ids and uids); S9 is replaced by S24: a Series or Comic status change whose `DownloadsSignature` (covered numbers and phases) changed enqueues the covered Episode or Issue keys |
| `grabsource.Covers` | over entries |
| search worker blocklist, queue, donor queue, current file | the grab planner in the manager (§6.6); the agent's evaluation no longer reads them |
| grab guard and lease holders | retired (§6.6) |
| RSS matcher's blocklist, current file, queue | the grab planner; the matcher sends candidates |
| redownload `awaitBlocklist` | retired with the consumer (§6.6) |
| history target resolution | the owner, through `remediation.item.download` |
| fileimport's Download read | the inspect and execute tasks carry what it read (§6.9) |
| DownloadClient Active/Queued | the engine records (§6.7) |
| `directgrab` | the `Assigned` transition's effect counts a direct-source grab (`limits.CountGrabAt`), idempotent by entry uid (the ring's entry is keyed by it) |
| Search controller `inFlightDownload` and `resolveGrab` | a `Candidate` to the owner (§6.6) |
| retrigger | the import intent (§6.9) |
| `replay-download` | retired; dead letters resolve to the owner (§8.5) |
| ui projection, `/downloads`, SSE, `pkg/pipeline` | §6.12 |
| e2e | §6.13 |

### 6.12 The ui

- `ui/projection` stops listing Downloads (`watchedKinds`, `projection.go:470-479`) and builds
  download rows from the six owner kinds it already lists: one row per entry, keyed by entry
  uid, with the owner as its link. `SubscribeDownloads` fans the same rows out.
- 1 Hz progress comes from `clustarr-progress` `download.<entry uid>` through a new
  `ui.Options.TransferProgress(ctx, uid) (schema.DownloadProgress, bool, error)`, bound by
  `internal/cli/ui` to the read-only bus under a 2 s deadline, beside the fold's
  `TranscodeProgress` (loop spec §7.7). `TestBothUICommandsWireEveryUIOption` covers it;
  `TestUINeverWrites` is unaffected.
- `ui/views/downloads.templ` renders the same columns from the entry and the progress value;
  the import detail links to the owner page, which reads the `clustarr-imports` record through
  the read-only bus for its per-file table.
- `pkg/pipeline` reads `DownloadEntry` instead of `downloadv1alpha1.Download`.
- `config/rbac/ui_role.yaml` drops `downloads` (and the chart copy follows).

### 6.13 e2e

Scenarios 1-4, 6, 14 and 15 create Downloads directly today
(`test/e2e/helpers_test.go:1244,1272`). They change to the user's path: a Search with
`spec.grab` naming a fixture release, which the owner key admits; the wait helpers read
`status.downloads[?(@.id=="…")].phase` and the transfer record; `patchDownloadLabel`
(`:1393`) becomes the `remove … blocklist` intent. `test/e2e/main_test.go`'s
`expectedCRDCount` drops by one in N+1. A new scenario, `TestEngineRestartKeepsSeeding`,
deletes a torrent engine pod mid-seed and asserts the transfer is re-attached, its record
rewritten, no piece rehashed and nothing removed. Like every scenario since Phase C, these are
written and run in Phase H.

---

## 7. Every other agent write, converted

### 7.0 How the manager writes what agents wrote: names kept, one pass, one apply per manager

Every field manager an agent wrote under keeps its name (R7) and is written from
`cmd/manager` only, so `managedFields` carry over and nothing migrates ownership:

| Field manager | Written after this design by | Set |
|---|---|---|
| `catalogarr-grab` | the item key's grab planner (loop) | `pendingGrab`, `lastSearchedAt`, `searchAttempts`, `donorSearchAttempts`, `searchDispatch` |
| `catalogarr-metadata` | the item key (loop) for the six loop kinds that carry metadata (Movie, Series, Album, Book, Audiobook, Comic); the Artist and Author reconcilers for theirs | `status.metadata`, `status.artwork` |
| `catalogarr-artwork` | the item key (loop), Movie and Series | `status.overlay` |
| `catalogarr-worker` | the Search controller | Search `finishedAt`, `indexerOutcomes`, `results` |
| `importarr-worker` | the owner key's import effects (§6.9); the scan and list appliers (§7.6); the rename actuator (loop spec §3.9) | MediaFile spec; item spec of scan- and list-added items |
| `importarr` | the scan applier; the ImportList controller | `observed-fingerprint`; the Trakt token Secret |
| `indexarr-worker` | the Indexer controller | Indexer `WorkerFields` |
| `indexarr` | the Indexer controller | the session Secret (now its only writer), the facade key Secret |
| `clustarr-dlq-projector` | the projector, now in the manager | the two dead-letter annotations |
| `grabarr-engine` | nothing | retired with Download (`k8s.RetiredFieldManagers()` in N, deleted in N+1) |

**One pass, one apply per manager.** A loop item pass renders, in this order, the
`catalogarr` set (the fold's rollups plus §6's entries, blocklist and nonces), then each other
manager's set it owns on that kind. Each set goes in its own `k8s.PatchStatusCAS`, each a
complete declaration of that manager's set (CLAUDE.md, "Server-side apply replaces a field
manager's ownership set on every apply"), each preconditioned on the resourceVersion the
previous apply returned. `catalogarr`'s apply is never skipped (loop spec §3.12); each other
set is compared against the cached object's values at exactly its own paths and skipped when
equal, because a manager-scoped set is fully known even though the cache strips
`managedFields`. A Conflict on any apply ends the pass with a 1 s requeue; the next pass
re-renders every set from a fresh view, so a half-written pass is completed, never rolled
back.

**Why not fold every item field under `catalogarr`.** It would make one apply per pass, but
every live item's `managedFields` would still hold `catalogarr-metadata`, `-grab` and
`-artwork` entries; forced ownership makes the loop a silent co-owner, and a field the loop
later stops sending (a `pendingGrab` cleared) would survive under the stale manager. The fold
had to strip `catalogarr-markers` from every MediaFile for exactly this (loop spec §7.3.8).
Keeping the names avoids a strip of about 16,500 items and its rollback step.

### 7.1 Metadata and original artwork (W10, W11)

Today the item reconcilers and `metadatarefresh` (manager) decide a refresh is due
(`controller/movie/reconciler.go:413-447`, `controller/metadatarefresh/refresher.go:181-189`)
and publish the task; the gateway does provider I/O, renders `status.metadata` and fetches
artwork, and applies both under `catalogarr-metadata` with no compare-and-swap.

After:

- **Due** stays the manager's, now in the item key's metadata planner for the six loop kinds that carry metadata
  and in the Artist and Author reconcilers. The task carries the inputs (provider ids,
  language, schema version, `force`).
- **The gateway answers in a record.** `clustarr-item-metadata`, key `RecordKey(item uid)`,
  `schema.ItemMetadataRecord` (`catalog.ItemMetadata.v1`): the header, `inputs`, `refreshedAt`,
  `schemaVersion`, the kind's metadata block exactly as `metadata/patch.go` renders it today
  (`json.RawMessage`, decoded by the kind's renderer), and a `failure`. It also keeps writing
  `clustarr-metadata-extended` and the L2 cache as today.
- **Original artwork is read from the object store.** The fetcher already writes each
  `original` object with versioned metadata naming the item, type, source, source URL, digest
  and size (`artwork.ObjectMeta`, W4.104; artwork design §B as amended). The manager keeps its
  own `objindex` over `clustarr-artwork` (leader-only, the ui's code, `pkg/events/objindex`),
  and the item key renders `status.artwork[]` from the item's `original` objects. If
  `ObjectMeta` lacks a leaf `status.artwork` carries (`updatedAt`), the A5 task adds it to the
  object metadata: the fetcher is that variant's only writer (ADR-0011). A lost bucket now reads
  as no artwork at once and is refetched when due, instead of waiting 7 to 30 days
  (`.superpowers/unify/research/nats-object-store.md`).
- **Which image to fetch** (`artwork/sources.go:65`, `fetcher.go:237,307`) is a plan over the
  item's spec and metadata: it moves to the item key, which publishes `catalogarr-artwork-fetch`
  tasks naming the URLs; the fetcher does I/O and writes objects.
- **Wake-ups:** a `recordsource` on `clustarr-item-metadata` (S25) and the manager's `objindex`
  (S26) enqueue the item key.
- **Ordering and lost updates:** records are compare-and-swap with one writer (the one-replica
  gateway, ADR-0007); a record is incorporated only when its `inputs` equal what the item last
  asked for and its `refreshedAt` is after `status.metadata.refreshedAt`. The apply is CAS.
- **Latency:** one KV write and one reconcile, about 50-200 ms, on a path that takes seconds of
  provider I/O.

### 7.2 Overlays (W4)

The overlay planner (`overlayplan/plan.go:185,272`; the winning profile, the badges, the inputs
digest) is already linked by the manager and moves into the item key (Movie, Series), which
publishes `catalogarr-artwork-render` with the plan's digest. The renderer decodes, draws and
writes the `overlay` object with its metadata (the inputs digest, the profile). The item key
renders `status.overlay` from that object's metadata through the same `objindex`, under
`catalogarr-artwork`. W4.82's `PatchOverlay` compare-and-swap is superseded; the renderer's
deletion of a stale overlay object stays (it is its variant).

### 7.3 Search bookkeeping and the wanted sweep (W1, the search half)

The wanted sweep's per-item half runs in the catalog agent today (`wantedscan.go:143-188`:
which items are due, `IndexOnly`, at most 20 first-time live donor searches per sweep per
namespace), on a `WantedScan` the manager's `wantedcron` publishes per namespace. It moves into
the item key's **search planner**: due under the backoff ladder (6 h · 2^n, capped at 7 d,
`wantedcron/backoff.go:49`), `IndexOnly` when the item was searched before, the donor cap, and
the per-indexer pacing class (§5.4). `wantedcron` keeps only its timer, now enqueueing item
keys at `LowPriority`. The planner records `searchDispatch`, publishes the search task and, on
the record's answer, renders `lastSearchedAt` and the attempt counters (unless every indexer
was paced, `worker.go:394-402`), all under `catalogarr-grab`.

### 7.4 Search status (W3)

The search agent answers every search task (interactive or automatic) in `clustarr-searches`,
key `RecordKey(search task uid)` (the Search CR's UID for an interactive search, the item's
task uid otherwise): `schema.SearchRecord`: `seq`, `finishedAt`, `indexerOutcomes` (≤ 100),
`queryMode`, and the ranked candidates (≤ 200), each with its release, decision, score and
rejection reasons. The Search controller incorporates an interactive one into Search status
under `catalogarr-worker` (CAS); the item key's grab planner reads an automatic one (§6.6).
The search agent no longer reads blocklist, queue or current file (§6.11): it evaluates the
release against the profile and the item's identity; the item-state rules run in the manager.

### 7.5 Indexers (W31-W35)

- **Health and backoff.** The fan-out and the RSS poll report each query's outcome in
  `clustarr-indexer-health`, key `RecordKey(indexer uid)`: counters, the last failure and its
  class, `indexedReleases`, `lastRssAt`, `lastRssNewCount`. One writer: the index agent is one
  replica (ADR-0010 refinement), and its two writers share an in-process CAS. The Indexer
  controller (manager) runs the escalation ladder (`app/indexer/status/health.go`, Prowlarr's
  ten rungs) and writes `WorkerFields` under `indexarr-worker`; the agent reads
  `disabledUntil` from its cache before querying. Between a failure and the manager's verdict
  (well under a second), the agent suppresses the indexer locally for at most 30 s: a
  mechanism that bounds a burst, not a policy (§3.5, P87).
- **Sessions.** A relogin writes only `clustarr-indexer-sessions` (`Save`) or deletes there by
  revision (`Drop`, `DeleteRevision`). The Indexer controller mirrors the KV session into the
  Secret under `indexarr`, its only writer; split §5.3.2's agent-side Secret apply and its
  Drop-CAS on the Secret go (W32, W33). `TestSessionDropDoesNotWipeANewerSave` keeps its KV
  half.
- **Facade key.** The manager creates the `--facade-api-key-secret` Secret if absent
  (create-only, `indexarr`), in the index manager registration; the agent reads it (`get` by
  name) and fails closed until it exists, re-reading every 30 s (W31).

### 7.6 Imports, scans and lists (W15-W22, W25-W30)

**Library scans.** The rescan agent walks, parses, fingerprints and attributes (all pure
computation over the cache it reads) and reports **observations** on
`clustarr.intake.scan.<namespace>.<KVKeyToken(scan uid)>`, one per file whose facts differ
from the MediaFile the agent read, or that has none: `schema.ScanObservation`: `kind`
(`present` | `missing` | `unmatched`), `path`, `size`, `modTime`, `fingerprint`, the proposed
attribution (item kind, name, provider id, keys) with its evidence, the frozen import fields,
the MediaFile it read (`basis`: UID, resourceVersion, a hash of its `importarr-worker` spec),
and the scan. The manager's scan applier (`app/import/manager/scanapply`, leader-only
consumer `importarr-intake-scan`) decides:

- **present, attributed:** create or update the MediaFile (`mediafilespec.Apply` under
  `importarr-worker`, the name `k8s.ChildName(item, "mediafile", path)` recomputed and
  compared; the observed fingerprint under `importarr`); create the Movie or Series the
  attribution names if it does not exist, by `names.Movie` / `names.Series`, under
  `importarr-worker`, `addOptions.monitor: none` as today. An existing object whose provider
  id differs is never overwritten: the observation becomes `unmatched`, reason
  `nameCollision`. The scanner still never guesses: an ambiguous attribution arrives as
  `unmatched` and is recorded as such.
- **missing:** delete the MediaFile with UID and resourceVersion preconditions once it has
  read `FileMissing` for 10 minutes (today's rule, `prune.go:71`), now the manager's.
- **unmatched:** goes to the scan's counters; the agent's tally in `clustarr-progress` is
  unchanged, and the LibraryScan controller incorporates it as today.
- **stale basis:** if the MediaFile's `importarr-worker` spec no longer hashes to `basis`
  (another write moved it), the applier acks and asks for a re-observation of that folder
  instead of applying.
- **Renames:** the rescan's rename pass (W16) is deleted; the loop's rename actuator renames,
  gated on no open scan of the folder (loop spec §3.9).

**Lists.** The import-list agent fetches the provider's list and reports a **snapshot**:
`clustarr-importlist` (today's key, kept) gains the full listed set (provider ids, titles,
years; chunked past 512 KiB), `syncedAt` replaces the `importlist-synced-at` annotation (W25).
The ImportList controller (manager) does the diff (`importlist.Dedupe`, `ApplySyncLevel`, the
exclusions, the classified-series rule, "writes only the items it added itself"), and applies,
unmonitors or deletes items under `importarr-worker`, deletes with a UID precondition (W28),
and, for `removeAndDelete`, publishes a recycle task for the files and deletes their
MediaFiles (W29). Today `controller/importlist/controller.go:427` already writes the list's
status from the KV result.

**Trakt tokens (W30).** The ImportList controller refreshes a token before it expires (within
24 h of `tokenExpiresAt`) and is the Secret's only writer, under `importarr`. The agent reads
the Secret; on a 401 it answers its sync `needsToken`, the controller refreshes and re-issues
the sync. Trakt rotates refresh tokens, so one refresher removes the race two writers had.

**fileimport (W21, W22, W24)** is §6.9.

### 7.7 Events (W8, W9, W14, W40, W44)

No agent emits a Kubernetes Event. The manager emits:

| Event | Recorder | Emitted on |
|---|---|---|
| `DeadLettered` | `clustarr-dlq-projector` | the projector's annotation, now in the manager |
| one per domain event | `catalogarr-history` | the history sink, now a leader-only consumer in the manager on `CLUSTARR_EVENTS` |
| `ArtworkFetchFailed` | `metadata-gateway` (name kept) | a failure in the item's metadata record or object metadata, on the transition |
| `ProxyUDPUnavailable` | `grabarr-engine` (name kept) | the engine record's `proxyUDP` turning `unavailable` |
| `DownloadAddFailed`, `EngineGone`, `BlocklistEntryDropped`, `CommandRetrying` | `downloads` | the owner key's transitions |

The `events` domain keeps the RSS matcher; the redownload consumer retires (§6.6), and the DLQ
projector and history sink move out, so its `minReplicas 1` (split §3.5.4) now protects only
`catalogarr-rss-matcher` on `CLUSTARR_RELEASES` (memory, discard-old on a single node).

### 7.8 Summary: every audit row and its replacement

| Rows | Report | Bucket or subject | Manager consumer | Field manager | Fence | Added latency |
|---|---|---|---|---|---|---|
| W1, W5 | search answers; RSS candidates | `clustarr-searches`; `clustarr.intake.candidate.>` | item key (grab and search planners) | `catalogarr-grab` | Seq in `searchDispatch`; candidate Msg-Id | one reconcile |
| W2, W6 | candidates (the grab is the manager's) | as W1 | item key | `catalogarr` | owner reconcile serialises | one reconcile, plus the delay profile |
| W3 | search answer | `clustarr-searches` | Search controller | `catalogarr-worker` | Seq; CAS | one reconcile |
| W4 | overlay object metadata | `clustarr-artwork` (object) | item key via `objindex` | `catalogarr-artwork` | the object's inputs digest | object watch, one reconcile |
| W7, W8 | DLQ envelopes | `CLUSTARR_DLQ` | DLQ projector (manager) | `clustarr-dlq-projector` | durable consumer | none |
| W9 | domain events | `CLUSTARR_EVENTS` | history sink (manager) | recorder only | durable consumer | none |
| W10, W11, W14 | metadata answer; original objects | `clustarr-item-metadata`; `clustarr-artwork` | item key; Artist and Author reconcilers | `catalogarr-metadata` | inputs and `refreshedAt`; CAS | one reconcile |
| W15, W17-W20 | scan observations | `clustarr.intake.scan.>` | `importarr-intake-scan` | `importarr-worker`, `importarr` | basis UID, RV, spec hash | intake hop, paced (§5.4) |
| W16 | none | | rename actuator (loop) | `importarr-worker` | | after the scan |
| W21, W22, W24 | inspect and execute answers | `clustarr-imports` | owner key | `catalogarr`; MediaFile `importarr-worker` | Seq in `entry.import.dispatch`; UID preconditions | one extra task round trip |
| W25-W29 | list snapshot | `clustarr-importlist` | ImportList controller | `importarr-worker`, `importarr` | snapshot revision; UID preconditions | one reconcile |
| W30 | `needsToken` | `clustarr-importlist` | ImportList controller | `importarr` | CAS on the Secret | a re-issued sync on a 401 |
| W31 | none | | index manager registration | `indexarr` | create-only | agent waits for the Secret |
| W32, W33 | session | `clustarr-indexer-sessions` | Indexer controller | `indexarr` | KV revision; Secret CAS | Secret lags KV by one reconcile |
| W34, W35 | query outcomes | `clustarr-indexer-health` | Indexer controller | `indexarr-worker` | CAS; counters monotone | under 1 s, with 30 s local suppression |
| W37-W44 | transfer and engine records | `clustarr-transfers`, `clustarr-engines` | owner key; DownloadClient controller | `catalogarr`; `grabarr` | Seq per entry; bootID | one reconcile |

---

## 8. Acks and updates in the manager

Owner decision 3: the manager receives the workers' acknowledgements and updates from the
streams and is the only writer of every CR. This section is the manager's stream-consumption
layer: what it consumes, how each signal reaches a CR, and how it survives a leader change.

### 8.1 What the manager consumes

Everything here starts only on the lease holder (a controller source, or a `k8s.LeaderOnly`
runnable), and the process exits on lease loss (split §5.8).

| Input | Mechanism | Consumer in the manager | What it drives |
|---|---|---|---|
| the fold's six records buckets | `recordsource` (S16-S21) | loop file key | the per-file blocks |
| `clustarr-transfers`, `-imports`, `-searches`, `-item-metadata` | `recordsource` with `item` refs (S25, S27-S29) | loop item keys; Search controller | entries, imports, candidates, metadata |
| `clustarr-engines` | `recordsource` | DownloadClient controller | engine status; a new `bootID` enqueues the owners pinned to it |
| `clustarr-indexer-health` | `recordsource` | Indexer controller | `WorkerFields`, the ladder |
| `clustarr-importlist` | KV watch (`UpdatesOnly`, today's key) | ImportList controller | the diff (§7.6) |
| `clustarr-indexer-sessions` | KV watch | Indexer controller | the session Secret mirror |
| `clustarr-artwork` | `objindex` (`pkg/events/objindex`), with its creation-time check (loop spec §4.9 amendment) | loop item keys (S26) | `status.artwork`, `status.overlay` |
| `clustarr-progress` `agent.*`, `encoder-limits.*` | reads through a 5 s cache; no watch | `app/dispatch`; the transcode ledger | admission (§5) |
| `CLUSTARR_INTAKE` | durables `catalogarr-intake-candidate`, `importarr-intake-scan` | item keys (candidates); `scanapply` | §6.6, §7.6 |
| `CLUSTARR_TASK_EVENTS` (new) | durable `clustarr-task-events` | `app/intake/advisory` | delivery state on the CR (§8.3) |
| `$JS.EVENT.METRIC.CONSUMER.ACK.<stream>.<durable>` | core subscription, no stream | `app/intake/advisory` | metrics only (§8.2) |
| `CLUSTARR_DLQ` | `clustarr-dlq-projector`, moved from agent `events` | DLQ projector | `clustarr.io/dead-lettered` (§8.5) |
| `CLUSTARR_EVENTS` | `catalogarr-history`, moved from agent `events` | history sink | Kubernetes Events |
| `CLUSTARR_ADVISORIES` (`MAX_DELIVERIES`) | the `clustarr-dlq-watch-*` durables, unchanged | `busconn.WatchDeadLetters` and every `Subscribe` | DLQ copies |
| PubAck of every manager publish | the publish call | the planner's effect | `Dispatch.delivery.state: published`; the dispatch ledger |

**New loop sources** (numbered after the loop spec's S1-S23): **S24** a Series or Comic
status change whose `DownloadsSignature` (covered numbers, phases, pending candidates) changed
→ the covered Episode or Issue keys; **S25** `clustarr-item-metadata` → the item key;
**S26** the manager's `objindex` on `clustarr-artwork` → the item key of the object's
`item` metadata; **S27** `clustarr-transfers` → the owner key; **S28** `clustarr-imports` →
the owner key; **S29** `clustarr-searches` → the item key and, for an Episode or Issue, its
container's key; **S30** the candidate inbox (`source.Channel`, §8.4) → the owner key at
`PriorityUser`. S9 (the Download watch) goes in A3. `TestOnlyTheLoopWatchesMediaFile` is
unaffected: none of these watches MediaFile.

### 8.2 Advisories

**Versions.** `github.com/nats-io/nats-server/v2 v2.15.0` and `github.com/nats-io/nats.go
v1.53.1` (`go.mod:28-29`, the new `jetstream` API throughout); the live server is
`nats:2.15.0-alpine` under kustomize (`config/nats/statefulset.yaml:28`) and 2.14.6 under the
chart's subchart (the installers wave's S11 pins one). Everything below exists in both:

| Subject (prefix, `server/jetstream_api.go:278-287`) | Payload (`server/jetstream_events.go`) | Raised |
|---|---|---|
| `$JS.EVENT.ADVISORY.CONSUMER.MSG_NAKED.<stream>.<consumer>` | `JSConsumerDeliveryNakAdvisory` (`:136`): `stream`, `consumer`, `consumer_seq`, `stream_seq`, `deliveries` | on every nak, delayed or not (`server/consumer.go:3249-3275`) |
| `$JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED.<stream>.<consumer>` | `JSConsumerDeliveryTerminatedAdvisory` (`:151`): the same plus `reason` | on every term (`consumer.go:3329-3360`); on a WorkQueue stream the message is removed |
| `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<consumer>` | `JSConsumerDeliveryExceededAdvisory` (`:122`) | on the next pull after a final delivery lapses (research `nats-worker-pools.md` §3.4) |
| `$JS.EVENT.METRIC.CONSUMER.ACK.<stream>.<consumer>` | `JSConsumerAckMetric` (`:106`): `stream_seq`, `deliveries`, `ack_time` | sampled, only when the consumer sets `SampleFrequency` (`nats.go jetstream/consumer_config.go:177-180`) |

**A stream of their own.** `CLUSTARR_ADVISORIES` captures only `MAX_DELIVERIES`
(`pkg/events/subjects.go:70-71`), feeds the dead-letter watchers, and on a single node is a
1 MiB memory stream with discard-old. Nak traffic there could evict a `MAX_DELIVERIES`
advisory before its watcher copies the dead letter, so naks and terms get a new stream,
`CLUSTARR_TASK_EVENTS`: subjects `MSG_NAKED` and `MSG_TERMINATED` for the dispatched durables
only (the §5.1 list, rendered from the topology, never `>`), WorkQueue, not Durable, MaxBytes
8 MiB (its single-node share is small, and losing one is losing visibility, not work),
MaxAge 1 h. One durable, `clustarr-task-events`, leader-only in the manager.

**From an advisory to a CR.**

- **Naked:** the message is still in its stream, so the intake reads it by
  `stream_seq` (`jetstream.Stream.GetMsg`, `nats.go jetstream/stream.go:548`) and takes its
  subject and `Clustarr-Id` header (the clustarr header W4.91 added because a fired schedule
  strips `Nats-Msg-Id`). The resolver registry (the DLQ projector's
  `app/catalog/history/target.go` resolvers, extended to every dispatched subject) maps it to
  the CR key and the dispatch seq the Msg-Id carries.
- **Terminated:** on a WorkQueue stream the message is gone, so natsbus terms with
  `TermWithReason(<Clustarr-Id>)` (`nats.go jetstream/message.go:395`) and the intake resolves
  from `reason`.
- **Stale:** an advisory whose seq is below the CR's current dispatch seq is acked and
  dropped.
- **Unresolvable:** acked, counted in `clustarr_task_events_total{outcome="unresolved"}`.

**Ack sampling is metrics only.** `events.ConsumerSpec.SampleFrequency` (new, opt-in; `10%` on
the dispatched durables, `100%` on the engine durables, whose volume is small) feeds
`clustarr_task_ack_delay_seconds{consumer}` and `clustarr_task_deliveries{consumer}`. It is
not mapped to CRs: a WorkQueue stream deletes an acked message, so its subject cannot be read
back, and the task's answer record is already the authoritative "done".

### 8.3 A task's lifecycle on the CR

The fold's `Dispatch` (loop spec §2.3; `seq`, `answeredSeq`, `dispatchedAt`, `withdrawn`)
gains two optional fields, so every dispatched block (MediaFile's, the item's
`searchDispatch`, a grab entry's, an import's) shows where its task is:

```go
type Dispatch struct {
	// … the fold's fields …

	// Destination is the durable, pool or engine instance the task went to.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Destination string `json:"destination,omitempty"`
	// Delivery is what the manager knows about the task between publish
	// and answer. Written on transitions only (below), never per nak.
	// +optional
	Delivery *DeliveryState `json:"delivery,omitempty"`
}

type DeliveryState struct {
	// +kubebuilder:validation:Enum=waiting;published;claimed;retrying;deadLettered
	State DeliveryPhase `json:"state"`
	// Reason when waiting or retrying: Budget, Paced, NoAgent,
	// NoCapableAgent, EngineNotReady, Nak.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Message string `json:"message,omitempty"`
	// Attempts is the deliveries count of the last nak the manager recorded.
	// +optional
	Attempts int32 `json:"attempts,omitempty"`
	// +optional
	LastNakAt *metav1.Time `json:"lastNakAt,omitempty"`
	// +optional
	DeadLetteredAt *metav1.Time `json:"deadLetteredAt,omitempty"`
}
```

| Task state | Signal | Rendered as |
|---|---|---|
| due, not admitted | `dispatch.Admit` refuses (§5.4) | `delivery.state: waiting`, reason `Budget`, `Paced`, `NoAgent`, `NoCapableAgent` or `EngineNotReady` |
| published | the manager's own effect, after the apply, and its PubAck | `seq > answeredSeq`, `destination`, `delivery.state: published` (rendered in the apply that issues the seq) |
| claimed | the record's `claimed` (transcode, graft) or `deferred` (markers) | `delivery.state: claimed` |
| in progress | heartbeats (`InProgress`) and `clustarr-progress` | nothing in etcd (loop spec §4.11) |
| naked | `MSG_NAKED` | `delivery.state: retrying`, `attempts`, `lastNakAt` on the **first** nak and when `deliveries` reaches `MaxDeliver − 1`; other naks only in metrics |
| answered | the record at `seq` (`answered`, `failed`) | incorporated; `answeredSeq = seq`; `delivery` cleared |
| terminated | `MSG_TERMINATED`, then the DLQ copy | `delivery.state: deadLettered`, `deadLetteredAt`; the CR's `DeadLettered` condition from the annotation (§8.5) |
| dead-lettered | `MAX_DELIVERIES` → the watcher's DLQ copy → the projector | as terminated |
| timed out | the planner's own timeout from `dispatchedAt` | the planner's rule (republish or fail), as the fold has it |

The two fields add at most about 450 bytes to a block. The fold's MediaFile budget
(loop spec §2.11.3) gains them for its three dispatches (≈ 1.4 KB); the A1 task re-runs
`TestMediaFileAtEveryCapFitsTheBudget`.

### 8.4 Leader-only consumption and replay after a leader change

- **Durables keep their state on the server.** A new leader binds the same durables
  (intake, task events, DLQ projector, history); whatever the old leader had not acked is
  redelivered after `AckWait`.
- **Intake is acked only after its decision lands.** The candidate consumer parks each
  candidate in a leader-local inbox keyed by owner, enqueues the owner key at
  `PriorityUser`, and holds the message with `InProgress` heartbeats until that owner's pass
  reports the candidate incorporated, pended or refused (a channel keyed by Msg-Id), within a
  60 s handler budget (`ConsumerSpec.HandlerTimeout`); on timeout it naks with a 10 s delay.
  `MaxAckPending` (64) bounds the inbox. A leader crash loses the inbox and the server
  redelivers. The scan applier writes, then acks; a crash between them re-applies an
  idempotent create-or-update.
- **Records replay through status.** `recordsource` opens with `UpdatesOnly`; the informer's
  initial list reconciles every key, and every pass reads the records of its outstanding
  dispatches (loop spec §4.9), so a record written while no leader ran is read by the next
  leader's first pass.
- **Ledgers are rebuilt, never trusted.** The dispatch ledger (§5.4) and the transcode
  ledger (fold §5.6) are rebuilt from status at start; until a ledger reports rebuilt, its
  `Admit` refuses with reason `Rebuilding`.
- **Advisories are history, not state.** A task-events message lost in a leader change costs a
  stale `delivery` field until the next transition; correctness rests on records and status.

### 8.5 With `busconn.WatchDeadLetters` and the DLQ projector

- The `MAX_DELIVERIES` watchers are unchanged: every `Subscribe` binds its
  `clustarr-dlq-watch-<stream>-<durable>` watcher, and the manager's leader-only
  `busconn.WatchDeadLetters` (`pkg/busconn/deadletters.go:34`, `internal/cli/manager/run.go:102-106`)
  backs up consumers with no live subscriber. Each copies the lapsed message to `CLUSTARR_DLQ`
  and deletes a WorkQueue original.
- The DLQ projector moves from agent `events` (`app/catalog/agent/events/register.go:66`, an
  `EveryReplica` runnable) into the manager as a leader-only consumer, same durable
  (`clustarr-dlq-projector`), same field manager, same annotations and Events. Its resolvers
  learn the new subjects (engine commands, import inspect and execute, intake) and map
  Download-keyed v1 subjects to the owner through `remediation.item.download` during
  release N.
- The task-events intake is a second net for a term that produced no DLQ copy (the research's
  M2 case, a copy refused with 10188, which W4.91 fixed at the source): when a
  `MSG_TERMINATED` advisory resolves to a CR and no DLQ copy with its `Clustarr-Id` arrives
  within 60 s, the intake annotates the CR through the projector's code, under
  `clustarr-dlq-projector`.
- The history sink moves the same way and keeps its durable and recorder.
- `TestOnlyTheManagerEnsuresTopology` (split §5.15) is unchanged: the manager creates every
  durable, including the per-engine ones (§6.7) and the new intake and task-events durables;
  agents only bind.

---

## 9. RBAC end state and guards

### 9.1 Roles

The identities are split §4.6's; the roles are generated from markers as there. After this
design no agent package carries a write marker, so the generated agent roles hold only:

| Identity | Reads (`get`, `list`, `watch`) | `secrets` | Writes |
|---|---|---|---|
| `agent-catalog` | the ten media kinds, `mediafiles`, `qualityprofiles`, `delayprofiles`, `indexers`, `overlayprofiles`, `searches` | none | none |
| `agent-events` | the media kinds, `mediafiles`, `qualityprofiles`, `delayprofiles`, `indexers` | none | none |
| `agent-metadata` | the eight metadata kinds, `metadataproviders` | `get` (provider keys) | none |
| `agent-import` | the media kinds, `mediafiles`, `rootfolders`, `libraryscans`, `importlists`, `importexclusions`, `qualityprofiles` | `get` (list credentials, the Trakt token) | none |
| `agent-index` | `indexers`, `indexerdefinitions`, `indexerproxies` | `get` (credentials, the session and facade Secrets) | none |
| `agent-caption` | `subtitleprofiles`, `subtitleproviders` (the fold's) | `get` | none |
| `grabarr-engine` | `downloadclients` | `get` (proxy and provider Secrets) | none |
| `markers` | none: a ServiceAccount with no binding | | |
| transcode pools and graft Jobs | none: `AutomountServiceAccountToken: false` | | |
| `manager` | everything it reconciles | `get`, `create`, `patch` (its own Secrets: facade key, sessions, Trakt tokens, the External Metrics CA) | every write in this design; `downloads` `get`, `list`, `watch`, `update`, `delete` in release N only (§10) |

No agent role grants `events`, `leases`, any `*/status`, `*/finalizers` or `*/scale`. The
lists are what the markers produce from the code after A3-A6; the A7 task regenerates them,
and the table above is the check.

**Makefile.** `RBAC_ROLES` is unchanged from split §4.6. `RBAC_PATHS_grabarr-engine` drops
`./app/grab/status`, which goes. The write markers move with their code into the manager
packages (`app/grab/lifecycle`, `app/catalog/grabplan`, `app/import/importplan`,
`app/import/manager/scanapply`, `app/intake`, `app/dispatch`), which join
`RBAC_PATHS_manager`.

### 9.2 Guards

| Guard | Where | Asserts |
|---|---|---|
| `TestNoAgentRoleGrantsAWriteVerb` | `test/guards` | every agent identity's generated role, and its chart copy: verbs ⊆ {`get`, `list`, `watch`}; no `*/status`, `*/finalizers`, `*/scale`, `events` or `leases`; `secrets` only `get`; the markers SA unbound; pool and graft pods mount no token |
| `TestAgentsCallNoKubernetesWriter` | `test/guards` | type-checked (`golang.org/x/tools/go/packages`, because `events.KV` also has `Create`, `Update` and `Delete`): in every module package `go list -deps` reaches from the agent roots (`app/catalog/agent/{catalog,events,metadata}`, `app/import/agent`, `app/indexer/agent`, `app/caption/agent`, `app/grab/agent/{torrent,usenet}`, `cmd/markers`, `cmd/transcode`), no call to `k8s.PatchStatus`, `PatchStatusCAS`, `Apply`, `EnsureFinalizer`, `RemoveFinalizer` or `ScaleTo`; no `Create`, `Update`, `Patch`, `Delete`, `DeleteAllOf`, `Status` or `SubResource` on a value implementing controller-runtime's `client.Writer` or a client-go typed client; no `GetEventRecorder`, `GetEventRecorderFor` or `record.EventRecorder` |
| `TestAgentsPublishNoTask` | `test/guards` | in the same packages, no `Publish` whose subject comes from a `Work*Subject` builder or the engine command builder: agents publish only to `clustarr.intake.>`, `clustarr.rel.>` and `clustarr.evt.>` |
| `TestAgentLinksNoPlanner` | `test/guards` (extends split §4.5.3's `TestAgentLinksNoManagerRegistration`) | the agent roots link none of `app/catalog/grabplan`, `app/grab/lifecycle`, `app/import/importplan`, `app/import/manager/...`, `app/intake`, `app/dispatch`, `app/remediation` |
| `TestEveryFieldManagerHasItsHome` | `test/guards` (W5.19) | every `k8s.Manager*` but `ManagerUI` has the one home `cmd/manager`; the multi-home allow-list (`catalogarr-grab`, `importarr`, `importarr-worker`, `grabarr-engine`) is empty; `grabarr-engine` is in `RetiredFieldManagers()` |
| `TestRecordsWritersAreTheirAgents` | `test/guards` (extends the fold's `TestRecordsBucketsOpenOnlyThroughTheirStores`) | each new bucket's writer API is constructed only in its writer's package |
| `starttest.AssertWroteOnlyAs` | `test/starttest` | each agent identity's RBAC-enforced start writes nothing |
| `TestAgentClientIsReadOnly` | `internal/cli/agent` | `AgentManagerOptions` wraps the client in `k8s.ReadOnly` (writers return `k8s.ErrAgentWrite` and count `clustarr_agent_write_refused_total`) and gives the manager a discarding event recorder |

The runtime wrapper is the belt to the guards' braces: an agent write that slipped past both
fails loudly and is counted, rather than succeeding where RBAC was misconfigured.

---

## 10. Migration

Nothing here has run. Like the fold, the change deploys as two releases; every
kind-cluster-plex step needs the owner's explicit OK.

### 10.1 What has to survive

On 2026-10-06 kind-cluster-plex held 122 Downloads (ADR-0016's Context): seeding torrents,
finished usenet jobs, blocklisted releases with `blocklistedUntil`, possibly held imports, and
any user `spec.paused` or `spec.priority`. Seeding must not stop, no torrent may rehash, no
payload may be deleted, no blocklist entry may be lost, a held import must keep its 24-hour
clock, and a user's pause must hold. The release-N report (§10.2) counts each before anything
is changed.

### 10.2 Release N: adopt; the Download kind becomes read-only

- **Placement.** Recommended in the same release N as the split and the fold (Q2), sharing the
  fold's `app/catalog/legacyfold` Migrator, its `--legacy-fold=hold|apply` and
  `--legacy-fold-retain` (72 h) flags, its report command and its runbook.
- **No writer.** Release N removes the Download controller, the engines' Download
  reconcilers, the blocklist sweeper, `directgrab`, `retrigger` and `replay-download`. The CRD
  stays, so the objects stay readable for adoption and rollback.
- **Adoption in the owner key** (`app/remediation/adopt`, beside the fold's): for every
  Download whose controlling owner reference is this owner (a pack's is the Series) and whose
  UID is in neither `status.downloads[].uid` nor `status.legacyDownloads.adopted`:
  - **live** (`Pending` through `Seeding`, or `Imported` and still seeding): an entry with
    `id` = the Download's name, `uid` = its UID (so `clustarr-progress` `download.<uid>` and
    every dead letter keep resolving), `client` = `spec.clientRef`, `engine` =
    `status.engine`, the release clamped from `spec.release`, `source`, `purpose`, the
    episodes or issues from `spec.target.keys` through the cache, seed criteria, effective
    removal flags, `phase`, `stage`, `failureReason`, `outputPath`, the timestamps, the import
    summary from `status.import`, and `dispatch.seq = records.NextSeq(0, 0, now)` with
    `answeredSeq` 0, so the first command is owed;
  - **blocklisted:** a blocklist entry with `until` = `blocklistedUntil`;
  - **terminal with no transfer and no payload** (`Failed`, or `Imported` with its transfer
    removed and nothing at `outputPath`): nothing, recorded as dismissed;
  - **`Imported`, transfer removed, payload still on disk** (today's leftover, kept until the
    Download is deleted): in `apply` mode only, an entry in `Removing` with
    `removeData: removeDataOnDelete`, so the payload goes as §6.4 now removes it; the
    pre-flight report counts these and their bytes first (Q6).
  `status.legacyDownloads` (release N only, on the six owner kinds,
  `{adopted: [{name, uid, outcome, at}], held, message}`, MaxItems 64) records it; an owner
  with more live Downloads than its entry cap is `Held: TooMany` and reported, never
  truncated.
- **The engines re-attach without Downloads.** A release-N engine pod starts from its journal
  (the torrent descriptor's `name` and the usenet manifest's `name` are the Download name,
  which is the entry `id`), writes a transfer record for each transfer, and waits for
  commands. The first command for an adopted entry fills the journal's `entryUID` and `seq`.
  The torrent `imported` flag, which only Download status held, arrives in the command.
  Nothing is removed: an entry nobody adopted is an orphan (§6.7), reported, kept.
- **The Migrator** (leader-only, field manager `clustarr-legacy-fold` for annotations, the
  default owner for finalizer Updates), every 60 s:
  1. **Intent:** copies a non-default `spec.paused` or `spec.priority` into the owner's
     `download.clustarr.io/paused` or `priority` annotation, and an `import-target` or
     `import-override` into a `download.clustarr.io/import` intent with nonce
     `fold-<generation>`, before adoption (the fold's §7.3.5 order).
  2. **Held imports:** seeds `clustarr-imports` at `RecordSubKey(uid, "inspect")` from
     `status.import` (`records.Seed`), so the hold keeps its `heldSince`.
  3. **Source hashes:** backfills MediaFile `spec.importedFrom.infoHash` from the Download its
     `downloadRef` names, through `mediafilespec.Apply` with compare-and-swap under
     `importarr-worker`.
  4. **Finalizers:** once the owner records an adoption, strips `download.clustarr.io/download`
     and `download.clustarr.io/engine` from that Download, so deleting it later runs nothing.
  5. **Census and deletion:** deletes an adopted Download only after `retain` and only with
     both finalizers gone.
- **Report.** `manager legacy-fold report` gains Downloads: counts by phase, blocklisted, held
  imports, intents carried, the adoption plan per owner, owners over cap, and transfers no
  Download names.
- **Runbook** (amends loop spec §7.5): step 5's quiesce stops grabs and searches under the old
  release (scale its search, grab and RSS consumers to zero) and lets running imports finish;
  the engines keep seeding until the DownloadClient controller re-renders them with the new
  image; step 10 checks `clustarr_legacy_fold_objects{kind="Download",state="unadopted"}` is 0
  and that every torrent re-attached without a hash check (`Client.Info`, the engine log).

### 10.3 Release N+1: the CRD goes

- **Code:** the `Download` type and `DownloadList` (`api/download/v1alpha1/download_types.go`;
  `DownloadClient` and the `download.clustarr.io` group stay), their applyconfigurations
  (`git rm`, the generator does not delete), `status.legacyDownloads`, the Migrator's Download
  part, the v1 `ImportTask` decoder, `k8s.ManagerGrabarrEngine` from
  `RetiredFieldManagers()`, `pkg/k8s/deadletter.go`'s Download kind, the `ReplayKinds` entry,
  `pkg/crdcheck/download_cel_test.go` (its rules now test `DownloadSource` in
  `api/common/v1alpha1`).
- **NATS:** `clustarr-leases` and `clustarr-pending` deleted, and the `catalogarr-grab` and
  `catalogarr-redownload` durables retired through `Topology.Retired` after their drain gate.
- **Installers:** `downloads` leaves every role and the chart copy; `config/crd/kustomization.yaml:40`
  and `config/crd/bases/download.clustarr.io_downloads.yaml` go; `crdbases_test.go` and
  `test/e2e/main_test.go`'s `expectedCRDCount` drop by one.
- **The CRD by hand** (Helm never deletes CRDs, `charts/clustarr/crds/README.md:21-40`):
  `kubectl delete crd downloads.download.clustarr.io` after the census reads 0.

### 10.4 Rollback

| From → to | When | Effect | Steps |
|---|---|---|---|
| N (`hold`) → previous | any time | Lossless: nothing adopted or stripped; the previous engines re-attach from the Downloads | the fold's §7.6 steps 1-6 |
| N (`apply`) → previous | within `retain` | Adopted Downloads are present and frozen at adoption, without finalizers (the previous controllers re-add theirs on their first reconcile, `controller.go:173`, `torrent/reconciler.go:160`). Grabs made under N have no Download, and the previous reapers would remove their transfers (torrent: the transfer, not its data; usenet: its scratch directory) | **before** starting the previous engines: `bin/manager downloads export --namespace <ns>`, which creates a Download for every entry that has none (spec from the entry; status phase, engine, output path and import from the entry and its records) under `catalogarr-grab`; then §7.6 |
| N (`apply`) → previous | after deletion | Lossless for live transfers with the export, which recreates every Download from the entries; blocklist entries are exported as blocklisted Downloads | as above |
| N+1 → N | | Lossless: the entries hold everything | re-apply N's CRDs (`git show`), then `helm rollback` |

`manager downloads export` is the only code that writes a Download in N or N+1; it runs from
an operator's shell, not in any deployed process, and refuses to run while a manager holds the
lease.

---

## 11. Sequencing against the plan

The plan (`docs/superpowers/plans/2026-10-06-manager-agent-split.md`) and its ledger
(`.superpowers/unify/progress.md`) are not edited here; the plan is revised after the owner
reviews this design. State on 2026-10-07: Waves 0-5, 4f, 7 and 8 are built; the fold's F0 is
done, F1 and F2 are in progress (records core, MediaFile types), F3 runs next.

### 11.1 Planned tasks this design changes

| Task | Change |
|---|---|
| **F3.1-F3.6** (loop core, probe, naming, markers, rename and replay) | None. F3 proceeds as written. |
| **F4.1-F4.4** (item keys) | Proceed as written; three notes for A3 to pick up: S9 (the Download source) and `TestARefusedImportOverATranscodedFileStopsReadingDownloading` stay until A3 replaces them with entries and S24; F4.2's `itemstatus.Apply` stays `catalogarr`-only (A4 and A5 add the other managers' applies, §7.0); the `.spec.target.<kind>` Download indexes stay until A3 |
| F5.1-F5.5 (subtitles) | None |
| F6.1-F6.7 (transcode, admission) | None; A2's `app/dispatch` sits beside the transcode ledger and does not change it |
| **F7.1** (donor on the item) | Derives `status.audio.donor` from the owner's `audioDonor` entry instead of an imported Download, if A3 has landed (the recommended order); otherwise A3 amends it |
| **F8.3, F8.4, F8.5** (Migrator, adoption, report, flags) | A8 extends them to Downloads: one Migrator, one report, one flag pair |
| **F8.6** (dead letters and replays target MediaFile) | Edits the DLQ projector after A2 moves it into the manager; its paths change accordingly |
| **F8.7** (ui projection) | Its MediaFile half unchanged; A3 rewrites the Downloads half |
| **F8.9** (e2e) | Scenario 1's download legs follow §6.13 |
| **F8.11** (split spec amendments) | Adds this design's supersessions of split §3.5.3, §4.6, §5.2, §5.3, §10.2.4 and loop spec §4.14 |
| **F8.12** (fold gate) | Adds §9.2's guards |
| **F9.1-F9.5** (N+1) | A9 removes Download alongside |
| W4.20-W4.25 (indexer sessions, built) | The agent stops writing the Secret (§7.5); the KV `Drop` compare-and-swap stays; `TestSessionDropDoesNotWipeANewerSave` keeps its KV case and loses its Secret case |
| W4.82 (`PatchOverlay` CAS, built) | Superseded by A5 (overlay status from object metadata) |
| W4.90-W4.100 (bus, built) | Used as built: bind-only `Subscribe` (the manager creates every durable, engines' too), `HandlerTimeout` (the candidate intake's budget), the `Clustarr-Id` header (the term reason), leader-only `ConsumerState` (capacity) |
| W4.60-W4.75 (autoscale, built) | The HPA metric is unchanged; the QueueGauge gains `clustarr_dispatch_waiting` and `clustarr_dispatch_unattended`; `agentdomain.Domains()` changes: `catalog` loses `catalogarr-grab`, `events` keeps only `catalogarr-rss-matcher` (history, redownload and the projector leave) |
| **W5.19** (`TestEveryFieldManagerHasItsHome`) | Its multi-home allow-list empties (A7) |
| **W6.2** (`TestEveryConsumerHasExactlyOneHome`) | Learns the manager-hosted durables (two intake, task events, projector, history) and the per-engine durable family; the retired ones leave |
| **W6.9** (one ClusterRole per identity) | Generates read-only agent roles once A3-A6 have moved the write markers; A7's role guard runs with it |
| **W6.11** (topology switch) | The `events` Deployment carries only the RSS matcher; the manager's grace covers its intake durables' 60 s budget |
| W6.15 (e2e renames) | The download helpers of §6.13 |
| **W10.2** (ADR-0018) | Cites ADR-0019 for agent writes and policy |
| **W10.4, W10.5, W10.7** (design of record, docs, CLAUDE.md) | Rewrite the invariants this design changes: "One controller-writer per resource" and the MediaFile split (both writers are now in the manager), "Kubernetes watches are the default coupling" (agents couple through NATS only), the Download, blocklist and grab-lease text, `CLAUDE.md:7`'s `kubectl get` line (to `kubectl get movies,series,mediafiles`), and the ADR index refinements for 0008 and 0016 |

### 11.2 New waves

| Wave | Content | Depends on |
|---|---|---|
| **A0** | Owner review of this design; plan revision (tasks for A1-A9, the amendments of §11.1) | — |
| **A1** API and topology (serial, one owner) | item status fields of §6.2 and §6.10; `DownloadSource` and the enums to `api/common`; MediaFile `importedFrom.infoHash`; `Dispatch.destination`, `delivery`; `schema` types (`Candidate`, `ScanObservation`, `EngineCommand`, `TransferRecord`, `EngineRecord`, the import, search, metadata and indexer-health records); `RecordHeader.Item`; the six buckets, `CLUSTARR_INTAKE`, `CLUSTARR_WORK_ENGINE`, `CLUSTARR_TASK_EVENTS`; `ConsumerSpec.SampleFrequency`; `StreamAdmin.EnsureConsumer`/`DeleteConsumer`; `k8s.ReadOnly`; the budget test | F4.4 |
| **A2** manager intake and acks | `app/intake` (the candidate inbox with ack-after-decision, the scan-applier skeleton), `app/dispatch` (ledger, `Admit`, budgets, unattended detection), agent presence (writer in every agent, reader in the manager), the task-events intake, natsbus `TermWithReason`, the DLQ projector and history sink moved into the manager | A1 |
| **A3** downloads | Series and Comic as loop keys; `app/grab/lifecycle`; entries, blocklist and intents on the owners; engines on commands, records, journal and presence; the DownloadClient controller from engine records with per-engine durables; the two-phase import (`importplan`); readers (§6.11), S24; the ui Downloads page | A2 |
| **A4** grab and search policy | `app/catalog/grabplan` (candidates, delay, `pendingGrab`); the search planner (the per-item wanted sweep, `IndexOnly`, the donor cap, the `search` Pacer class); search answers in records; RSS candidates; the Search controller on records and candidates; leases, pending, scheduled grabs and redownload retired | A3 |
| **A5** metadata and artwork | metadata records; the manager's `objindex`; the fetch and overlay plans in the item key; Events from the manager | A2 (beside A4, after A3 for the item keys) |
| **A6** import, lists, indexers | scan observations and `scanapply`; the rescan rename pass retired; the list diff in the ImportList controller; Trakt tokens in the manager; the Indexer health ladder and RSS scheduling in the Indexer controller; the session mirror; the facade key in the manager | A2 (parallel with A4 and A5: no shared packages) |
| **A7** RBAC and guards | §9's roles and guards | A3-A6; before W6.1 |
| **A8** release N migration | adoption, the Migrator's Download part, the report, `downloads export`, the runbook, e2e | A3-A6; runs inside F8 |
| **A9** release N+1 removal | §10.3 | F9's position (after W10.8) |

**Recommended order:** F3 → F4 → A1 → A2 → A3 → A4 → A5 (A6 beside A4 and A5) → F5 → F6 → F7
→ F8 with A8 → A7 → W6 → W6b → W7 → W8 → W9 → W10 → F9 with A9.

The item-status work lands while F4's item keys are fresh, F7.1 is written once against
entries, and release N's Migrator is built once for both migrations. The alternative (A after
F7) keeps the fold's momentum and costs one amendment each to F7.1 and the F8 Migrator (Q3).

---

## 12. Open questions for the owner

Only decisions that are the owner's; each with a recommendation.

**Q1. Orphan transfers.** A transfer an engine holds that no entry claims (after an etcd
restore, a bug, a hand-added torrent). Today the reapers remove it after 10 minutes (the
torrent transfer but not its data; the usenet scratch directory). **Recommend report-only:**
listed on the DownloadClient (`OrphansPresent`, the count, an Event), removed only by the
`download.clustarr.io/remove-orphan` intent, which the ui offers. Data safety (owner decision:
never delete user data on an absence) outweighs a few idle transfers. The alternative is a
per-DownloadClient opt-in, `spec.orphans: remove`, after 24 hours.

**Q2. Which release.** **Recommend the fold's release N:** one quiesce, one runbook, one
Migrator, and the engines roll once at cutover anyway (split §3.5.5). The alternative, a
separate pair after N, lowers release N's blast radius and needs its own quiesce and its own
two rollouts.

**Q3. Wave order.** **Recommend A1-A6 between F4 and F5** (§11.2). The alternative is after
F7.

**Q4. Imports in two phases.** Decision 4 puts "attribute, create or delete this file" in the
manager, which splits fileimport into inspect and execute (§6.9) and adds a task round trip
per import, about a second. **Recommend accepting it.** The alternative is an explicit
exception: fileimport keeps deciding with inputs the manager sends in the task, and the
manager only accepts or rejects the finished import, which keeps one round trip but leaves
the upgrade and remediation rules in an agent.

**Q5. Blocklist cap.** A per-item blocklist in status must be capped: 32 entries for a movie,
album, book or audiobook and 64 for a series or comic, each kept until its 90-day `until`
(today's TTL), the oldest dropped first with an Event. A dropped release could be grabbed
again. **Recommend these caps;** an item with 32 bad releases in 90 days is already a case
for a person. The alternative is a KV blocklist keyed by info hash and guid with a TTL, which
is unbounded but invisible to `kubectl` and lost with NATS.

**Q6. Leftover payloads of imported grabs.** Today a torrent removed after its seed goal
(`removeCompleted`), or a usenet job removed on import, leaves its payload on disk until the
Download object is deleted, which in practice is when its item is deleted. A grab entry is
dropped once its transfer is removed, so this design removes the payload with the transfer
when `removeDataOnDelete` is true (the default): the library holds its own hard link or copy,
so only the download area shrinks. At release N the leftovers of earlier imports would be
removed in the same way (§10.2), after the pre-flight report has counted them and their bytes.
**Recommend this** (Sonarr's "Remove Completed" removes the data too). The alternative keeps
each payload as a retained, transfer-less entry until the item is deleted, which grows every
owner's status by an entry per upgrade.

**Q7. `kubectl get downloads`.** It goes. **Recommend** the `Download` print column and the
`status.downloadPhase` selectable field on every item kind (§6.2), plus the ui's Downloads
page. A small read-only `bin/manager downloads list` (all entries across kinds, from the
cache) is cheap to add if a single table matters on the command line.

---

## 13. Risks

- **The manager does more.** Planners for grabs, imports, lists, indexer health and routing
  join the loop on one leader. Mitigations: priorities (a user's action is never behind a
  sweep), planner isolation (loop spec §3.6), admission budgets, and the per-planner metrics.
  Trigger: reconcile queue depth or p99 latency.
- **Series status size and write rate.** Every episode grab writes the Series; every Series
  write reaches every Series informer (the manager, the ui, cluster-plex). Phase transitions
  are a handful per grab, fewer than today's Download writes, but on one larger object. The
  budget test holds the size; cluster-plex's Series handlers must ignore `status.downloads`
  (its `SeedKey` is metadata only; verify in cluster-plex before release N, read-only).
- **Engine re-attach by name at adoption.** The first release-N engine maps journal entries to
  entries by the Download name. A name mismatch would make an orphan, which is kept, not
  removed (§6.7); `TestEngineRestartKeepsSeeding` and the adoption e2e hold it.
- **Two-phase imports** add a state the import planner must get right (an inspect answered,
  then the item changed before execute). The execute task carries the plan's basis (the
  MediaFiles it replaces, by UID and resourceVersion), the agent refuses a stale plan, and the
  planner re-inspects.
- **Advisory volume.** Nak advisories on busy durables are many; they go to their own small
  stream and are written to a CR only on the first nak and the last.
- **Rollback writes Downloads again.** The export tool is release-N code that must stay
  correct for a release it never runs under; the upgrade-and-rollback e2e scenario
  (F8.9's) covers it.
- **A lost wake costs latency, never correctness**: every outstanding dispatch requeues on its
  own timeout, and every pass reads its records.
