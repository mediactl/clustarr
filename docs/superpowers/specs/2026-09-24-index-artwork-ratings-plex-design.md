# Selectable release index, artwork store, rating overlays and the Plex provider

**Status:** Approved design, 2026-09-24. Amends the design of record
(`2026-09-18-clustarr-design.md`) and amendment 1. Where this document and
either of those disagree, this document wins for the parts it covers.

**Supersedes:** ADR-0003's "SQLite on an RWO volume" as the only index
engine (ADR-0010); amendment 1 §A3.4's "the metadata gateway caches art on
`/data`" (ADR-0011); the library page design's decision 1, "cover art is
hotlinked" (this document, §B.6).

**Amended 2026-10-07** (research note
`.superpowers/unify/research/nats-object-store.md`, experiments E1-E16 on
nats-server v2.15.0 and nats.go v1.53.1; the owner's decisions of that day):
§B.1-§B.9 carry dated "Amended 2026-10-07" notes, and the body above each stays
the record. The owner decided that fast lookups use deterministic names plus a
full, versioned metadata map on every object, with **no object-store links**
(held by a guard), and that the ui pushes cover changes to open pages with an
SSE `art` event fed by its read-only watch. Since the manager/agent split
(`2026-10-06-manager-agent-split-design.md`) the gateway runs in agent
`metadata`, the renderer in agent `catalog` and the reaper in the manager
(`app/catalog/artwork.Reaper`, split R9); the notes use those homes. The plan
carries the work as Wave 4f (W4.101-W4.112 of
`docs/superpowers/plans/2026-10-06-manager-agent-split.md`).

**Research:** `docs/research/plex-metadata-provider.md` (verified against
Plex's official example and developer.plex.tv). The MDBList and OMDb
response shapes are recorded as fixtures during implementation (§C.3); no
shape in this document is to be trusted over a recorded response.

## 0. Intent

Clustarr becomes the metadata source for Plex. The metadata gateway already
owns `status.metadata` for every kind; a JetStream object store holds the
artwork; a renderer composites Kometa-style rating badges onto posters; and
an HTTP endpoint on the ui service speaks Plex's Custom Metadata Provider
protocol, so Plex pulls titles, summaries and overlaid posters from clustarr
instead of from its own agents. Alongside, the release index gains a
Postgres engine so indexarr can shed its volume and run more than one
replica, provisioned by CloudNativePG, and the unused nack dependency
leaves the chart.

Four parts, built in the order A, B, C, D (A may run beside B):

- **A.** Release index on Postgres, selectable; CNPG as a chart dependency;
  nack removed.
- **B.** The artwork object store: bucket, keys, the gateway as writer of
  originals, `spec.artwork` overrides, `status.artwork`, the `/art` route.
- **C.** Ratings in `status.metadata` from TMDB, MDBList and OMDb with
  declared capabilities and fallthrough; the `OverlayProfile` kind; the
  renderer role writing overlay posters.
- **D.** The Plex Metadata Provider roots on the ui service.

Out of scope, deferred with a named follow-up: file-fact and status-ribbon
overlays (resolution, HDR, audio, Airing/Ended); overlays on anything but
the poster; uploaded custom art (only URL overrides here); music, book and
comic Plex providers (Plex supports only movie and TV libraries); Plex
stream metadata and provider preferences (Plex does not support them yet).

## A. Release index on Postgres

### A.1 Store

`pkg/relindex` gains a second implementation of the existing `Store`
interface (`Upsert`, `Search`, `Prune`, `Stats`), opened by

```go
func OpenPostgres(ctx context.Context, dsn string) (Store, io.Closer, error)
```

beside the existing `Open(ctx, path)`. The driver is `github.com/jackc/pgx/v5`
through `pgx/v5/stdlib` and `database/sql`, so both stores share one query
style and `sql.DB` pooling. The Postgres store never creates the database or
role; CNPG (§A.4) or the operator does.

Schema, migration 1, applied by the same append-only ladder as SQLite's
(`relindex_schema(version int)` holds one row where SQLite uses
`user_version`; edit nothing in place, append a migration):

```sql
CREATE TABLE releases (
  id           bigserial PRIMARY KEY,
  indexer      text        NOT NULL,
  guid         text        NOT NULL,
  title        text        NOT NULL,
  title_norm   text        NOT NULL,
  grp          text        NOT NULL DEFAULT '',
  protocol     text        NOT NULL DEFAULT '',
  categories   integer[]   NOT NULL DEFAULT '{}',
  size_bytes   bigint      NOT NULL DEFAULT 0,
  published_at timestamptz,
  fetched_at   timestamptz NOT NULL,
  info_json    bytea,
  search       tsvector GENERATED ALWAYS AS
                 (to_tsvector('simple', title_norm || ' ' || grp)) STORED,
  UNIQUE (indexer, guid)
);
CREATE INDEX releases_fetched_at      ON releases (fetched_at);
CREATE INDEX releases_indexer_fetched ON releases (indexer, fetched_at);
CREATE INDEX releases_categories      ON releases USING gin (categories);
CREATE INDEX releases_search          ON releases USING gin (search);
```

Semantics are identical to SQLite's, field for field:

- `Upsert` is `INSERT ... ON CONFLICT (indexer, guid) DO UPDATE` of every
  column, in one transaction per batch, after the same `validate`.
- `Search` with `Text` uses `search @@ plainto_tsquery('simple', $1)`
  ordered by `ts_rank_cd(search, plainto_tsquery('simple', $1)) DESC, id
  DESC`; without text, `fetched_at DESC, id DESC`. `plainto_tsquery`
  parses arbitrary input safely, so `fts.go`'s `matchExpr` has no Postgres
  twin. `Categories` is `categories && $n::integer[]`. `Indexers`,
  `Protocol`, `Since` and `Limit` map one to one.
- `Prune` is `DELETE FROM releases WHERE fetched_at < $1`.
- `Stats` is a count, a distinct-indexer count, min and max `fetched_at`,
  and `pg_total_relation_size('releases')` for `SizeBytes`.

The `simple` configuration does no stemming or stop-wording, matching FTS5's
`unicode61`. Title folding stays the caller's job through
`release.TitleNorm` on both sides, as today.

### A.2 One contract suite for both engines

The relindex tests move into `pkg/relindex/storetest` as
`storetest.Run(t, open func(t *testing.T) relindex.Store)`, and both
engines run it. SQLite opens a temp file. Postgres comes from
`github.com/fergusstrange/embedded-postgres` with its cache directory set to
`$CLUSTARR_PG_ASSETS`; a new Makefile target `pg-assets` populates that
directory once, the way `setup-envtest` populates `KUBEBUILDER_ASSETS`, and
`make test` exports it. `TestPostgresStoreContract` skips with a message
naming the variable when it is unset or empty. No test reaches the network.

### A.3 Pivot

A new flag on `indexarr` and `clustarr all`:

- `--index-dsn`, env `CLUSTARR_INDEX_DSN`. Non-empty selects Postgres and
  `--index-path` is ignored with a log line saying so. Empty keeps SQLite
  at `--index-path`, the default.

Readiness keeps pinging through `Stats` (`IndexReadyChecker`). The RSS
worker and the search fan-out change nothing since they write through
`Store`. With a DSN indexarr may run several replicas, so the retention
sweep runnable declares `NeedLeaderElection() == true`; under SQLite one
replica is the leader and nothing changes.

> **As built (2026-09-24).** Leader election turns on for every indexarr
> controller, not only the retention sweep: with `--index-dsn` set,
> `--leader-elect` is accepted and every controller declares
> `NeedLeaderElection() == true` alongside the sweep; with the DSN empty,
> nothing changes from the SQLite, single-replica shape. Minor (deferred):
> `clustarr all --index-dsn` with no `--namespace` fails at manager
> construction with controller-runtime's generic leader-election-namespace
> error rather than a `Validate` message.

### A.4 CloudNativePG

Chart (`charts/clustarr`):

- Dependency `cloudnative-pg` (repository
  `https://cloudnative-pg.github.io/charts`, the operator), condition
  `cloudnative-pg.enabled`, default `false`.
- Values `postgres.enabled` (default `false`), `postgres.cluster.instances`
  (1), `postgres.cluster.storage.size` (`5Gi`),
  `postgres.cluster.storage.storageClass` (empty), `postgres.existingSecret`
  (empty; when set, the DSN Secret to use instead of the operator's).
- Template `postgres-cluster.yaml`: `postgresql.cnpg.io/v1 Cluster` named
  `<release>-postgres`, `bootstrap.initdb.database: clustarr`, owner
  `clustarr`, rendered only when `postgres.enabled`. It carries
  `helm.sh/hook: post-install,post-upgrade` and no delete policy: CNPG's
  admission webhook fails closed until the operator runs, so the `Cluster`
  cannot be created in the same pass as the operator. The README says to
  install with `--wait`.
- indexarr Deployment when `postgres.enabled`: env `CLUSTARR_INDEX_DSN` from
  Secret `<release>-postgres-app` key `uri` (or `postgres.existingSecret`),
  no volume and no PVC, default rollout strategy, `indexarr.replicas`
  honoured. Otherwise the current shape, unchanged.

Kustomize: a component `config/postgres` with the same `Cluster` and a patch
giving indexarr the env, no volume and default strategy; `config/README`
notes the operator must be installed first. `TestChartAndKustomizeAgree...`
gains a postgres-enabled case so the two paths cannot drift.

> **As built (2026-09-24), ruling supersedes this section's hook sentence
> and the plan's R2.** The `Cluster` renders as a normal resource carrying
> `helm.sh/resource-policy: keep`, never a hook: Helm's default
> hook-delete-policy, with no `helm.sh/hook-delete-policy` annotation, is
> `before-hook-creation`, so a `post-install,post-upgrade` hook of this kind
> would be deleted and recreated on every `helm upgrade`, destroying the
> database. CNPG's admission webhook still fails closed until the operator's
> own Deployment is Ready, so a first install that also installs the
> operator is two steps (`cloudnative-pg.enabled=true`,
> `postgres.enabled=false` first, wait for the operator, then upgrade with
> `postgres.enabled=true`), or one step against an operator already running
> elsewhere; `clustarr.validate`'s `fail` catches the unordered case at
> render time (`postgres.enabled && cloudnative-pg.enabled &&
> .Release.IsInstall`). `--wait` does **not** substitute for this ordering —
> it only waits on the release's own resources, never an externally managed
> operator's — and the README is corrected to the explicit two-step /
> `kubectl wait --for=condition=Available
> deployment/cnpg-controller-manager -n cnpg-system` instructions. Minor
> (deferred): no test renders `postgres.enabled=false && indexarr.replicas>1`
> to prove the `clustarr.validate` fail; the `cnpg-system` namespace
> assumption is unverified until Phase H; the README's Postgres section still
> phrases install/upgrade as two verbs rather than addressing `helm upgrade
> --install` users directly.

### A.5 nack

Removed from `Chart.yaml`, `Chart.lock`, `values.yaml`, the README's
dependency table and `charts/clustarr/charts/`. Any test naming it is
updated. CLAUDE.md's fresh-clone gotcha lists the tarballs that remain.

### A.6 ADR-0010

"The release index engine is selectable: SQLite FTS5 by default, Postgres
FTS via CloudNativePG for multi-replica indexarr." Supersedes ADR-0003,
which stays in the index as superseded.

## B. Artwork store

### B.1 Bus API

`pkg/events` gains an object-store contract beside `KV`:

```go
type ObjectInfo struct {
    Name    string
    Size    int64
    Digest  string            // hex SHA-256 of the content
    ModTime time.Time
    Headers map[string]string
}
type ObjectStore interface {
    Get(ctx context.Context, name string) (ObjectInfo, io.ReadCloser, error)
    Put(ctx context.Context, name string, r io.Reader, headers map[string]string) (ObjectInfo, error)
    Delete(ctx context.Context, name string) error
    Info(ctx context.Context, name string) (ObjectInfo, error)
    List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}
```

`Bus` gains `ObjectStore(bucket string) ObjectStore`; a missing object is
`events.ErrObjectNotFound`. `Topology` gains `ObjectStores
[]ObjectStoreSpec{Name, Description string; Storage Storage; MaxBytes
int64; Replicas int}`, ensured by `Ensure` like buckets. membus implements it
in memory; natsbus over `jetstream.ObjectStore`, with `Digest` converted
from NATS's `SHA-256=<base64url>` form. The contract suite in
`pkg/events/contracttest` covers it, and a real-server test in `natsbus`
proves a 20 MiB object round-trips (the `max_payload` limit does not apply,
JetStream chunks).

> **Amended 2026-10-07** (`nats-object-store.md` §3, §4, §6.5-§6.6, §7). The
> contract becomes:
>
> ```go
> type ObjectMeta struct {
>     Headers  map[string]string // how to serve the bytes, where they came from (§B.2)
>     Metadata map[string]string // whose bytes they are (§B.2's keys); ObjectMeta.Metadata in NATS
> }
> type ObjectInfo struct { /* ...as above... */ Metadata map[string]string }
> type ObjectEvent struct {
>     Info    ObjectInfo
>     Deleted bool // a tombstone: Info carries only the name
>     Synced  bool // the replay's end marker; Info is zero
> }
> type ObjectStoreStatus struct {
>     Bucket  string
>     Created time.Time // a bucket deleted and created again has a new one
>     Bytes   uint64
> }
> type ObjectStore interface {
>     Get(ctx context.Context, name string) (ObjectInfo, io.ReadCloser, error)
>     Put(ctx context.Context, name string, r io.Reader, meta ObjectMeta) (ObjectInfo, error) // replaces the headers map
>     SetMeta(ctx context.Context, name string, meta ObjectMeta) error // new: metadata only, chunks untouched
>     Delete(ctx context.Context, name string) error
>     Info(ctx context.Context, name string) (ObjectInfo, error)
>     List(ctx context.Context, prefix string) ([]ObjectInfo, error)
>     Watch(ctx context.Context, opts ...WatchOption) (<-chan ObjectEvent, error) // new
>     Status(ctx context.Context) (ObjectStoreStatus, error)                     // new
> }
> // ObjectStoreAdmin is optional (natsbus implements it; membus has no chunks
> // and returns 0).
> type ObjectStoreAdmin interface {
>     PurgeOrphanChunks(ctx context.Context, bucket string, grace time.Duration) (purged int, bytes uint64, err error)
> }
> ```
>
> - **`Put` is a complete declaration.** NATS `Put` replaces the whole
>   `ObjectMeta`, so metadata set any other way is gone after the next `Put` that
>   does not send it again (E3) -- the object-store twin of CLAUDE.md's
>   server-side-apply rule. Each writer renders its complete set through one
>   function (§B.2).
> - **`SetMeta`** is NATS `UpdateMeta` with the name unchanged, so no rename can
>   happen (a rename purges the old meta subject without a tombstone, E5). It
>   rewrites no chunk and opens no stall window, but it has **no
>   compare-and-swap** (`publishMeta` sends no expected sequence): an `UpdateMeta`
>   that read NUID X before a racing `Put` stored Y and purged X would republish
>   X, whose chunks are gone. So it is called only by the variant's writer, under
>   that writer's per-item lock (§B.3). A deleted object is `ErrObjectNotFound`
>   (nats.go's `ErrUpdateMetaDeleted`).
> - **`Watch`** wraps nats.go's `ObjectStore.Watch` (an ordered push consumer on
>   `$O.<bucket>.M.>`): a replay of every object's latest meta, tombstones
>   included, then `Synced`, then one event per change: every `Put`, re-`Put`,
>   `SetMeta` and `Delete` (a chunk write, a stream purge and a link's target
>   changing fire none). It takes the loop spec's `WatchOption`s (§4.15 as
>   amended): `WatchUpdatesOnly` skips the replay and the `Synced` marker;
>   `WatchFromRevision` is refused with `events.ErrWatchOptionUnsupported`,
>   because nats.go accepts `ResumeFromRevision` and `MetaOnly` on an object
>   store and silently ignores both (E6). nats.go's callback blocks on a 32-slot
>   channel, so natsbus relays through an unbounded per-watcher queue and never
>   stalls the subscription; membus does the same and never drops. The channel
>   closes only on ctx, `Close` or a reset that cannot recover, not on a plain
>   reconnect (E10: a server restart resumes with no second replay).
> - **A bucket deleted and created again under a running watch is silently
>   skipped:** the ordered consumer resumes at the old stream's last sequence + 1
>   (E9: 25 new objects, then only the last five delivered). `Status().Created`
>   is how a reader notices; `pkg/events/objindex` (§B.8) checks it.
> - **`PurgeOrphanChunks`** (§B.5) reclaims what two concurrent `Put`s of one
>   name leak: each writes its chunks under a new NUID and purges only the NUID
>   it read before it started, so the loser's chunks are never referenced again
>   (E8: 30 rounds of two racing `Put`s left 30 orphaned chunk sets, 18 MB from
>   600 KiB objects); a `Put` that crashes between its chunks and its meta leaks
>   the same way. The meta-level `List` cannot see them, and the artwork bucket
>   has no `MaxAge` and is `DiscardNew`, so a leak ends as refused `Put`s at 5 GiB.
> - **No links in the contract** (owner decision (a)). nats.go's `AddLink` and
>   `AddBucketLink` are not exposed, and the guard `TestNoObjectLinks` bans
>   them repo-wide. A link fires no watch event when its target changes, carries
>   no size, digest or headers of its own (E3), still `GetInfo`s as live after
>   its target is deleted (E4), costs every `Get` one more meta read, and cannot
>   be created over any name a regular object ever held, even a deleted one,
>   which deviates from ADR-20 (E1). The deterministic `ArtworkKey` already
>   resolves in one direct get and fires a watch event on every change.
> - `ObjectStoreSpec` gains `Metadata map[string]string`, mapped into
>   `jetstream.ObjectStoreConfig.Metadata` and ensured by `Ensure` (§B.2).
> - natsbus's `objectError` maps `ErrUpdateMetaDeleted` to `ErrObjectNotFound`,
>   and `ErrCantGetBucket` (a bucket link, which nothing creates) to an error
>   that names it rather than a bare 500 cause.

### B.2 Bucket and keys

One bucket, `events.BucketArtwork = "clustarr-artwork"`, file storage, three
replicas where the topology already asks for three,
`events.ArtworkMaxBytes = 5 GiB`. The chart's JetStream file-store note
gains a line: 20Gi assumes a 5 GiB artwork bucket; raise both together.

```go
func ArtworkKey(kind commonv1.MediaKind, uid types.UID, imageType, variant string) string // pkg/events imports only api/common
// "<kind>/<uid>/<imageType>/<variant>", e.g. "movie/8b2c.../poster/original"
```

`ArtworkVariant` is `original` or `overlay`. Object headers:
`Content-Type`; `Clustarr-Source` (`provider` or `custom`);
`Clustarr-Source-URL`; and on overlays `Clustarr-Rendered-From`, the inputs
digest of §C.6.

> **Amended 2026-10-07** (`nats-object-store.md` §2, §3, §6.1-§6.2; owner
> decision (a)). **The name is the key; there are no links.** `ArtworkKey` stays
> the stable per-item, per-type lookup key: one direct get resolves it, `Put`
> keeps it pointing at the current bytes, and every change to it is a watch
> event. Content-addressed blobs behind per-item links, a "served" alias per
> item and type, and aliases by provider id were all weighed and rejected (§B.1
> as amended; research §2.4): each adds a hop or a third writer, and none fires
> a watch event when its target changes.
>
> **Every object carries a full, versioned metadata map**, sent whole on every
> `Put` by its variant's one writer, so an object describes itself: a reader
> needs no name parsing and no Kubernetes read. In `ObjectMeta.Metadata`:
>
> | Key | Value | Variants |
> |---|---|---|
> | `clustarr.io/meta-version` | `"1"` (`events.ArtworkMetaVersion`); raised when the set changes, and the backfill (§B.5) keys on it | both |
> | `clustarr.io/kind` | the item's `commonv1.MediaKind` (repeats the name) | both |
> | `clustarr.io/uid` | the item's UID (repeats the name) | both |
> | `clustarr.io/namespace`, `clustarr.io/name` | the item's key, which the name does not carry | both |
> | `clustarr.io/image-type` | `catalogv1.ImageType` (repeats the name) | both |
> | `clustarr.io/variant` | `original` or `overlay` (repeats the name) | both |
> | `clustarr.io/language` | ISO 639-1 of the provider image (`Image.Language`); omitted when empty or for a custom override. Not part of the key while the store keeps one image per type | both |
> | `clustarr.io/width`, `clustarr.io/height` | decimal pixels: the original's from the `image.DecodeConfig` the gateway already runs, the overlay's from the rendered bounds | both |
> | `clustarr.io/profile` | the winning OverlayProfile's name | overlay |
> | `clustarr.io/original-digest` | hex digest of the original it was drawn on, so a reader can tell an overlay is behind its original without reading status | overlay |
>
> The headers stay exactly as above: they say how to serve the bytes and where
> they came from; the metadata says whose bytes they are. Each fact lives in one
> place: titles and anything else a metadata refresh changes never go in
> metadata, nor do provider URLs (they stay in `Clustarr-Source-URL`).
> `Description` is not used. The key constants live in `pkg/events`
> (`subjects.go`, beside `ArtworkKey`). Readers never depend on the metadata
> being present: the index (§B.8) parses names, so the order of rollout does not
> matter.
>
> **Bucket metadata** records the key scheme, ensured by `Ensure` through
> `ObjectStoreSpec.Metadata`: `clustarr-artwork` carries
> `clustarr.io/key-scheme: "<kind>/<uid>/<imageType>/<variant>"` and
> `clustarr.io/meta-version: "1"` (the scheme the writers use, not a claim that
> the backfill has finished); `clustarr-fingerprints` carries
> `clustarr.io/key-scheme: "<probeHash>.<start|end>[.v<FingerprintVersion>]"`.
> Fingerprints get no per-object metadata; their version is in the name
> (split spec §7.2.7 as amended, `segments.FingerprintKey`).

### B.3 Writers

Two writers, split by variant, the same discipline as spec versus status on
MediaFile:

- The **metadata gateway** (`catalogarr --role metadata`) is the sole writer
  of `original` objects and of `status.artwork`.
- The **renderer** (`catalogarr --role artwork`, §C.6) is the sole writer of
  `overlay` objects and of `status.overlay`.

Neither reads, writes or deletes the other's variant, except the reaper
(§B.5).

> **Amended 2026-10-07** (`nats-object-store.md` §1.3 G3, §6.3). "Neither
> writes the other's variant" now covers metadata too, and three rules join it:
>
> - **The renderer serialises per item.** It had two render slots and no
>   per-item lock, so two `RenderOverlay` tasks for one item with different
>   Msg-Ids (one from the gateway's pass, one from the OverlayProfile
>   controller) could `Put` one name at once and leak a full copy (§B.1 as
>   amended). An in-process keyed lock, the shape of the gateway's
>   `Fetcher.Lock`, now spans plan, draw, `Put` or `Delete`, and the status
>   record: the second task finds the overlay current at §C.6 step 3. That
>   closes the leak while the `catalog` domain runs one replica (its HPA is
>   0..1, split §9.1.1); the reaper's orphan purge bounds it if it ever runs more.
>   The gateway's writes were already serialised by `Fetcher.Lock` on its one
>   replica.
> - **`SetMeta` is the owner's alone**: called only by the variant's writer, on
>   its own variant, under its per-item lock, since it has no compare-and-swap.
> - **The reaper** keeps its role as the only code that deletes both variants,
>   and gains the orphan-chunk purge and the artwork audit (§B.5 as amended). It
>   never `Put`s and never calls `SetMeta`.
> - **The ui writes nothing**: its watch (§B.8) is a read.
>
> `TestArtworkWritersAreTheTwoVariantOwners` (split §5.15 as amended) holds the
> writers; `TestRendererSerialisesOneItem` (two concurrent tasks for one item
> leave one chunk subject) holds the lock.

### B.4 Fetching originals

On every successful metadata fetch, and on every `ImportArtwork` task
(§B.7), the gateway resolves one source URL per image type: the
`spec.artwork` override for that type if present, else the first
`status.metadata.images` entry of that type. For each type whose source URL
differs from `status.artwork[].sourceURL`, or whose object is missing, it:

1. fetches through `metadata.ReadBody` with `ArtworkMaxImageBytes = 20 MiB`
   and the provider's per-host limiter;
2. requires `Content-Type` in `image/jpeg`, `image/png`, `image/webp`, and
   `image.DecodeConfig` to succeed with both dimensions at most 8000;
3. puts `<kind>/<uid>/<type>/original` with the headers of §B.2;
4. writes the entry to `status.artwork`.

A failed fetch leaves the previous entry and object in place and emits a
Kubernetes Event `ArtworkFetchFailed` on the item with the reason. A custom
URL that fails never falls back to the provider URL: the user asked for that
image, and a silent substitute is a guess.

After storing a poster original whose digest changed, the gateway publishes
one `RenderOverlay` task (§C.6) for the item.

> **As built (2026-09-24).** The rate limiter is per image host, not per
> provider — an image hosted on a CDN a provider's API does not itself front
> is limited on its own host. The stored `Content-Type` follows the decoded
> image format rather than the response header verbatim. Every artwork pass
> — not only one that found a changed digest — republishes the
> `RenderOverlay` task idempotently, closing the gap where a crash between
> the `status.artwork` apply and the publish could strand an item with no
> render ever queued. A poster drop (an override withdrawn, a fetch that now
> resolves to no source) also publishes the render task; the renderer's own
> "no original" branch (§C.6 step 1) is what actually deletes `poster/overlay`
> and clears `status.overlay` — the gateway never deletes an `overlay`
> object itself.
>
> **Amended 2026-10-07** (`nats-object-store.md` §6.2, §6.9). Step 3's `Put`
> sends the complete set of §B.2 as amended, built by
> `app/catalog/artwork.ObjectMeta` (the light package both writers and the
> reaper already import) from the item's identity, the image type, the variant,
> the resolved source (`artwork.Source` gains `Language`) and the decoded
> dimensions. **Backfill is lazy and the owner's:** in `Sync`'s keep-verbatim
> branch, the `Info` that `stale()` already makes is returned to `Sync`, and an
> object whose `clustarr.io/meta-version` is below `events.ArtworkMetaVersion`
> gets one `SetMeta` with the full set, under `Fetcher.Lock`: no refetch, no
> chunk rewrite, about 0.7 KB of meta per object. An old object carries no
> dimensions and status has none, so the backfill reads the object's first
> 64 KiB for `image.DecodeConfig` and closes the reader (the research's
> backfill assumed `SetMeta` alone could send the whole set). `SetMeta`, not a re-`Put` of
> the same bytes, because it rewrites no chunks, opens no stall window for
> readers (§B.8 as amended) and needs no transient double space in a
> `DiscardNew` bucket.

### B.5 Reaper

`app/catalog/metadata/artwork.Reaper`, a leader-elected runnable in the
gateway, every 6 hours lists the bucket and deletes every object whose
`<kind>/<uid>` prefix names no live item of that kind, after a 30-minute
grace period from the object's `ModTime`, following `grabarr`'s reapers. It
is the only code that deletes both variants.

> **Amended 2026-10-07** (`nats-object-store.md` §1.3 G1 and G3, §6.6, §6.9).
> The reaper is `app/catalog/artwork.Reaper`, leader-only in the manager (split
> R9, §5.13). Two duties join the delete, both inside the sweep that already
> lists the whole bucket; neither is a watch (D5: every legitimate change is
> followed by a status apply the manager already watches, and what only the
> store can show needs repair within hours, not seconds):
>
> 1. **Orphan-chunk purge.** `events.ObjectStoreAdmin.PurgeOrphanChunks(ctx,
>    bucket, grace)` (§B.1 as amended), natsbus only: it lists the chunk
>    subjects `$O.<bucket>.C.>` from STREAM.INFO and the live NUIDs from the
>    library's `List`, and purges each chunk subject whose NUID no live meta
>    names **and** whose last message is older than the grace (the existing 30
>    minutes, which covers a `Put` in progress, since chunks precede their meta).
>    It runs for `clustarr-artwork` and `clustarr-fingerprints`, and counts
>    `clustarr_object_orphan_chunks_purged_total{bucket}` and
>    `clustarr_object_orphan_bytes_purged_total{bucket}`. Chunks leaked before the
>    fix are purged by the first sweep after rollout. (Sizing the live leak first
>    is two read-only `nats` CLI commands on kind-cluster-plex, which wait for the
>    owner's OK.)
> 2. **The artwork audit.** For every item in the manager's synced cache (the
>    eight kinds with `status.artwork`, and Movie and Series for
>    `status.overlay`), it compares the recorded digests with the listing:
>    - an `original` that is missing or carries another digest gets an
>      `ArtworkFetchTask`; the schema needs no change, because the gateway's
>      fetch handler runs a full pass whatever the drift and `stale()` refetches
>      a missing or mismatched object. Until now nothing repaired a lost object
>      until the item's next metadata refresh, 7 to 30 days later (a NATS volume
>      wiped and the bucket re-created by `KeepTopology`, a manual
>      `nats object rm`, a crash between a `Put` and its status apply);
>    - an `overlay` that is missing or carries another digest while
>      `status.overlay` is set gets a `RenderOverlay` task;
>    - an object whose `clustarr.io/meta-version` is below current gets its
>      variant's task too, which finishes the backfill (§B.4 as amended, §C.6 as
>      amended) promptly instead of at each item's next refresh.
>
>    Msg-Ids are `schema.MsgIDForArtworkFetch(uid, "audit-"+reason+"-"+gen)` and
>    `schema.MsgIDForRenderOverlay(uid, "audit-"+reason+"-"+gen)`, `gen` being the
>    bucket's creation time, so a re-created bucket is audited afresh and a quiet
>    one publishes nothing twice. **Publishing is paced against consumer lag**
>    (`NumPending + NumAckPending`, split §9.0): only while
>    `catalogarr-artwork-fetch` (or `-render`) lags fewer than 256, leaving the
>    rest for the next tick. After a bucket loss the audit is the whole library,
>    about 16,000 items, and `CLUSTARR_WORK_CATALOGARR` is a 7 MiB discard-oldest
>    memory stream on single-node NATS, where queueing by the thousand silently
>    drops the neighbours' work (CLAUDE.md). It counts
>    `clustarr_artwork_audit_tasks_total{variant,reason}` and sets
>    `clustarr_artwork_objects{variant,meta_version}` (backfill progress).
> 3. **When.** Every sweep (6 hours); at once when `Status().Created` differs from
>    the last sweep's (checked every minute, one STREAM.INFO); and every 10
>    minutes while a paced backlog remains.
>
> The audit reads status from the manager's cache, which is synced before
> leader-only runnables start; the delete's metadata-only, uncached liveness
> lists stay as they are.

