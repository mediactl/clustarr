# M7 — selectable release index, artwork store, rating overlays, Plex provider

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** indexarr can run on Postgres (CloudNativePG) with no volume and several replicas; artwork lives in a JetStream object store and is served by the ui; posters carry Kometa-style rating badges from TMDB, MDBList and OMDb; Plex pulls movie and TV metadata and art from clustarr through its Custom Metadata Provider protocol.

**Architecture:** four parts. **A** adds a second `relindex.Store` and the chart wiring for it. **B** adds `events.ObjectStore`, makes the metadata gateway the writer of `original` art objects and the ui their reader. **C** adds ratings to `status.metadata` through a capability-declaring `RatingsProvider`, a pure renderer `pkg/overlay`, the `OverlayProfile` kind and a `catalogarr --role artwork` worker writing `overlay` objects. **D** mounts two Plex provider roots on the ui service over the projection cache.

**Tech stack:** Go 1.27, controller-runtime v0.25.1, nats.go v1.53.1 (`jetstream.ObjectStore`), `jackc/pgx/v5` via `database/sql`, `fergusstrange/embedded-postgres` (tests only), `golang.org/x/image` (draw, font/opentype, gofont/gobold), templ + htmx (ui).

**Spec:** `docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md` — read it first; §-references below are to it. Research: `docs/research/plex-metadata-provider.md`.

---

## Global Constraints

- GPL-3.0 header (`hack/boilerplate.go.txt`) on every Go file. `slog` through `context`; spans on every handler and outbound call; `clustarr_` metrics with no title/path/URL labels.
- **All status writes via `pkg/k8s.PatchStatus`, each a complete declaration of everything that manager owns.** Every CLAUDE.md "Gotchas" variant applies: no partial early returns, no two applies per reconcile, re-`Get` before applying after slow work, `With*` list builders append. **An over-claim is silent** — assert manager splits on `managedFields`, against an object that already has status.
- **No `float32`/`float64` under `api/`.** Ratings are `ValueCentis int32`.
- **Cap every status list** (`+kubebuilder:validation:MaxItems`). Pointers plus `*OrDefault` accessors for any defaulted scalar a Go client must be able to send as zero (`pkg/crdcheck` guards both).
- **The caller owns rate limiting**: new clients take `Config.Limiter`, never default one. **Every HTTP body is read through `metadata.ReadBody`** or an equivalent cap.
- **NATS KV keys via `events.KVKeyToken`**; object names via `events.ArtworkKey`. New bus shapes are contract-tested against a **real embedded server** (`pkg/events/natsbus/*_contract_test.go` pattern), not only membus.
- **The ui never writes** (`ui/guard_test.go`), never imports `pkg/k8s`; its bus is read-only and is connected in `cmd/clustarr`, not in `ui/`.
- **Registration is where inert code hides.** Every new runnable, consumer and role is reachable from its subcommand and from `clustarr all`, and `cmd/clustarr/runnable_registration_test.go` sees it.
- **No network in tests.** Provider fixtures are recorded files under `test/data/`. Postgres in tests is the embedded binary at `$CLUSTARR_PG_ASSETS`; e2e is **written, not run** (user instruction, Phase H runs it).
- **New Go dependencies only in W0-1, serially.** Workers never run `go get` or `go mod tidy`.
- **Commit with a pathspec** (`git commit -m '...' -- <paths>`). Never `git add -A`, `commit -a`, `git stash`, `reset --hard`. **Never push.** One owner per shared file per wave; W0 files (`api/`, `pkg/events/subjects.go`, `pkg/events/topology.go`, `pkg/k8s/fieldmanager.go`, `go.mod`) are frozen after W0.
- `make generate && make manifests` after any `api/` change; chart RBAC synced between its sentinels; `make lint` clean before every commit.

## Rulings

- **R1 — the sweep is leader-only under both engines.** `sweepReleaseIndex` (`app/indexer/run.go:811`) becomes a runnable with `NeedLeaderElection() == true`. Under SQLite the single replica is the leader; nothing changes.
- **R2 — the CNPG `Cluster` is a Helm hook.** CNPG's webhook fails closed before the operator runs, so the `Cluster` template carries `helm.sh/hook: post-install,post-upgrade` and no delete policy, and the README says `--wait`. Kustomize documents "operator first".
- **R3 — a failed custom URL never falls back to the provider image** (§B.4). The entry and object stay as they were; an Event says why.
- **R4 — Plex gets only the four ratings it accepts** (§D.5). Metacritic, trakt and letterboxd are overlay-only.
- **R5 — MDBList and OMDb shapes are recorded before they are coded** (§C.3). C1 checks `MDBLIST_API_KEY` and `OMDB_API_KEY` in the environment; with neither it records nothing, implements TMDB's provider only, and reports the block. It does not write a client from memory.
- **R6 — logos ship embedded with a NOTICE** (user decision over the trademark concern, 2026-09-24). `pkg/overlay/logos/NOTICE` names each mark's owner.
- **R7 — paging is 0-based** (§D.2); the research note marks Plex's base as unverified, Phase H settles it.

## Review Focus

Inputs the spec implies but no requirement names; each has a pinned test in the task shown.

