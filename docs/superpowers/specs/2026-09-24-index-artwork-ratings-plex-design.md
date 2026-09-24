# Selectable release index, artwork store, rating overlays and the Plex provider

**Status:** Approved design, 2026-09-24. Amends the design of record
(`2026-09-18-clustarr-design.md`) and amendment 1. Where this document and
either of those disagree, this document wins for the parts it covers.

**Supersedes:** ADR-0003's "SQLite on an RWO volume" as the only index
engine (ADR-0010); amendment 1 §A3.4's "the metadata gateway caches art on
`/data`" (ADR-0011); the library page design's decision 1, "cover art is
hotlinked" (this document, §B.6).

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

### B.3 Writers

Two writers, split by variant, the same discipline as spec versus status on
MediaFile:

- The **metadata gateway** (`catalogarr --role metadata`) is the sole writer
  of `original` objects and of `status.artwork`.
- The **renderer** (`catalogarr --role artwork`, §C.6) is the sole writer of
  `overlay` objects and of `status.overlay`.

Neither reads, writes or deletes the other's variant, except the reaper
(§B.5).

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

### B.5 Reaper

`app/catalog/metadata/artwork.Reaper`, a leader-elected runnable in the
gateway, every 6 hours lists the bucket and deletes every object whose
`<kind>/<uid>` prefix names no live item of that kind, after a 30-minute
grace period from the object's `ModTime`, following `grabarr`'s reapers. It
is the only code that deletes both variants.

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

### B.9 ADR-0011

"Artwork lives in a JetStream object store, one bucket, two writers split
by variant." Supersedes amendment 1 §A3.4's disk cache.

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