### B.6 API

On the spec of Movie, Series, Artist, Album, Author, Book, Audiobook and
Comic:

```go
// Artwork overrides the provider's image for a type. One entry per type.
// +optional
// +kubebuilder:validation:MaxItems=9
// +listType=map
// +listMapKey=type
Artwork []ArtworkOverride `json:"artwork,omitempty"`

type ArtworkOverride struct {
    Type ImageType `json:"type"`  // +required
    URL  string    `json:"url"`   // +required, +kubebuilder:validation:Pattern=`^https?://`
}
```

On the status of the same eight kinds, written by the gateway under the
manager that already writes that kind's `status.metadata`:

```go
// +kubebuilder:validation:MaxItems=9
// +listType=map
// +listMapKey=type
Artwork []ArtworkEntry `json:"artwork,omitempty"`

type ArtworkEntry struct {
    Type      ImageType     `json:"type"`
    Source    ArtworkSource `json:"source"`     // provider | custom
    SourceURL string        `json:"sourceURL"`
    Digest    string        `json:"digest"`     // hex SHA-256
    SizeBytes int64         `json:"sizeBytes"`
    UpdatedAt metav1.Time   `json:"updatedAt"`
}
```

On the status of Movie and Series only, written by the renderer under a new
manager `k8s.ManagerCatalogarrArtwork`:

```go
Overlay *OverlayEntry `json:"overlay,omitempty"`

