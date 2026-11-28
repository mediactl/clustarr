# ADR-0025: The manager works in proportion to what changed: reconciliation resumes, the library is queried, one pacer audits, and the data plane leaves the control plane

**Status:** Proposed, 2026-11-28 (owner: "properly analyze the manager's CPU and memory usage
via profiling and determine where the peak usage, sustained load, etc are coming from.
Ideally, we would come up with a design that doesn't need a giant startup burst or any
periodic bursts since we can control for these types of events via proper systems design";
"Historically, we've sacrificed CPU for memory or vice versa, and it seems like both are now
evenly inefficient"; and of restarts, "review the NATS.io guidance/docs to determine if it
has a pattern for restarts"). Design, with the measurements, the cursor for each input and
the waves: `docs/superpowers/specs/2026-11-28-manager-load-design.md`. Nothing here is built.
Each wave becomes its own design and is proven on kind before it is deployed; the records it
amends are amended when that wave is accepted, not now.

## Context

ADR-0018 put every reconciler in one manager, ADR-0019 made that manager the control plane
and the only writer, ADR-0016 gave per-file work to one remediation loop, and ADR-0024 moved
the kinds a controller creates into Postgres behind the seams a custom resource has.
Together they left the manager doing work in proportion to the size of the library, not to
what changed.

Measured on talos on 2026-11-28 (about 72,000 reconciled keys; 1536Mi and 750m limits, the
pod resized by hand to 2 CPU for starts, `GOGC=20`; the design's §1 has the method and the
full figures):

| Phase | Process CPU | GC (runtime estimate, high) | Allocated | Live heap | RSS |
| --- | --- | --- | --- | --- | --- |
| Settled | 0.11-0.23 cores | 0.07-0.13 | 6.5-14 MB/s | 480-580 MiB | 855-940 MiB |
| Start at 750m, 46 min | pinned at 0.75 | 0.5-0.7 | 36-49 MB/s | 650-818 MiB | 1.0-1.16 GiB |
| Start at 2 CPU, 25-30 min | 1.0-1.5 | 0.6-0.7 | 96-125 MB/s | 730-846 MiB | 1.05-1.17 GiB |

- **A start is a pass over the whole library that changes almost nothing.** In its first 30
  minutes the remediation controller ran 72,200 reconciles, against 2,900 in a settled 30
  minutes, and its item passes counted 27,000 status applies. The store's ownership records
  show which library rows those applies changed in that window: no Album of 6,891, 72
  Episodes, 98 MediaFiles. (The 2,350 Movie and Series applies went to the apiserver and
  were not measured.) The pass made 56,900 `stat` and 18,800 `readdir` calls on the NAS.
- **A start happens often.** The manager restarted four times in six hours, each for an
  image roll. Live TV serves nothing until the tuner's first render, about nine minutes at
  750m.
- **The settled manager is nearly idle, and garbage collection is the largest share of
  what it does**: 31% of a 90-second profile at 2 CPU, then the transcode admission ledger
  walked in full on each wake (20%), remediation passes (17%), a userspace WireGuard stack
  (7%) and the library collector (6%).
- **The library collector asks the apiserver for every owner by name every 30 seconds**: 24
  GETs a second, settled, against Raspberry Pi control planes whose apiservers were
  OOM-killed this month.

What that says about the design:

1. **The system is level-triggered only because of the start-up pass.** Kubernetes inputs
   and the intake streams resume. Record buckets (watched "from its current end",
   `pkg/records/recordsource`), timers held in the work queue and the in-process memos do
   not: "a new manager applies every item once" (`app/remediation/applymemo.go`). The pass
   is the correctness backstop and cannot simply be removed.
2. **Derived state is rebuilt by scanning** (the admission ledger every 5 minutes, the
   collector every 30 seconds, the wanted list twice a day, the Cardigann bundle at every
   leader start) where the store could maintain it or answer by index.
3. **The library is cached in full although the store can answer queries.** Informers exist
   because etcd cannot filter; Postgres can.
4. **ADR-0024 moved the apiserver's apply cost into the manager.** "An unchanged write is
   free" holds for Postgres. The manager learns a write is unchanged only after the whole
   merge (18% of a start's CPU).
5. **A data plane and batch work share the control plane's quota and heap**: the Live TV
   tuner, its render, the stream relay and a userspace network stack per stream.
6. **The manager parses.** The grab stage re-parses every recorded release title on every
   pass (23.5% of a start's CPU); ADR-0019 left parsing to agents.
7. **Any change rolls the manager.** One image serves the manager and the ui, and every pod
   template carries the chart version and the image tag as labels.
8. **CPU against memory was never the real trade.** The start-up peak forced `GOGC=20`,
   and a low `GOGC` then charges every allocation in the settled state. Remove the peak and
   both fall.

Two outside sources were read for this. VictoriaMetrics and VictoriaLogs keep everything in
a local directory of immutable parts with no object store and no write-ahead log. What
transfers is the discipline: a start opens data and reads only small metadata, every cache
takes a named share of one budget, bulk bytes stay off the garbage collector's path, and
background work runs on fixed pools and jittered timers. The engine itself (merges, time
partitions, bloom filters) answers append-only time series and does not transfer to keyed
documents Postgres already holds. NATS documents how each kind of consumer resumes: a
durable consumer with explicit acks resumes from the position the server keeps; an ordered
consumer, which a
Key/Value watch is, keeps nothing on the server, is not meant to resume after a crash, and
can be opened at a chosen sequence. Persisting our own sequence and reopening from it is
our composition of those primitives, not a pattern the docs name. Expiry is invisible to a
watcher unless limit markers are on, and (from the server's source, not the docs) a start
sequence older than the stream's first is moved up to it silently.

## Decision

**The manager's work is proportional to what changed.** A start with nothing changed passes
almost nothing, whole-library work is one pacer at a fixed rate, the store answers what a
scan used to, and work that is not control leaves the manager.

1. **Reconciliation resumes.** Every reconciled key, custom resources included, has a
   record `library.progress(kind, uid, passed_version, passed_generation, inputs_digest,
   planners_version, next_due, outcome)`, written only after the pass's own writes and
   effects have landed. `inputs_digest` hashes the versions of every object and record the
   pass read, the size and modification time of every file it stat'ed, and the generation
   of every leader-local book it consulted (the ledgers, the delivery book, the inbox). A
   woken pass gathers its inputs; if they hash to the stored digest it ends without
   planning or applying, so it releases nothing. At start, initial-list events wake nothing
   and one query is the only source of work:
   - library rows whose `version` is past `passed_version`, or behind it (a restore);
   - custom resources whose generation or resourceVersion differs from the stored one, and
     the readers of a shared input (a profile, a root folder) whose cursor differs, fanned
     out at the pacer's rate ahead of the audit;
   - keys in flight, from the indexed status phases;
   - keys whose `next_due` has passed.

   An item's own schedules (the 24-hour recheck, the wanted search, the next subtitle
   search) are its `next_due`, jittered by uid, not a sweep. A change to the planners is a
   `planners_version` mismatch and goes to the pacer.

2. **One pacer is the backstop.** It revisits every key about once in 24 hours, under one
   key a second, at the lowest priority, with no digest shortcut. A pass that writes
   although its digest matched is counted in `clustarr_audit_drift_total{kind,planner}`;
   any value above zero is a defect (an input without a cursor, or a planner that is not
   deterministic). The age of the oldest audited key is a metric. The informers'
   `SyncPeriod` is 0, and a guard test holds the closed list of tickers.

3. **Inputs from NATS resume the way NATS provides for.**
   - Work streams stay durable consumers acked after the decision has landed. On taking
     the lease the manager resets the consumer, so what the last leader held unacked comes
     back at once; this is verified against work-queue streams on the server fork before
     it is relied on.
   - Record buckets are watched from a stored floor with `ResumeFromRevision`. The floor,
     the lowest sequence whose woken pass has finished, is persisted every few seconds and
     before the client drains; a restart replays those seconds, which idempotent passes
     absorb. `recordsource` already reopens this way after a disconnect.
   - A lost cursor is detected, since the server will not say: the bucket's creation time
     changed, or the floor is below the stream's first sequence, and limit markers make
     expiry an event. The repair passes that domain's in-flight keys, not the library.
   - A task event is acked after its delivery is in status.

4. **Derived state is maintained.** The admission ledger updates as each event arrives and
   coalesces its wakes; its backlog is the indexed transcode phase, with eligibility written
   into status by the loop that decides it. The collector acts on its owners' delete events
   and checks one `SELECT DISTINCT` of owner uids against cached parents at start. Counts
   are SQL. The Cardigann bundle is applied when its digest changes.

5. **An unchanged apply is decided before the merge.** For each object and field manager
   the store keeps the hash of the configuration last applied (the schema's digest in it)
   and the version it produced; the same hash at that version is a no-op. Applies to custom
   resources keep theirs in `library.progress`. Server-side apply itself is unchanged.

6. **Parsing happens once, at the edge.** A candidate or record carries the parsed facts
   the manager decides on, with a `parserVersion`, in capped status fields. A pass that
   needs facts an entry lacks asks an agent at once and waits for its record; the pacer
   backfills the rest.

7. **The library is queried.** No informer, full or metadata-only, exists for a library
   kind. Reads go through the store's reader and a bounded cache of row bytes the change
   feed invalidates; lists go through ADR-0024's SQL narrowing; one feed consumer a kind
   drives the event sources. Every field index on a library kind becomes a selectable
   field with its expression index, or a value the loop writes into status for the
   purpose. `pkg/k8s/rowcache` is deleted, Movie and Series return to plain informers, and
   a guard refuses an informer for a library kind. The ui reads the same way, as its own
   wave.

8. **The data plane leaves the control plane.** The Live TV tuner, render, relay and
   egress become an executor of their own, a sixth binary behind the Service name and port
   Plex's devices already use. It writes nothing to Kubernetes and reports what it serves
   in a record; the IPTV controllers stay in the manager as the only writers of IPTV
   status, so ADR-0019 gains no exception. It persists its last render and renders again
   only when the digest of its inputs changes. The leader-endpoint publisher retires.

9. **Memory is bounded structures, not garbage-collector settings.** Every retained
   structure has a bound taken from one budget derived from the memory limit, and bulk
   bytes are flat and pointer-free. `GOGC` returns to the default with the memory limit as
   the bound. The CPU limit equals `GOMAXPROCS` (2).

10. **A restart is rare and cheap.** One image per binary, with the commit in a label and
    not in the binary. `helm.sh/chart` and `app.kubernetes.io/version` come off pod
    templates, so a pod rolls only when its own image or configuration changed.

11. **The metrics show waste.** Every pass carries what woke it; `applied` is split into
    sent and written; a start reports the seconds until it settled; the render, the pacer
    and the consumers' pending counts and ack floors are measured.

Not decided here: bulk bytes as files. The JetStream object stores (about 11 GiB, held three
times on the control planes) cannot resume a watch, so every manager start re-reads all
53,000 artwork objects. Immutable files on the data volume with their index as rows only
the manager writes would answer that; it needs its own ADR, which would supersede ADR-0011
and amend ADR-0021.

### Acceptance

Proven on kind with a library of talos's size, then on talos.

| Budget | Target | Verified by | From wave |
| --- | --- | --- | --- |
| Restart with nothing changed | settled within 60 s; at most 1% of keys passed in 10 min; at most 50 writes | start-settled metric, passes by trigger | 2 |
| Periodic work | one pacer at one key a second or less; drift zero for 7 days; audit age under 36 h | pacer and drift metrics, the ticker guard | 2 |
| Apiserver | at most one non-watch request a second settled; no Cardigann apply on an unchanged bundle | `rest_client_requests_total` | 1 |
| Postgres | no timer-driven sequential scan of a kind's table | `pg_stat_user_tables` deltas | 1 |
| Live TV | serving within 10 s of its own pod starting | render metric | 3 |
| Peak against settled, starts included | RSS at most 1.25 times, live heap at most 1.2 times, over 24 h | working-set maximum over median | 5 |
| Settled memory | live heap at most 200 MiB under a 640Mi limit | runtime metrics | 5 |
| Garbage collection | at most 0.02 cores and 2 cycles a minute settled | `/cpu/classes/gc/total` (it overestimates, so passing it passes) | 6 |
| Latency | no CFS throttling; scheduler p99 at most 5 ms, starts included | cAdvisor, `/sched/latencies` | 6 |
| Allocation | at most 2 MB/s settled | runtime metrics | 6 |

### Order

Each wave is one release, cut over cold, with what it amends (the design's §5 has the
detail).

| Wave | Parts | Amends |
| --- | --- | --- |
| 0 | 11, and why every Album pass applies at a start | `docs/observability.md` |
| 1 | 4 | ADR-0016 (the 5-minute resync goes), ADR-0024 (the collector) |
| 2 | 1, 2, 3, 5 | ADR-0016, ADR-0019 (a dropped wake is bounded by cursors and the audit), ADR-0020 (floors before drain, limit markers), ADR-0024 §3.3 |
| 3 | 8 | ADR-0018 (a sixth binary), the placement design §5.4, `docs/claude/invariants.md` |
| 4 | 6 | ADR-0019 |
| 5 | 7 | ADR-0024 §3.2, ADR-0018 ("one cache"), the row cache design, `invariants.md` (a library kind is read through the manager's cache) |
| 6 | 9, 10 | ADR-0018 (one image per binary), the conventions record |
| 7 | the ui queries | none |

Wave 2's first start has no progress records: every key goes to the pacer and the library
is revisited once over about a day, with in-flight keys passed at once.

## Alternatives considered

- **A storage engine or a memory-mapped store in the manager** (the VictoriaMetrics design
  taken literally). A store with a commit-ordered change feed exists and other pods read
  it; ADR-0024 rejected a database inside the manager's pod for that reason. It would make
  the manager stateful on one node and give the project a second engine to own.
- **Raise the limits and keep the pass.** At 2 CPU a start is still 25 minutes at up to
  1.9 cores and 1.17 GiB, several times a day, to change 170 library rows.
- **Garbage-collector and CPU tuning as the fix.** That is the present state. Part 9's
  settings are right once the peak is gone and a compromise before.
- **Warm standbys.** Controllers start on election, so a standby saves the cache sync and
  not the pass; a roll already releases the lease.
- **Shard the manager, or several leaders.** The evidence is waste, not scale; ADR-0016's
  ledger assumes one leader and a Series fans out to its Episodes across any shard.
- **Rewrite the manager's writes as plain documents** to avoid the merge. With unchanged
  applies skipped, real writes are about 0.3 a second; the rewrite would give up what the
  store's differential tests against the apiserver bought.
- **A parse memo in the manager.** It hides the cost and keeps the parser in the control
  plane.
- **Durable consumers for the record buckets.** A replicated consumer per bucket on the
  control planes, with acks tied to pass latency.
- **Keep or extend the row cache.** Right while the apiserver held the library; it pays a
  decode on reads and a dictionary build and recompression at each start for what Postgres
  now answers.
- **Informer resync as the backstop.** An unpaced burst every 10 hours through every
  handler that acts on a same-version update.
- **Rules in Postgres triggers.** The Go rules would exist twice and drift.
- **Live TV as an agent domain.** Every agent domain is one binary and one image, so any
  agent change would roll Live TV: the coupling part 10 removes. Agents also scale from
  zero on consumer lag, while the executor must always run behind a stable Service with its
  own egress and possibly network privileges. A sixth binary keeps its own image digest,
  ServiceAccount and lifecycle.

## Consequences

- A restart stops being an event: a roll or a drain costs seconds (a crash still waits out
  the 60-second lease), and Live TV no longer depends on the manager having started.
- The settled footprint is set by work in flight and a bounded cache, not by the library.
  The memory limit can fall from 1536Mi toward 640Mi and the hand resize ends.
- The apiservers lose a constant 24 requests a second and about 1,500 at each start.
- **A missed input costs up to a day, not until the next restart.** Today a restart, which
  is frequent, silently repairs what a dropped wake left behind; after this the audit
  repairs it within its cycle and counts it. The drift counter has to be watched.
- **A planner fix reaches the whole library in up to a day**, where today it reaches it at
  the next restart. Whether one planner's backfill may be prioritised is the owner's
  decision.
- Passes read Postgres on their path: an outage that already stops library writes also
  stops passes that read a row outside the cache.
- New state with one writer, the manager: progress records, input cursors, record floors
  and the persisted apply memo, beside the tables cluster-plex reads and outside that
  contract. New capped status fields: parsed facts and eligibility; the item status size
  budget test is extended for them.
- A sixth binary and Deployment, and one image per binary to publish.
- Three guards are part of the design: every input a pass reads has a registered cursor
  (the pass reader, the KV wrapper, the IO executor and each book record what a pass
  read, and a test fails on an input without one); each planner
  returns the same result for the same inputs until the due time it declared; the list of
  tickers is closed.
- What does not change: one manager under one lease, the manager as the only writer, one
  remediation loop, server-side apply and its field managers, the library tables as
  cluster-plex's contract, agents coupling through NATS only.
- Not measured, and owed before wave 2 is designed: what wakes a pass (no label exists),
  the make-up of the start-up memory peak, CFS throttling on the day, the outcome of the
  Movie and Series applies, and the cause of two 10-minute bursts that fit Live TV renders.

## Revisit triggers

- Audit drift that cannot be driven to zero: the cursor model is missing a class of input,
  and the pass at start stays until it is found.
- The oldest audited key is older than 36 hours: the pacer is starved; raise its priority
  or its rate.
- Point reads from Postgres put a settled pass's p99 above 50 ms: reconsider what is
  cached, not whether.
- The library passes about 86,000 keys, where a 24-hour audit no longer fits under one key
  a second: lengthen the cycle or split the audit by kind.
- A second writer of library rows is wanted: parts 1 and 5 assume one.
- The manager must run more than one active replica: parts 1 to 4 make that possible, and
  it is a new decision.