1. **A provider or custom image URL that answers 200 with HTML** (an error page, a login wall) must be rejected by `image.DecodeConfig`, leave the previous object in place and emit `ArtworkFetchFailed` — B2.
2. **A Postgres DSN that cannot connect** must fail readiness and log without the password: the error string from `OpenPostgres` never contains the DSN's password — A1.
3. **`X-Plex-Container-Start` past `totalSize`** returns an empty container with the true `totalSize`, never 500 — D1.
4. **A poster smaller than the badge's minimum** (60x90) still renders: no zero font size, no panic, badge clamped inside the poster — C2.
5. **A rating whose value and votes are both zero** (MDBList's "unrated") is omitted from `status.metadata.ratings` and never drawn as "0" — C1 and C2.

---

## W0 — serial (controller session runs these; nothing parallel until W0 is committed)

### W0-1 — dependencies

- [ ] `go get github.com/jackc/pgx/v5@latest github.com/fergusstrange/embedded-postgres@latest golang.org/x/image@latest && go mod tidy`. If `hack/deps/deps.go` pins imports for `go mod tidy`, add the three there.
- [ ] `go build ./... && go vet ./...` green. Commit `go.mod go.sum hack/deps/deps.go` — `chore: add pgx, embedded-postgres and x/image for M7`.

### W0-2 — API types, generated code, manifests

**Files:** `api/catalog/v1alpha1/shared_types.go`, `movie_types.go`, `series_types.go`, `artist_types.go`, `album_types.go`, `author_types.go`, `book_types.go`, `audiobook_types.go`, `comic_types.go`, `metadataprovider_types.go`, new `overlayprofile_types.go`; `config/crd/bases/*`, `config/rbac/*`, chart CRDs (find how `charts/clustarr` ships CRDs — `grep -rn 'crd' Makefile charts/clustarr/templates | head`; follow it); `api/applyconfiguration/**`.

**Produces (exact names, later tasks depend on them):**

```go
// shared_types.go
type ArtworkSource string            // +kubebuilder:validation:Enum=provider;custom
const ( ArtworkSourceProvider ArtworkSource = "provider"; ArtworkSourceCustom ArtworkSource = "custom" )
type ArtworkOverride struct { Type ImageType `json:"type"`; URL string `json:"url"` } // URL Pattern=`^https?://`, MaxLength=2048
type ArtworkEntry struct {
    Type ImageType `json:"type"`; Source ArtworkSource `json:"source"`; SourceURL string `json:"sourceURL"`
    Digest string `json:"digest"`; SizeBytes int64 `json:"sizeBytes"`; UpdatedAt metav1.Time `json:"updatedAt"`
}
type OverlayEntry struct { ProfileRef string `json:"profileRef"`; Digest string `json:"digest"`; RenderedFrom string `json:"renderedFrom"`; UpdatedAt metav1.Time `json:"updatedAt"` }
type RatingSource string  // Enum=imdb;tmdb;rottenTomatoesCritic;rottenTomatoesAudience;metacritic;trakt;letterboxd
const ( RatingSourceIMDb RatingSource = "imdb"; RatingSourceTMDB = "tmdb"; RatingSourceRTCritic = "rottenTomatoesCritic"
        RatingSourceRTAudience = "rottenTomatoesAudience"; RatingSourceMetacritic = "metacritic"; RatingSourceTrakt = "trakt"; RatingSourceLetterboxd = "letterboxd" )
type Rating struct { Source RatingSource `json:"source"`; ValueCentis int32 `json:"valueCentis"`; Votes int32 `json:"votes,omitempty"` }
```

- Spec of Movie, Series, Artist, Album, Author, Book, Audiobook, Comic: `Artwork []ArtworkOverride` `json:"artwork,omitempty"`, `+optional`, `MaxItems=9`, `+listType=map`, `+listMapKey=type`.
- Status of the same eight: `Artwork []ArtworkEntry` `json:"artwork,omitempty"`, same markers.
- Status of Movie and Series only: `Overlay *OverlayEntry` `json:"overlay,omitempty"`.
- `MovieMetadata.Ratings []Rating` and `SeriesMetadata.Ratings []Rating`: `json:"ratings,omitempty"`, `MaxItems=7`, `+listType=map`, `+listMapKey=source`. `SeriesMetadata.FirstAired *metav1.Time` `json:"firstAired,omitempty"`.
- `MetadataProviderMDBList MetadataProviderType = "mdblist"`, `MetadataProviderOMDb MetadataProviderType = "omdb"`; add both to the type's `Enum` marker and, if the CRD has a CEL rule listing types that need `secretRef`, to it.
- `overlayprofile_types.go`: `OverlayProfile`, `OverlayProfileList`, `OverlayProfileSpec{Selector *metav1.LabelSelector; Kinds []commonv1.MediaKind (MaxItems=2, Enum movie;series); Badges []OverlayBadge (MinItems=1, MaxItems=4, listType map key source); Corner OverlayCorner (Enum bottomRight;bottomLeft;topRight;topLeft, default bottomRight); Geometry *OverlayGeometry}`, `OverlayBadge{Source RatingSource}`, `OverlayGeometry{WidthPercent, RadiusPercent, PaddingPercent, LogoPercent, ScorePercent, OpacityPercent *int32}` each `Minimum=1`, `Maximum=100`, with accessors `WidthPercentOrDefault()` … returning 14, 2, 2, 60, 45, 90 on nil (document each default on the field, no `+kubebuilder:default` — the typed-client gotcha), `OverlayProfileStatus{Hash string; Selected int32; Conditions []metav1.Condition (MaxItems=8)}`, printcolumns Hash, Selected, Ready, Age. Register in `groupversion_info.go`'s `SchemeBuilder` beside `ImportExclusion`.
- [ ] `make generate manifests`; sync chart CRDs and RBAC the way the Makefile/tests expect. `go test ./api/... ./pkg/crdcheck/...` with `KUBEBUILDER_ASSETS` exported: `TestNoCRDDefaultIsUnreachableFromGo`, `TestEveryStatusListIsCapped` and the CRD compile pass. `cmd/clustarr` parity tests pass.
- [ ] Commit — `feat(api): artwork overrides and entries, overlay entry, ratings, series firstAired, mdblist/omdb providers, OverlayProfile kind`.

### W0-3 — events, schema and field-manager constants

**Files:** `pkg/events/subjects.go`, `pkg/events/topology.go`, `pkg/events/schema/` (new `artwork.go`), `pkg/k8s/fieldmanager.go`, `pkg/events/bus.go` (interface only — implementations are B1).

**Produces:**

```go
// subjects.go
const ( BucketArtwork = "clustarr-artwork"; ArtworkMaxBytes int64 = 5 << 30
        ArtworkVariantOriginal = "original"; ArtworkVariantOverlay = "overlay"
        FilterCatalogArtworkFetch  = "clustarr.work.catalogarr.artwork.fetch.>"
        FilterCatalogArtworkRender = "clustarr.work.catalogarr.artwork.render.>"
        ConsumerCatalogArtworkFetch  = "catalogarr-artwork-fetch"
        ConsumerCatalogArtworkRender = "catalogarr-artwork-render" )
func ArtworkKey(kind commonv1.MediaKind, uid types.UID, imageType, variant string) string // "<kind>/<uid>/<imageType>/<variant>"; panics on empty parts or a part containing "/"
func WorkArtworkFetchSubject(mediaKey string) string  // clustarr.work.catalogarr.artwork.fetch.<tok(mediaKey)>
func WorkArtworkRenderSubject(mediaKey string) string // clustarr.work.catalogarr.artwork.render.<tok(mediaKey)>
// topology.go
type ObjectStoreSpec struct { Name, Description string; Storage Storage; MaxBytes int64; Replicas int }
// Topology gains ObjectStores []ObjectStoreSpec, cloned/validated/ForSingleNode'd like Buckets; Default() lists {BucketArtwork, "Artwork originals and overlays", StorageFile, ArtworkMaxBytes, 3}
// two ConsumerSpecs on StreamWorkCatalogarr with the ConsumerCatalogMetadata numbers (AckWait 60s, MaxDeliver 8, BackOff 30s,2m,10m,1h,6h, MaxAckPending 32): fetch filters FilterCatalogArtworkFetch, render filters FilterCatalogArtworkRender
// bus.go
type ObjectInfo struct { Name string; Size int64; Digest string; ModTime time.Time; Headers map[string]string }
type ObjectStore interface {
    Get(ctx context.Context, name string) (ObjectInfo, io.ReadCloser, error)
    Put(ctx context.Context, name string, r io.Reader, headers map[string]string) (ObjectInfo, error)
    Delete(ctx context.Context, name string) error
    Info(ctx context.Context, name string) (ObjectInfo, error)
    List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}
var ErrObjectNotFound = errors.New("events: object not found")
// Bus gains ObjectStore(bucket string) ObjectStore
// schema/artwork.go
type ArtworkFetchTask struct { MediaRef commonv1.MediaRef `json:"mediaRef"` }
type RenderOverlayTask struct { MediaRef commonv1.MediaRef `json:"mediaRef"`; Reason string `json:"reason,omitempty"` } // reason: original|ratings|profile
func MsgIDForArtworkFetch(uid types.UID, specHash string) string   // "<uid>/artwork/<specHash>"
func MsgIDForRenderOverlay(uid types.UID, inputsDigest string) string // "<uid>/render/<inputsDigest>"
// fieldmanager.go
ManagerCatalogarrArtwork FieldManager = "catalogarr-artwork" // added to FieldManagers()
```

- [ ] Tests: `ArtworkKey` panics on `"a/b"`, tok'd subjects match the filters (`StreamForSubject`), `Topology.Validate` rejects a duplicate object-store name, `pkg/events/events_test.go`'s topology snapshot updated. Adding `ObjectStore` to `Bus` breaks membus/natsbus compilation: add stub methods returning a `notImplementedObjectStore` whose every call returns `errors.New("object store: not implemented until B1")` so W0-3 compiles and B1 replaces them. Commit — `feat(events): artwork bucket, keys, work subjects, consumers, ObjectStore contract; catalogarr-artwork manager`.

---

## Part A — release index on Postgres (parallel with B)

### A1 — `storetest` and the Postgres store

**Files:** create `pkg/relindex/storetest/storetest.go`, `pkg/relindex/postgres.go`, `pkg/relindex/postgres_schema.go`, `pkg/relindex/postgres_test.go`; modify `pkg/relindex/store_test.go` and siblings (move their cases into `storetest`, keep a thin `TestSQLiteStoreContract` calling `storetest.Run`).

**Interfaces:** consumes `relindex.Store`, `relindex.Query`, `relindex.Release`, `relindex.Stats` unchanged. Produces `func OpenPostgres(ctx context.Context, dsn string) (Store, io.Closer, error)` and `func storetest.Run(t *testing.T, open func(t *testing.T) Store)`.

- [ ] **storetest.** Lift every behavioural test in `pkg/relindex/*_test.go` (upsert idempotence on `(indexer, guid)`, text match on `title_norm` and `grp`, category any-of, indexer filter, protocol, `Since` on fetched not published, ordering with and without text, `Limit` ≤ 0 means unlimited, `Prune` by `fetched_at`, `Stats`, the `titlenorm_test.go` round trip, the nil-vs-zero `PublishedAt` contract, the attacker-controlled text cases from `fts_test.go` — `"a AND"`, `"\"unterminated"`, `"NEAR("`, control characters, an all-punctuation query must not error and must return zero or all rows consistently across engines) into `storetest.Run`. SQLite-only tests (`user_version`, `ExportedForTestDB`, WAL) stay where they are.
- [ ] **Postgres store.** `postgres.go`: `OpenPostgres` opens `sql.Open("pgx", dsn)`, pings with the ctx, runs the ladder in `postgres_schema.go` (`relindex_schema(version integer)`; `migrations[0]` is §A.1's DDL verbatim), and returns a `pgStore`. Queries per §A.1: parameters `$1…`, `categories && $n::integer[]` via `pgtype.FlatArray[int32]` or `pq`-free `[]int32` through pgx's stdlib (pgx v5 encodes `[]int32` natively), `plainto_tsquery('simple', $1)`. `Upsert` validates the batch first with the existing `validate`, then one transaction. `Stats.SizeBytes` from `pg_total_relation_size('releases')`. **Errors never include the DSN**: wrap with `fmt.Errorf("relindex: postgres: %w", err)` and never format `dsn` into a message.
- [ ] **Tests.** `postgres_test.go`: `TestPostgresStoreContract` — skip with `t.Skip("CLUSTARR_PG_ASSETS unset; run make pg-assets")` when unset; else `embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().CachePath(assets).RuntimePath(t.TempDir()).Port(free port).Database("relindex"))`, `Start`, `t.Cleanup(Stop)`, and `storetest.Run` against `OpenPostgres`. Add `TestOpenPostgresErrorOmitsPassword` (Review Focus 2): `OpenPostgres(ctx, "postgres://u:s3cret@127.0.0.1:1/x?connect_timeout=1")` errors and `!strings.Contains(err.Error(), "s3cret")`. Run `go test ./pkg/relindex/...` with `CLUSTARR_PG_ASSETS` set to a scratch dir once (this downloads the binary — do it once, outside the suite, via the A2 Makefile target if you build that first, else `go run` a five-line program that starts and stops an embedded instance with that `CachePath`).
- [ ] Commit — `feat(relindex): Postgres store behind the same Store contract; storetest runs both engines`.

### A2 — pivot, leader-only sweep, `pg-assets`

**Files:** `cmd/clustarr/flags.go`, `cmd/clustarr/services.go` (indexarr flags), `cmd/clustarr/all.go` (dev path), `app/indexer/run.go:95-110,355-370,795-845`, `Makefile`, `cmd/clustarr/runnable_registration_test.go`.

**Interfaces:** consumes `relindex.OpenPostgres`. Produces `indexer.Options.IndexDSN string`; env `CLUSTARR_INDEX_DSN`; Makefile targets `pg-assets`, and `test` exporting `CLUSTARR_PG_ASSETS`.

- [ ] `flags.go`: `indexDSNEnv = "CLUSTARR_INDEX_DSN"`; `services.go` indexarr: `--index-dsn` (help: "Postgres DSN for the release index. Non-empty selects Postgres and ignores --index-path."); `all.go` the same flag. `app/indexer/run.go`: `Options.IndexDSN`; in the open path `if o.IndexDSN != "" { log.Info("indexarr: release index on Postgres; --index-path ignored"); store, closer, err = relindex.OpenPostgres(ctx, o.IndexDSN) } else { relindex.Open(ctx, o.IndexPath) }`. The `IndexReadyChecker` is unchanged.
- [ ] **R1.** Replace `sweepReleaseIndex`'s plain func with a small type `indexSweeper{store}` implementing `Start(ctx) error` and `NeedLeaderElection() bool { return true }`; `mgr.Add` it. Test in `app/indexer/run_test.go` (or the existing registration test): the sweeper is leader-elected.
- [ ] **Makefile.** `PG_ASSETS ?= $(GOBIN)/pg-assets`; target `pg-assets: ## Download the embedded Postgres binary for pkg/relindex's Postgres tests.` running `go run ./hack/pgassets $(PG_ASSETS)` (write `hack/pgassets/main.go`: start-and-stop an embedded instance with `CachePath(os.Args[1])`); `test:` and `test-race:` depend on `pg-assets` and export `CLUSTARR_PG_ASSETS="$(PG_ASSETS)"`. `test-unit` does not.
- [ ] Tests: `TestEveryManagerRunnableIsRegistered` still passes (the sweeper is a manager runnable now); a flag test in `cmd/clustarr` proves `--index-dsn` reaches `indexer.Options.IndexDSN` for both `indexarr` and `all` (mirror `ui_options_wiring_test.go`'s approach). `make test` green. Commit — `feat(indexarr): --index-dsn selects the Postgres release index; retention sweep is leader-only; make pg-assets`.

### A3 — chart, kustomize, nack removal, ADR-0010

**Files:** `charts/clustarr/Chart.yaml`, `Chart.lock`, `values.yaml`, `README.md`, `templates/deployments.yaml:69-110`, new `templates/postgres-cluster.yaml`, `charts/clustarr/charts/` (delete `nack-0.35.0.tgz`, add the `cloudnative-pg` tarball via `helm dependency build`); `config/manager/indexarr.yaml`, new `config/postgres/{kustomization.yaml,cluster.yaml,indexarr-patch.yaml}`, `config/README.md`; `cmd/clustarr/deploy_parity_test.go`, `cmd/clustarr/storage_security_test.go`, `cmd/clustarr/chart_images_test.go` (whichever names nack or the index PVC); `docs/adr/0010-release-index-engine-selectable.md`, `docs/adr/README.md`, `docs/adr/0003-*.md` status line; CLAUDE.md fresh-clone gotcha (the tarball list).

- [ ] **Chart.** Remove the `nack` dependency and its `values.yaml` block and README rows. Add dependency `cloudnative-pg` (repo `https://cloudnative-pg.github.io/charts`, pin the current release — `helm search repo cloudnative-pg/cloudnative-pg` after `helm repo add`), condition `cloudnative-pg.enabled`, default `false`. Values per §A.4 under `postgres:`. `postgres-cluster.yaml` per §A.4 and R2: `apiVersion: postgresql.cnpg.io/v1`, `kind: Cluster`, `metadata.name: {{ include "clustarr.fullname" . }}-postgres`, `annotations: {helm.sh/hook: post-install,post-upgrade, helm.sh/hook-weight: "0"}`, `spec.instances`, `spec.storage.{size,storageClass}`, `spec.bootstrap.initdb.{database: clustarr, owner: clustarr}`. indexarr Deployment: when `postgres.enabled`, env `CLUSTARR_INDEX_DSN` `valueFrom.secretKeyRef{name: (default (printf "%s-postgres-app" fullname) .Values.postgres.existingSecret), key: uri}`, drop the `clustarr-index` volume/mount/PVC, no `Recreate`, `replicas: .Values.indexarr.replicas`. README: a "Postgres release index" section with the `--wait` sentence and the public-exposure note is not here (that is D2).
- [ ] **Kustomize.** `config/postgres/kustomization.yaml` as a `Component` with `cluster.yaml` (same Cluster, name `clustarr-postgres`) and a strategic-merge patch on `Deployment clustarr-indexarr` adding the env from Secret `clustarr-postgres-app` and removing the volume and strategy (`$patch: delete` on the volume entry, `strategy: {type: RollingUpdate}`). `config/README.md`: "install the CloudNativePG operator first, then `kustomize build config/default | kubectl apply --server-side -f -` with the component enabled".
- [ ] **Parity tests.** Extend `TestChartAndKustomizeAgreePerComponent` (or add a sibling) with a postgres-enabled case: `helm template --set postgres.enabled=true` versus `kustomize build` of an overlay including the component; both render the same indexarr env, no PVC, no Recreate, and the same `Cluster` spec. Assert the chart no longer references nack anywhere (`grep -r nack charts/` empty except Chart.lock history is regenerated).
- [ ] `helm dependency build charts/clustarr` (network, once, by you — record the versions in the commit message). ADR-0010 per §A.6; 0003's status → "Superseded by ADR-0010, 2026-09-24"; README index row. CLAUDE.md: the fresh-clone gotcha now lists `nats`, `keda`, `cloudnative-pg`.
- [ ] `make test` and `make lint` green. Commit — `feat(chart): CloudNativePG dependency and Cluster hook; indexarr on Postgres without a volume; drop nack; kustomize postgres component; ADR-0010`.

---

## Part B — artwork store (parallel with A)

### B1 — `events.ObjectStore` on both buses

**Files:** `pkg/events/membus/objectstore.go` (new), `pkg/events/natsbus/objectstore.go` (new), `pkg/events/natsbus/objectstore_contract_test.go` (new, real embedded server like `kvkey_contract_test.go`), `pkg/events/contracttest/contracttest.go` (add the object-store section), `pkg/events/topology_nats.go` (`EnsureTopology` creates or updates object stores with `js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket, Description, Storage, MaxBytes, Replicas})`), `pkg/events/membus/membus.go` (`Ensure` registers stores; remove the W0-3 stub).

**Interfaces:** consumes W0-3's `events.ObjectStore`, `ObjectInfo`, `ErrObjectNotFound`, `ObjectStoreSpec`. Produces working `Bus.ObjectStore(bucket)` on both buses.

- [ ] **membus.** A map `name → {data []byte, headers, modTime}` under the bus mutex; `Digest` = hex SHA-256 computed on `Put`; `Get` returns `io.NopCloser(bytes.NewReader)`; `List` filters by prefix, sorted by name; `Delete` of a missing name is `ErrObjectNotFound`; a `Put` to an unknown bucket (not in the ensured topology) errors — mirror how membus treats an unknown KV bucket.
- [ ] **natsbus.** Wrap `jetstream.ObjectStore`: `Put` uses `PutBytes`/`Put` with `jetstream.ObjectMeta{Name, Headers: nats.Header}`; `Get` returns `ObjectResult` as the `io.ReadCloser`; `Info` maps `ObjectInfo.Digest` — NATS gives `"SHA-256=<base64url>"`; decode base64url, re-encode hex; a malformed digest is an error, not an empty string; `ErrObjectNotFound` from `jetstream.ErrObjectNotFound`; `List` via `store.List(ctx)` filtered by prefix (JetStream has no prefix listing; document that a 5 GiB bucket lists in one call and that the reaper is the only lister).
- [ ] **Contract suite** (both buses): put/get round trip with headers; digest is 64 hex chars and equals a locally computed SHA-256; overwrite changes digest and `ModTime`; `Info` on missing is `ErrObjectNotFound`; `Delete` then `Get` is `ErrObjectNotFound`; `List("movie/")` returns only that prefix in name order. **Real-server test:** a 20 MiB random object round-trips byte-identical through natsbus with the server's default `max_payload` (proves chunking). Commit — `feat(events): JetStream object store behind Bus.ObjectStore on natsbus and membus, contract-tested`.

### B2 — the gateway fetches originals; reaper; re-fetch trigger

**Files:** create `app/catalog/metadata/artwork/fetcher.go`, `fetcher_test.go`, `reaper.go`, `reaper_test.go`, `handler.go` (the `ConsumerCatalogArtworkFetch` handler), `drift.go`, `drift_test.go`; modify `app/catalog/metadata/worker.go:150-200` (call the fetcher after a successful fetch and status apply; publish `RenderOverlay`), `app/catalog/metadata/patch.go` (each `build<Kind>MetadataAC` caller's status apply gains `WithArtwork(...)` from the fetcher's result — the gateway's apply for a kind is one complete declaration, so `status.artwork` is rendered in the same apply as `status.metadata`), `app/catalog/run.go:713+` (`setupMetadataGateway` registers the fetch consumer and the reaper), the eight item reconcilers (`app/catalog/controller/{movie,series,artist,album,author,book,audiobook,comic}`: call `artwork.Drift` and publish one `ArtworkFetchTask`), `config/rbac` via markers (`events` create is already granted; add `get,list` on nothing new — check).

**Interfaces:** consumes B1's `ObjectStore`, W0's types and subjects. Produces:

```go
// app/catalog/metadata/artwork
type Fetcher struct { Store events.ObjectStore; HTTP *http.Client; Limiter func(host string) *rate.Limiter; Recorder events.EventRecorder; Clock clockwork.Clock }
const MaxImageBytes = 20 << 20
// Sync fetches every image type whose resolved source URL differs from the current entry or whose object is missing.
// It returns the complete new status.artwork list (previous entries kept where a fetch failed, R3) and whether the poster's digest changed.
func (f *Fetcher) Sync(ctx context.Context, obj client.Object, kind commonv1.MediaKind, overrides []catalogv1alpha1.ArtworkOverride, images []catalogv1alpha1.Image, current []catalogv1alpha1.ArtworkEntry) (entries []catalogv1alpha1.ArtworkEntry, posterChanged bool)
func ResolveSources(overrides []catalogv1alpha1.ArtworkOverride, images []catalogv1alpha1.Image) map[catalogv1alpha1.ImageType]source // custom wins
func Drift(overrides []catalogv1alpha1.ArtworkOverride, entries []catalogv1alpha1.ArtworkEntry) (specHash string, drifted bool) // §B.7
type Reaper struct { Store events.ObjectStore; Client client.Reader; Interval, Grace time.Duration } // NeedLeaderElection() true; §B.5
```

- [ ] **Fetcher tests first** (httptest servers, membus): custom URL beats provider URL; unchanged source URL and present object → no fetch (count requests); missing object with unchanged URL → fetch; `text/html` 200 (Review Focus 1) → entry unchanged, object unchanged, one `ArtworkFetchFailed` Event with reason `not an image`; body over `MaxImageBytes` → `ErrResponseTooLarge`, same handling; 9000-pixel-wide PNG → rejected `too large`; headers on the stored object are exactly `Content-Type`, `Clustarr-Source`, `Clustarr-Source-URL`; a webp with `image/webp` decodes (`golang.org/x/image/webp` registered by blank import); `posterChanged` true only when the poster digest differs from the previous entry's.
- [ ] **Worker wiring.** In `worker.go`, after the metadata apply succeeds: `entries, posterChanged := fetcher.Sync(...)`; the apply config for the kind is built once with both `status.metadata` and `status.artwork` (re-`Get` the object before the apply — the fetch is slow work; CLAUDE.md's lost-update rule). When `posterChanged`, publish `schema.RenderOverlayTask{MediaRef, Reason: "original"}` on `WorkArtworkRenderSubject(mediaKey)` with `MsgIDForRenderOverlay(uid, posterDigest)`. The `ConsumerCatalogArtworkFetch` handler does the same `Sync` + apply for an `ArtworkFetchTask` without re-fetching metadata.
- [ ] **Drift + reconcilers.** `Drift` hashes `spec.artwork` (sorted by type) and reports drift when any override's URL ≠ the entry's `sourceURL`, or an entry says `custom` but no override of that type exists. Each of the eight reconcilers publishes `ArtworkFetchTask` under `MsgIDForArtworkFetch(uid, specHash)` when drifted (one line each, through one shared helper `artwork.PublishFetch(ctx, bus, obj, kind)`). envtest per kind is overkill: test the helper once with membus and prove each reconciler calls it by a table over the eight kinds that creates the object with an override and asserts one message on the subject.
- [ ] **Release regression (mandatory).** envtest: a Movie that already has `status.metadata` and `status.artwork`; run a metadata refresh whose fetch fails (provider 500) — `status.artwork` still has its entries and `status.metadata` is intact; and `managedFields` shows `status.artwork` owned by the gateway's manager for Movie (`ManagerCatalogarrMetadata` or whichever manager writes that kind's `status.metadata` — read `patch.go`/`worker.go`, do not assume) and nothing under `status.overlay`.
- [ ] **Reaper tests:** an object under `movie/<uid>` with no Movie of that UID, older than `Grace`, is deleted; younger is kept; both variants are deleted; an object under a live UID is kept. Register in `setupMetadataGateway`; registration test sees it. Commit — `feat(catalogarr): metadata gateway stores artwork originals, records status.artwork, reaps orphans; spec.artwork drift re-fetches`.

### B3 — the ui serves art; hotlinks removed; ADR-0011

**Files:** `cmd/clustarr/services.go` (ui command) and `all.go` (connect the bus, pass `ui.Options.Artwork`), `ui/server.go` (`Options.Artwork events.ObjectStore`), `ui/routes.go`, new `ui/art.go`, `ui/art_test.go`, `ui/projection/library.go:76-79,195-235` (`Poster` becomes the `/art` URL), `ui/detail.go:155` (`imageOf` → art URL), `ui/views/*.templ` (drop `referrerpolicy` hotlinks; `make templ`), `ui/guard_test.go:61-90`, `cmd/clustarr/ui_options_wiring_test.go`, `docs/adr/0011-artwork-in-jetstream-object-store.md`, `docs/adr/README.md`.

**Interfaces:** consumes B1. Produces `GET /art/{kind}/{uid}/{type}`; `projection.ArtURL(kind commonv1.MediaKind, uid types.UID, t catalogv1alpha1.ImageType, digest string) string` → `/art/<kind>/<uid>/<type>?v=<digest>`.

- [ ] **Route.** `handleArt`: validate `kind` ∈ the nine `MediaKind`s and `type` ∈ `ImageType`; try `ArtworkKey(kind, uid, type, ArtworkVariantOverlay)` then `…Original` via `Store.Get`; 404 on both missing; copy the body with `Content-Type` from headers, `ETag: "<digest>"`, `Cache-Control: public, max-age=31536000, immutable` iff `r.URL.Query().Get("v") == digest` else `no-cache`; `If-None-Match` equal → 304 with no body; `Options.Artwork == nil` → 404. Tests with membus: overlay preferred; original fallback; 404; ETag/304; immutable only on matching `v`; an unknown kind is 404 not 500.
- [ ] **Projection.** `LibraryItem.Poster` = `ArtURL(kind, uid, poster, overlayDigest or originalDigest)` when `status.overlay` or a poster `status.artwork` entry exists, else `""`; templates render the placeholder on `""` (they already do). Detail/series headers use the same. Remove every provider-URL `<img src>` and `referrerpolicy` attribute; `ui/library_card_test.go` asserts no `http://`/`https://` image src remains in rendered cards.
- [ ] **Wiring.** In `cmd/clustarr` the ui command calls `k8s.ConnectBus(natsURL, "ui", k8s.WithBusHooks(obs.BusHooks()))` (the AST guard for hooks applies — see `run.go` patterns), passes `bus.ObjectStore(events.BucketArtwork)`; `clustarr all` passes its shared bus. Extend `TestBothUICommandsWireEveryUIOption` (it fails on any unset option — set `Artwork`). `ui/guard_test.go`: add `Put`, `PutBytes`, `UpdateMeta`, `Seal`, `AddLink`, `Purge` to `neverWriteSelectors`; a test proves a file calling `store.Put` under `ui/` is rejected.
- [ ] ADR-0011 per §B.9; amend `docs/superpowers/specs/2026-09-23-library-page-design.md` decision 1 with a dated note. `make test`, `make lint`, `make templ`, `make css` if classes changed. Commit — `feat(ui): serve artwork from the object store at /art with digest ETags; drop provider hotlinks; ADR-0011`.

---

## Part C — ratings and overlays (after W0; C1 and C2 parallel; C3 after both and B2)

### C1 — ratings providers with capabilities and fallthrough; `firstAired`

**Files:** `pkg/metadata/provider.go` (`RatingsProvider`), `pkg/metadata/registry.go` (`Ratings []RatingsProvider`), `pkg/metadata/model.go` (constants `RatingSourceIMDb…` as strings matching the CRD enum), `pkg/metadata/clients/tmdb/tmdb.go` (implements `RatingsProvider` for `tmdb`; series `first_air_date` → `Series.FirstAired` if not already), new `pkg/metadata/clients/mdblist/{mdblist.go,mdblist_test.go}`, `pkg/metadata/clients/omdb/{omdb.go,omdb_test.go}`, fixtures `test/data/metadata/mdblist/*.json`, `test/data/metadata/omdb/*.json`, `docs/research/ratings-providers.md`; `app/catalog/metadata/enrich.go` (`enrichRatings`), `app/catalog/metadata/patch.go:69-190` (`WithRatings`, `WithFirstAired`), `app/catalog/controller/metadataprovider/registry.go:138-260` and `prober.go` (build and probe the two clients), `charts/clustarr/README.md` provider table.

**Interfaces:**

```go
// pkg/metadata
type RatingsProvider interface { Provider; RatingSources(kind commonv1.MediaKind) []string; Ratings(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) (Ratings, error) }
// mdblist.New(Config{APIKey string; BaseURL string; HTTP *http.Client; Limiter *rate.Limiter}) (*Client, error) — Limiter nil is an error, never a default
// omdb.New(Config{...same}) (*Client, error)
// app/catalog/metadata: func enrichRatings(ctx, reg *pkgmetadata.Registry, kind, ids pkgmetadata.ExternalIDs, prior []catalogv1alpha1.Rating) []catalogv1alpha1.Rating
```

- [ ] **R5 first.** If `MDBLIST_API_KEY` or `OMDB_API_KEY` is set, record with `curl` into the fixture dirs: MDBList `GET {base}/tmdb/movie/{tmdbID}?apikey=…` and `/tmdb/show/{id}` for one movie and one show, plus one unknown id; OMDb `GET {base}/?i=tt…&apikey=…` for one movie, one series and one unknown id. **Strip the key from the fixture names and bodies.** Write `docs/research/ratings-providers.md` from the recorded bodies (field names, units, "unrated" encoding, quota headers). With neither key: implement TMDB only, write the note's skeleton with UNRECORDED markers, and say so in the report.
- [ ] **Clients** (only for recorded shapes): parse into `metadata.Ratings` keyed by the enum strings; a value and votes both zero is omitted (Review Focus 5); `ReadBody` cap 1 MiB; sentinel `metadata.ErrNotFound` on the unknown-id fixture; `Probe` hits the cheapest endpoint. Table tests over the fixtures through a `httptest` server, plus a test that the key never appears in any error string.
- [ ] **Fallthrough.** `enrichRatings` per §C.2: providers in `Registry.Ratings` order (priority-sorted like `Artwork`); `need := declared ∩ unfilled`; skip when empty; one call; on error `log.Warn` and continue; copy only `need`; unfilled at the end are carried from `prior`. Tests with fake providers: higher priority wins; a failing provider blanks nothing; a provider not declaring a source is never asked for it; carry-forward on total failure; call counts prove one call per provider.
- [ ] **Wiring.** `worker.go` calls `enrichRatings` for movie and series after `enrich`, passing the target's current `status.metadata.ratings`; `patch.go` renders `WithRatings` (sorted by source) and `WithFirstAired`. Registry builds `mdblist`/`omdb` from `MetadataProviderSpec` (secret key `apiKey`, `BaseURL` override, per-host limiter from the controller as for the others); the prober reports `Ready`. envtest: a `MetadataProvider` of type `mdblist` with a stub `BaseURL` reaches `Ready=True`. Commit — `feat(metadata): ratings from tmdb/mdblist/omdb with declared sources and fallthrough; series firstAired`.

### C2 — `pkg/overlay`

**Files:** create `pkg/overlay/{overlay.go,template.go,badge.go,format.go,logos.go,render_test.go,format_test.go}`, `pkg/overlay/logos/{metacritic,imdb,tmdb,rt-critic,rt-audience,trakt,letterboxd}.png` + `NOTICE`, `pkg/overlay/testdata/` synthetic posters, goldens under `test/data/overlay/*.png`.

**Interfaces (§C.5):** `Badge{Source string; Score string; Logo image.Image}`, `Template{Corner Corner; WidthPct, RadiusPct, PaddingPct, LogoPct, ScorePct, OpacityPct int}`, `DefaultTemplate()`, `Render(base image.Image, badges []Badge, t Template) (*image.NRGBA, error)`, `FormatScore(source string, centis int32) (string, bool)` (false when the value is zero), `TemplateSpec(spec catalogv1alpha1.OverlayProfileSpec) Template` (uses the `*OrDefault` accessors), `TemplateHash(t Template) string`, `Logo(source string) (image.Image, bool)`.

- [ ] **Geometry.** Box width `W = base.width * WidthPct/100`; corner radius `W * RadiusPct/WidthPct`… — define every dimension as a fraction of poster width in `template.go` with named constants (`defaultWidthPct = 14`, `defaultRadiusPct = 2`, `defaultPaddingPct = 2`, `defaultLogoPct = 60`, `defaultScorePct = 45`, `defaultOpacityPct = 90`) and a test that `DefaultTemplate()` equals them (so a drift is a named failure). The box: outer corner square and flush with the poster edge, three rounded corners (draw with an alpha mask built from `x/image/vector` or a hand-rolled rounded-rect rasteriser — keep it in `overlay.go`), fill `#1F1F1F` at `OpacityPct`. Logo scaled to `LogoPct` of box width preserving aspect, centred horizontally, sitting in the top region; score text in `gobold` sized so its cap height is `ScorePct` of the remaining box height, white, centred. Stacking: badges away from the corner along the vertical edge with `PaddingPct` between. Corner mirroring for the four corners.
- [ ] **Small posters (Review Focus 4):** clamp box to at least 24 px wide and font to at least 8 px; a 60x90 poster renders without panic and the badge stays within bounds (test asserts every opaque badge pixel is inside the image).
- [ ] **Goldens.** Synthetic 400x600 gradient poster; cases: one metacritic badge; four badges stacked; each corner; a PNG-with-alpha base; `-update` flag regenerates. Compare pixel-exact (deterministic: no antialiasing randomness; if the rasteriser is not deterministic across platforms, compare with a small per-pixel tolerance and say so in the test).
- [ ] `FormatScore`: metacritic/RT → `"53"`; imdb/tmdb/trakt/letterboxd → `"7.2"`; zero → `("", false)`. `NOTICE` per R6. Commit — `feat(overlay): pure poster badge renderer with goldens`.

### C3 — `OverlayProfile` controller and the render role

**Files:** create `app/catalog/controller/overlayprofile/{controller.go,controller_test.go,doc.go}`, `app/catalog/worker/artwork/{handler.go,handler_test.go,doc.go}`, `app/catalog/status/artwork.go` (manager field sets: the gateway's `artwork` leaves, the renderer's `overlay` leaves, `PatchOverlay` refusing any other manager); modify `app/catalog/run.go` (`RoleArtwork Role = "artwork"`, `Roles()`, `setupControllers` registers the profile controller, new `setupArtworkWorker`, `RoleAll` includes it), `cmd/clustarr/services.go` role help, `cmd/clustarr/runnable_registration_test.go`, `config/rbac` markers (`overlayprofiles` get/list/watch/update/patch + status; movies/series get/list/watch + status patch for the renderer), chart RBAC sentinels, `config/samples/catalog_v1alpha1_overlayprofile.yaml`.

**Interfaces:** consumes B1, B2 (`Fetcher` writes originals; renderer reads them), C1 (`status.metadata.ratings`), C2. Produces the durable `catalogarr-artwork-render` handler and `status.overlay`.

- [ ] **Controller.** Reconcile: `TemplateSpec` → `TemplateHash` → `status.hash`; list Movies/Series matching `spec.selector` and `spec.kinds`; overlap with any other profile (same rule and `Overlap` condition as `app/squash/controller/transcodeprofile/controller.go:220-240`, lower name wins); `status.selected`; `Ready`. When hash or selection changed since the last observed status (compare against `status.hash` and a `selected` set kept in status? — keep it simple: on every reconcile publish `RenderOverlayTask{Reason: "profile"}` per selected item with `MsgIDForRenderOverlay(uid, hash)`; the dedup window absorbs repeats), one complete `PatchStatus` under `ManagerCatalogarr`. Watches: OverlayProfile, plus Movie/Series label changes via `EnqueueRequestsFromMapFunc` mapping to every profile.
- [ ] **Renderer handler** per §C.6, step for step; `Store.Info` first, render only on digest mismatch; JPEG q90; `Put` with `Content-Type: image/jpeg`, `Clustarr-Source: render`, `Clustarr-Rendered-From: <inputs digest>`; then re-`Get` the item and `PatchOverlay` (complete declaration of `status.overlay`) under `ManagerCatalogarrArtwork`. No profile → delete overlay object if present, `PatchOverlay` with nil. Badge with no rating → omitted; none left → same as no profile.
- [ ] **Tests.** Handler with membus + envtest: renders and stores when inputs differ; skips when equal (no `Put`, count calls); removes on no profile; `managedFields` shows `status.overlay` under `catalogarr-artwork` and `status.artwork` untouched (co-owner-drop variant per CLAUDE.md: apply as the gateway first, then the renderer, then assert the gateway's leaves survive). Controller: overlap loser gets `Overlap=True`; selection count; publishes one task per selected item. Registration test: `catalogarr --role artwork` starts the consumer; `clustarr all` includes it; RBAC envtest under the catalogarr identity can patch `movies/status`.
- [ ] Commit — `feat(catalogarr): OverlayProfile controller and the artwork render role writing overlay posters`.

---

## Part D — Plex Metadata Provider (after B3 and C1)

### D1 — `ui/plex`

**Files:** create `ui/plex/{provider.go,root.go,match.go,metadata.go,images.go,children.go,paging.go,ratingkey.go,mapping.go,*_test.go}`, goldens `ui/plex/testdata/*.json`; modify `ui/server.go` (`Options.Plex *PlexOptions{ExternalURL string}`), `ui/routes.go` (`mux.Handle("/plex/", plex.Handler(...))` when `Options.Plex != nil`), `cmd/clustarr/services.go` + `all.go` (`--plex-provider` default true, `--external-url` env `CLUSTARR_EXTERNAL_URL`), `cmd/clustarr/ui_options_wiring_test.go`, `ui/projection` (expose a lookup by UID and by external id: `Index.ByUID(uid)`, `Index.ByTMDB(kind, id)`, `Index.ByTVDB(id)`, `Index.ByIMDb(kind, id)`, `Index.Episodes(seriesUID)` — read `ui/projection/index.go` and extend).

**Interfaces:** consumes B3's `ArtURL`, C1's ratings and `firstAired`, the projection. Produces the six routes of §D.2 under `/plex/movies` and `/plex/tv`.

- [ ] **Goldens first.** From `docs/research/plex-metadata-provider.md` §"Metadata object" and the example's `test/*.test.ts`, write expected JSON for: root (movies, tv), match by guid, match by title+year (two results ordered exact-year first), movie metadata, show metadata with `includeChildren`, season, episode, images container, children page 2 of 3. Tests build the projection from fixture objects (a Movie with tmdbID, imdb externalID, ratings from all seven sources; a Series with two seasons of three episodes, `firstAired`) and compare `encoding/json`-canonicalised output.
- [ ] **Implementation** per §D.1–D.5. `ratingkey.go`: `Movie/Series/Episode → string(uid)`, `SeasonKey(seriesUID, n) = fmt.Sprintf("%s-s%02d", uid, n)`, parse back with a regexp `^([0-9a-f-]{36})(?:-s(\d{2}))?$`. `match.go`: guid forms `tmdb://`, `tvdb://`, `imdb://` then title (`release.TitleNorm` equality against title + alternate titles), year exact → ±1 → any. `mapping.go`: the §D.5 table; `Rating[]` via R4 (`imdb → imdb://image.rating audience`, `tmdb → themoviedb://image.rating audience`, `rottenTomatoesCritic → rottentomatoes://image.rating.ripe critic`, `rottenTomatoesAudience → rottentomatoes://image.rating.upright audience`; values `centis/100.0` for /10 sources, `centis/1000.0` for the RT pair, formatted with one decimal); omit optional fields with no source. `paging.go`: headers or query params, defaults 0/20, R7; start ≥ total → empty `Metadata` with the true `totalSize` (Review Focus 3). Root without `ExternalURL` → 503 JSON `{"error":"--external-url is required for the Plex provider"}`. Every response `Content-Type: application/json`, `Cache-Control: no-store`.
- [ ] **Wiring.** Flags; `TestBothUICommandsWireEveryUIOption` covers `Plex`; the guard suite passes (`ui/plex` reads only). Startup logs one warning when `--plex-provider` is on without `--external-url`. Commit — `feat(ui): Plex Custom Metadata Provider roots for movies and tv over the projection cache`.

### D2 — e2e scenario 18, exposure notes, ADR-0012

**Files:** `test/e2e/plex_test.go` (new), `test/e2e/helpers_test.go` if a helper is needed, `charts/clustarr/README.md` and `values.yaml` ingress comments, `docs/observability.md` (exposure section), `docs/adr/0012-plex-provider-on-the-ui-service.md`, `docs/adr/README.md`.

- [ ] Scenario 18 (build tag `e2e`, pattern of `test/e2e/ui_test.go`): with the fixture library's Movie and Series reconciled, `GET /plex/movies` → identifier; `POST …/matches` with the fixture's tmdb guid → one result; `GET …/library/metadata/{key}` → title, `thumb` absolute under the ui's external URL; `GET` that `thumb` → 200 `image/*`; `GET /plex/tv/library/metadata/{seriesKey}/children` → seasons; `/grandchildren` with `X-Plex-Container-Size: 1` → `size: 1`, `totalSize` = episodes. **Written, not run** — say so in the report.
- [ ] README: "Plex provider" section — how to add it in Plex (`Settings → Metadata Agents → Add Provider`, the two URLs), that it is unauthenticated and must not be exposed on a public ingress, and `--external-url`. ADR-0012 per §D.7. Commit — `test(e2e): scenario 18 Plex provider; docs: exposure notes and ADR-0012`.

---

## W3 — docs sweep (controller session)

- [ ] Design spec: §4 (the W0-2 fields, OverlayProfile), §6 (catalogarr `artwork` role; ui roots and `--external-url`), §11 (object store bucket; Postgres option; the RWO index PVC is now the SQLite-only shape), §16 (M7 this work); amendment 1 §A3.4 (art served from the store); CLAUDE.md: services table (catalogarr `artwork` role, ui Plex roots), invariants (ui may hold a read-only bus; artwork writers split by variant), gotchas (CNPG hook ordering; NATS object digest is base64url; `make pg-assets`), status paragraph "M7 (done)"; `docs/superpowers/plans/2026-09-18-remaining-work.md` carried items closed or added (file-fact overlays, upload path, Plex paging base, music providers). Commit — `docs: M7 recorded in the spec, amendment, CLAUDE.md and the remaining-work plan`.

---

## Self-review (done 2026-09-24)

- **Spec coverage:** A.1–A.6 → A1–A3; B.1–B.9 → W0-3, B1–B3; C.1–C.6 → W0-2, C1–C3; D.1–D.7 → D1–D2; E → W0-1, W3 and each task's registration/guard steps. Gap found and fixed: §B.4's `RenderOverlay` after a poster change lives in B2's worker wiring; §C.4's re-render on profile change lives in C3's controller.
- **Names:** `events.ArtworkKey`, `ArtworkVariantOriginal/Overlay`, `WorkArtworkFetchSubject/RenderSubject`, `ConsumerCatalogArtworkFetch/Render`, `schema.ArtworkFetchTask/RenderOverlayTask`, `k8s.ManagerCatalogarrArtwork`, `catalogv1alpha1.ArtworkOverride/ArtworkEntry/OverlayEntry/Rating/RatingSource/OverlayProfile*`, `relindex.OpenPostgres`, `storetest.Run`, `overlay.Render/FormatScore/TemplateSpec/TemplateHash`, `ui.Options.Artwork/Plex`, `projection.ArtURL` — used identically across tasks.
- **Review Focus:** each of the five has its test named in A1, B2, C1/C2, D1.