type OverlayEntry struct {
    ProfileRef   string      `json:"profileRef"`
    Digest       string      `json:"digest"`
    RenderedFrom string      `json:"renderedFrom"`
    UpdatedAt    metav1.Time `json:"updatedAt"`
}
```

Each kind's existing field-manager declaration in `app/catalog` lists the
gateway's `artwork` leaves and the renderer's `overlay` leaves as two
disjoint sets; `managedFields` tests hold each manager to its set.

`status.metadata.images` is unchanged and keeps the provider URLs.

> **Amended 2026-10-07.** No API change: the object metadata of §B.2 as amended
> lives on the object, never in status, and no status field records the
> metadata version.

### B.7 Triggering a re-fetch

The item's reconciler compares `spec.artwork` against `status.artwork`: any
type whose override URL differs from the entry's `sourceURL`, or whose
override was removed while the entry says `custom`, publishes one
`ImportArtwork` task on `events.WorkArtworkFetchSubject(mediaKey)`,
`clustarr.work.catalogarr.artwork.fetch.<mediaKey>`, consumed by the
gateway's `catalogarr-artwork-fetch` durable. Msg-Id is
`<uid>/artwork/<hash of spec.artwork>` so a hot reconcile loop publishes
once.

*As built (2026-09-24):* the comparison is between `status.artwork` and
every source a pass would store -- each type's override, else the first
fetchable provider image in `status.metadata.images`
(`artwork.ResolveSources`, the map `Fetcher.Sync` fetches from) -- and the
Msg-Id hash covers those resolved sources, not `spec.artwork` alone. Drift
over overrides only left every item whose metadata was fetched before the
gateway stored artwork with no `status.artwork` until its metadata TTL ran
out (7 days for a released movie, 30 for an ended series): 810 of 819
Movies and 68 of 147 Series on kind-cluster-plex, each drawn as a
placeholder once the ui stopped hotlinking. The fetch task now backfills
them from the stored images without refetching metadata, and retries a
provider image whose fetch failed once per duplicate window, as a custom
URL already was.

> **Amended 2026-10-07** (`nats-object-store.md` §6.6). A second, level-driven
> trigger joins the item reconciler's drift: the reaper's artwork audit (§B.5 as
> amended) publishes the same `ArtworkFetchTask` for an item whose recorded
> original is missing from the bucket, carries another digest, or carries
> metadata below `events.ArtworkMetaVersion`. Drift compares sources with
> status; only the audit asks whether the object is still there.

### B.8 Serving

The ui service gains a read-only bus. `cmd/clustarr`'s ui command connects
through `k8s.ConnectBus` and passes `Bus.ObjectStore(BucketArtwork)` into
`ui.Options.Artwork` (nil stays legal and renders placeholders); `ui/` never
imports `pkg/k8s`, preserving the guard.

```
GET /art/{kind}/{uid}/{type}[?v=<digest>]
```

serves `<kind>/<uid>/<type>/overlay` when it exists, else `original`, else
404. Headers: `Content-Type` from the object, `ETag: "<digest>"`,
`Cache-Control: public, max-age=31536000, immutable` when `v` equals the
served digest and `Cache-Control: no-cache` otherwise; `If-None-Match`
yields 304. The projection's `LibraryItem.Poster` becomes
`/art/<kind>/<uid>/poster?v=<digest>` with the overlay digest when
`status.overlay` is set and the original's otherwise, and `""` when neither
exists. Hotlinking of provider URLs is removed from every template.

`TestUINeverWrites` extends its banned selector list with `Put`, `PutBytes`,
`UpdateMeta`, `Seal`, `AddLink` and `Purge`. The ui role gains no verbs.

> **As built (2026-09-24, final fix wave).** The first cut gave the ui a
> bus and neither installer gave the ui Deployment `NATS_URL`, so it dialled
> the binary's default Service, which no installer creates. `ConnectBus`
> retries a failed connect in the background without returning an error,
> so every `/art` and Plex image request hung, then answered 500. Both
> installers now set it (the chart from `clustarr.natsUrl`, like every other
> Deployment), `TestEveryNATSDialingDeploymentCarriesNATSURL` requires it of
> every Deployment whose command takes `--nats-url`, the ui logs once at
> startup when the bus is not yet connected, and it closes the connection
> on shutdown.
>
> **Amended 2026-10-07** (`nats-object-store.md` §1.2, §1.3 G2 and G4, §4.3, §5,
> §6.4-§6.5; owner decision (b)). The ui holds **one read-only watch** on
> `clustarr-artwork` per process, and `/art` stops streaming objects straight
> from NATS into responses.
>
> **Why.** A `Get` is a direct get of the meta subject plus the creation and
> deletion of an ordered consumer on the chunks (2.1 ms for a 300 KiB poster,
> loopback); `/art` made one even for a 304, because it read the object before
> its `If-None-Match` check, and probed the overlay first for the eight kinds
> that never have one. Worse, `Put` purges the old chunks as soon as the new meta
> is stored, so a reader streaming an object that is overwritten mid-read stops
> receiving chunks and fails only at its context deadline (5 s by default) with
> `read pipe: i/o timeout`, never a digest error (E14, E14b), after `/art` had
> already sent a 200 with its `Content-Length`: a truncated image. (The DeepWiki
> claim that a new NUID protects in-flight reads of the old object is wrong.)
>
> **The index.** `pkg/events/objindex` (generic, importable by `ui/`):
> `objindex.New(store events.ObjectStore) *Index`, started by `ui.Run` when
> `Options.Artwork` is non-nil. It keeps `name -> {Digest, Size, ContentType,
> ModTime, Metadata}` for live objects and drops names on tombstones;
> `Lookup(name)`; `Synced()` is true from the replay's end marker until a reopen;
> `Subscribe()` delivers `(name, oldDigest, newDigest)` changes. On a channel
> close it reopens with a full replay after a backoff of 1 s doubling to 30 s,
> building the new map aside and swapping it in at the end marker (a full replay
> of 10,000 metas is about 120 ms and 7 MB, so resuming is not worth nats.go's
> missing revision support). Every 60 s it compares `Status().Created` and
> reopens on a change: a bucket re-created under a running watch is otherwise
> skipped silently (§B.1 as amended, E9). (The research also proposed reopening
> when a replay holds far fewer objects than the index; a reopen's replay
> replaces the map anyway, and the creation time is what detects a re-created
> bucket, so that rule is not adopted.) `Subscribe` never blocks the index: a
> subscriber whose buffer is full loses changes, which the 5 s projection tick
> repairs. About 250 bytes per object.
>
> **`GET /art/{kind}/{uid}/{type}`:**
>
> 1. **Choose the variant.** Only `movie` and `series` posters can have an
>    overlay; every other kind and type goes straight to `original`. With the
>    index synced: the overlay entry if present, else the original, else 404 with
>    no NATS call. Unsynced, or with no entry: `Info` (the overlay first only
>    where one can exist, then the original). The index never makes a miss
>    authoritative on its own.
> 2. **ETag and caching** come from the chosen entry's digest, **before any
>    byte is read**: an `If-None-Match` that matches answers 304 with no `Get`;
>    `?v=<digest>` that matches gets `immutable`, as before.
> 3. **Bytes come from a digest-keyed LRU** (`--art-cache-bytes`, default
>    64 MiB; the key is the digest, so an entry is never stale and needs no
>    invalidation). On a miss the object is read **whole**, bounded by
>    `events.ArtworkMaxImageBytes + 1` (20 MiB, the gateway's cap moved into
>    `pkg/events` so the ui can name it) and a 10 s deadline, and its digest is
>    checked against the one chosen. On an `i/o timeout`, a digest mismatch or a
>    changed digest (overwritten mid-read), it re-resolves once and retries; only
>    then are the headers and body written. A 200 never begins before the bytes
>    are in hand.
> 4. **The SSE `art` event** (owner decision (b)). The index's changes map to
>    `(kind, uid, type) -> served digest`: the overlay if present, else the
>    original. A change to an original under an existing overlay changes nothing
>    served and emits nothing until the renderer `Put`s. `GET
>    /events/library/{tab}` emits `event: art` with
>    `data: {"key":"movie/<uid>/poster","v":"<digest>"}` for items on the
>    subscriber's page; the item page, which has no stream today (the research
>    assumed one), gets `GET /events/art?key=<kind>/<uid>/<type>` (up to 16
>    keys), emitting the same event for those keys only. A few lines in
>    `ui/static/art.js` swap `img[data-art="<key>"]`'s `src` to
>    `/art/<key>?v=<digest>`; the templates that render an `/art` URL gain
>    `data-art`. The 5 s projection tick still re-renders cards from status and
>    lands on the same URL harmlessly; the event makes the swap sub-second.
>
> Nothing here writes: `Watch`, `Status`, `Info` and `Get` are reads, so "the UI
> may hold a bus connection for reads plus one request" stands. The guard list
> grows (split §4.5.2 as amended): `TestUINeverWrites` adds `PutString`,
> `PutFile`, `AddBucketLink`, `SetMeta`, `CreateObjectStore`,
> `UpdateObjectStore`, `CreateOrUpdateObjectStore`, `DeleteObjectStore` and
> `PurgeOrphanChunks`, and bans importing `nats.go` or `nats.go/jetstream` under
> `ui/`. The Plex provider (`ui/plex`) may consult the index to omit an image
> whose object the synced index does not hold (§D.5, "omitted, never blanked");
> that is optional and not planned.
>
> **Cross-repo, not done on this branch: cluster-plex.** Its watcher decides
> whether to refresh Plex by hashing `status.metadata`, `status.overlay`,
> `status.title`, `status.overview` and `status.airDate`
> (`cluster-plex/pkg/clustarrwatch/fields.go:46-53`, compared at
> `watcher.go:302`), not `status.artwork`. A custom override (`spec.artwork`) or
> an audit backfill changes `status.artwork` alone, which changes the `?v=` URL
> the Plex provider would answer with, but Plex never asks again and keeps the
> old poster. The fix is `{"status","artwork"}` in `shown` (`Trim` already keeps
> every `shown` path) with `TestMetadataHashCoversArtwork`; cluster-plex has no
> NATS connection, so a watch is not an option there. The owner schedules it in
> cluster-plex.

### B.9 ADR-0011

"Artwork lives in a JetStream object store, one bucket, two writers split
by variant." Supersedes amendment 1 §A3.4's disk cache.

> **Amended 2026-10-07.** A partial change, not a supersession
> (`docs/adr/README.md`, "Lifecycle"): the decision stands, and the ADR index
> records a one-line refinement under ADR-0011: "2026-10-07: no object links;
> objects carry versioned metadata; the ui indexes the bucket by watch; the
> reaper audits and purges orphan chunks."

## C. Ratings and overlays

### C.1 API

In `api/catalog/v1alpha1`:

```go
// +kubebuilder:validation:Enum=imdb;tmdb;rottenTomatoesCritic;rottenTomatoesAudience;metacritic;trakt;letterboxd
type RatingSource string

