# The library store: episodes, files and albums as rows in Postgres

**Date:** 2026-11-26
**Status:** designed, not built. ADR-0024 (Accepted) records the decision; the
owner's rulings on its open points are in §6.
**Owner's ask:** "Could we re-design clustarr to use significantly less memory
in the api server? ... Do we really need these all as CRDs or is there a
cleaner way to do this without hammering the API server?" Then "Yes" to both
premises: losing `kubectl` for the kinds that move, and Postgres as a required
dependency.

## 1. Problem

Measured on talos on 2026-11-26 (Kubernetes 1.37, three control planes with
four cores each, apiserver limit 6Gi):

| Kind | Objects | Stored | Decoded in each apiserver |
| --- | --- | --- | --- |
| MediaFile | 33,839 | 232 MiB | 870 MiB |
| Episode | 27,750 | 79 MiB | 354 MiB |
| Album | 6,890 | 62 MiB | 321 MiB |
| Movie | 1,294 | 21 MiB | 115 MiB |
| Series | 753 | 11 MiB | 63 MiB |

- An apiserver holds every custom resource decoded as generic maps, about 4.7
  times its stored size, and each of the three holds its own copy. Each has a
  live heap of 2.1-2.4 GiB, of which about 1.7 GiB is the table above, and a
  resident size of 4.7-5.2 GiB (Go lets the heap double before it collects).
- More than half of the 1.7 GiB (about 0.93 GiB) is `metadata.managedFields`,
  the record server-side apply keeps of which manager owns which field.
- The decoded sizes come from decoding a copy of the live objects locally, not
  from a heap profile: talos runs its apiservers with `--profiling=false`.
