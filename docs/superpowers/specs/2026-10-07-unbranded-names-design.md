# Unbranded names: the *arr brands leave code and wire, migrated in release N (Wave U)

**Status:** Accepted design (owner decisions of 2026-10-07 are binding; §10 holds the only open
questions). **Plan:** `docs/superpowers/plans/2026-10-07-unbranded-names.md` (Wave U, U1-U7, and the
amendments to F8.3-F8.5, A8, W6, W10, F9/A9). **Branch:** `unify-manager-agent`, worktree
`/home/appkins/src/mediactl/clustarr-unify`. **Inventory basis:** `cae73ac6` (A3.5 committed, A3.6
in flight); every count below is from `git grep` at that commit, which reads committed content
only. A3.6-A3.10 add more names (engine durables, the engine recorder); U1 re-runs the inventory at
its start and the gate (§7) is the inventory from then on.

## 0. Summary

The owner (2026-10-07): "rename anything internal named like 'catalogarr' to simply 'catalog' --
the 'branded' naming is not necessary in the internal libraries and creates mis-spelled
confusion." Scope: **everything, migrated** -- Go identifiers, packages, files, errors, logs,
comments, CRD descriptions, docs, user agents, Event sources and reasons, CLI flags, and the stored
and wire names (field managers in `managedFields`, NATS streams, durables, subjects, RPC subjects,
queue groups, KV, annotation and label keys and values, metric names and label values, the
Kubernetes objects clustarr creates, Helm values keys), using the domain and binary names. The
product name **clustarr** stays (module path, API groups `*.clustarr.io`, the `clustarr_` metric
prefix, `clustarr-*` buckets, the `CLUSTARR_*` stream prefix). This supersedes split ruling R7
("field manager / durable names stay") for the brand only.