type Rating struct {
    Source      RatingSource `json:"source"`
    ValueCentis int32        `json:"valueCentis"` // 0-1000 for /10 sources, 0-10000 for /100 sources
    Votes       int32        `json:"votes,omitempty"`
}
```

`MovieMetadata` and `SeriesMetadata` gain `Ratings []Rating` (MaxItems=7,
listType map on `source`). `SeriesMetadata` gains `FirstAired *metav1.Time`,
filled from TVDB `firstAired` or TMDB `first_air_date`; Plex requires it.

`MetadataProviderType` gains `mdblist` and `omdb`. Both take `secretRef`
key `apiKey`. `BaseURL` overrides apply as for every other provider, which is
how the e2e stubs reach them.

### C.2 Capabilities and fallthrough

```go
// pkg/metadata
type RatingsProvider interface {
    Provider
    RatingSources(kind commonv1.MediaKind) []RatingSource
    Ratings(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) (Ratings, error)
}
```

`Registry` gains `Ratings []RatingsProvider`, ordered by priority like
`Artwork`. Declared sources:

| Provider | Movie and series sources |
| --- | --- |
| tmdb | tmdb (from the fetch it already performs) |
| mdblist | imdb, tmdb, rottenTomatoesCritic, rottenTomatoesAudience, metacritic, trakt, letterboxd |
| omdb | imdb, rottenTomatoesCritic, metacritic |

`enrichRatings` in the gateway walks providers in priority order. For each
provider it computes `need` = the sources it declares that are still
unfilled; skips it when `need` is empty; calls it once; on error logs at
warn and continues to the next provider; on success copies only the `need`
sources. A provider can therefore never overwrite a source a higher-priority
provider filled, and a failing provider never blanks anything. Sources still
unfilled at the end are carried forward from the item's current
`status.metadata.ratings`, exactly as ids are carried forward today, so a
transient outage cannot strip badges. The result is written into
`status.metadata.ratings` with the rest of the fetch.

MDBList is keyed by TMDB id (`/tmdb/movie/{id}`, `/tmdb/show/{id}`). OMDb is
keyed by IMDb id only; without one in `ExternalIDs` it returns no ratings
rather than searching by title. Each client takes an injected limiter and
reads through `metadata.ReadBody`; the controller holds one limiter per
host as for every provider.

> **As built (2026-09-24): series carry no ratings in M7.** The table's
> "Movie and series" is the design, not what shipped. TMDB declares its
> `tmdb` source for movies only (`RatingSources` answers nil for any other
> kind), because series metadata comes from TVDB, which supplies no
> ratings, and no recorded TMDB `/tv/{id}` response exists to build a
> series ratings call against. MDBList and OMDb, the other two rows, were
> not built (§C.3, ruling R5). So `Series.status.metadata.ratings` stays
> empty, a series `OverlayProfile` renders nothing, and the Plex provider
> sends no `Rating[]` for a show, until TMDB TV ratings (from a recorded
> `/tv/{id}` fixture) or MDBList/OMDb land. Both are M7 carried items in
> the remaining-work plan.

### C.3 Recorded shapes

The MDBList and OMDb clients are built against responses recorded from the
real APIs during implementation into `test/data/metadata/mdblist/` and
`test/data/metadata/omdb/`, and a research note
`docs/research/ratings-providers.md` records the verified field names,
units and quotas. Until recorded, no field name from memory is to be relied
on; the implementation task blocks on the recording.

> **As built (2026-09-24), ruling R5.** Neither client was built: no
> `MDBLIST_API_KEY`/`OMDB_API_KEY` was available at implementation time.
> `docs/research/ratings-providers.md` exists as a skeleton carrying
> `UNRECORDED` markers in place of verified field names, and a
> `MetadataProviderType: mdblist` or `omdb` CR reports `Ready=False`,
> reason `InvalidSpec`, until the fixtures are recorded and the clients
> built (follow-up, `docs/superpowers/plans/2026-09-18-remaining-work.md`'s
> M7 carried items). TMDB ratings ship for movies only: series get none in
> M7 (§C.2's as-built note).
>
> **As built (2026-09-24, later): MDBList.** Its shapes were recorded from
> the live API into `test/data/metadata/mdblist/` and
> `docs/research/ratings-providers.md`, and `pkg/metadata/clients/mdblist`
> was built against them. It declares all seven sources for movies and
> series, keys a movie by TMDB id and a series by TMDB or else TVDB id
> (`/tvdb/show/{id}` answers the same document), and so gives Series their
> first ratings. Two departures from §C.1/§C.2: MDBList reports Rotten
> Tomatoes as two sources, `tomatoes` (critic) and `popcorn` (audience),
> and each `value` on the source's own scale (imdb /10, letterboxd /5,
> everything else /100), which the client converts to §C.1's centis; and
> the Secret takes an optional second key, `apiKeySecondary`, because the
> quota is 1000 requests a day per key. OMDb is still unbuilt.

### C.4 OverlayProfile

A new kind in `catalog.clustarr.io/v1alpha1`, namespaced:

```go
type OverlayProfileSpec struct {
    // Selector matches Movie and Series labels. Nil selects nothing.
    Selector *metav1.LabelSelector `json:"selector,omitempty"`
    // Kinds limits the selection; default both.
    // +kubebuilder:validation:MaxItems=2
    Kinds []commonv1.MediaKind `json:"kinds,omitempty"`   // movie | series
    // Badges, drawn bottom-up along the chosen corner. Default one, metacritic.
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:MaxItems=4
    Badges []OverlayBadge `json:"badges,omitempty"`
    // +kubebuilder:validation:Enum=bottomRight;bottomLeft;topRight;topLeft
    // +kubebuilder:default=bottomRight
    Corner OverlayCorner `json:"corner,omitempty"`
    Geometry *OverlayGeometry `json:"geometry,omitempty"`
}
type OverlayBadge struct { Source RatingSource `json:"source"` }
// Percentages of poster width unless stated. Pointers with *OrDefault
// accessors (the typed-client defaulting gotcha).
type OverlayGeometry struct {
    WidthPercent   *int32 `json:"widthPercent,omitempty"`   // 14
    RadiusPercent  *int32 `json:"radiusPercent,omitempty"`  // 2
    PaddingPercent *int32 `json:"paddingPercent,omitempty"` // 2
    LogoPercent    *int32 `json:"logoPercent,omitempty"`    // 60, of box width
    ScorePercent   *int32 `json:"scorePercent,omitempty"`   // 45, of box height
    OpacityPercent *int32 `json:"opacityPercent,omitempty"` // 90
}
type OverlayProfileStatus struct {
    Hash       string             `json:"hash,omitempty"`
    Selected   int32              `json:"selected,omitempty"`
    Conditions []metav1.Condition `json:"conditions,omitempty"` // Ready, Overlap; MaxItems=8
}
```

Its controller (`catalogarr --role controller`) computes `status.hash`
through `overlay.TemplateSpec`, the same conversion the renderer draws from
(a test fails if a render field stops moving it); marks the loser of two
overlapping selectors with `Overlap`, the lower name winning, as
`TranscodeProfile` does; and on any change to hash or selection publishes
one `RenderOverlay` task per selected item. An item matched by no
non-overlapped profile has its overlay removed on its next render task.

> **As built (2026-09-24).** "Lower name wins" is kept as designed — but
> the comparison to `TranscodeProfile` above is wrong and should be read as
> struck: `TranscodeProfile`'s own overlap rule is oldest-wins, not
> lower-name-wins, so the two kinds tie-break differently. `status.hash`
> includes the profile's badges, not only its selector and geometry, so a
> badge-only edit re-renders. The controller keys each render task's Msg-Id
> by the selected item's `resourceVersion`, and publishes one for an item a
> change just *deselected* too, so its overlay is cleared rather than left
> stale. RBAC on `overlayprofiles` is `get,list,watch` plus a `status`
> patch, matching every other controller-owned kind. `Kinds`' enum is
> narrowed to `movie`/`series` by CEL only; the raw OpenAPI schema still
> lists all ten `MediaKind` values, since the field's underlying Go type
> admits more values than this kind's business rule allows.

### C.5 The renderer package

`pkg/overlay`, pure Go, no cluster:

```go
type Badge struct { Source RatingSource; Score string; Logo image.Image }
type Template struct { Corner Corner; WidthPct, RadiusPct, PaddingPct, LogoPct, ScorePct, OpacityPct int }
func DefaultTemplate() Template
func Render(base image.Image, badges []Badge, t Template) (*image.NRGBA, error)
func FormatScore(src RatingSource, centis int32) string
func TemplateSpec(p catalogv1alpha1.OverlayProfileSpec) Template
```

Geometry, from the two references: the box sits in the chosen corner with
its outer corner square and flush with the poster edge and the other three
corners rounded; background near-opaque dark grey (`#1F1F1F` at
`OpacityPct`); the logo centred in the top part and the score in bold white
beneath it. Every dimension scales with poster width so a 1000x1500 poster
and a 500x750 thumbnail match. Several badges stack away from the corner
along the poster's vertical edge with `PaddingPct` between them.

