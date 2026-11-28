# Manager load: measurements and design for ADR-0025

**2026-11-28.** The record behind `docs/adr/0025-manager-work-proportional-to-change.md`
(Proposed): what was measured on talos, what was read in the code and in two outside
sources, the cursor each input gets, the waves, and what is still owed. Owner: "properly
analyze the manager's CPU and memory usage via profiling and determine where the peak usage,
sustained load, etc are coming from"; "a comprehensive architecture that eliminates the tech
debt without any compromising config like tackling everything by constant garbage
collection". The design was argued in four rounds with a second reader acting as principal
architect, who read the code and the NATS documentation independently; where a position
below is that reader's and not a measurement, it says so. Nothing here is built.

## 1. Measurements

Pod `clustarr-manager-6cb767db4d-4t8hl`, image `sha-76f75ea`, started 08:49:35Z, CPU limit 2
(resized by hand; the Deployment says 750m), memory 1536Mi limit / 768Mi request,
`AUTOMEMLIMIT=0.8`, `GOGC=20` (both from the owner's values; the chart sets neither).
Sources: VictoriaMetrics (the Go runtime's metrics, controller-runtime's, `clustarr_*`),
pprof (CPU 90 s settled; heap after a forced collection; allocations since start), a 30 s
CPU profile taken inside a start-up pass at 750m (03:58Z), and the Postgres catalog.
`/cpu/classes/gc/total` is documented as an overestimate that is not directly comparable to
process CPU time; read the GC columns as upper bounds.


### 1.1 The day in numbers (10-minute rows; four restarts at 03:16, 03:54, 06:27, 08:49Z)

| phase | process CPU (cores) | of which GC (runtime's count) | GC cycles/min | alloc MB/s | live heap MiB | RSS MiB | queue depth |
|---|---|---|---|---|---|---|---|
| settled, 750m limit (04:50-05:30) | 0.11-0.14 | 0.12-0.13 | 5 | 6.5 | 480-490 | 855 | 0 |
| settled, 2 CPU, later build (07:10-08:40, 09:20-10:00) | 0.15-0.23 | 0.065-0.09 | 6-9 | 10-14 | 550-580 | 890-940 | 0-1 |
| start-up pass at 750m (03:54 to 04:40, 46 min) | pinned 0.75 | 0.5-0.69, mark assist 0.2-0.37 | 17-27 | 36-49 | 650-818 | 1,000-1,160 | 62,000 falling to 0 |
| start-up pass at 2 CPU (06:27 to ~06:58; 08:49 to ~09:15) | 1.0-1.5 avg, 1.9 peak | 0.6-0.7, assist 0.3-0.4 | 41-60 | 96-125 | 730-846 | 1,050-1,170 | 62-64,000 falling to 0 |
| unexplained bursts on the earlier build (05:38-05:48, 06:08-06:17) | pinned 0.75 for ~10 min | 0.6-0.8 | 24-32 | 54-72 | 470 rising to 800 | 1,220-1,280 | 0 |

- 24 h of live heap at 5-minute resolution: baseline 420-500 MiB with frequent excursions to 650-820 lasting 10-60 minutes, about half the day; flat 550-580 since 06:35Z today.
- The 05:38 and 06:08 bursts: almost no reconciles (2-10/s), row cache decodes low, no Live TV viewers or tuners in use, logs silent. Unprofiled. Best hypothesis: Live TV `Tuner.render` re-run by IPTVProvider spec edits another session was making at that time (render = 17.4 GB of allocation in the 75 min since this start; documented at ~100 MB live and ~9 min at 750m).
- Settled baseline rose ~85 MiB at 06:27: the Live TV per-stream WireGuard egress (commit 67cf20b9) runs a userspace WireGuard device and gVisor netstack inside the manager.

### 1.2 The start-up pass, counted (first 30 minutes after the 08:49 start)

- Reconciles: controller `mediafile` (the remediation loop: files and items) 72,200, with 14,400 s of summed reconcile time (8 of its 16 workers busy on average); transcode-admission 11,400; iptvchannel 1,510; indexerdefinition 750 (Cardigann bundle re-applied every start).
- Remediation pass outcomes: MediaFile unchanged 38,300, applied 617; **Episode applied 17,300**, unchanged 5,800; **Album applied 6,890 (every Album)**; Movie applied 1,300; Series applied 1,050; Artist applied 423. So about 27,000 status applies after a restart in which nothing in the world changed.
- Effects in the same window: dispatchAnswered 13,600, ledger 3,900, publish 605. File I/O: 56,900 stat and 18,800 readdir on the NAS.
- Pass reads: 105k Series memo hits, 63k Episode memo, 44k Episode reads, 39k RootFolder reads, 39k QualityProfile reads (22k memo), 23k Album reads, 22k Series reads.
- The same counters for a settled 30 minutes: 2,900 passes total (Episode unchanged 1,350, MediaFile unchanged 894, applied ~480 in all), 1,640 stat, 49 readdir. Settled work is about 4% of the start-up pass's rate.

### 1.3 Where the start-up pass's CPU goes (30 s profile at 750m, 03:58Z, 75% of one core sampled)

- remediation ItemReconciler 52%: grab stage Plan 25% which is `decision.ParseForItemState` -> `release.Parse` 23.5% (every item's recorded downloads and held releases re-parsed from their title strings on every pass); album ReconcileItem 18%.
- Live TV `Tuner.render` 20.7%.
- `library.Store.Apply` 18% (mutate 17%): the manager's in-process server-side apply for library rows: structured-merge-diff 4.4%, kube-openapi validation 2.9%, YAML/JSON round trips.
- runtime: mallocgc 19%, gcBgMarkWorker 15%, gcAssistAlloc 14%.
- row cache read 2.9% (it was 63% before today's decoded-object memo).

### 1.4 Allocation since start (75 min, 132 GB total = 29 MB/s average)

| holder (cumulative) | GB | share |
|---|---|---|
| remediation Reconcile | 70.3 | 53% |
| - of which ItemReconciler | 55.5 | 42% (album 13.6, episode 12.2, search stage 10.8, grab stage 7.6, naming 3.7, downloads 3.4) |
| library Store.Apply (in-process SSA) | 22.4 | 17% (mutate 19.2, yaml.Unmarshal 3.2; kube-openapi Validate 5.3 inside) |
| row cache read (decode) | 21.3 | 16% |
| Live TV Tuner.render | 17.4 | 13% (xmltv.splitElements 10, encoding/xml) |
| library Collector.Sweep (every 30 s, all owned rows) | 13.6 | 10% (~90 MB per sweep) |
| admission Ledger.rebuild/scanBacklog (every 5 min) | 6.1 | 4.6% (~400 MB per resync) |
| logging Handler.rebuild | 3.7 | 2.8% |
| release.Parse 3.0, quality.FromCRD 2.5, ParseForItemState 2.1 | | |
Flat: DeepCopyJSONValue 4.3 GB, SeriesMetadata/SeriesStatus DeepCopyInto 4.1 + 3.5 GB, gVisor buffers 3.7 GB, WireGuard pools 2.6 GB.

### 1.5 Settled CPU (90 s profile, 16.9 s sampled = 0.19 cores)

- GC background mark 31%.
- transcode-admission Reconcile 20.5% (`Ledger.Pass` 19.9%: dispatchedLocked, rerouteLocked, candidatesLocked, inBacklogLocked, openCounts, metricsLocked: full walks of the in-memory ledger of every video file on each wake; 1,590 wakes per 30 min, all counted as retries).
- remediation Reconcile 16.7% (items 14.2%).
- userspace WireGuard + netstack ~7% (a stream was playing).
- library Collector.Sweep 6.2%.
- row cache decode 4.1%; mallocgc 3.8%.

### 1.6 Heap in use after a forced GC (526 MB)

| holder | MB |
|---|---|
| WireGuard device buffer pools (`PopulatePools`) | 82 |
| index sets (`sets.Set[string].Insert`, client-go indexers: addKeyToIndex cum 78, updateLocked cum 83) | 63 |
| encoding/json/v2 makeString (decoded strings held by informer objects and memo) | 53 |
| row cache compressed rows (`codec.compress` 42 + recompress) | ~49 |
| row cache per-row metadata (`rowify`) | 21 (cum 46) |
| admission ledger entries | 18-20 |
| remediation priority queue + its metrics | 16 + 8 |
| maps.clone 14, reflect.growslice 12, reflect.mapassign 10.6 | |
| library MetadataListerWatcher (metadata-only informers of the library kinds) | 10 |
| controller-runtime field indexes | 9.5 |
| DeltaFIFO | 9 |
| Live TV tuner cache (`loadCache`) | 32 cum |
Row cache as measured: Album 6,890 rows 7.1 MiB (35 MiB as objects), Episode 27,700 rows 9.4 MiB (43), MediaFile 35,100 rows 18.2 MiB (122), Movie 1,290 rows 3.1 MiB (14), Series 752 rows 1.9 MiB (10): 40 MiB holding 225 MiB.

### 1.7 Postgres (the library store)

mediafile 178 MB / 35,059 rows / 56,048 updates since stats reset; episode 104 MB / 27,709 rows / 48,094 updates; album 59 MB / 6,891 rows; `head` 6 rows with 183,914 updates (the per-kind change-feed head); `change` 3 MB (feed, 1 h retention). The whole library is ~340 MB on disk.

### 1.8 Other processes holding the same data

ui 875 Mi RSS and 115m CPU (its own informers of everything, read-only); agents 166-382 Mi each; usenet engine 953 Mi.

### 1.9 "Applied" is not "written", and the other late figures

- The item passes' `applied` outcome means the apply got past the in-process memo
  (`app/remediation/items.go`), not that a row changed. From the store's ownership records
  (the `managed` column's per-manager times), in the 08:49-09:25Z start window 0 of 6,891
  Albums, 72 Episodes and 98 MediaFiles had any field manager's fields change, against
  27,000 item passes counted `applied`. Table totals since the 2026-11-26 cutover agree:
  `album` 7,575 updates ever, `library.head` 184,221 updates, `library.change` 183,644
  inserts; the feed held 1,262 changes for the last 62 minutes measured (0.34 a second).
  The 2,350 Movie and Series applies go to the apiserver and were not measured.
- Apiserver requests from the manager: 66,400 GETs, 5,180 PATCHes and 582 POSTs in the
  first 30 minutes of a start; 43,800 GETs, 596 PATCHes and 374 PUTs in a settled 30
  minutes. The GETs are the library collector (about 730 a sweep, every 30 seconds).
- Scheduler latency p99: 0.5 ms settled, 53 ms in a burst at 750m, 117 ms in a start at
  750m, 69 ms in a start at 2 CPU. Mean remediation pass: 0.37 s in a 750m start, 0.084 s
  in a 2 CPU start, 0.02-0.05 s settled. `GOMAXPROCS` is 2 in both.
- Objects in use: 3.5 million; `encoding/json/v2.makeString` 950,000, index sets 292,000,
  field indexes 270,000, the priority queue 151,000, pgx row functions 131,000.
- Rolls: Helm revisions 310 to 313 restarted the manager four times between 03:16Z and
  08:49Z. A fifth was avoided at 10:30Z: a chart version bump meant to change one image
  would have rolled every pod, because every pod template carries `helm.sh/chart`
  (`charts/clustarr/templates/_helpers.tpl`); `app.kubernetes.io/version` is stamped the
  same way.

## 2. What the code does that the measurements show

- Boot: library store opened and migrated; row cache for Album/Episode/MediaFile/Movie/Series; informers of the six library kinds are fed by one `SELECT` of the whole table each, then `LISTEN` plus a 15 s feed poll per informer; after sync the row cache samples 2,000 rows for a zstd dictionary and recompresses every row; `WarmInformers` on every replica; NATS topology ensured; the Cardigann loader applies 752 IndexerDefinitions (~1,500 API calls) every leader start; the artwork object index lists the whole bucket (53k objects).
- **The start-up pass is by construction**: the remediation controller's file watch `CreateFunc` enqueues every MediaFile in the initial list (app/remediation/sources.go:88); item passes follow. 16 workers, priority queue, no rate limiter anywhere in app/ (controller-runtime defaults).
- Per-key state is in memory only: `keyStates` (backoffs and a normalised status memo per key, unbounded), `ApplyMemo` (UID -> resourceVersion + digest, 262,144 entries, lost on restart, which is why a restart re-applies status).
- Periodic whole-library work: admission `ledgerResync` + `scanBacklog` every 5 min (every video MediaFile key, a Get and an `Eligible` each); library `Collector.Sweep` every 30 s (SQL over every owned row in six tables, held in a Go slice); `wantedcron` at 00:00 and 12:00 lists all Movie/Episode/Album/Book/Audiobook; informer resync 10 h (default, nothing sets SyncPeriod); MediaFile recheck 24 h per file (clumps after a restart because every file is passed at start); object-store maintenance 6 h.
- Live TV in the manager: the tuner (playlist 28.5k live entries of 1.24M streamed, guide 13k channels from a ~500 MB XMLTV source, render on any provider config change, any fetch landing, any Plex grid arriving; one at a time, 2 s debounce), the stream relay (64 MiB off-heap ring per stream) and since today a userspace WireGuard egress per stream.
- Leader election: lease 60 s / renew 45 s; losing the lease exits the process; replicas 1; RollingUpdate maxUnavailable 0 so two pods overlap on a roll, and a standby would already hold four warmed caches. Every image roll restarts the manager.
- Runtime: no SetGCPercent/FreeOSMemory in code; GOGC 20 and AUTOMEMLIMIT 0.8 come from the owner's values; chart default resources are 200m/640Mi requests, 2 CPU/1664Mi limits; talos overrides CPU to 750m.


## 3. Two outside sources

### 3.1 VictoriaMetrics and VictoriaLogs (source, commit 8d6d347c)

- Storage is a local directory of immutable parts listed in an atomically replaced `parts.json` (tmp, fsync, rename, fsync dir); no WAL (a few seconds of loss accepted because recent data is in memory parts flushed on a timer); no checksums (corruption panics).
- Start-up reads only the part list and each part's small metaindex; data files are opened and mmapped lazily on first read. No replay, no index rebuild.
- Memory: one budget (`-memory.allowedPercent`, 60% of the cgroup limit) from which every cache takes a named fraction; the remainder is deliberately left to the OS page cache; bulk data is read through read-only mmap; caches (`fastcache`) keep bytes in 64 KiB chunks from anonymous mmap, off the Go heap; a two-generation working-set cache and a block cache that admits an entry only after N misses; pooled buffers, flat `[]byte` + offsets instead of pointer structures. It sets GOGC=30 itself and tells operators to raise it if GC exceeds 5% of CPU; no GOMEMLIMIT.
- Background work is bounded by fixed worker semaphores and jittered timers; merges are sized by free memory/disk; snapshots are hard links; object storage is only for backups.
- VictoriaLogs reuses the same engine with per-day partitions, per-stream index and column blocks with bloom filters.
- The researcher's judgement of what transfers to a keyed, read-mostly, single-writer document store already persisted in Postgres: one memory budget; bulk bytes off-heap/mmap; a persisted snapshot + lazy open so restart reads metadata only; atomic whole-file writes and no WAL because Postgres is the truth; immutable generations for readers; bounded, jittered background work; cache admission. What does not transfer: LSM merges, time partitions, bloom filters, the inverted index.

### 3.2 NATS on restarts (docs.nats.io, against nats.go v1.56.0 and the server fork at v2.15.3-clustarr.1)

The second reader's findings, each from the page named.

| Mechanism | What the documentation says | Cost |
| --- | --- | --- |
| Durable consumer, explicit ack | The server keeps the position; a client "resumes from that position, backlog included"; recreating the consumer loses it (learn/jetstream/policies). Unacked messages return after AckWait, 30 s by default (learn/jetstream/delivery-and-acknowledgment). | Replicated server-side state per consumer, on the control planes at R3; redelivery tied to AckWait. |
| Ordered consumer | No acks, one replica, in memory, removed after 5 idle minutes; can start at a chosen sequence; "Don't build a long-running job on an ordered consumer expecting it to resume ... after a crash" (learn/jetstream/ordered-consumer). | Nothing on the server; the application keeps its own position. |
| Key/Value watch | An ephemeral ordered consumer: the last value of each key, then changes (learn/key-value/watching). nats.go has `ResumeFromRevision` (`kv_options.go`). | The opening snapshot reads the whole bucket unless a revision is given. |
| Last per subject | The newest message per subject, what a watch is built on (learn/jetstream/policies). | Grows with the keys: the whole-store scan behind this cluster's NATS memory spikes. |
| Consumer reset (server 2.14+) | Resets a consumer to its ack floor or a sequence (nats-architecture-and-design ADR-60); `Stream.ResetConsumer` in nats.go v1.56.0. | An admin call; untested here on work-queue streams. |

- Expiry by MaxAge is invisible to a watcher unless limit markers are enabled
  (learn/key-value/ttl-and-limits). From the fork's source, not the docs
  (`selectStartingSeqNo`): a start sequence below the stream's first is moved up to it
  silently.
- Server-side rolls are already handled: the chart sets lame duck (grace 10 s, duration
  30 s) and clients need do nothing (running-a-nats-service/nats_admin/lame_duck_mode);
  upgrades go non-leaders first and the meta-leader last, each replica current before the
  next (nats_admin/upgrading_cluster). A PodDisruptionBudget with `minAvailable: 2` is
  worth adding if the chart lacks one.
- Client drain is already right: Drain does not ack for the client
  (learn/resilient-clients/drain-and-shutdown) and ADR-0020 acks before draining.
- An object-store watch cannot resume: nats.go accepts `ResumeFromRevision` there and
  replays from the start (`pkg/events/natsbus/objectstore.go`), so every manager start
  re-reads all 53,000 artwork objects.

## 4. The cursor each input gets

Progress is `library.progress(kind, uid, passed_version, passed_generation, inputs_digest,
planners_version, next_due, outcome)`, beside the tables cluster-plex reads and outside
that contract. A pass writes it strictly after its own writes and effects have landed.

| Input | Cursor | Where it lives | A lost cursor is |
| --- | --- | --- | --- |
| Library rows | `version > passed_version` is the change set; survives the feed's one-hour trim | `library.progress` | not possible: the row carries its version. A record ahead of its row (a restore, a failover) queues the key. |
| Live changes to library rows | the change feed, whose trigger adds a change mask (generation, labels, annotations, status-only, writer) so the self-trigger rules need no old object | the feed's sequence, in memory | covered by the row above at the next start |
| Custom resources (about 3,500) | generation and resourceVersion, equality only | `library.progress` | an inequality: the key is passed |
| Shared inputs (profiles, root folders) | `input_cursor(kind, name, rv)`; a difference fans out through the existing index functions at the pacer's rate, ahead of the audit | Postgres | an inequality |
| In-flight work | the status phase indexes | the rows | not applicable |
| Record buckets | a floor per bucket, the lowest sequence whose woken pass has finished, persisted every few seconds and before the drain; opened with `ResumeFromRevision(floor + 1)` | Postgres | the bucket's `Created` changed, or the floor is below the stream's first sequence; limit markers longer than the longest outage turn expiry into PURGE events. Repair: pass that domain's in-flight keys. |
| Intake streams (candidates, scans) | the durable consumer's ack floor; acked after the decision lands; reset on taking the lease | NATS | not possible while the consumer exists; `NumPending`, `NumAckPending` and the ack floor become gauges |
| Task events | acked after the pass has written the delivery into status; beyond the stream's one-hour MaxAge, status and due times stand in | NATS and status | bounded by `next_due` |
| Engine commands | none: each carries the whole desired state at a sequence | | |
| Global KV (MAL-Dubs, release groups) | reread, or resume by revision | in memory | harmless: small |
| Time | `next_due`, the earliest due time any planner returned, jittered by uid; a loader pages in the next window | `library.progress` | not applicable |
| Code | `planners_version`, a hash of one version constant per planner; a mismatch goes to the pacer | `library.progress` | not applicable |

A missed input is made visible three ways. The audit: when the pacer recomputes a key whose
digest matched and the pass writes, `clustarr_audit_drift_total{kind,planner}` counts it.
`TestEveryPassInputHasACursor`: the pass reader, the Env's KV wrapper, the IO executor and
each leader-local book record what a pass read, and an input with no registered cursor
fails the test. (`inputs_digest` covers the same set: object and record versions, the size
and modification time of every file the pass stat'ed, and each book's generation.) A time test
per planner: the same inputs at t and t+Δ give the same result unless t+Δ is past the due
time the planner returned.

## 5. The waves

Each is its own release, cut over cold (no adopting release), proven on kind first.

| Wave | What | Notes |
| --- | --- | --- |
| 0 | A trigger label on every pass; `applied` split into sent and written; `audit_drift_total`; a start-settled metric; a render metric; consumer gauges. Find why every Album pass applies at a start. | The empty apply memo explains it; the split metric confirms it. No ADR amended. |
| 1 | Maintained derived state: the admission ledger incremental, its backlog from the indexed phase with eligibility in status; the collector on delete events with one `SELECT DISTINCT` at start; counts in SQL; the Cardigann bundle on its digest. | Removes most settled CPU and the 24 GETs a second. Amends ADR-0016 and ADR-0024 (§3.4, §9). |
| 2 | Resumable reconciliation, the pacer, the NATS cursors and the persisted apply memo. Initial-list events wake nothing. | The first start has no progress records: every key goes to the pacer, one cycle of about a day; in-flight keys go at once. The library informers still exist until wave 5, so the memory lines of the acceptance table hold from wave 5. Amends ADR-0016, 0019, 0020, 0024 §3.3. |
| 3 | The Live TV executor: tuner, render, relay and egress in a sixth binary; the IPTV controllers stay; the last render persisted; the Service name and port kept; the leader-endpoint publisher retired. | Amends ADR-0018, the placement design §5.4 and `invariants.md`. Independent of waves 1 and 2. |
| 4 | Agents return parsed facts with a parser version. | Entries written before the cutover read as version 0: a pass that needs them asks at once; the pacer backfills the rest. Refines ADR-0019. |
| 5 | The library is queried: no informer for a library kind, a bounded byte cache, one feed consumer a kind, field indexes as selectable fields; `rowcache` deleted. | Amends ADR-0024 §3.2, ADR-0018's "one cache", the row cache design and `invariants.md`'s first bullet and `TestAManagerServesAKindThatHasNoCRD`. |
| 6 | Bounded structures from one budget; default `GOGC`; CPU limit 2; one image per binary; the chart and version labels off pod templates. | Amends ADR-0018 and the conventions record. |
| 7 | The ui queries (its Plex index's full listing every 5 seconds becomes feed-driven). Then bulk bytes as files, under its own ADR. | That ADR would supersede ADR-0011, amend ADR-0021 and retire ADR-0020 §5 for the stores that move. |

## 6. What wave 5 has to convert, and the guards

- `GetFieldIndexer().IndexField` starts an informer. Every index on a library kind
  (`mfindex.MediaKind`, `remediation.mediafile.item`, the search-in-flight indexes, the
  anime and MAL indexes, `artist.AlbumByArtistRefIndex`) becomes a selectable field with
  its expression index, or a value the loop writes into status for the purpose. Not all of
  them are plain paths today. Kubernetes caps a CRD version at 8 selectable fields and
  MediaFile has 6. The library schemas are installed nowhere, so the store may take more,
  but nothing may then validate those files as CRDs.
- `Client.Cache.DisableFor` names every library kind, so a cached Get cannot start an
  informer lazily; a guard refuses `GetInformer` for one.
- Reads go through the store's reader and a bounded byte cache the feed invalidates;
  staleness is what an informer's was, and writes stay compare-and-swap on version.
- The three watchers that need events (the loop's file arm, its item rollups,
  `missingfile`) take them from one feed consumer a kind through `source.TypedSource`.
- New status fields (parsed facts, eligibility) are capped lists, and the item status size
  budget test is extended (a Series holds 24 entries).
- What the second reader would not do: a storage engine or an mmap'd store in the manager;
  sharding or several leaders; warm standbys; GC or CPU tuning or hand resizes as fixes;
  server-side apply rewritten as writer documents; a manager-side parse memo as the end
  state; informer resync as a backstop; business schedules as sweeps; Go rules duplicated
  in Postgres triggers; metadata informers for library kinds; agents writing any index.

## 7. Owed, and the owner's decisions

Not measured: what wakes a pass (no label exists); the make-up of the 1.2 GiB start-up
peak (needs a start with heap profiles at minutes 3 to 5, and one with Live TV off); CFS
throttling on the day (cAdvisor is not scraped); the outcome of the Movie and Series
applies; the cause of the 10-minute bursts at 05:38Z and 06:08Z, which fit Live TV renders
re-run by provider edits; Postgres statement costs (`pg_stat_statements` is not installed).

For the owner: accepting the one-day first cycle at wave 2's cutover; accepting that a
missed input or a planner fix takes up to a day to reach the whole library, or allowing a
prioritised backfill for one planner; whether bulk bytes as files gets its ADR.

## 8. Measuring it again

- Metrics: `kubectl get --raw
  /api/v1/namespaces/observability/services/vmselect:8481/proxy/select/0/prometheus/api/v1/query_range?...`
  with the selector `{pod=~"clustarr-manager.*"}`. The useful series:
  `process_cpu_seconds_total`, `go_cpu_classes_gc_total_cpu_seconds_total` (and
  `..._gc_mark_assist_...`), `go_gc_cycles_total_gc_cycles_total`,
  `go_gc_heap_allocs_bytes_total`, `go_gc_heap_live_bytes`,
  `process_resident_memory_bytes`, `workqueue_depth`,
  `controller_runtime_reconcile_total`, `clustarr_remediation_passes_total{kind,outcome}`,
  `clustarr_remediation_io_calls_total{op}`, `rest_client_requests_total`,
  `go_sched_latencies_seconds_bucket`.
- Profiles: `kubectl -n clustarr-system port-forward pod/<manager> 16071:6060`, then
  `/debug/pprof/heap?gc=1`, `/debug/pprof/profile?seconds=90`, `/debug/pprof/allocs`
  (cumulative since start, so it holds the start-up pass). Read allocations with
  `go tool pprof -sample_index=alloc_space -top -cum`.
- Rows a window wrote: count library rows with a `managed` entry whose `time` falls in the
  window, per table.
- Logs: VictoriaLogs at `services/vlselect:9471/proxy/select/logsql/query`, stream
  `{kubernetes.pod_namespace="clustarr-system",kubernetes.container_name="manager"}`.