At `cae73ac6` the seven brands appear on **9,941 lines in 986 tracked files** (catalogarr 3,629
occurrences, indexarr 2,724, importarr 1,874, squasharr 1,766, grabarr 1,286, captionarr 920,
segmentarr 334), plus 221 Go occurrences of the derived `squash` (the `app/squash` tree). Most are
prose. The **stored** names are few and concentrated: 19 field managers on about 33,000 objects, 6
work streams, 20 durables and one dynamic durable family, 4 wire schema names, 3 label or annotation
keys and 4 label values (on Jobs N recreates, the engine workloads' selector and the facade Secret),
1 finalizer, 1 kept Secret with data, 1 ServiceAccount, 6 legacy leases and 10 values keys.

**How it lands.** Wave U renames the code alone, right after A3 and before A4/A5/A6, behind one
legacy-name table, `pkg/legacynames`, the only Go package allowed to spell an old name. Release N's
one Migrator (F8.3-F8.5, A8) renames `managedFields` entries in place, NATS topology code displaces
the old work streams and hands the two event-stream durables over at their ack floor, and the
rollback gains `manager legacy-fold restore-names`. A gate (§7) forbids the brands everywhere but the
legacy table, the historical docs, three cited historical paths and the chart README's 0.4.x
upgrade section.

## 1. The naming rule

**Token map** (case variants follow: `Catalogarr`→`Catalog`, `CATALOGARR`→`CATALOG`):

| Brand | New | Brand | New |
| --- | --- | --- | --- |
| `catalogarr` | `catalog` | `squasharr` | `transcode` |
| `importarr` | `import` | `captionarr` | `caption` |
| `indexarr` | `index` | `segmentarr` | `markers` |
| `grabarr` | `grab` | `squash` (derived: `app/squash`, `squashmanager`, `origSquash`) | `transcode` |

Rules, each decided here (cost if wrong in brackets):

- **R-U1 Mechanical by default.** A name is the token map applied to the old name; nothing else in
  it changes. In particular `clustarr.work.segmentarr.markers.normal.<key>` becomes
  `clustarr.work.markers.markers.normal.<key>` (the doubled token is the stream's domain followed by
  the task, as `clustarr.work.catalog.metadata` is) and `app/indexer` stays (not branded). [A later
  cosmetic rename is a second migration.]
- **R-U2 One exception: the transcode pool durables.** `squasharr-transcode-<profile>-<class>`
  becomes `transcode-pool-<profile>-<class>`, not `transcode-transcode-…`: the doubled word says
  nothing, and `transcode-pool-` mirrors the pool Job `transcode-pool-<profile>-<class>` (old
  `squasharr-pool-…`) and gives the withdrawal path an unambiguous prefix on its stream. [None:
  the old family is deleted with its stream, §4.2.]
- **R-U3 Retired names are never renamed.** A name the release that introduces it into N already
  retires keeps its old spelling, in `pkg/legacynames`: field managers `catalogarr-markers` (released
  by F8.4) and `grabarr-engine` (retired by A3.6; its Downloads are deleted by A8.2); durables
  `catalogarr-grab`, `catalogarr-redownload` (draining, R22), `squasharr-transcode-results`,
  `catalogarr-segments-result` (loop §7.3.9); the TranscodeJob finalizer
  `squasharr.clustarr.io/task-withdrawal`; the six `<svc>.clustarr.io` leases. Renaming them would
  write a name no release ever owns. [Wasted writes; no data risk.]
- **R-U4 Go keywords and shadowing.** An identifier the map would turn into a keyword or a name
  that shadows an imported package takes `<domain><last path element>`: the import alias
  `importarr` (for `app/import/agent`) becomes `importagent`, not `import`; a local `squash` in a
  file that imports `pkg/transcode` becomes `transcodeRun`. The compiler finds every case.
- **R-U5 Prose.** In live docs, comments, log and error text a brand naming a component becomes the
  domain word (`catalog`, `the import agent`, `the transcode controller`); a sentence the map makes
  read badly ("transcode transcodes") is reworded, never left branded. A historical narrative in a
  live doc ("catalogarr crash-looped on 2026-09-29") names the component by its new name ("the
  caption service crash-looped").
- **R-U6 The product stays.** `clustarr`, `Clustarr`, `CLUSTARR_`, `clustarr_`, `clustarr-*`,
  `*.clustarr.io` are not brands.
- **R-U7 Branch-new names migrate nothing.** A name this branch introduced (never on `main`, so
  never deployed) is renamed in code and needs no Migrator step (§2.7 marks each).

## 2. Inventory by class (`cae73ac6`)

Command, over every tracked file type:

```bash
git grep -n -i -E 'catalogarr|importarr|indexarr|grabarr|squasharr|captionarr|segmentarr' HEAD
```

By area (lines): `app/` 2,674; `pkg/` 710; `cmd/` 436; `test/` 447 (`test/e2e/` 214); `api/` 219;
`ui/` 61; `internal/` 9; `config/` 476 (`crd/` 173, `manager/` 157, `rbac/` 72, `prometheus/` 36,
`e2e/` 22, `postgres/` 11, `default/` 4, `README.md` 5, `samples/` 1); `charts/` 257; `hack/` 18;
`Makefile` 14; `CLAUDE.md` 99; `README.md` 6; `docs/observability.md` 18; `docs/gpu-nodes.md` 5;
`docs/adr/` 67 (`README.md` 4); `docs/research/` 238; `docs/superpowers/specs/` 1,169;
`docs/superpowers/plans/` 3,013. `images/` and `.github/` hold none (one `app/squash` path comment in
`images/Dockerfile.native:24`).

"Stored" means the name survives in etcd, NATS or a values file across a rollout; everything else
is ephemeral (recomputed by the next binary).

### 2.1 Go identifiers, packages, directories, files (ephemeral)

- **Identifiers:** 118 distinct branded identifiers, 1,309 occurrences in code (strings and
  comments stripped); 36 exported in non-test code (the `k8s.Manager*`, `events.Stream*`,
  `events.Filter*`, `events.Consumer*`, `events.QueueGroup*`, `lifecycle.RecorderGrabarrEngine`),
  the rest test helpers and import aliases (`importarr`, `indexarr`, `catalogarr`, `grabarr`,
  `captionarr`, `grabarrstatus`, `squasharrstatus`, `captionarrstatus`). The misspelled
  `QueueGroupCatalogar` (the owner's "mis-spelled confusion") becomes `QueueGroupCatalog`.
- **Derived `squash`:** 221 Go occurrences in 188 files, almost all the import path
  `github.com/mediactl/clustarr/app/squash/...` (134 import lines in 75 files), plus
  `squashmanager` (10), `origSquash` (2), `TestManagerSideSquashLinksNoWorker` (2), the manager's
  registration name `"squash"` (`internal/cli/manager/run.go:257`) and locals named `squash`.
- **Directory:** `app/squash` → `app/transcode` (16 directories, 101 files; package names are the
  leaves -- `pool`, `transcodejob`, `transcodeprofile`, `audiograft`, `graftstate`, `grafttask`,
  `jobspec`, `manager`, `status`, `task`, `worker`, `graft`, `inprocess` -- so no package name
  changes and nothing collides with `pkg/transcode`; aliases `squashmanager` → `transcodemanager`).
- **Files with a brand in the path:** 26: `pkg/events/schema/importarr.go` and `importarr_test.go`
  (→ `import.go`, `import_test.go`); 21 installer files (`config/manager/` 10,
  `config/rbac/` 7, `config/e2e/` 2, `config/postgres/indexarr-patch.yaml`,
  `charts/clustarr/templates/segmentarr-worker.yaml`), all deleted or moved by W6.9/W6.11/W6.15
  (§5); 3 historical docs (`docs/research/phase-d1-indexarr-spec.md`,
  `docs/superpowers/plans/2026-09-19-phase-d1-indexarr.md`,
  `docs/superpowers/plans/2026-09-22-phase-d2-grabarr.md`), which keep their names (§7).

### 2.2 Error, log, span and ui text (ephemeral)

Go string literals holding a brand: 1,119 occurrences (all files); in non-test code 493, of which
277 are messages (`"catalogarr: artist: %w"`, `"squasharr worker: …"` ×60, `"indexarr: …"`,
`"usenetengine: indexarr: %s"`) and 216 are names (§2.6-§2.10). Span names: 20 (`indexarr.search.fanout`,
`indexarr.rpc.serve.{search,download,query,blocklist}`, `indexarr.download*`,
`indexarr.query*`, `indexarr.relindex.prune`, `indexarr.proxy.flaresolverr`,
`indexarr.status.publish_event`, `indexarr.blocklist`, `squasharr.worker.process`) → `index.*`,
`transcode.worker.process`. User-visible ui text: 5 (`ui/add.go:189`, `ui/views/add.templ:88` and
its generated `add_templ.go:203`, `ui/forms/kinds.go:111,224,297,328`). Field-index names (cache
indexes, in memory): `squasharr.transcodejob.spec.{mediaFileRef,profileRef}`,
`squasharr.audiograft.{spec.itemRef.name,status.mediaFileRef}` (die with F5-F7). User agents: **0**
(every User-Agent is `clustarr/…` or `Clustarr …`).

### 2.3 Comments and docs (ephemeral)

Go comment lines: 2,367 (1,648 non-test, 719 test), `api/` 219 of them. Live docs: 173 lines
(`CLAUDE.md` 99, chart README 36, `docs/observability.md` 18, `README.md` 6, `docs/gpu-nodes.md`
5, `config/README.md` 5, `docs/adr/README.md` 4). Historical docs: 4,483 lines (`docs/superpowers/`
4,182, `docs/research/` 238, ADR files 63).

### 2.4 CRD descriptions and print columns (generated, ephemeral)

173 lines in `config/crd/bases/*.yaml` (largest: `download.clustarr.io_downloads.yaml` 26,
`catalog.clustarr.io_{series,movies,mediafiles}.yaml` 16 each), all descriptions generated from the
219 `api/` comment lines. Print columns and selectable fields: **0**. The CRD object in etcd carries
the description, but every install re-applies the CRD, so it is ephemeral.

### 2.5 Field managers (STORED: `metadata.managedFields` of every object written)

19 branded names in `pkg/k8s/fieldmanager.go`; about 1,086 references to the constants (21 in
non-test code per constant at most). On `kind-cluster-plex` they sit on about 33,000 objects
(16,450 items with 15,630 Episodes, 13,754 MediaFiles, plus profiles, providers, indexers, lists,
scans, searches, DownloadClients and their Secrets/PVCs).

| Constant (old → new) | Value (old → new) | Objects carrying it in N-1 | Fate |
| --- | --- | --- | --- |
| `ManagerCatalogarr` → `ManagerCatalog` | `catalogarr` → `catalog` | every catalog kind's status, MediaFile status, QualityProfile seeds (spec), RootFolder/DelayProfile/MetadataProvider/OverlayProfile/Search status | renamed |
| `ManagerCatalogarrSeries` → `ManagerCatalogSeries` | `catalogarr-series` → `catalog-series` | Episode status | renamed |
| `ManagerCatalogarrWorker` → `ManagerCatalogWorker` | `catalogarr-worker` → `catalog-worker` | Search status | renamed |
| `ManagerCatalogarrMetadata` → `ManagerCatalogMetadata` | `catalogarr-metadata` → `catalog-metadata` | item `status.metadata`/`artwork` | renamed |
| `ManagerCatalogarrGrab` → `ManagerCatalogGrab` | `catalogarr-grab` → `catalog-grab` | item grab fields | renamed |
| `ManagerCatalogarrFanout` → `ManagerCatalogFanout` | `catalogarr-fanout` → `catalog-fanout` | Issue status | renamed |
| `ManagerCatalogarrArtwork` → `ManagerCatalogArtwork` | `catalogarr-artwork` → `catalog-artwork` | Movie/Series `status.overlay` | renamed |
| `ManagerCatalogarrClassify` → `ManagerCatalogClassify` | `catalogarr-classify` → `catalog-classify` | Series spec | renamed; **read** by the import list (§4.1.5) |
| `ManagerCatalogarrMarkers` → `ManagerRetiredMarkers` | `catalogarr-markers` (kept) | MediaFile status | retired (R-U3); released by F8.4 |
| `ManagerImportarr` → `ManagerImport` | `importarr` → `import` | ImportList/ImportExclusion/LibraryScan status, Trakt token Secrets, MediaFile annotation, Download `status.import` | renamed (Downloads excluded) |
| `ManagerImportarrWorker` → `ManagerImportWorker` | `importarr-worker` → `import-worker` | MediaFile spec, scan/list-added item spec, AudioGraft | renamed (AudioGraft excluded) |
| `ManagerIndexarr` → `ManagerIndex` | `indexarr` → `index` | Indexer/IndexerDefinition/IndexerProxy status, bundle IndexerDefinition spec, session Secrets, facade Secret | renamed |
| `ManagerIndexarrWorker` → `ManagerIndexWorker` | `indexarr-worker` → `index-worker` | Indexer status, session Secrets | renamed |
| `ManagerGrabarr` → `ManagerGrab` | `grabarr` → `grab` | DownloadClient status, engine StatefulSets/Deployments, scratch PVCs, Download status | renamed (workloads replaced, Downloads excluded) |
| `ManagerGrabarrEngine` → `ManagerRetiredEngine` | `grabarr-engine` (kept) | Download status | retired (R-U3) |
| `ManagerSquasharr` → `ManagerTranscode` | `squasharr` → `transcode` | TranscodeProfile status, TranscodeJob/AudioGraft status | renamed (TranscodeJob/AudioGraft excluded) |
| `ManagerSquasharrPool` → `ManagerTranscodePool` | `squasharr-pool` → `transcode-pool` | pool Jobs | renamed in code; legacy Jobs deleted (§4.4) |
| `ManagerCaptionarr` → `ManagerCaption` | `captionarr` → `caption` | SubtitleProfile/SubtitleProvider status, SubtitleRequest status | renamed (SubtitleRequest excluded) |
| `ManagerCaptionarrWorker` → `ManagerCaptionWorker` | `captionarr-worker` → `caption-worker` | SubtitleRequest status | renamed in code; nothing to migrate (SubtitleRequest excluded) |

Unbranded and unchanged: `clustarr-dlq-projector`, `clustarr-ui`, `clustarr-autoscale`,
`clustarr-legacy-fold`, `clustarr` (`k8s.DefaultFieldOwner`).

### 2.6 NATS (streams, durables, subjects: STORED; RPC, queue groups: wire)

**Streams** (6, stored):

| Constant | Old | New | Subjects old → new | Storage (single node) |
| --- | --- | --- | --- | --- |
| `StreamWorkCatalogarr` → `StreamWorkCatalog` | `CLUSTARR_WORK_CATALOGARR` | `CLUSTARR_WORK_CATALOG` | `clustarr.work.catalogarr.>` → `clustarr.work.catalog.>` | memory |
| `StreamWorkImportarr` → `StreamWorkImport` | `CLUSTARR_WORK_IMPORTARR` | `CLUSTARR_WORK_IMPORT` | `…importarr.>` → `…import.>` | memory |
| `StreamWorkIndexarr` → `StreamWorkIndex` | `CLUSTARR_WORK_INDEXARR` | `CLUSTARR_WORK_INDEX` | `…indexarr.>` → `…index.>` | memory |
| `StreamWorkCaptionarr` → `StreamWorkCaption` | `CLUSTARR_WORK_CAPTIONARR` | `CLUSTARR_WORK_CAPTION` | `…captionarr.>` → `…caption.>` | memory |
| `StreamWorkSquasharr` → `StreamWorkTranscode` | `CLUSTARR_WORK_SQUASHARR` | `CLUSTARR_WORK_TRANSCODE` | `clustarr.work.transcode.>` **unchanged (overlaps)** | memory |
| `StreamWorkSegmentarr` → `StreamWorkMarkers` | `CLUSTARR_WORK_SEGMENTARR` | `CLUSTARR_WORK_MARKERS` | `…segmentarr.>` → `…markers.>` | file (Durable, 256 MiB) |

`FilterWorkCatalogarr`, `FilterWorkImportarr`, `FilterWorkIndexarr`, `FilterWorkCaptionarr`,
`FilterWorkSquasharr`, `FilterWorkSegmentarr` → `FilterWork{Catalog,Import,Index,Caption,Transcode,Markers}`.
Every other `Filter*` keeps its identifier and takes the new token in its value. Subject builders
(`WorkSearchSubject`, `WorkGrabSubject`, `WorkMetadataSubject`, `WorkWantedScanSubject`,
`WorkScanSubject`, `WorkListSubject`, `WorkFileImportSubject`, `WorkImport{Inspect,Execute}Subject`,
`SubjectImportRecycleSweep`, the recycle-files subject, `WorkRSSSubject`, `WorkFetchSubject`,
`WorkArtwork{Fetch,Render}Subject`, `WorkSegments{Plan,Analyze}Subject`, `WorkMarkersSubject`) take
the new token. DLQ subjects (`clustarr.dlq.<service>.<task>.<id>`) and MAX_DELIVERIES advisory
subjects (`…MAX_DELIVERIES.<stream>.<durable>`) derive from these; DLQ copy ids (`dlq:<durable>:<id>`)
from the durable.

**Durables** (20 static in N-1, all stored; plus families):

| Old | New | Stream (old) | Fate in N |
| --- | --- | --- | --- |
| `catalogarr-rss-matcher` | `catalog-rss-matcher` | `CLUSTARR_RELEASES` (kept) | **handover** at the legacy ack floor (§4.2.3) |
| `catalogarr-history` | `catalog-history` | `CLUSTARR_EVENTS` (kept) | **handover** |
| `catalogarr-redownload` | (kept, R-U3) | `CLUSTARR_EVENTS` | draining (A4.4 R22), deleted N+1 (A9.2) |
| `catalogarr-search-high`, `catalogarr-search-normal`, `catalogarr-metadata`, `catalogarr-artwork-fetch`, `catalogarr-artwork-render` | `catalog-…` | `…CATALOGARR` | new on the new stream; legacy goes with its stream |
| `catalogarr-grab` | (kept, R-U3) | `…CATALOGARR` | goes with its stream (R22's "deleted in N+1" moves to N) |
| `importarr-scan`, `importarr-list`, `importarr-fileimport` | `import-…` | `…IMPORTARR` | new |
| `indexarr-rss` | `index-rss` | `…INDEXARR` | new |
| `captionarr-fetch-high`, `captionarr-fetch-normal` | `caption-…` | `…CAPTIONARR` | new |
| `squasharr-transcode-results` | (kept, R-U3) | `…SQUASHARR` | goes with its stream |
| `squasharr-transcode-<profile>-<class>` (dynamic) | `transcode-pool-<profile>-<class>` (R-U2) | `…SQUASHARR` | goes with its stream |
| `catalogarr-markers` | `catalog-markers` | `…SEGMENTARR` | new on `…MARKERS` |
| `catalogarr-segments-plan` | `catalog-segments-plan` | `…SEGMENTARR` | new |
| `segmentarr-analyze` | `markers-analyze` | `…SEGMENTARR` | new |
| `catalogarr-segments-result` | (kept, R-U3) | `…SEGMENTARR` | goes with its stream |
| `clustarr-dlq-watch-<stream>-<durable>` (derived) | derived | `CLUSTARR_ADVISORIES` | legacy watchers deleted with their durables (§4.2.1) |

**Branch-new durables** (R-U7, no migration): `catalogarr-intake-candidate` → `catalog-intake-candidate`,
`importarr-intake-scan` → `import-intake-scan`, `importarr-probe-high`/`-low` → `import-probe-high`/`-low`,
`importarr-recycle` → `import-recycle`, `grabarr-engine-<client>-<ordinal>` → `grab-engine-<client>-<ordinal>`
(`EngineConsumerPrefix`).

**RPC subjects and queue groups** (wire, not stored): `clustarr.rpc.indexarr.{search,download,query}`
→ `clustarr.rpc.index.*`; `clustarr.rpc.indexarr.blocklist` (branch-new) → `clustarr.rpc.index.blocklist`;
`clustarr.rpc.catalogarr.metadata.{lookup,search,resolve,extras}` → `clustarr.rpc.catalog.metadata.*`;
queue groups `indexarr` → `index`, `catalogarr` → `catalog`.

**Schema names** (`Clustarr-Schema` header; stored in messages and records): 11 `importarr.*` →
`import.*`. Live in N-1 (`main`): `importarr.ScanTask.v1`, `.v2`, `importarr.ListTask.v1`, `.v2`
(§4.2.5). Branch-new: `ProbeTask.v1`, `ProbeRecord.v1`, `ImportInspectTask.v1`,
`ImportExecuteTask.v1`, `Import.v1`, `ListSnapshot.v1`, `RecycleSweepTask.v1`, `RecycleFilesTask.v1`.

**Envelope `Source`** (header, informational, never parsed: `envelope.go:145` only returns it): 11
prefixes `catalogarr@`, `catalogarr-worker@`, `captionarr@`, `captionarr-worker@`, `importarr@`,
`importarr-fileimport-retrigger@`, `indexarr@`, `grabarr-controller@`, `squasharr-controller@`,
`squasharr-worker@` (and `main`'s `segmentarr-worker@`), plus the bare `Source: "importarr"`
(`app/import/worker/importlist/sync.go:510`) → token map (`markers@` for segmentarr-worker). Old
values stay readable in history and DLQ records; nothing migrates.

**KV buckets, KV keys, object stores, Msg-Id formats:** **0** branded (every bucket is
`clustarr-*`; every Msg-Id format in `pkg/events/{agents,subjects,probe}.go` and
`pkg/events/schema/artwork.go` is unbranded; DLQ copy ids derive from durables).

### 2.7 Annotations, labels, finalizers (STORED on the objects named)

| Kind | Old | New | Carried by | Fate |
| --- | --- | --- | --- | --- |
| label key | `squasharr.clustarr.io/template-hash` | `transcode.clustarr.io/template-hash` | pool Jobs | legacy Jobs deleted (§4.4) |
| annotation key | `squasharr.clustarr.io/applied-template` | `transcode.clustarr.io/applied-template` | pool Jobs | same |
| label key | `squasharr.clustarr.io/audiograft` | `transcode.clustarr.io/audiograft` | graft and reduce Jobs | same |
| finalizer | `squasharr.clustarr.io/task-withdrawal` | (kept, R-U3) | TranscodeJob | removed by the Migrator (F8.3), kind deleted N+1 |
| label value | `app.kubernetes.io/managed-by=squasharr` | `=transcode` | pool and graft Jobs (the pool cache and list select by it, `transcodejob/pools.go:88,599`) | legacy Jobs deleted |
| label value | `app.kubernetes.io/component=squasharr-graft` | `=transcode-graft` | graft Jobs | same |
| label value | `app.kubernetes.io/component=grabarr-engine` | `=grab-engine` | engine StatefulSets/Deployments **selector** and pods | workload replaced (§4.4) |
| label value | `app.kubernetes.io/component=indexarr` | `=index` | the facade Secret | new Secret carries it; old kept (§4.4) |

No `catalog.clustarr.io/…`, `download.clustarr.io/…`, `clustarr.io/…` or other key is branded.
The `transcode.clustarr.io/…` prefix is the existing API group's, already used by
`transcode.clustarr.io/{hardware,profile,priority,suspend,retry,cancel}`; none of the new keys
exists there today.

### 2.8 Metrics

Metric **names**: **0** branded. Label **values**: `stream` (the 6 stream names), `consumer` and
`durable` (every durable in §2.6) on `clustarr_work_queue_pending`, `clustarr_consumer_{pending,ack_pending,waiting,max_ack_pending}`,
`clustarr_stream_fill_ratio`, `clustarr_work_handled_total`, `clustarr_work_duration_seconds`,
`clustarr_bus_{muted_total,lapsed_handlers,saturated}`, `clustarr_dispatch_{waiting,unattended}`,
`clustarr_task_*`, and the External Metrics series `clustarr_consumer_lag{stream,consumer}`; the
controller-runtime `controller` label value `indexarr-directgrab` (that controller is deleted by
A3.7). No dashboard or alert is in the repo, and the live cluster runs no Prometheus.

### 2.9 Events and conditions

Event recorders: `catalogarr-history` (`app/catalog/manager/register.go:105`) → `catalog-history`;
`grabarr-engine` (`app/grab/agent/torrent/register.go:108`, `lifecycle.RecorderGrabarrEngine`) →
`grab-engine`. Event reasons: **0** branded. Condition reason: `IndexarrUnavailable` (Search Ready,
`app/catalog/controller/search/reconciler.go:562`) → `IndexUnavailable` (stored on transient
Searches; nothing matches it). Search `status.indexerOutcomes[].name` values (stored, **read** by
the Search reconciler, `reconciler.go:461-492`): `catalogarr/search-worker`, `catalogarr/truncated`
and the reserved prefix `catalogarr/` (`app/catalog/searchoutcome/names.go`) → `catalog/…`
(read both in N, §4.6); `search.ListOutcomeName` `indexarr` → `index` (display only).

### 2.10 Kubernetes object names

| Object | Old | New | Created by | Stored data |
| --- | --- | --- | --- | --- |
| facade API-key Secret | `indexarr-facade`, chart `<fullname>-indexarr-facade` (`app/indexer/agent/register.go:66`, `internal/cli/agent/options.go:48`, `_helpers.tpl:33`) | `index-facade`, `<fullname>-index-facade` | the manager (A6.4), else the user | **yes**: the keys every Torznab client holds |
| engine ServiceAccount | `grabarr-engine`, chart `<fullname>-grabarr-engine` (`downloadclient/workload.go:126`) | `grab-engine`, `<fullname>-grab-engine` | installers | no |
| engine ClusterRole (generated) | `clustarr-grabarr-engine-role` (`config/rbac/grabarr_engine_role.yaml`) | `clustarr-grab-engine-role` (`grab_engine_role.yaml`) | `make manifests` (W6.9) | no |
| pool Jobs | `squasharr-pool-<profile>-<class>` (`pool/render.go:90`) | `transcode-pool-<profile>-<class>` | the manager | no |
| leases | `{catalogarr,importarr,indexarr,grabarr,squasharr,captionarr}.clustarr.io` (`pkg/k8s/leaselock.go:39-42`) | (kept, R-U3: read by the legacy-lease gate) | N-1 | no; the runbook deletes them |
| per-service workloads | Deployments, ServiceAccounts, Services, ServiceMonitors `catalogarr`, `catalogarr-metadata`, `importarr`, `importarr-worker`, `indexarr`, `grabarr`, `squasharr`, `captionarr`, `captionarr-worker`, `segmentarr-worker`; ClusterRoles and bindings `clustarr-{catalogarr,importarr,indexarr,grabarr,squasharr,captionarr}-role` | `manager`, `agent-*`, `markers` (split §10.0) | installers | no; Helm deletes them at the W6.11 cutover |

PVCs (`<fullname>-data`, `<fullname>-index`, `<client>-scratch`, the engines' claim templates),
ConfigMaps, the CNPG `Cluster` (`<fullname>-postgres`), the ui Service and the art-signing Secret
are unbranded.

### 2.11 CLI flags, environment, values, templates, kustomize paths

- **CLI flags:** **0** branded. Flag and env **values** that name renamed objects:
  `--facade-api-key-secret`/`CLUSTARR_FACADE_API_KEY_SECRET` (default `index-facade`),
  `--engine-service-account`/`CLUSTARR_ENGINE_SERVICE_ACCOUNT` (default `grab-engine`), and the
  `--consumer-slots`/`CLUSTARR_CONSUMER_SLOTS` keys (durable names).
- **Environment variable names:** **0** branded `CLUSTARR_*`; one test-only
  `SQUASHARR_WORKER_TEST_REEXEC` → `TRANSCODE_WORKER_TEST_REEXEC`.
- **Helm values keys** (stored in users' values files): 10 top-level keys of chart 0.4.x --
  `catalogarr`, `catalogarrMetadata`, `importarr`, `importarrWorker`, `indexarr`, `grabarr`,
  `squasharr`, `captionarr`, `captionarrWorker`, `segmentarrWorker` -- all retired by W6.13 (§4.7).
  Template file `templates/segmentarr-worker.yaml` (moved to `markers.yaml` by W6.11), helper
  variable `$squashEnv`, component names in `_helpers.tpl`.
- **Kustomize paths:** the 20 installer files of §2.1 (W6.9/W6.11/W6.15).

### 2.12 Makefile, hack, images, CI, e2e

`Makefile`: 14 lines -- `RBAC_ROLES` and `RBAC_PATHS_*` (identities `catalogarr importarr indexarr
grabarr grabarr-engine squasharr captionarr`, recut by W6.9/W6.11), `RBAC_PATHS_squasharr :=
./app/squash/...` (U2 must change the path or `make manifests` loses the role), comments, and one
message (`:281`). `hack/`: 18 lines (`e2e.sh` Deployment list `:48-56` owned by W6.15; comments in
`kind.sh`, `parity-clips.sh`, `deps/deps.go`, `pack-cardigann`, `sync-cardigann`). `images/`: 0
(one path comment). `.github/`: 0. `test/e2e/`: 214 lines, Go (renamed by U2-U5; its legacy
Deployment names are W6.15's).

### 2.13 Summary

| Class | Distinct names | Occurrences (lines unless noted) | Stored |
| --- | --- | --- | --- |
| Go identifiers | 118 (+ `squash`-derived 5) | 1,309 + 221 | no |
| Directories / files | 1 tree (16 dirs, 101 files) / 26 paths | -- | no |
| Error, log, span, ui text | 277 messages, 20 spans, 5 ui | 1,119 Go literals | no |
| Comments and docs | -- | Go 2,367; live docs 173; historical 4,483 | no |
| CRD descriptions | -- | 173 (generated) | no |
| Field managers | 19 (17 renamed, 2 retired) | ~1,086 refs | **yes**: ~33,000 objects |
| NATS streams | 6 | -- | **yes** |
| NATS durables | 20 + 1 family (+6 branch-new) | -- | **yes** (handover 2, kept 4) |
| NATS work-subject tokens | 5 (+ derived DLQ/advisory) | ~50 constants | **yes** (in legacy streams, DLQ 720 h) |
| RPC subjects / queue groups | 8 / 2 | -- | no (wire) |
| Schema names | 11 (4 live) | -- | **yes** (4) |
| Envelope Source | 11 prefixes (12 with `main`'s `segmentarr-worker@`) + 1 bare | -- | in records, never read |
| KV / object stores / Msg-Id | 0 | -- | -- |
| Label/annotation keys, label values | 3 keys, 4 values | -- | **yes** (disposable or replaced objects) |
| Finalizers | 1 | -- | **yes** (kept, R-U3) |
| Metric names / label values | 0 / every stream and durable | -- | no (no TSDB on the cluster) |
| Event recorders / reasons / condition reasons / outcome names | 2 / 0 / 1 / 3 | -- | outcome names and reason on transient Searches |
| User agents | 0 | -- | -- |
| Object names | facade Secret, engine SA, engine role, pool Jobs, 6 leases, 10 workloads | -- | **yes**: facade Secret data; leases |
| CLI flags / env names | 0 / 1 test-only | -- | -- |
| Helm values keys | 10 | -- | **yes** (users' values files) |
| Makefile / hack / images / CI | -- | 14 / 18 / 0 / 0 | no |

## 3. The legacy-name table, `pkg/legacynames`

One package, standard library only, the **only** Go code allowed to spell an old name (§7). It holds
data, not behaviour, so every consumer -- the Migrator, `restore-names`, the topology's displaced and
handover entries, the read-both helpers, the report, tests -- reads one list.

- `Tokens []Token` -- the seven brands and `squash`, longest first, with `New`.
- `FieldManagers map[string]string` -- the 17 renamed managers, old → new.
- `MarkersStatusManager = "catalogarr-markers"`, `EngineTelemetryManager = "grabarr-engine"` --
  retired, never renamed.
- `Streams map[string]string` -- the six work streams, old → new.
- `SubjectTokens map[string]string` -- the five work-subject tokens, old → new.
- `Durables map[string]string` -- the 14 renamed static durables; `Handover map[string]string` -- the
  two event-stream durables; `GrabDurable`, `RedownloadDurable`, `TranscodeResultsDurable`,
  `SegmentsResultDurable`, `PoolDurablePrefix = "squasharr-transcode-"` -- kept names.
- `Schemas map[string]string` -- the live `importarr.*` schema values, old → new.
- `LeaseIDs []string` -- the six legacy leases (moved from `k8s.LegacyLeaseIDs`).
- `TaskWithdrawalFinalizer`, `PoolManagedBy = "squasharr"`, `GraftLabel`, `GraftComponent`,
  `EngineComponent`, `EngineServiceAccount`, `FacadeSecret = "indexarr-facade"`,
  `FacadeSecretSuffix = "-indexarr-facade"`.
- `SearchOutcomeNames map[string]string`, `SearchOutcomePrefix = "catalogarr/"`.
- `ValuesKeys []string` -- the 10 values keys of chart 0.4.x (W6.13's rejection test reads them).
- `Subject(s string) string` -- maps token 3 of a `clustarr.{work,dlq,rpc}.<token>.…` subject
  through `SubjectTokens` (and the DLQ's durable-derived `catalogarr` service token), else returns
  `s`; `Manager(old string) (string, bool)`; `LegacyManager(new string) (string, bool)`.

It must name exactly what `main` deploys: U1 builds it from `git grep` on `main` (not the branch), and
`TestLegacyNamesAreWhatMainDeploys` holds a golden list. N+1 deletes everything but `ValuesKeys`
and the three durables it retires (§4.9).

## 4. Migration per stored class (release N)

One Migrator, F8.3's `legacyfold.Migrator`, with ADR-0016's and ADR-0019's adoption: one interval
(60 s), one write limiter (`DefaultDeletesPerSecond`, 20/s, now covering every Migrator write), one
census, one report, one rollback document. The names work never waits for the fold and the fold
never waits for it.

### 4.1 Field managers in `managedFields`

**4.1.1 Mechanism: rewrite the entries in place.** For each object the Migrator reads its
`managedFields` through the APIReader (metadata-only `PartialObjectMetadataList`, 500 per page,
as F8.3's markers pending set does: the cache strips `managedFields`), computes the renamed list
with the pure `legacyfold.RenameManagedFields(entries, rename)` and sends one JSON patch to the main
resource, the shape `k8s.io/client-go/util/csaupgrade.UpgradeManagedFieldsPatch` uses for kubectl's
client-side-apply upgrade:

```json
[{"op": "replace", "path": "/metadata/managedFields", "value": [ … ]},
 {"op": "replace", "path": "/metadata/resourceVersion", "value": "<read rv>"}]
```

under field owner `clustarr-legacy-fold`. A stale `resourceVersion` is a 409; the object is
re-read at the next pass. The apiserver uses the request's `managedFields` as the base for a
main-resource write (`decodeLiveOrNew`; subresource requests ignore them), and the patch changes no
field, so no entry is added for `clustarr-legacy-fold`. `csaupgrade` cannot be reused as is: it
merges `Update` entries into an `Apply` entry, and this is `Apply`→`Apply` and `Update`→`Update`.
**Fallback**, only if the envtest of §4.1.6 shows the apiserver dropping `subresource: status`
entries from a main-resource write: status entries move by the markers pattern (loop §7.3.8:
the new name applies the full set, then an empty apply under the old name releases it), at two
writes per (object, manager).

**4.1.2 The merge rule.** For each entry whose `manager` is a key of
`legacynames.FieldManagers`: rename it; if an entry already exists with the new name and the same
`operation`, `subresource` and `apiVersion`, union the two `fieldsV1` sets
(`sigs.k8s.io/structured-merge-diff/v6/fieldpath.Set.Union`, already required at v6.4.2; no
`go.mod` change) into that entry, keep the later `time`, and drop the renamed one. Entries of
retired managers, unbranded managers and other controllers are untouched. Two entries that differ
only in `apiVersion` are not merged: the object is counted `unmergeable` and left (every clustarr
kind is served at `v1alpha1` only, and every core kind clustarr writes at one version, so the count
must be 0; the pre-flight report stops the runbook otherwise).

**Why union is right regardless of order.** The rename makes each object exactly what it would be
had the managers always carried the new names. If N's controller applied under the new name first,
SSA already moved every field whose value changed (force) and co-owns every field whose value did
not; the old entry keeps only what N stopped sending. The union re-claims that remainder under the
new name, and N's next apply under the new name releases it -- which is what N's renderer means by
not sending it. Without the rename the old entry would co-own it forever and N's omit-to-clear
would silently fail: the CLAUDE.md gotcha whose co-owner hides a release.

**4.1.3 Which objects.** Every object of these kinds in every namespace the manager watches: Movie,
Series, Episode, Album, Artist, Author, Book, Audiobook, Comic, Issue, MediaFile, Search,
RootFolder, QualityProfile, DelayProfile, MetadataProvider, OverlayProfile, LibraryScan,
ImportList, ImportExclusion, Indexer, IndexerDefinition, IndexerProxy, DownloadClient,
TranscodeProfile, SubtitleProfile, SubtitleProvider. **Not** Download, TranscodeJob, SubtitleRequest
or AudioGraft (adopted and deleted in N; their entries go with them, and `hold` mode keeps them as
N-1 wrote them). Core objects are reached from their owners, never by listing (the manager has no
`list` on Secrets): each Indexer's session Secret (`status.sessionSecretRef`), each Trakt ImportList's
token Secret (`importliststate.TraktTokenSecretName`), each DownloadClient's scratch PVC
(`<client>-scratch`). Engine workloads are replaced (§4.4), pool and graft Jobs deleted (§4.4),
the legacy facade Secret left as it is (§4.4).

**4.1.4 Ordering, pacing, cost.** The names pass starts with the Migrator (leader-only, after cache
sync) in both `apply` and `hold` modes (the rename is lossless and reversible) and runs beside the
controllers; nothing waits for it, because the union rule makes the result independent of who
wrote first. About 33,000 patches at 20/s take about 28 minutes, once; a restart finds only what is
left. Each patch changes `resourceVersion`, so every watcher of the kind sees one update event per
object -- spread over the pass by the limiter, the same order of load as F8.4's markers release.
Census `clustarr_legacy_fold_objects{kind,state="legacy_managers"}`; outcomes
`clustarr_legacy_fold_preparations_total{action="rename_managers",result=ok|conflict|unmergeable|error}`.
Until the census reads 0 two things are true: a field N stopped sending may stay one pass longer
(co-owned by the old entry), and code that reads `managedFields` by manager must accept the old
name (§4.1.5).

**4.1.5 Readers.** Three non-test call sites read `managedFields` by manager name:
`app/import/worker/importlist/catalogitem.go:276` (`catalogarr-classify`: an import list's re-apply
keeps a classified Series' values -- without the old name it would overwrite the owner's
classification on every unrenamed Series), `app/catalog/status/artwork.go:199`, and
`app/catalog/worker/grab/perform.go:416` (retired by A4.4). U3 adds `k8s.OwnedBy(entry, manager)`,
true for the manager or its `legacynames.LegacyManager`, and every reader uses it; N+1 drops the
legacy half. `legacyfold.HasMarkersEntry` reads the retired name on purpose.

**4.1.6 How tests prove no stale co-ownership** (deferred to the batch; envtest, real apiserver;
assertions read `metadata.managedFields` through a direct client, never the cache, per the
CLAUDE.md gotchas):

- `TestRenameManagedFieldsMergesFieldSets` (pure): rename; union with an existing same-key entry,
  later `time`; `Apply` and `Update` kept apart; status and main kept apart; retired and unknown
  managers untouched; differing `apiVersion` reported unmergeable.
- `TestTheNamesPassRenamesEveryLegacyEntry`: objects of each kind seeded by applies under every
  legacy name (status and main); one object where the new name has already applied a subset; after
  the pass no legacy manager remains, each new entry's set equals the union, every field value is
  byte-identical, and no entry names `clustarr-legacy-fold`.
- `TestTheRenameRewritesStatusSubresourceEntries`: the main-resource patch rewrites the
  `subresource: status` entries (the fallback trigger of §4.1.1).
- `TestAReleaseAfterTheRenameRemovesTheField` (the falsifiable one): field F applied only under
  `catalogarr`; rename; an apply under `catalog` without F removes F. Its control, without the
  rename, shows F surviving -- the test fails if the rename is skipped.
- `TestTheRenameRacesAWriterAndRetries`: a real second writer between read and patch makes the
  patch 409; the next pass renames; the writer's change survives.
- `TestRestoreNamesIsTheInverse`: rename then restore yields the original sets.
- `TestClassifiedSeriesKeepsItsValuesBeforeTheRename`: a Series whose spec is owned by the legacy
  classifier keeps its values through an import list re-apply.

### 4.2 NATS

**4.2.1 Work streams: displaced, not copied.** All six renamed work streams are *displaced*:
`events.Topology.Displaced []DisplacedStream{Old, New}` (Old from `legacynames.Streams`).

- The manager's start-up `EnsureTopology` (pre-leader, split §5.9) never deletes. While Old exists
  it creates neither New nor New's consumers, and reports them as `Blocked`; `StreamAdmin.Missing`
  reports them missing, so agents' `AwaitTopology` waits (as it does for any missing object).
- `busconn.KeepTopology` (leader-only, so the legacy-lease gate has opened and every N-1 writer is
  down) retires each present Old at lease acquisition, before its first ensure: it reads the census
  (messages per subject token, per durable pending), logs it at Warn, hands it to the Migrator's
  metrics (`action="retire_stream"`), deletes the stream (its durables go with it), deletes the
  legacy durables' dead-letter watchers on `CLUSTARR_ADVISORIES` and purges their advisory
  subjects, then ensures, which creates New. A failure is logged and retried at the next interval.
- **Nothing is copied.** N re-derives every queue from status: searches, metadata, artwork and
  markers are level-triggered planners in N; RSS and wanted-scan schedules are re-seeded at start;
  N-1's v1 MarkersTasks and FetchTasks were going to be acked as stale anyway (loop §7.3.9); grab
  messages and delay holds are not adopted (A-plan R22); scans, list syncs and imports are quiesced
  before the upgrade (runbook steps 5-6). Copying would have to rewrite subjects, schedules and
  dedup ids for messages N mostly discards.
- **Why displace every stream, not only the overlapping one.** `CLUSTARR_WORK_TRANSCODE` keeps the
  subjects `clustarr.work.transcode.>`, which overlap the old `CLUSTARR_WORK_SQUASHARR`, so JetStream
  refuses to create it while the old one exists. The other five do not overlap, but on a single node
  they are memory streams scaled into a 64 MiB budget; creating N's set beside N-1's doubles the
  reservation at start, and a NATS server whose memory store is smaller than both fails every
  `Ensure` ("insufficient memory resources" -- the 2026-09-24 crash-loop gotcha).
  Displacing all six keeps the peak at one set.

Cost if wrong: N-1's queued work is lost rather than resumed after a rollback (N-1 re-derives it;
a delay-profile hold restarts -- the cost R22 already accepted).

**4.2.2 Retired durables on displaced streams** go with their streams: `catalogarr-grab` (R22's
"deleted in N+1" becomes N), `squasharr-transcode-results` and `catalogarr-segments-result` (their
`Topology.Retired` entries become unnecessary; `Retired` may name an undeclared legacy stream, and
a NotFound stream is success).

**4.2.3 Durables on kept streams: handover at the ack floor.** `catalogarr-history`
(`CLUSTARR_EVENTS`, 168 h, 2 GiB) and `catalogarr-rss-matcher` (`CLUSTARR_RELEASES`, 72 h, 4 GiB)
cannot simply be created anew: a new durable delivers everything (`DeliverAll`), which would
re-record a week of history Events and re-match three days of releases. `ConsumerSpec.HandoverFrom`
names the legacy durable; `Ensure` creates the new durable, when it is absent and the legacy one
exists, with `DeliverByStartSequence` at the legacy `AckFloor.StreamSeq + 1`; when both exist and
the legacy floor is ahead (a rollback ran N-1 after N), it recreates the new one at the legacy
floor; every update keeps the existing consumer's `DeliverPolicy` and `OptStartSeq` (JetStream
refuses to change them). The legacy durables stay idle through N (a limits stream holds nothing
for an idle consumer), so a rollback resumes them where N-1 left off, and N+1 deletes them
(`Topology.Retired`). Overlap: if an N-1 consumer still runs when N's manager starts, a few messages
are handled twice; the candidate intake's 1 h Msg-Id dedup absorbs RSS repeats, and a repeated
history Event is harmless. `catalogarr-redownload` stays idle as R22 decided.

**4.2.4 RPC subjects and queue groups switch with the rollout.** No dual serving. Release N's
callers (ui, engines, manager) and responders (the metadata and index agents) all speak the new
subjects. The mixed window is the upgrade itself: the runbook stops every N-1 writer first, the
engines are quiesced (no NZB fetch in flight) and re-rendered by N's manager, and the only N-1
caller left running is the old ui pod during its own rollout, whose Add New search and Plex extras
calls get `ErrNoResponders` -- a 503, the documented answer (Plex keeps its trailers on a 503,
CLAUDE.md). Cost if wrong: a minute of 503s on two ui routes during the upgrade.

**4.2.5 Schema names.** `import.ScanTask` and `import.ListTask` list their `main` predecessors
(`importarr.ScanTask.v1`, `.v2`, `importarr.ListTask.v1`, `.v2`) in `LegacySchemas()` for N, so a
DLQ replay of an N-1 dead letter still decodes. The branch-new schema values rename outright.

**4.2.6 DLQ, replay and resolvers.** `CLUSTARR_DLQ` is kept (720 h). N-1's dead letters keep their
subjects (`clustarr.dlq.catalogarr.…`) and their `Clustarr-…` original-subject headers. The
replayer republishes to `legacynames.Subject(original)`, so a replay lands on the new stream; the
history resolvers (`app/catalog/history/target.go`) and the DLQ projector resolve
`legacynames.Subject(subject)`. The `clustarr.io/dead-lettered` annotations N-1 wrote keep naming
the old DLQ subjects, which still exist. N+1 drops the mapping after its gate reads zero legacy-subject
dead letters (they age out at 720 h, or the operator purges `clustarr.dlq.<old token>.>`).

**4.2.7 Msg-Id, KV, object stores.** Dedup windows are per stream and no message moves between
streams, so no N publish can be suppressed by an N-1 id; the DLQ's 10-minute window sees new copy
ids (`dlq:<new durable>:…`). No KV key or object name is branded. Advisories for legacy durables
already in `CLUSTARR_ADVISORIES` age out (720 h) once their watchers are gone.

**4.2.8 Single-node budget.** `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` is re-run with
N-1's six work streams pre-created at N-1's scaled sizes: start-up `Ensure` must succeed (blocked
replacements are not created), and after `KeepTopology` retires them the full N topology fits.

### 4.3 Annotations, labels, finalizers

**Rule, for every key or value that names a brand on an object that outlives the rollout:** N
writes the new key, reads both, and the Migrator copies old to new and deletes the old in one patch;
a finalizer is never renamed in place -- the code that removes it accepts both keys for the release,
so an object in deletion is never stranded. **In this inventory no such key needs the rule:** the
three keys and two of the label values (`managed-by=squasharr`, `component=squasharr-graft`) sit on
pool, graft and reduce Jobs, which N deletes and recreates (§4.4); `component=grab-engine` sits on replaced workloads (§4.4); the facade Secret is a
new object (§4.4); the one finalizer, `squasharr.clustarr.io/task-withdrawal`, belongs to a kind N
deletes, keeps its legacy spelling (R-U3), and is removed by the Migrator exactly as loop §7.3.7
already removes it (`legacyfold.FinalizerTaskWithdrawal` points at `legacynames`).

### 4.4 Kubernetes objects with branded names

- **Facade Secret: copy, keep the old until N+1.** In N the manager creates the facade key (A6.4,
  `EnsureFacadeKey`). When the configured Secret (default `<fullname>-index-facade`) is absent and the
  legacy one (`legacynames.FacadeSecret`, or the configured name with `FacadeSecretSuffix`) exists,
  it creates the new Secret with the legacy Secret's keys unchanged, labelled
  `app.kubernetes.io/component=index`, under field manager `index`; otherwise it generates as today.
  The legacy Secret is never written or deleted by code; N+1's runbook deletes it after confirming
  the two hold the same keys. A user-named Secret (`agents.index.facade.apiKeySecret`) is used as is.
  Cost if wrong (a generated key instead of a copy): every Torznab client loses access until re-keyed.
- **Engine ServiceAccount:** the installers render `<fullname>-grab-engine` (kustomize
  `grab-engine`) and its binding to `clustarr-grab-engine-role`; Helm deletes the old SA (no data).
  N's manager re-renders the engines with the new SA (they restart in N anyway: new image, new argv).
- **Engine workloads: replace on a selector change.** The selector carries
  `app.kubernetes.io/component`, which is immutable. U3 generalises the DownloadClient controller's
  `replaceForClaimTemplates`: when a live StatefulSet's or Deployment's selector differs from the
  desired one, it deletes the workload with background propagation and applies the new one. PVC
  safety: a StatefulSet is replaced only while its `persistentVolumeClaimRetentionPolicy.whenDeleted`
  is unset or `Retain` (clustarr never sets it; anything else reads `Ready=False, InvalidSpec` and
  is not touched); the usenet scratch PVC is a separate object; `/data` is the shared claim. The
  claim templates keep their names, so the new pods mount the same claims. One engine restart per
  client, which N's re-render costs anyway.
- **Pool, graft and reduce Jobs** labelled `app.kubernetes.io/managed-by=squasharr`: the runbook
  deletes the pool Job before the upgrade (step 4); the Migrator's preparation deletes any left
  (background propagation, after the fold's task withdrawal), counted
  `action="delete_legacy_job"`; N's controllers create `transcode-pool-…` Jobs.
- **Leases:** read by the legacy-lease gate under their old names; the runbook deletes them (step 15).
- **Per-service workloads:** deleted by Helm at the W6.11 cutover; nothing to migrate.

### 4.5 Metrics

No metric is renamed. Label values change at the upgrade boundary (§2.8); a series keyed on
`consumer="catalogarr-search-high"` ends and `consumer="catalog-search-high"` begins. No dashboard
or alert exists in the repo or on the cluster; `docs/observability.md` and W10.5's
`docs/autoscaling.md` print the new values. The manager's autoscale reconciler renders every HPA's
External-metric selector from the topology, so the HPAs follow by themselves.

### 4.6 Ephemeral classes (no migration; renamed in code)

Identifiers, packages, files, errors, logs, spans, comments, CRD descriptions, ui text, Event
recorder names (Events expire in an hour), Envelope `Source` values, field-index names, controller
and registration names, RPC subjects and queue groups (§4.2.4), metric label values (§4.5). Two
stored-but-transient values are read both ways in N: the Search outcome names and prefix
(`searchoutcome.IsWorkerOutcome`/`IsTruncatedOutcome`/`Reserved` accept the `legacynames` spelling)
and the classifier's `managedFields` (§4.1.5); the condition reason `IndexUnavailable` is written
fresh by the next reconcile.

### 4.7 Helm values keys and CLI flags

**Values (W6.13, chart 0.5.0).** Not renamed: chart 0.5.0's closed schema rejects every 0.4.x key
("additional properties 'catalogarr' not allowed"), and the README's "Upgrading from 0.4.x" table
names old key → new key; that section is the one place a live doc names the old keys (§7). New keys
are domain-named already (`manager`, `agents.<domain>`, `markers`); their `consumerSlots` keys are
the new durable names (`markers.consumerSlots.markers-analyze`,
`agents.import.consumerSlots.import-fileimport`), and the facade default is `<fullname>-index-facade`.
`TestRetiredValuesKeysAreRejected` reads `legacynames.ValuesKeys`.

**CLI flags.** None is branded. The installers W6.11 writes render the renamed defaults
(`CLUSTARR_ENGINE_SERVICE_ACCOUNT=<fullname>-grab-engine`,
`CLUSTARR_FACADE_API_KEY_SECRET=<fullname>-index-facade`, `CLUSTARR_CONSUMER_SLOTS` keys).

### 4.8 Rollback from N (additions to loop §7.6)

Loop §7.6's procedure, with three steps added (each refuses while `manager.clustarr.io` is held):

- **3a. Restore names.** `bin/manager legacy-fold restore-names --namespace clustarr-system
  --nats-url <url>` from N's commit: the inverse rename (new → legacy, same merge rule) on every
  object of §4.1.3, then the deletion of N's six renamed work streams and their durables (they hold
  only N's tasks, which nothing old consumes; deleting them frees the single-node memory N-1's
  streams need, and `CLUSTARR_WORK_TRANSCODE` must go before N-1 can create
  `CLUSTARR_WORK_SQUASHARR` on the same subjects). It prints the counts and exits non-zero while any
  object still holds a new-named entry. Without it N-1's controllers co-own with the renamed entries,
  and every N-1 clear-by-omission (an `activeDownloadRef`, a `pendingGrab`) silently never clears.
- **3b. Engine workloads.** `kubectl -n clustarr-system delete statefulset,deployment -l
  app.kubernetes.io/component=grab-engine` (claims are kept); N-1's grab controller re-renders them
  with its selector (its apply against N's selector would be refused as immutable).
- **5 (amended).** After `helm rollback`, N-1 finds its idle `catalogarr-history`,
  `catalogarr-rss-matcher` and `catalogarr-redownload` where it left them and resumes them; its
  `EnsureTopology` recreates its work streams empty; the facade Secret it reads was never touched.

| From → to | Effect of the names work |
| --- | --- |
| N → previous | Lossless for objects with 3a; N-1's queued work at upgrade time is not resumed (re-derived, §4.2.1); history and RSS resume from the upgrade-time floor. |
| N+1 → N | Nothing to do: N+1 holds the new names. |

### 4.9 Release N+1

`pkg/legacynames` keeps only `ValuesKeys` and the three idle legacy durables N+1 retires;
`k8s.OwnedBy` loses its legacy half; `Topology.Displaced` empties; `Topology.Retired` gains those
durables on kept streams (`catalogarr-history`, `catalogarr-rss-matcher`,
`catalogarr-redownload`) and their dead-letter watchers, read from `legacynames` (N+2 deletes the
three names and the entries); `LegacySchemas` drop the `importarr.*` values; the replay mapping goes; `restore-names` goes
with the legacy fold (F9.1). **N+1 gate:** the names census reads 0 (`legacy_managers`), no
legacy-subject dead letter remains, the legacy facade Secret was compared and deleted by the
operator.

## 5. Sequencing

**Order:** F3 → F4 → A1 → A2 → A3 → **Wave U** → A4 / A5 / A6 → F5 → F6 → F7 → F8 with A8 (and
U's Migrator amendments) → A7 → W6 → W6b → W9 → W10 → F9 with A9. Wave N (ADR-0020/0021) follows
the implementation waves and uses the new names.

**Why alone, and why now.** A rename touches nearly every package; run beside another wave it
would take that wave's uncommitted hunks into its commits (the pathspec-scopes-files-not-authors
gotcha). After A3 the downloads code it would otherwise rename twice has landed; before A4-A6 the
three parallel waves start on one vocabulary, and F5-F9, W6, W10 inherit it.

**Tasks** (`docs/superpowers/plans/2026-10-07-unbranded-names.md`):

| Task | What | Why in this order |
| --- | --- | --- |
| U1 | `pkg/legacynames`; every retired or legacy-read name re-pointed at it (incl. dual-use literals in tests) | isolates every string that must keep its spelling before any mechanical pass |
| U2 | `app/squash` → `app/transcode`; `importarr.go` → `import.go`; every branded Go identifier and alias; `Makefile` RBAC path | compile-only, zero behaviour change |
| U3 | Kubernetes names: field-manager values, label/annotation keys and values, object-name defaults, `k8s.OwnedBy`, Search outcome read-both, engine workload replace | the stored half the Migrator migrates |
| U4 | NATS names: streams, subjects, durables, queue groups, RPC, schema values and `LegacySchemas`; `Displaced`, `HandoverFrom`, `KeepTopology` retire; DLQ `legacynames.Subject` | the wire half, with its topology mechanics |
| U5 | every remaining Go string and comment, `api/` comments, `make generate manifests`, `make templ` | cosmetic, after the semantic changes |
| U6 | non-Go files Wave U owns and live docs | outside the W6 installer set |
| U7 | `hack/brand-gate.sh`, `make brand-gate`, the wave gate | proves the result |

**The legacy installer set is W6's, not U's.** `config/manager/`, `config/rbac/`,
`config/prometheus/`, `config/postgres/`, `config/e2e/`, `config/README.md`, `charts/clustarr/`,
`Makefile`'s RBAC identities and `hack/e2e.sh` are deleted or rewritten by W6.9-W6.15, whose task
text names those files by their branded paths (W6.11: "Delete:
`config/manager/{catalogarr,…}.yaml`"). Renaming them first would invalidate W6's instructions and be
undone days later. The gate carries them in a temporary allow-list that W6.17 deletes (§7).

**Rebasing onto `main`.** Other sessions keep committing to `main` under the old names. Every rebase
of this branch re-applies U2-U5's commands to the incoming files (the plan's U5 step 1 is written to
be re-run), and the gate catches what the commands miss.

## 6. Mapping table for later tasks

Agents executing A4-A9, F5-F9, W6, W6b, W9, W10 and Wave N read their task text through this table.
The task text is not edited (the amendment pointers say so); a name in it that the table maps is
written with the new name. Retired names (R-U3) are used through `legacynames`.

**Packages and files:** `app/squash/<p>` → `app/transcode/<p>` (every subpackage, same package
name); alias `squashmanager` → `transcodemanager`; `pkg/events/schema/importarr.go` →
`pkg/events/schema/import.go`. Unchanged: `app/catalog`, `app/import`, `app/indexer`, `app/grab`,
`app/caption`, `pkg/transcode`.

**Go identifiers:**

| Old | New |
| --- | --- |
| `k8s.ManagerCatalogarr{,Series,Worker,Metadata,Grab,Fanout,Artwork,Classify}` | `k8s.ManagerCatalog{,Series,Worker,Metadata,Grab,Fanout,Artwork,Classify}` |
| `k8s.ManagerCatalogarrMarkers` | `k8s.ManagerRetiredMarkers` (value `legacynames.MarkersStatusManager`) |
| `k8s.ManagerImportarr{,Worker}`, `k8s.ManagerIndexarr{,Worker}` | `k8s.ManagerImport{,Worker}`, `k8s.ManagerIndex{,Worker}` |
| `k8s.ManagerGrabarr` / `k8s.ManagerGrabarrEngine` | `k8s.ManagerGrab` / `k8s.ManagerRetiredEngine` (value `legacynames.EngineTelemetryManager`) |
| `k8s.ManagerSquasharr{,Pool}`, `k8s.ManagerCaptionarr{,Worker}` | `k8s.ManagerTranscode{,Pool}`, `k8s.ManagerCaption{,Worker}` |
| `k8s.LegacyLeaseIDs` | `legacynames.LeaseIDs` (`k8s.LegacyLeaseIDs` kept as an alias until OD6 deletes the gate) |
| `events.StreamWork{Catalogarr,Importarr,Indexarr,Captionarr,Squasharr,Segmentarr}` | `events.StreamWork{Catalog,Import,Index,Caption,Transcode,Markers}` |
| `events.FilterWork{Catalogarr,…,Segmentarr}` | `events.FilterWork{Catalog,…,Markers}` |
| `events.ConsumerSegmentarrAnalyze` | `events.ConsumerMarkersAnalyze` |
| `events.ConsumerSquasharrResults` | unchanged identifier, value `legacynames.TranscodeResultsDurable` (F7 deletes it) |
| `events.ConsumerCatalogGrab`, `events.ConsumerCatalogRedownload` | unchanged identifiers, values `legacynames.GrabDurable`, `.RedownloadDurable` |
| `events.QueueGroupIndexarr`, `events.QueueGroupCatalogar` | `events.QueueGroupIndex`, `events.QueueGroupCatalog` |
| `lifecycle.RecorderGrabarrEngine` | `lifecycle.RecorderGrabEngine` |
| every other `*Catalogarr*`, `*Importarr*`, … identifier | the token map (R-U4 for keywords) |

`events.Consumer*`, `events.Filter*` (other than `FilterWork*`), `events.RPC*`, `events.Work*Subject`
and `events.TranscodeTaskConsumerName` keep their identifiers; their values change.

**Strings** (what a task's text, shell step or test expectation spells): field managers per §2.5;
streams, durables, subjects, RPC subjects, queue groups and schema values per §2.6 (with
`transcode-pool-<profile>-<class>` and the doubled `clustarr.work.markers.markers.`); labels and
annotations per §2.7; objects per §2.10 (`<fullname>-grab-engine`, `<fullname>-index-facade`,
`transcode-pool-…`, `clustarr-grab-engine-role`); Event recorders `catalog-history`, `grab-engine`;
Source prefixes per §2.6; the Makefile identity `grab-engine` (`RBAC_PATHS_grab-engine`,
`config/rbac/grab_engine_role.yaml`); values `consumerSlots` keys are durable names. A task that
spells a value it **retires** in N (`catalogarr-grab`, `catalogarr-redownload`,
`squasharr-transcode-results`, `catalogarr-segments-result`, `catalogarr-markers` as a field manager,
`grabarr-engine` as a field manager, the task-withdrawal finalizer, the leases) uses the
`legacynames` constant.

**Commands** (`kubectl`, `nats`) in runbooks and e2e text: the new names, except steps that act on
N-1's objects before or during the upgrade (scale-downs, drain checks, `-l
app.kubernetes.io/managed-by=squasharr`, the legacy streams), which keep the old names; those live in
`docs/superpowers/` (allowed, §7).

## 7. The gate

Committed by U7 as `hack/brand-gate.sh` (called by `make brand-gate` and by the guard
`TestNoBrandNameOutsideTheLegacyTable`). Its pattern spells no brand, so the script does not match
itself:

```bash
re='(catalog|import|index|grab|caption|segment)arr|squas[h]'
{ git grep -n -I -i -E "$re" -- . \
    ':(exclude)pkg/legacynames' \
    ':(exclude)docs/superpowers' ':(exclude)docs/research' ':(glob,exclude)docs/adr/[0-9]*.md' \
    "${temp[@]}" || true; } | awk '…'   # full text in the plan, U7
```

**Allow-list (permanent):**

| Path | Why |
| --- | --- |
| `pkg/legacynames/` | the legacy-name table (N+1: `ValuesKeys` and three retiring durables) |
| `docs/superpowers/` | dated specs, plans and runbooks: records of what was built, and the runbook steps that act on N-1's objects |
| `docs/research/` | dated research notes |
| `docs/adr/[0-9]*.md` | ADRs are immutable records; `docs/adr/README.md` is live and checked |
| three path citations, stripped before matching: `docs/research/phase-d1-indexarr-spec.md`, `docs/superpowers/plans/2026-09-19-phase-d1-indexarr.md`, `docs/superpowers/plans/2026-09-22-phase-d2-grabarr.md` (and `.superpowers/sdd/2026-09-22-phase-d2-grabarr/`) | live files cite historical files by path (`test/e2e/download_test.go:21`, `helpers_test.go:1122`, `hack/deps/deps.go:55`) |
| `charts/clustarr/README.md`, lines inside `## Upgrading from 0.4.x` | the one live table naming the retired values keys |

**Temporary allow-list, deleted by W6.17:** `charts/clustarr`, `config/manager`, `config/rbac`,
`config/prometheus`, `config/postgres`, `config/e2e`, `config/README.md`, `Makefile`, `hack/e2e.sh`
(the legacy installer set, §5). Everything else -- Go in every directory, `api/`, the generated
CRDs, `ui/`, `test/`, `hack/` (but `e2e.sh`), `images/`, `.github/`, `config/{crd,default,samples,nats,…}`,
`CLAUDE.md`, `README.md`, `docs/*.md`, `docs/adr/README.md` -- must be clean from U7 on.

The gate sees `squash`, so `app/squash` cannot creep back; it does not see `iptvtunerr` or other
words ending in "rr".

## 8. Supersessions and amendments

- **Split ruling R7** ("every existing k8s.Manager* string and every NATS durable/consumer/stream
  name stays unchanged"): superseded for the brand (owner, 2026-10-07). Its second half -- a new
  manager or a CAS path where the split puts one name in two processes -- stands.
- **Split §3.11 "Unchanged"** (managers, durables, streams, subjects, RPC subjects, queue groups,
  controller names, Envelope `Source` strings, the engine SA, engine selectors, the
  `<fullname>-indexarr-facade` Secret): superseded by §2/§4; the ui Service, the art-signing Secret
  and the PVCs stay. **Split §10.0**'s kept names: `<fullname>-grabarr-engine`, component
  `grabarr-engine`, pool prefix `squasharr-pool-` and `managed-by=squasharr` become §2.10's.
- **Loop §7.3.9** (retiring `squasharr-transcode-results` and `catalogarr-segments-result` by
  `Topology.Retired`): they go with their displaced streams (§4.2.2).
- **A-plan R22** ("`catalogarr-grab` … deleted in N+1"): deleted in N with its stream.
- The per-task amendments (F8.3-F8.6, F8.9, F8.11, F8.12, F9.1, F9.4, F9.5, A4.4, A6.4, A7.2,
  A8.1-A8.5, A9.2, A9.5, W6.9, W6.11, W6.13-W6.15, W6.17, W10.2, W10.4-W10.8) are the plan's
  "Amendments" table; each task carries one `> **Amended by Wave U:**` line.
- No new ADR: names are not architecture. `docs/adr/README.md` gets a one-line note (U6).

## 9. Risks

1. **A missed reader of `managedFields` by name** (§4.1.5) misreads every unrenamed object for
   ~28 minutes after the upgrade; the classifier's case would overwrite owner classifications.
   Mitigation: `k8s.OwnedBy` plus a guard that no non-test code compares `ManagedFieldsEntry.Manager`
   outside `pkg/k8s` (`TestManagedFieldsAreReadThroughOwnedBy`).
2. **Rollback without `restore-names`** silently breaks N-1's clear-by-omission. Mitigation: it is
   a numbered rollback step that refuses while the lease is held and fails loudly while any new
   name remains; F8.9's upgrade-and-rollback scenario runs it.
3. **A dual-use string renamed by the mechanical pass** (`catalogarr-markers` is a retired manager
   and a renamed durable; `catalogarr-grab` a renamed manager and a retired durable; `grabarr-engine`
   a retired manager and a renamed SA, recorder, label value and role). Mitigation: U1 classifies every
   occurrence before U5's pass and replaces the retired uses with `legacynames` constants; U5 ends with
   a review grep for the new-looking spellings of retired names.
4. **Main keeps committing old names** until the branch merges. Mitigation: the rename commands are
   re-runnable per rebase; the gate fails the rebase otherwise.
5. **The live NATS memory store** may be smaller than assumed; displacing all six streams (§4.2.1)
   is what keeps N's start inside N-1's footprint.
6. **The facade Secret.** A copy, not a rename; anything outside clustarr that reads the Secret by
   name (a script, an ExternalSecret) must follow (the facade's Service URL already changes at
   W6.11).
7. **cluster-plex** carries 8 prose lines (`squasharr`, `catalogarr` in comments and docs) and no
   wire dependency (it reads the ui Service, the art key, `plex-token` and the CRDs); out of scope
   here.

## 10. Open questions (owner)

- **Q1. Historical docs keep their names.** The owner's scope lists "docs". This design renames every
  live doc (CLAUDE.md, READMEs, `docs/*.md`, `docs/adr/README.md`) and leaves the records --
  ADRs, dated specs and plans (4,182 lines), research notes (238) -- as written, with one note in the
  ADR index and a top note in the three in-flight specs. Rewriting them would change what the records
  say was built and the paths and names the in-flight plans' agents verify against the tree.
  **Recommend: keep them as written.** Cost if the owner wants them renamed: a later docs-only sweep
  plus moving three files that live files cite.
- **Q2. The doubled token `clustarr.work.markers.markers.*`** (TheIntroDB's markers tasks on the
  markers stream). R-U1 keeps the mechanical result rather than invent a word.
  **Recommend: keep it.** Choosing another task token (`…markers.introdb.*`) now costs nothing extra
  (the stream is displaced either way); later it is a second migration.