> **As built (2026-09-24, later): the box is Plex's episode-count box,
> measured.** The owner supplied a Plex screenshot and asked for the badge
> to match its episode-count box exactly. Measured on its 1249x1869
> poster: 237x207 px (18.98% of the poster's width by 16.57% of it, aspect
> 237:207), flush with two poster edges, square on the two corners on
> those edges and rounded (about 25px, 2% of the width) only on the inner
> corner, pure black at 80% (every box pixel is 0.2x the poster beneath),
> its count 56px tall (27% of the box) and centred. `pkg/overlay` now draws
> exactly that: `widthPercent` defaults to 19 and the height follows at
> 237:207 (not square), one rounded corner (not three), black at an
> `opacityPercent` default of 80 (not `#1F1F1F` at 90), and `scorePercent`
> (default 27) of the box height, as §C.4 always documented -- the first
> cut applied it to the height left under the logo, which at the old
> defaults drew every score at the 8px font floor. `RenderVersion` 3
> re-renders every stored overlay. The owner's profile anchors it
> `topLeft`, clear of Plex's own count in the top right.
>
> Then, at the owner's choice: the logo sits **left of** the score on one
> row, the row centred in the box both ways by the glyphs' ink (not their
> advance), and `logoPercent` became the logo's **height** as a share of the
> box height (default 32, a little taller than the 27% digits); a row too
> wide for the box -- "100" beside a logo -- shrinks as a whole. The score
> is drawn in **Open Sans Bold** (OFL, `pkg/overlay/fonts/`), Plex's face:
> rendered at the count's 56px, its "665" overlaps the screenshot's glyphs
> at IoU 0.87, against 0.76 for Inter Bold and 0.78 for Open Sans SemiBold.
> `RenderVersion` 4.
>
> The owner then compared the stacked badge beside a Plex item and chose
> it over the row: the logo is back **above** the score (`logoPercent` the
> logo's width, default 60), keeping Open Sans Bold and the ink-centred
> score. `RenderVersion` 5.

`FormatScore`: metacritic, rottenTomatoesCritic and rottenTomatoesAudience
as whole numbers 0 to 100; imdb, tmdb, trakt and letterboxd with one
decimal 0 to 10. Drawing uses `golang.org/x/image/draw` and
`x/image/font/opentype` with the embedded `gofont/gobold` face (BSD, already
in x/image). Logos are embedded PNGs under `pkg/overlay/logos/` with a
`NOTICE` stating they are their owners' marks, used to identify the rating
source, as Kometa carries them. PNG goldens under `test/data/overlay/` are
rendered from a synthetic poster; each geometry percentage is a named
default one test pins. Output is JPEG quality 90.

### C.6 The renderer role

`catalogarr --role artwork` runs the durable `catalogarr-artwork-render` on
`events.WorkArtworkRenderSubject(mediaKey)`,
`clustarr.work.catalogarr.artwork.render.<mediaKey>`, published by the
gateway (§B.4) and the OverlayProfile controller (§C.4), Msg-Id
`<uid>/render/<inputs digest>`. Per task:

1. `Get` the item; find its profile (the non-overlapped profile whose
   selector matches); if none, delete `poster/overlay` if present, clear
   `status.overlay`, done.
2. Compute the inputs digest: SHA-256 over the original's digest, the
   profile hash and the item's ratings sorted by source.
3. `Info` the overlay; if `Clustarr-Rendered-From` equals the digest, done.
4. `Get` the original poster, decode, `Render` with the profile's badges
   (a badge whose source has no rating is omitted; no badges means no
   overlay, treated as step 1's "none"), encode, `Put` `poster/overlay`
   with `Clustarr-Rendered-From`, and write `status.overlay`.

The role never touches `original` objects or `status.artwork`. It is not
leader-elected and scales by consumer.

> **As built (2026-09-24).** Renders are bounded to a default 2 concurrent
> in-flight per process (an unbounded fan-out of large-poster decodes could
> OOM the catalogarr pod carrying the role, review finding on C3). An
> original that fails to decode clears any existing overlay instead of
> leaving a stale one — the same "none" outcome as a missing original.
> Finding the item's profile (step 1) reads the current `OverlayProfile`
> list uncached on every task rather than from an informer cache, so a
> profile edit is picked up by the very next render, not the cache's
> resync interval. Minor (deferred): a profile deleted while no catalogarr
> replica is leader strands its overlays; a known-undecodable original is
> re-attempted on every later task, with no negative cache.
>
> **Final fix wave.** A decoded original wider than `MaxRenderWidth` (2000)
> is downscaled to it, aspect kept, before `Render`, so the canvas and the
> JPEG are never larger than a 2000px-wide poster's; only the decode itself
> still holds a max-size 8000x8000 original (96-256 MB, down from about
> 512 MB per render with its full-size canvas). Step 2's digest also folds
> in a `RenderVersion` constant, so a renderer that draws differently from
> the same inputs re-renders every stored overlay. The gateway publishes
> render tasks for Movie and Series only, and the renderer acknowledges a
> task for any other kind instead of discarding it, which had
> dead-lettered one per non-video poster.
>
> **Amended 2026-10-07** (`nats-object-store.md` §6.3, §6.9, §7; §B.3 as
> amended). The four steps run under a per-item in-process lock (key
> `<kind>/<namespace>/<name>`). Step 4's `Put` sends the complete overlay set of
> §B.2 as amended (`artwork.ObjectMeta`, with the profile, the original's digest
> and the rendered bounds). Step 3, finding the overlay current, calls `SetMeta`
> with the full set when its `clustarr.io/meta-version` is below current (the
> backfill; the overlay's dimensions come from a 64 KiB `image.DecodeConfig`
> read, as the gateway's do). Step 4 reads the original whole under a 30 s
> deadline and treats a timed-out read like a changed original,
> `ErrInputsMoved`, which retries: an original overwritten mid-read otherwise
> held a render slot until the read's deadline (§B.8 as amended). The renderer
> stays on its durable `RenderOverlay` work queue and never watches the bucket:
> a watch cannot wake a domain at zero, has no ack, redelivery or dead letter,
> and cannot see the ratings and profile changes that also trigger a render.

## D. Plex Metadata Provider

### D.1 Placement and flags

Two provider roots on the ui service, read-only over the projection's
cache, following Plex's one-parent-type-per-provider recommendation:

| Root | Types | Identifier |
| --- | --- | --- |
| `/plex/movies` | 1 | `tv.plex.agents.custom.clustarr.movies` |
| `/plex/tv` | 2, 3, 4 | `tv.plex.agents.custom.clustarr.tv` |

Flags on the ui command: `--plex-provider` (bool, default true) and
`--external-url` (env `CLUSTARR_EXTERNAL_URL`), the absolute base every
`thumb`, `art` and `Image[].url` is built on. With `--plex-provider` and no
external URL the roots return 503 with a body naming the flag, logged once
at startup; readiness is unaffected. The provider is unauthenticated by
protocol and exposes the whole catalog: the README, the chart's ingress
values and `docs/observability.md`'s exposure section state it must not sit
behind a public ingress.

> **As built (2026-09-24).** The chart gained `ui.plex.enabled` and
> `ui.plex.externalURL` values that render as `--plex-provider` and
> `--external-url` on the `ui` Deployment; kustomize carries the same two
> flags on `config/manager/ui.yaml`, matching the binary's own flag names
> for parity. Both default to the routes being reachable but inert (503, no
> external URL) rather than a guessed hostname.

### D.2 Routes

Relative to each root `R`:

| Method and path | Feature |
| --- | --- |
| `GET R` | `MediaProvider` with `Types[{type, Scheme[{scheme}]}]` and `Feature[{type: match, key: /library/metadata/matches}, {type: metadata, key: /library/metadata}]` |
| `POST R/library/metadata/matches` | match |
| `GET R/library/metadata/{ratingKey}` | metadata; honours `includeChildren` |
| `GET R/library/metadata/{ratingKey}/images` | `MediaContainer{Image[]}` |
| `GET R/library/metadata/{ratingKey}/children` | tv only; seasons of a show, episodes of a season; paged |
| `GET R/library/metadata/{ratingKey}/grandchildren` | tv only; episodes of a show; paged |

Paging reads `X-Plex-Container-Start` (default 0) and
`X-Plex-Container-Size` (default 20) as headers or query params, and
answers `offset`, `size`, `totalSize`. Whether Plex's start is 0- or
1-based is unverified in the research note; 0-based is implemented and the
Phase H run against a real server settles it. Every response is
`Content-Type: application/json` with `Cache-Control: no-store`.

### D.3 Identity

`ratingKey` charset is `[A-Za-z0-9_-]`, which excludes the periods a
Kubernetes name may carry, so keys are UIDs: movie, series and episode use
the object UID; a season is `<seriesUID>-s<NN>`, `NN` zero-padded to two
digits. `guid` is `<identifier>://<type>/<ratingKey>`.

> **As built (2026-09-24, final fix wave).** "Zero-padded to two digits"
> is a minimum: season 100, or a daily show's season 2024, mints `-s100`
> and `-s2024`, and the parser accepts two to four digits (only the
> canonical spelling, so `-s007` is refused). Each root resolves only the
> types §D.1 gives it: a show's ratingKey under `/plex/movies`, or a movie's
> under `/plex/tv`, is a 404, and a match for another root's type is empty.

### D.4 Match

Order of evaluation, first hit wins, results are full Metadata objects:

1. `guid` in the request: `tmdb://N` against `spec.tmdbID`, `tvdb://N`
   against `spec.tvdbID`, `imdb://ttN` against
   `status.metadata.externalIDs.imdb`.
2. Otherwise `title` (or `parentTitle`/`grandparentTitle` for seasons and
   episodes) normalised by `release.TitleNorm` against each item's title and
   alternate titles, with `year` exact first, then within one, then any
   when no year was given; ordered exact-year first, then by title.
3. Seasons and episodes resolve their show by 1 or 2, then `index` and
   `parentIndex`, or `date` against `airDate` when indexes are absent.

Only clustarr's own catalog is answered from. No match returns an empty
container, and Plex falls through to the next provider in the agent.

> **As built (2026-09-24, final fix wave).** The first cut applied "then
> any" even when a year was given, so an automatic match bound
> *Dune (2021)* to *Dune (1984)*. An automatic match that names a year now
> takes exact then ±1 only; the any-year tier is kept for a request with no
> year and for `manual=1`, where Plex shows a ranked list. Rule 3 is
> exempt: a season's or episode's request carries its own release year,
> not the show's, so there the year orders the show candidates but never
> excludes one. The ui builds the catalogue index once per 5 s
> (`projection.IndexTTL`, singleflight) rather than per request.

### D.5 Metadata mapping

| Plex field | Movie | Show | Season | Episode |
| --- | --- | --- | --- | --- |
| `title`, `originalTitle`, `summary`, `year` | metadata title, originalTitle, overview, year | same | `Season N`, show year | episode title, overview |
| `originallyAvailableAt` | earliest of inCinemas, digitalRelease, physicalRelease | `firstAired` | first episode `airDate` | `airDate` |
| `contentRating` | certification | certification | | |
| `duration` | runtimeMinutes × 60000 | runtimeMinutes × 60000 | | runtime × 60000 when known |
| `Genre[]`, `Guid[]`, `Collection[]`, `Network[]`, `Country[]` | genres, ids, collection | genres, ids, network | | |
| `thumb`, `art`, `Image[]` | `/art/.../poster?v=`, `/art/.../fanart?v=` as `coverPoster`, `background`, plus `logo` as `clearLogo` | same | show's | screenshot as `snapshot` when stored |
| `Rating[]` | see below | same | | |
| parent and grandparent keys, `index`, `parentIndex` | | | show refs, number | show and season refs, numbers |

`Rating[]` uses Plex's closed vocabulary only: imdb → `imdb://image.rating`
type `audience`; tmdb → `themoviedb://image.rating` type `audience`;
rottenTomatoesCritic → `rottentomatoes://image.rating.ripe` type `critic`;
rottenTomatoesAudience → `rottentomatoes://image.rating.upright` type
`audience`. Values are 0 to 10 floats: `centis/100` for /10 sources and
`centis/1000` for the Rotten Tomatoes pair. Metacritic, trakt and letterboxd
are never sent to Plex; they appear only on the overlay poster. Optional
fields with no clustarr source (`Role`, `Director`, `Writer`, `tagline`,
`theme`) are omitted, never blanked.

### D.6 Tests

`ui/plex` is held to JSON goldens derived from the official example's test
fixtures for every route and type, plus a paging test at the boundaries. A
new e2e scenario 18 in `test/e2e/plex_test.go` walks root → match →
metadata → images → children against the fixture library on kind and
fetches every image URL it is handed; it is written and, like every
scenario since Phase C, executed in Phase H.

### D.7 ADR-0012

"The Plex Metadata Provider is served read-only by the ui service,
unauthenticated per protocol, and must not be publicly exposed."

## E. Cross-cutting

- **Dependencies**, added serially before any parallel work:
  `github.com/jackc/pgx/v5`, `github.com/fergusstrange/embedded-postgres`,
  `golang.org/x/image` (promoted to direct if indirect). Nothing else.
- **Events topology**: the object store (§B.1), two work subjects and
  durables (§B.7, §C.6) on the catalogarr work stream, and their
  `AckWait`/`MaxDeliver`/`Backoff` following the metadata consumer's.
- **Guards**: every new status list is capped; `crdcheck`'s
  default-reachability and cap tests pass; the registration guard sees the
  new role, runnables and durable; RBAC regenerates for the OverlayProfile
  kind and the item status fields; the chart-versus-kustomize test covers
  the postgres component and the ui's new flags;
  `TestBothUICommandsWireEveryUIOption` covers `Artwork`, `Plex` and
  `ExternalURL`.
- **Docs**: the design of record's §4 (fields above), §6 (catalogarr's
  `artwork` role, the ui's roots), §11 (storage: the bucket, Postgres as an
  option) and §16 (this work as M7); amendment 1 §A3.4 (art served from the
  store); the library page design's decision 1; CLAUDE.md's services table,
  invariants (the ui may hold a read-only bus; two artwork writers split by
  variant) and gotchas; ADRs 0010, 0011, 0012 and the ADR index;
  `docs/research/ratings-providers.md` (§C.3).
- **Build order**: A and B in parallel, then C, then D. Each part ends
  green on `make test` and `make lint`; commits on `main` with pathspecs,
  nothing pushed from a task.