- The same objects cost writes too. A status apply that changes nothing still
  runs a whole apply (`docs/claude/gotchas.md`, "A server-side apply that
  changes nothing..."); the album wave of 2026-11-26 held one control plane at
  3.8 of 4 cores, and the rename loop of 2026-11-18 wrote 29 times a second
  for eight days.
- The design of record set a revisit trigger at 50,000 media objects (§18,
  "Object count"). talos holds about 72,000 and the count follows the library.

MediaFile, Episode and Album are 68,479 of those objects and about 1.55 of the
1.7 GiB. What a person declares (Series, Movie, Artist, Author, Comic and
every settings kind) is a few thousand objects and under 0.2 GiB.

## 2. Decision

**A kind a controller creates is a row; a kind a person declares is a custom
resource.**

| | Kinds | Where |
| --- | --- | --- |
| Library kinds | MediaFile, Episode, Album, Book, Audiobook, Issue | the library store (Postgres) |
| Declared kinds | Series, Movie, Artist, Author, Comic, and every profile, folder, indexer, provider, list and Live TV kind | Kubernetes, unchanged |

The six library kinds are the ones a reconciler or import creates: a Series
creates its Episodes, an Artist its Albums, an Author its Books and
Audiobooks, a Comic its Issues, and import a MediaFile for each file it
places. Book, Audiobook and Issue are small (under 200 objects each on talos)
and move so that there is one model, not two.

The storage moves; the model does not. A library object keeps its Go type, its
name, its spec and status, its finalizers and its owner. The reconcilers, the
remediation loop and the planners keep reading and writing objects through the
seams they use today (§3.2, §3.3). What goes away is the apiserver's part:
the watch cache, the ownership records and the apply.

Expected on talos once all six have moved (estimates from §1's table):
apiserver live heap about 0.6-0.7 GiB each, resident about 1.5 GiB each, and
no library traffic through the apiserver or etcd at all.

## 3. Design

### 3.1 The store

One Postgres database, `library`, required by every install. It lives in the
CloudNativePG Cluster the chart already renders for the release index
(`templates/postgres-cluster.yaml`, `clustarr-postgres` on talos; owner,
2026-11-26, §6), apart from `plex-db`. That Cluster is now rendered always;
`postgres.enabled` goes on selecting only the release index's engine
(ADR-0010), and `postgres.cluster.*` keeps its meaning. `pkg/library` owns
the schema,
the migrations (the pattern of `pkg/relindex/postgres_schema.go`: pgx/v5
through `database/sql`) and every statement.

One table per kind, all of one shape:

| Column | Holds |
| --- | --- |
| `namespace`, `name` | the key, as today (names stay deterministic) |
| `uid` | generated at insert |
| `version` | the sequence number of the change that last touched the row: the object's `resourceVersion` |
| `generation` | bumped when the spec changes |
| `created_at`, `deleting_at`, `finalizers` | the lifecycle (§3.4) |
| `owner_kind`, `owner_name`, `owner_uid` | the controlling owner |
| `labels`, `annotations` | as today |
| `spec`, `status` | the writer documents (§3.3), JSONB |
| generated columns | the few values a reader filters on (parent ref, season, phase, media kind), each indexed |

A trigger on each table, not the writer's code, stamps `version` and
`generation`, appends one row to `library.change` (`seq`, the transaction id,
kind, key, version, operation) and sends a `NOTIFY`. A write that changes
nothing is suppressed in the same trigger: no version, no change, no wake.

**The change feed** is `library.change` read in `seq` order from a cursor,
with `NOTIFY` only as the wake-up. Sequence numbers commit out of order, so a
reader takes only entries older than the oldest open transaction and never
skips one that commits late. Writers keep transactions to one statement so
the feed does not stall. Entries are trimmed after a retention period; a
reader whose cursor fell behind the trim relists.

Three roles: the manager reads and writes; the ui reads, and may update only
`spec`, `labels` and `annotations` (a column grant, so the database enforces
what `TestUINeverWrites` asserts today); a read-only role for the agents,
cluster-plex and people.

### 3.2 Reads

`pkg/library` provides a `ListerWatcher` per kind: a list is one snapshot read
of the table, a watch is the change feed from that snapshot's sequence number.

The manager's cache already builds its own informer for the large kinds
(`pkg/k8s/rowcache`, 2026-11-03) and holds them as compressed rows. For a
library kind that informer is handed the store's `ListerWatcher` in place of
the apiserver's. Everything above it is unchanged: `Get` and `List` through
`mgr.GetCache()`, the field indexes, the watch handlers and map functions, the
priority queue, `rollup.Watch`, the pass reader. The cache needs a REST
mapping for a kind that no longer has a CRD; the manager supplies a static one.

A direct read (`mgr.GetAPIReader()`, as the admission ledger's rebuild uses)
goes to the store by kind, through a client wrapper (§3.3).

### 3.3 Writes

`library.Client` wraps the manager's `client.Client`: it serves the six
library kinds from the store and passes every other kind through. Write sites
do not change, and `pkg/k8s.PatchStatus` stays the one entry for status.

The store keeps server-side apply's rules without its records. `spec`,
`status` and the metadata maps are each a set of **writer documents**, one per
field manager:

- **An apply by a writer replaces that writer's document**, and takes the
  leaves it sets out of every other writer's document. That is apply with
  `ForceOwnership`, which is always on here. A field the writer stops sending
  is gone, as today.
- **A merge patch or an update** merges into the writer's document with the
  same removal.
- **The object as read** is the union of the documents. Leaves are disjoint by
  construction, so the order does not matter. A keyed list (conditions by
  type) is a leaf per key; any other list is one leaf.
- **A precondition** on `resourceVersion` is `WHERE version = $n`; no row
  updated is a conflict, exactly where callers handle one today.

So the invariants in `docs/claude/invariants.md` keep their wording: one apply
per manager per reconcile, every apply a complete declaration, re-`Get` before
applying after slow work. `remediation.ApplyMemo` (2026-11-26) becomes
unnecessary for these kinds, because an unchanged write is free.

Validation the CRD schema did (list caps, enums, required fields) runs in the
client wrapper before the statement, generated from the same markers on the
same types. The types stay in `api/catalog/v1alpha1` with their deepcopy and
apply configurations; they stop producing a CRD.

### 3.4 Lifecycle across two stores

- **Finalizers.** Deleting a row that has finalizers sets `deleting_at`;
  removing the last finalizer deletes the row. The item finalizer's code is
  unchanged.
- **Owners.** A parent in Kubernetes cannot garbage-collect a row. The
  parent's own finalizer deletes its rows, and a periodic sweep deletes rows
  whose owner no longer exists (by `owner_uid`), so a missed delete converges.
- **No transaction spans both stores.** Nothing needs one today: a parent's
  reconcile creates children level by level and is safe to repeat.

### 3.5 The UI

The ui's reader builds its cache with the same `ListerWatcher`, so no page
changes in the wave that moves a kind. Its writes to a library kind (a spec
patch, an annotation) go through `library.Client` under the ui's role. Pages
that list a whole kind then move to paged queries one at a time, which is
where the ui's own memory falls (about 900 MiB a pod on talos today).

### 3.6 Agents

Four agent domains read library kinds from Kubernetes today with `get`, `list`
and `watch` (`config/rbac/agent_*_role.yaml`): import reads all six, catalog
and events all but MediaFile, metadata the four items with documents. They
read the store instead, through `library.Client` under the read-only role, and
lose those rules from their ClusterRoles. An agent still writes nothing, in
either store; ADR-0019's guards extend to the store's write statements.

### 3.7 cluster-plex

`plexproject` reads MediaFiles and their items through the dynamic client. It
reads the store instead, with the read-only role and the same list-then-feed
shape. The tables and the change feed become a contract between the two
repositories, versioned with the schema, and the two deploy together at each
cutover.

### 3.8 People

- The UI stays the view.
- `psql` with the read-only role, and one view per kind that shows the columns
  `kubectl get` printed.
- A `library` subcommand of the manager binary for what recipes do with
  `kubectl` today: get one object as YAML, list by parent, set or remove an
  annotation, patch a spec field. Writes go through `library.Client`, never a
  hand `UPDATE`, so versions and the feed stay right.

### 3.9 Operations

- **Availability.** With the database down, library reconciles fail and retry;
  declared kinds, Live TV and the agents carry on.
- **A failover must not lose a commit the manager has seen.** The release
  index tolerates asynchronous replication (it is rebuilt from the feeds); the
  library does not, because the manager's cache would be ahead of the
  database. With more than one instance the Cluster runs a synchronous standby
  (`postgres.cluster.synchronous`), the index's role opts out for itself
  (`synchronous_commit = local`), and a reader that sees the feed's sequence
  go backwards relists. Proven in wave 0 (§4, item 4).
- **Size.** `clustarr-postgres` on talos is three instances with 5Gi and a 2Gi
  limit, shared with the release index. The library's documents are about
  230 MiB without their ownership records; wave 0 measures what the index
  already uses before the first kind moves.
- **Backup.** An etcd snapshot no longer holds the library, and that Cluster
  has no backup configured today. The chart exposes CNPG's scheduled backup;
  whether talos turns it on is not decided (§6).
- **Metrics.** `clustarr_library_*`: statements by kind and outcome (applied,
  unchanged, conflict), feed lag, relists. Listed in `docs/observability.md`.

## 4. What must be proven before a kind moves (wave 0)

Wave 0 builds `pkg/library`, the client wrapper and the cache wiring, and
moves **Issue** (the smallest kind) on the kind rig. It ends when each of
these has a test that fails with the mechanism broken:

1. **The feed never skips.** Two transactions commit in the opposite order to
   their sequence numbers; a reader sees both.
2. **The store merges as the apiserver does.** The same sequence of applies,
   patches and updates run against envtest and against the store gives the
   same object, for every writer of every library kind.
3. **The cache serves a kind that has no CRD**, through controller-runtime's
   cache and the row cache, with indexes and handlers.
4. **Conflicts surface as conflicts**, including across a CNPG switchover in
   the middle of a write.
5. **A manager start** reads talos's 33,839 MediaFiles from the store in less
   time and memory than the apiserver list takes today.

If 2 or 3 cannot be made to hold, the design stops there and this document is
revised: both are what let the reconcilers stay as they are.

## 5. Waves

Each wave ends on the kind cluster (`test/e2e`) and then cuts talos over cold:
stop the manager, copy the kind's objects into the store, delete the objects
and the CRD, start the new manager, ui and cluster-plex. One runbook per wave.

| Wave | Moves | Apiserver live heap, each (estimate) |
| --- | --- | --- |
| 0 | the store, the seams, Issue | unchanged |
| 1 | MediaFile | about -870 MiB |
| 2 | Episode | about -350 MiB |
| 3 | Album | about -320 MiB |
| 4 | Book, Audiobook | small |
| 5 | removal: the row cache's apiserver path for these kinds, `ApplyMemo`, kubectl recipes in the records | none |

MediaFile goes first because it is half the memory, it is almost entirely
observation, and its status has one writer (the remediation loop).

The copy is a one-time tool under `hack/`, run from the runbook and deleted
after wave 4. It is not a migration in a release (owner, 2026-10-07: cold
turkey); it exists because a rescan cannot restore what talos has spent weeks
computing (owner, 2026-11-26, §6).

## 6. Owner's rulings, 2026-11-26

1. **Copy at each cutover, not rescan.** ("Copy".) The tool is the one-time
   one under `hack/` (§5). A rescan would rebuild files and probes but not
   transcode provenance, markers, subtitle state, grab history or handled
   nonces.
2. **The database shares the existing Cluster.** ("Small CNPG cluster -
   separate from plex, but can share the existing one (clustarr-postgres)".)
   The library is its own database and roles in `clustarr-postgres`, never in
   `plex-db` (§3.1, §3.9).
3. **Book, Audiobook and Issue move too.** ("Yes".) One model for every kind
   a controller creates.

Still open: whether talos backs that Cluster up (§3.9).

## 7. Not in this design

- Movie, Series, Artist, Author and Comic stay custom resources. Their cached
  provider documents (`status.metadata`, 7 KB on a Movie) could leave status
  later; that is a separate, smaller change.
- No aggregated API server. A read-only one over the store could give
  `kubectl get` back later; nothing here prevents it.
- The per-file records in the Durable KV bucket (ADR-0016) and the object
  stores are unchanged.

## 8. Size of the change

Non-test files that name each kind today: MediaFile 133, Episode 75, Album 59,
Book 59, Audiobook 59, Issue 45. With §3.2 and §3.3 holding, the code that
changes is the cache and client wiring, the ui's reader, cluster-plex's
reader, the agents' readers, the tests' harness (an embedded Postgres beside
envtest, as `pkg/relindex` uses), the chart and the records. Without them it
is every one of those files, which is why wave 0 proves them first.
