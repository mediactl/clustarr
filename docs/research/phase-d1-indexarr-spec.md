# Phase D1 — `indexarr` (M2): exhaustive source reference

Research output only. No code was modified. Every claim below carries a
`file:line` citation into the repo at commit `dc36655` (branch `main`).

Sources read in full or in the cited ranges:

- `docs/superpowers/specs/2026-09-18-clustarr-design.md` (the design of record)
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` (**wins on conflict**)
- `api/index/v1alpha1/{indexer,indexerdefinition,indexerproxy}_types.go`
- `pkg/events/{subjects,topology,bus,envelope}.go`, `pkg/events/schema/index.go`
- `pkg/events/natsbus/natsbus.go`
- `app/catalog/worker/rssmatcher/*`, `app/catalog/worker/search/{rpc,request}.go`
- `docs/research/indexers.md`, `docs/research/queue.md`, `docs/adr/0003-*`
- `app/indexer/run.go`, `config/manager/indexarr.yaml`, `config/default/pvc.yaml`,
  `charts/clustarr/values.yaml`, `config/rbac/role.yaml`, `go.mod`
- `docs/superpowers/plans/2026-09-18-remaining-work.md`

---

## 0. TL;DR for a plan author

- `indexarr` is **exactly one replica**, `strategy: Recreate`, RWO PVC
  `clustarr-index`, **one role** (`--role all`), **leader election forbidden**.
- The three RPC verbs, their payload structs and their subjects **already exist
  and are already called**. Nothing new needs inventing on the wire.
- The whole `pkg/events` topology for indexarr (stream, two durable consumers,
  two KV buckets, every subject builder) **is already shipped**. Build against
  it; do not re-declare it.
- **No SQLite driver is in `go.mod`.** `modernc.org/sqlite` must be added, once,
  serially.
- Eight concrete contradictions/gaps are listed in §12. Read that section first.

---

## 1. Service identity, topology and process shape

### 1.1 Spec §3 process topology (design.md:62)

> | `indexarr` | Indexer/IndexerDefinition/IndexerProxy controllers, search RPC, RSS worker, SQLite release index, Torznab facade | **exactly 1**, strategy `Recreate`, RWO PVC `clustarr-index` |

### 1.2 Spec §12 (design.md:800), verbatim

> Controllers 1 replica each, leader-elected (catalogarr workers scale
> horizontally; `catalogarr-metadata` and `indexarr` pinned to 1).

### 1.3 Spec §2 naming (design.md:38, :40, :41, :42)

- Service dir / subcommand: `app/indexer/`
- Image: `ghcr.io/mediactl/clustarr` — **"distroless static; indexarr"**
  (design.md:40). This is the ADR-0003 constraint on the SQLite driver.
- LeaderElectionID: `<service>.clustarr.io` → `indexarr.clustarr.io`
- Field manager (SSA): **`indexarr`** — one name, no `indexarr-worker`
  (design.md:42; enumerated and enforced at `pkg/k8s/fieldmanager.go:154`,
  `FieldManagers()` at `:145`, `Valid()` at `:168`). See §12.6.

### 1.4 Already-shipped skeleton — `app/indexer/run.go`

| Symbol | Value | Line |
|---|---|---|
| `ServiceName` | `"indexarr"` | `app/indexer/run.go:45` |
| `LeaderElectionID` | `ServiceName + ".clustarr.io"` | `:48` |
| `DefaultIndexPath` | `"/index/releases.db"` | `:52` |
| `DefaultFacadeBindAddress` | `":9696"` | `:56` |
| `Role` / `RoleAll` | `"all"` — the only role | `:62`, `:68` |
| `Roles()` | `[]Role{RoleAll}` | `:72` |
| `Role.RunsControllers()` | `r == RoleAll` | `:81` |
| `Role.RunsWorkers()` | `r == RoleAll` | `:84` |
| `Options` | embeds `k8s.Options`; `Role`, `IndexPath`, `FacadeBindAddress`, `Logging`, `Tracing` | `:87-107` |
| `DefaultOptions()` | `Role: RoleAll`, `IndexPath: DefaultIndexPath`, `FacadeBindAddress: DefaultFacadeBindAddress` | `:110` |

`Options.Validate()` (`:120-137`) enforces, verbatim:

- `indexarr: unknown --role %q, want one of %v` (`:122`)
- `indexarr: --index-path is required` (`:126`)
- `indexarr: --nats-url is required; the search RPC and the release firehose both use the bus` (`:128`)
- `indexarr: --leader-elect is not supported; §3 pins indexarr to exactly one replica` (`:134`)

`Run()` (`:146-206`) already does: `obs.Bootstrap` → `ctrl.GetConfig` →
`ctrl.NewManager(cfg, o.ManagerOptions())` → `k8s.ConnectBus(o.NATSURL,
ServiceName, k8s.WithBusHooks(obs.BusHooks()))` → `k8s.EnsureTopology(ctx, bus,
o.BusTopology())` → `k8s.AddProbes(mgr, {"jetstream": k8s.BusReadyChecker(nc,
bus)})` → `setupControllers` → `setupWorkers` → `mgr.Start`.

`ManagerOptions()` calls `o.Options.ManagerOptions(LeaderElectionID, false)`
(`:142`) — the `false` is "not leader elected".

The two registration points are **empty**:

```go
// app/indexer/run.go:216
func setupControllers(mgr ctrl.Manager, o Options) error { _, _ = mgr, o; return nil }
// app/indexer/run.go:230
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error { _, _, _ = mgr, bus, o; return nil }
```

Their TODOs are the M2 work list, verbatim (`app/indexer/run.go:212-215`,
`:225-229`):

> TODO(M2): indexer -- validate the definition, probe caps, test login, own the
> session Secret, schedule RSS with WithScheduleAt, and mirror KV health into
> status with Prowlarr's escalation table. (§6.2, §16 M2)
>
> TODO(M6): indexerdefinition (schema validation and sha256) and indexerproxy
> (http/socks and the FlareSolverr client). (§6.2, §16 M6)
>
> TODO(M2): serve rpc.indexarr.search and rpc.indexarr.download; run the RSS
> worker publishing new rows to CLUSTARR_RELEASES; open the SQLite FTS5 store at
> o.IndexPath and run its 10-minute expiry sweep. (§6.2, §16 M2)
>
> TODO(M6): the Cardigann engine and the Torznab facade on o.FacadeBindAddress.
> (§6.2, §16 M6)

And the readiness TODO (`app/indexer/run.go:185-187`):

> TODO(M2): add a "releaseindex" readiness check tied to the SQLite handle being
> open, per §13. Until the store exists the JetStream ping is the whole
> readiness gate.

---

## 2. Spec §6.2 — the single authoritative paragraph

`docs/superpowers/specs/2026-09-18-clustarr-design.md:620-621`, reproduced
**verbatim** (this is the paragraph the plan argues from):

> ### 6.2 indexarr (`app/indexer/`, `clustarr indexarr --role all`, exactly one replica)
>
> **Controllers:** `indexer` (validate definition/generic, probe caps, login
> test, owned session Secret, schedule RSS via `WithScheduleAt` on
> `work.indexarr.rss`, mirror KV health/limits into status with Prowlarr's
> escalation table `[0,60,300,900,1800,3600,10800,21600,43200,86400]s` + 15-min
> startup grace), `indexerdefinition` (schema validation, sha256),
> `indexerproxy`. **Search service:** NATS micro on `rpc.indexarr.search`:
> select Indexers (enabled, `EnableAutomaticSearch` or interactive per request,
> caps support the mode/categories, not `DisabledUntil`, under QueryLimit),
> errgroup with per-indexer `context.WithTimeout(spec.timeout)`, Cardigann
> engine or generic Torznab/Newznab client (id params gated by caps; fallback
> `t=search&q=`), parse titles with `pkg/release`, upsert every hit into SQLite,
> dedup by infohash then (indexer, guid) keeping best (priority, seeders) with
> `alsoOn` provenance, cap 500, record outcomes and
> `RecordSuccess/RecordFailure`. `rpc.indexarr.download` resolves links with the
> indexer session. **RSS worker:** `t=search` empty q per Indexer, insert new
> rows (UNIQUE(indexer, guid)), publish each new row to `CLUSTARR_RELEASES`.
> **Release index:** `app/indexer/releaseindex.Store` interface; SQLite
> (modernc.org/sqlite, WAL, FTS5 on `title_norm, grp`) on PVC `clustarr-index`
> (RWO, 5Gi), columns from `pkg/release` (source, resolution, modifier, codec,
> hdr, audio, languages, group, year, season, episode, ids), `expires_at` sweep
> every 10 min (72h), readiness tied to the DB open; Postgres FTS is a Store
> swap. **Facade:** `/{indexer}/api`, `/{indexer}/download`, `/search/api`
> (aggregate Torznab, Jackett filter grammar deferred).

Decomposed into obligations:

**Controller `indexer` (M2):**
1. validate `definition` / `definitionRef` / `generic`
2. probe caps
3. login test
4. owned session Secret
5. schedule RSS via `WithScheduleAt` on `work.indexarr.rss`
6. mirror KV health/limits into status with Prowlarr's escalation table
   `[0,60,300,900,1800,3600,10800,21600,43200,86400]` seconds
7. 15-minute startup grace

**Controllers `indexerdefinition`, `indexerproxy`: deferred to M6** per §16
(design.md:844), but the CRDs exist now.

**Search service (M2), verbatim selection predicate** — an Indexer is eligible iff:
- `enabled`
- `EnableAutomaticSearch`, **or** `EnableInteractiveSearch` when the request is
  interactive (`SearchRequest.UserInvoked`)
- caps support the mode/categories
- not within `DisabledUntil`
- under QueryLimit

**Search fan-out:** `errgroup` + per-indexer `context.WithTimeout(spec.timeout)`.

**Merge:** dedup by **infohash first**, then `(indexer, guid)`, keeping the best
`(priority, seeders)`, retaining `alsoOn` provenance; cap 500; record outcomes;
`RecordSuccess` / `RecordFailure`.

**RSS worker (M2):** `t=search` with an empty `q` per Indexer; insert new rows
with `UNIQUE(indexer, guid)`; publish each new row to `CLUSTARR_RELEASES`.

**Release index (M2):** interface named **`app/indexer/releaseindex.Store`**;
SQLite `modernc.org/sqlite`, WAL, **FTS5 on `title_norm, grp`**; PVC
`clustarr-index` RWO 5Gi; columns from `pkg/release`; `expires_at` sweep every
**10 min**, retention **72h**; readiness tied to the DB open.

**Facade (M6):** `/{indexer}/api`, `/{indexer}/download`, `/search/api`.

---

## 3. Spec §16 M2 entry, verbatim

`design.md:840`:

> - **M2 Indexers (generic):** pkg/torznab + pkg/newznab; Indexer controller
>   (generic Newznab/Torznab incl. Prowlarr/Jackett), caps/health/backoff/limits,
>   `rpc.indexarr.search|download|query`, RSS worker, SQLite FTS5 store,
>   `CLUSTARR_RELEASES` publishing.

`pkg/torznab` and `pkg/newznab` **already exist** (Phase B; see §9). M2's
remaining scope is the controller, the three RPCs, the RSS worker, the store and
the publishing.

M6 (`design.md:844`) keeps for later: `pkg/cardigann` wiring + `IndexerDefinition`
+ `IndexerProxy` (http/socks; FlareSolverr client) + the Torznab facade.

§16's must-fix map (`design.md:846`) names indexarr twice:
`indexarr single writer §3/§6.2`, `rpc.indexarr.download`, `single-reply RPC`.

---

## 4. The CRD contract — reproduced field by field

### 4.1 `api/index/v1alpha1/indexer_types.go`

#### Condition type constants (`:28-37`)

```go
IndexerConditionReady         = "Ready"          // True when the indexer is configured, reachable and usable.
IndexerConditionAuthenticated = "Authenticated"  // True when the indexer accepted the configured credentials.
IndexerConditionHealthy       = "Healthy"        // True when recent requests to the indexer succeeded.
IndexerConditionRateLimited   = "RateLimited"    // True while the indexer's query or grab limit is exhausted.
```

#### `LimitUnit` (`:39-48`)

```go
// +kubebuilder:validation:Enum=day;hour
type LimitUnit string
const (
    LimitUnitDay  LimitUnit = "day"
    LimitUnitHour LimitUnit = "hour"
)
```

#### `GenericNewznab` (`:50-61`)

| Field | Type | JSON | Markers | Doc |
|---|---|---|---|---|
| `Protocol` | `commonv1alpha1.Protocol` | `protocol` | `+required` | "Protocol is the transfer protocol the upstream serves." |
| `APIPath` | `string` | `apiPath,omitempty` | `+optional`, `+kubebuilder:default="/api"` | "APIPath is the path of the Newznab/Torznab API relative to baseURL." |

Struct doc (`:50-51`): "GenericNewznab configures a plain Newznab/Torznab
upstream, including Prowlarr and Jackett, instead of a Cardigann definition."

#### `Limits` (`:63-77`)

| Field | Type | JSON | Markers | Doc |
|---|---|---|---|---|
| `QueryLimit` | `*int32` | `queryLimit,omitempty` | `+optional` | "QueryLimit is the maximum number of search queries per unit." |
| `GrabLimit` | `*int32` | `grabLimit,omitempty` | `+optional` | "GrabLimit is the maximum number of grabs per unit." |
| `Unit` | `LimitUnit` | `unit,omitempty` | `+optional`, `+kubebuilder:default=day` | "Unit is the window the limits apply to." |

#### `IndexerSpec` (`:79-187`)

**Type-level CEL rule (`:81`), verbatim:**

```
+kubebuilder:validation:XValidation:rule="(has(self.definition) ? 1 : 0) + (has(self.definitionRef) ? 1 : 0) + (has(self.generic) ? 1 : 0) == 1",message="exactly one of definition, definitionRef or generic must be set"
```

| Line | Field | Type | JSON tag | Markers | Doc comment |
|---|---|---|---|---|---|
| 85 | `Definition` | `*string` | `definition,omitempty` | `+optional` | Definition is the id of a bundled Cardigann definition, e.g. "1337x". |
| 89 | `DefinitionRef` | `*string` | `definitionRef,omitempty` | `+optional` | DefinitionRef is the name of an IndexerDefinition to use. |
| 93 | `Generic` | `*GenericNewznab` | `generic,omitempty` | `+optional` | Generic configures a plain Newznab/Torznab upstream. |
| 97 | `BaseURL` | `string` | `baseURL` | `+required` | BaseURL is the indexer's base URL. |
| 102 | `Enabled` | `*bool` | `enabled,omitempty` | `+optional`, `default=true` | Enabled turns the indexer on or off without deleting it. |
| 109 | `Priority` | `int32` | `priority,omitempty` | `+optional`, `default=25`, `Minimum=1`, `Maximum=50` | Priority orders indexers when the same release is offered by several; lower wins. |
| 113 | `Settings` | `map[string]string` | `settings,omitempty` | `+optional` | Settings holds the non-secret Cardigann settings for the definition. |
| 118 | `SecretRef` | `*corev1.LocalObjectReference` | `secretRef,omitempty` | `+optional` | SecretRef names a Secret in the same namespace holding secret settings. **Recognised keys: `apikey`, `username`, `password`, `cookie`, `passkey`, `rss_key`.** |
| 123 | `EnableRss` | `*bool` | `enableRss,omitempty` | `+optional`, `default=true` | EnableRss allows the indexer to be polled for new releases. |
| 128 | `EnableAutomaticSearch` | `*bool` | `enableAutomaticSearch,omitempty` | `+optional`, `default=true` | EnableAutomaticSearch allows the indexer to be used by automatic searches. |
| 133 | `EnableInteractiveSearch` | `*bool` | `enableInteractiveSearch,omitempty` | `+optional`, `default=true` | EnableInteractiveSearch allows the indexer to be used by interactive searches. |
| 138 | `RssInterval` | `metav1.Duration` | `rssInterval,omitempty` | `+optional`, `default="15m"` | RssInterval is how often the RSS feed is polled. |
| 142 | `Limits` | `*Limits` | `limits,omitempty` | `+optional` | Limits caps queries and grabs per window. |
| 148 | `RequestDelay` | `metav1.Duration` | `requestDelay,omitempty` | `+optional`, `default="2s"` | RequestDelay is the minimum delay between requests to the indexer. **It is raised to the definition's requestDelay when that is larger.** |
| 153 | `Timeout` | `metav1.Duration` | `timeout,omitempty` | `+optional`, `default="30s"` | Timeout is the per-request HTTP timeout. |
| 157 | `ProxyRef` | `*string` | `proxyRef,omitempty` | `+optional` | ProxyRef names an IndexerProxy in the same namespace to route requests through. |
| 161 | `Categories` | `[]int32` | `categories,omitempty` | `+optional` | Categories are the Newznab category ids to search. |
| 165 | `AnimeCategories` | `[]int32` | `animeCategories,omitempty` | `+optional` | AnimeCategories are the Newznab category ids to search for anime. |
| 169 | `AnimeStandardFormatSearch` | `bool` | `animeStandardFormatSearch,omitempty` | `+optional` | AnimeStandardFormatSearch also searches anime using standard SxxEyy numbering. |
| 174 | `MinimumSeeders` | `int32` | `minimumSeeders,omitempty` | `+optional`, `default=1` | MinimumSeeders is the fewest seeders a torrent release may have to be considered. |
| 178 | `SeedCriteria` | `*commonv1alpha1.SeedCriteria` | `seedCriteria,omitempty` | `+optional` | SeedCriteria overrides the download client's seeding limits for releases from this indexer. |
| 182 | `DownloadClientRef` | `*string` | `downloadClientRef,omitempty` | `+optional` | DownloadClientRef names the DownloadClient to send grabs from this indexer to. |
| 186 | `Tags` | `[]string` | `tags,omitempty` | `+optional` | Tags are free-form labels used to match the indexer to catalog items. |

`commonv1alpha1.SeedCriteria` (`api/common/v1alpha1/download_types.go:27-45`):
`Ratio *resource.Quantity` (`ratio`), `SeedTime *metav1.Duration` (`seedTime`),
`PackSeedTime *metav1.Duration` (`packSeedTime`), `InactiveTime
*metav1.Duration` (`inactiveTime`).

`commonv1alpha1.Protocol` (`api/common/v1alpha1/release_types.go:39-48`):
`+kubebuilder:validation:Enum=torrent;usenet`; `ProtocolTorrent = "torrent"`,
`ProtocolUsenet = "usenet"`.

#### `SubCategory` (`:189-199`)

| Field | Type | JSON | Markers |
|---|---|---|---|
| `ID` | `int32` | `id,omitempty` | `+optional` |
| `Name` | `string` | `name,omitempty` | `+optional` |

#### `Category` (`:201-222`)

Struct doc, verbatim (`:202-208`) — this is load-bearing, do not "fix" it back
to a recursive tree:

> The spec writes Sub as []Category, i.e. an arbitrarily deep tree. A CRD schema
> cannot be recursive: controller-gen truncates the recursion to `items: {}` and
> the apiserver then rejects the CRD with "must not be empty for specified array
> items". The Newznab category tree is exactly two levels deep (a 1000-aligned
> parent and its leaves), so Sub is []SubCategory, which carries the same
> id/name payload with a schema the apiserver accepts.

| Field | Type | JSON | Markers |
|---|---|---|---|
| `ID` | `int32` | `id,omitempty` | `+optional` |
| `Name` | `string` | `name,omitempty` | `+optional` |
| `Sub` | `[]SubCategory` | `sub,omitempty` | `+optional`, **`+kubebuilder:validation:MaxItems=200`** |

#### `Caps` (`:224-247`)

| Field | Type | JSON | Markers | Doc |
|---|---|---|---|---|
| `Modes` | `map[string][]string` | `modes,omitempty` | `+optional` | "Modes maps a search mode (search, tv-search, movie-search, ...) to the query parameters it supports." |
| `Categories` | `[]Category` | `categories,omitempty` | `+optional`, **`MaxItems=200`** | "Categories is the category tree the indexer exposes." |
| `LimitsMax` | `int32` | `limitsMax,omitempty` | `+optional` | "LimitsMax is the maximum page size the indexer accepts." |
| `LimitsDefault` | `int32` | `limitsDefault,omitempty` | `+optional` | "LimitsDefault is the page size the indexer uses when none is requested." |
| `SupportsRawSearch` | `bool` | `supportsRawSearch,omitempty` | `+optional` | "SupportsRawSearch is true when the indexer accepts free-text queries." |

#### `IndexerStatus` (`:249-318`)

| Line | Field | Type | JSON tag | Markers | Doc comment |
|---|---|---|---|---|---|
| 253 | `ObservedGeneration` | `int64` | `observedGeneration,omitempty` | `+optional` | most recent generation observed by the controller |
| 261 | `Conditions` | `[]metav1.Condition` | `conditions,omitempty` + `patchStrategy:"merge" patchMergeKey:"type"` | `+optional`, `+listType=map`, `+listMapKey=type`, `+patchStrategy=merge`, `+patchMergeKey=type` | latest available observations |
| 265 | `Protocol` | `commonv1alpha1.Protocol` | `protocol,omitempty` | `+optional` | Protocol is the transfer protocol resolved from the definition. |
| 269 | `Privacy` | `string` | `privacy,omitempty` | `+optional` | Privacy is the privacy class resolved from the definition. |
| 273 | `Caps` | `*Caps` | `caps,omitempty` | `+optional` | Caps is the capability set last fetched from the indexer. |
| 277 | `EscalationLevel` | `int32` | `escalationLevel,omitempty` | `+optional` | EscalationLevel is the current back-off step after repeated failures. |
| 281 | `DisabledUntil` | `*metav1.Time` | `disabledUntil,omitempty` | `+optional` | DisabledUntil is when the indexer will be retried after back-off. |
| 285 | `InitialFailureAt` | `*metav1.Time` | `initialFailureAt,omitempty` | `+optional` | InitialFailureAt is when the current run of failures began. |
| 289 | `LastFailureAt` | `*metav1.Time` | `lastFailureAt,omitempty` | `+optional` | LastFailureAt is when the most recent failure happened. |
| 293 | `LastFailure` | `string` | `lastFailure,omitempty` | `+optional` | LastFailure describes the most recent failure. |
| 297 | `QueriesInWindow` | `int32` | `queriesInWindow,omitempty` | `+optional` | QueriesInWindow is the number of queries issued in the current limit window. |
| 301 | `GrabsInWindow` | `int32` | `grabsInWindow,omitempty` | `+optional` | GrabsInWindow is the number of grabs issued in the current limit window. |
| 305 | `LastRssAt` | `*metav1.Time` | `lastRssAt,omitempty` | `+optional` | LastRssAt is when the RSS feed was last polled. |
| 309 | `LastRssNewCount` | `int32` | `lastRssNewCount,omitempty` | `+optional` | LastRssNewCount is how many new releases the last RSS poll returned. |
| 313 | `IndexedReleases` | `int64` | `indexedReleases,omitempty` | `+optional` | IndexedReleases is the total number of releases seen from this indexer. |
| 317 | `SessionSecretRef` | `string` | `sessionSecretRef,omitempty` | `+optional` | SessionSecretRef names the Secret holding the indexer's login session. |

#### Object markers (`:320-329`)

```
+kubebuilder:object:root=true
+kubebuilder:subresource:status
+kubebuilder:ac:generate=true
+kubebuilder:resource:scope=Namespaced,shortName=idx,categories=clustarr
+kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.status.protocol`
+kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
+kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
+kubebuilder:printcolumn:name="Healthy",type=string,JSONPath=`.status.conditions[?(@.type=="Healthy")].status`
+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
```

Doc (`:331`): "Indexer is a configured torrent or usenet indexer."

### 4.2 `api/index/v1alpha1/indexerdefinition_types.go`

#### Conditions (`:26-31`)

```go
// IndexerDefinitionConditionValid is True when spec.yaml parses and
// validates against the bundled Cardigann schema.
IndexerDefinitionConditionValid = "Valid"
```

#### `DefinitionType` (`:33-43`)

```go
// +kubebuilder:validation:Enum=public;semiPrivate;private
type DefinitionType string
const (
    DefinitionTypePublic      DefinitionType = "public"
    DefinitionTypeSemiPrivate DefinitionType = "semiPrivate"
    DefinitionTypePrivate     DefinitionType = "private"
)
```

#### `IndexerDefinitionSpec` (`:45-57`)

| Field | Type | JSON | Markers | Doc |
|---|---|---|---|---|
| `YAML` | `string` | `yaml` | `+required`, **`+kubebuilder:validation:MaxLength=1048576`** | "YAML is the full Cardigann definition. It is validated by the controller against the bundled schema.json (v11)." |
| `Replaces` | `*string` | `replaces,omitempty` | `+optional` | "Replaces names the bundled definition id this definition overrides. When unset the definition is added alongside the bundled set." |

#### `CapsSummary` (`:59-70`)

| Field | Type | JSON | Markers |
|---|---|---|---|
| `Modes` | `map[string][]string` | `modes,omitempty` | `+optional` |
| `Categories` | `[]int32` | `categories,omitempty` | `+optional`, **`MaxItems=200`** |

#### `IndexerDefinitionStatus` (`:72-113`)

| Line | Field | Type | JSON | Markers |
|---|---|---|---|---|
| 76 | `ObservedGeneration` | `int64` | `observedGeneration,omitempty` | `+optional` |
| 84 | `Conditions` | `[]metav1.Condition` | `conditions,omitempty` | `+optional`, `listType=map`, `listMapKey=type`, `patchStrategy=merge`, `patchMergeKey=type` |
| 88 | `ID` | `string` | `id,omitempty` | `+optional` — "the Cardigann definition id parsed from spec.yaml" |
| 92 | `Name` | `string` | `name,omitempty` | `+optional` — "the human-readable indexer name parsed from spec.yaml" |
| 96 | `Language` | `string` | `language,omitempty` | `+optional` — "the definition's primary language (e.g. en-US)" |
| 100 | `Type` | `DefinitionType` | `type,omitempty` | `+optional` |
| 104 | `Protocol` | `commonv1alpha1.Protocol` | `protocol,omitempty` | `+optional` |
| 108 | `Sha256` | `string` | `sha256,omitempty` | `+optional` — "the hex digest of spec.yaml as last validated" |
| 112 | `Caps` | `CapsSummary` (**not a pointer**) | `caps,omitempty` | `+optional` |

#### Object markers (`:115-123`)

```
+kubebuilder:object:root=true
+kubebuilder:subresource:status
+kubebuilder:ac:generate=true
+kubebuilder:resource:scope=Cluster,shortName=idxdef,categories=clustarr
+kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
+kubebuilder:printcolumn:name="Protocol",type=string,JSONPath=`.status.protocol`
+kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.status.type`
+kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=="Valid")].status`
+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
```

**Cluster-scoped.** Doc (`:125-126`): "IndexerDefinition is a custom or
overriding Cardigann definition. The bundled definitions ship in the image and
are not represented as objects."

### 4.3 `api/index/v1alpha1/indexerproxy_types.go`

#### Conditions (`:25-29`)

```go
IndexerProxyConditionReady = "Ready" // True when the proxy is reachable.
```

#### `IndexerProxyType` (`:31-42`)

```go
// +kubebuilder:validation:Enum=flaresolverr;http;socks4;socks5
type IndexerProxyType string
const (
    IndexerProxyTypeFlareSolverr IndexerProxyType = "flaresolverr"
    IndexerProxyTypeHTTP         IndexerProxyType = "http"
    IndexerProxyTypeSocks4       IndexerProxyType = "socks4"
    IndexerProxyTypeSocks5       IndexerProxyType = "socks5"
)
```

#### `IndexerProxySpec` (`:44-71`)

| Line | Field | Type | JSON | Markers | Doc |
|---|---|---|---|---|---|
| 48 | `Type` | `IndexerProxyType` | `type` | `+required` | Type is the kind of proxy. |
| 52 | `Host` | `string` | `host` | `+required` | Host is the proxy hostname or IP address. |
| 56 | `Port` | `int32` | `port,omitempty` | `+optional` | Port is the proxy port. |
| 60 | `SecretRef` | `*corev1.LocalObjectReference` | `secretRef,omitempty` | `+optional` | names a Secret in the same namespace holding proxy credentials |
| 65 | `RequestTimeout` | `metav1.Duration` | `requestTimeout,omitempty` | `+optional`, `default="60s"` | how long a request through the proxy may take |
| 70 | `Selector` | `metav1.LabelSelector` (**value, not pointer**) | `selector,omitempty` | `+optional` | "Selector matches the Indexers (by label) that use this proxy. **At most one FlareSolverr proxy may match an Indexer; it is applied last.**" |

#### `IndexerProxyStatus` (`:73-94`)

| Line | Field | Type | JSON | Markers |
|---|---|---|---|---|
| 77 | `ObservedGeneration` | `int64` | `observedGeneration,omitempty` | `+optional` |
| 85 | `Conditions` | `[]metav1.Condition` | `conditions,omitempty` | `+optional`, listType=map/listMapKey=type/patchStrategy=merge/patchMergeKey=type |
| 89 | `LastCheckedAt` | `*metav1.Time` | `lastCheckedAt,omitempty` | `+optional` — when the proxy was last probed |
| 93 | `Version` | `string` | `version,omitempty` | `+optional` — proxy software version reported on the last probe |

#### Object markers (`:96-103`)

```
+kubebuilder:object:root=true
+kubebuilder:subresource:status
+kubebuilder:ac:generate=true
+kubebuilder:resource:scope=Namespaced,shortName=idxproxy,categories=clustarr
+kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
+kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`
+kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
```

### 4.4 Generated apply configurations (already present)

`api/applyconfiguration/index/index/v1alpha1/`:
`indexer.go`, `indexerspec.go`, `indexerstatus.go`, `indexerdefinition.go`,
`indexerdefinitionspec.go`, `indexerdefinitionstatus.go`, `indexerproxy.go`,
`indexerproxyspec.go`, `indexerproxystatus.go`, `caps.go`, `capssummary.go`,
`category.go`, `subcategory.go`, `genericnewznab.go`, `limits.go`.

`IndexerStatusApplyConfiguration` builders (`indexerstatus.go`):
`WithObservedGeneration`, `WithConditions`, `WithProtocol`, `WithPrivacy`,
`WithCaps`, `WithEscalationLevel`, `WithDisabledUntil`, `WithInitialFailureAt`,
`WithLastFailureAt`, `WithLastFailure`, `WithQueriesInWindow`,
`WithGrabsInWindow`, `WithLastRssAt`, `WithLastRssNewCount`,
`WithIndexedReleases`, `WithSessionSecretRef`.

`IndexerDefinitionStatusApplyConfiguration`: `WithObservedGeneration`,
`WithConditions`, `WithID`, `WithName`, `WithLanguage`, `WithType`,
`WithProtocol`, `WithSha256` (`indexerdefinitionstatus.go:123`), `WithCaps`.

`IndexerProxyStatusApplyConfiguration`: `WithObservedGeneration`,
`WithConditions`, `WithLastCheckedAt`, `WithVersion`.

Extractors: `ExtractIndexer`, `ExtractIndexerStatus`, `ExtractIndexerFrom`
(`indexer.go:60,84,90`).

All status writes go through `k8s.PatchStatus` (`pkg/k8s/patch.go:70`) with
manager `k8s.ManagerIndexarr` (`pkg/k8s/fieldmanager.go:120`).

---

## 5. Question 1 — the three RPC verbs: exact payload types

All six structs live in `pkg/events/schema/index.go`. Subjects are constants in
`pkg/events/subjects.go:156-158`; the queue group is
`events.QueueGroupIndexarr = "indexarr"` (`subjects.go:162`).

```go
// pkg/events/subjects.go:155-164
const (
    RPCIndexSearch      = "clustarr.rpc.indexarr.search"
    RPCIndexDownload    = "clustarr.rpc.indexarr.download"
    RPCIndexQuery       = "clustarr.rpc.indexarr.query"
    RPCMetadataLookup   = "clustarr.rpc.catalogarr.metadata.lookup"
    RPCMetadataSearch   = "clustarr.rpc.catalogarr.metadata.search"
    RPCMetadataResolve  = "clustarr.rpc.catalogarr.metadata.resolve"
    QueueGroupIndexarr  = "indexarr"
    QueueGroupCatalogar = "catalogarr"
)
```

Cap constant (`schema/index.go:26-28`):

```go
// MaxSearchReleases caps how many releases a single SearchResponse may carry.
// indexarr truncates beyond this, reporting the truncation per indexer.
const MaxSearchReleases = 500
```

### 5.1 `clustarr.rpc.indexarr.search`

**Request — `schema.SearchRequest`** (`schema/index.go:178-222`), schema string
`"index.SearchRequest.v1"`:

| Line | Field | Go type | JSON tag | Doc comment |
|---|---|---|---|---|
| 182 | `Kind` | `commonv1.MediaKind` | `kind` | Kind is the media kind being searched for. |
| 185 | `Text` | `string` | `text,omitempty` | Text is the free-text query. |
| 188 | `IDs` | `map[string]string` | `ids,omitempty` | IDs are the external IDs to search by, preferred over Text. |
| 191 | `Season` | `*int32` | `season,omitempty` | Season narrows a series search. |
| 194 | `Episode` | `*int32` | `episode,omitempty` | Episode narrows a series search. |
| 197 | `Year` | `int32` | `year,omitempty` | Year narrows a movie search. |
| 200 | `Categories` | `[]int32` | `categories,omitempty` | Categories limits the search to these Newznab category IDs. |
| 204 | `IndexerRefs` | `[]Ref` | `indexerRefs,omitempty` | IndexerRefs limits the search to these indexers. **Empty means every enabled indexer that supports the query.** |
| 207 | `Protocols` | `[]commonv1.Protocol` | `protocols,omitempty` | Protocols limits the search to these transfer protocols. |
| 211 | `Limit` | `int32` | `limit,omitempty` | Limit caps the number of releases in the reply. **It is clamped to MaxSearchReleases.** |
| 215 | `DeadlineMillis` | `int64` | `deadlineMillis,omitempty` | DeadlineMillis is how long the caller will wait. **indexarr replies once at min(deadline, 45s).** |
| 218 | `UserInvoked` | `bool` | `userInvoked,omitempty` | UserInvoked marks an interactive search. |

**Response — `schema.SearchResponse`** (`schema/index.go:224-238`), schema
string `"index.SearchResponse.v1"`:

| Line | Field | Go type | JSON tag | Doc |
|---|---|---|---|---|
| 227 | `Releases` | `[]Release` | `releases,omitempty` | Releases holds the merged results, capped at MaxSearchReleases. |
| 231 | `Outcomes` | `[]SearchOutcome` | `outcomes,omitempty` | Outcomes reports how each indexer fared, **including the ones that were still running when the reply was sent**. |
| 234 | `Truncated` | `bool` | `truncated,omitempty` | Truncated says the result set was cut at MaxSearchReleases. |

**`schema.SearchOutcome`** (`schema/index.go:157-176`) — note it has **no
`Schema()` method**; it is only ever nested:

| Line | Field | Go type | JSON tag | Doc |
|---|---|---|---|---|
| 160 | `IndexerRef` | `Ref` | `indexerRef` | the indexer this outcome is about |
| 163 | `IndexerName` | `string` | `indexerName,omitempty` | the indexer display name |
| 166 | `Status` | `SearchOutcomeStatus` | `status` | how the indexer fared |
| 169 | `Releases` | `int32` | `releases` | how many releases this indexer contributed (**no `omitempty`**) |
| 172 | `ElapsedMillis` | `int64` | `elapsedMillis,omitempty` | how long the indexer took, in milliseconds |
| 175 | `Error` | `string` | `error,omitempty` | the failure message for an error outcome |

**`schema.SearchOutcomeStatus`** (`schema/index.go:137-155`), with the verbatim
doc comment for each value:

```go
type SearchOutcomeStatus string
const (
    // SearchOutcomeOK means the indexer answered within the deadline.
    SearchOutcomeOK SearchOutcomeStatus = "ok"
    // SearchOutcomeTimeout means the indexer was still running when the
    // single reply had to be sent.
    SearchOutcomeTimeout SearchOutcomeStatus = "timeout"
    // SearchOutcomeError means the indexer failed.
    SearchOutcomeError SearchOutcomeStatus = "error"
    // SearchOutcomeSkipped means the indexer was disabled, rate limited or
    // did not support the query.
    SearchOutcomeSkipped SearchOutcomeStatus = "skipped"
)
```

These four map 1:1 onto the CRD enum
`catalogv1alpha1.IndexerOutcomeState` (`api/catalog/v1alpha1/search_types.go:52`,
`+kubebuilder:validation:Enum=ok;timeout;error;skipped`), which the Search CR's
`status.indexerOutcomes` uses (`search_types.go:64-84`: `Name string`, `State`,
`Count int32`, `DurationMs int32`, `Error string`).

> **Narrowing hazard:** `SearchOutcome.ElapsedMillis` is `int64`; the CRD's
> `IndexerOutcome.DurationMs` is `int32`. The catalogarr side must clamp.

### 5.2 `clustarr.rpc.indexarr.download`

**Request — `schema.DownloadRequest`** (`schema/index.go:240-254`), schema
string `"index.DownloadRequest.v1"`. Struct doc (`:240-241`): "DownloadRequest
asks indexarr to fetch a release payload using its own session cookies and
passkeys."

| Line | Field | Go type | JSON tag | Doc |
|---|---|---|---|---|
| 244 | `IndexerRef` | `Ref` | `indexerRef` | the indexer holding the session to use |
| 247 | `GUID` | `string` | `guid` | the indexer-scoped release identifier |
| 250 | `URL` | `string` | `url,omitempty` | the download link from the release |

**Response — `schema.DownloadResponse`** (`schema/index.go:256-276`), schema
string `"index.DownloadResponse.v1"`. Struct doc (`:256`): "DownloadResponse
carries **exactly one of** Bytes, MagnetURL or RedirectURL."

| Line | Field | Go type | JSON tag | Doc |
|---|---|---|---|---|
| 259 | `Bytes` | `[]byte` | `bytes,omitempty` | the .torrent or .nzb file |
| 262 | `MagnetURL` | `string` | `magnetURL,omitempty` | the magnet link, when the indexer returned one instead |
| 266 | `RedirectURL` | `string` | `redirectURL,omitempty` | where to fetch the payload, when the indexer will not proxy it |
| 269 | `ContentType` | `string` | `contentType,omitempty` | the MIME type of Bytes |
| 272 | `Error` | `string` | `error,omitempty` | the failure message when the fetch failed |

Caller contract from the CRD side: `downloadv1alpha1.IndexerDownload`
(`api/download/v1alpha1/download_types.go:205-227`) — `IndexerRef string`
(required, MinLength=1), `GUID string` (required, MinLength=1), `URL string`
(optional, MaxLength=2048). Its doc (`:205-208`): "grabarr resolves it through
indexarr at grab time to obtain the actual .torrent or .nzb bytes; those bytes
are deliberately never stored on the Download object."

### 5.3 `clustarr.rpc.indexarr.query`

**Request — `schema.QueryRequest`** (`schema/index.go:278-296`), schema string
`"index.QueryRequest.v1"`. Struct doc (`:278-280`): "QueryRequest is a direct
query against the indexarr release index, used by Search custom resources in
query mode and by the Torznab facade."

| Line | Field | Go type | JSON tag | Doc |
|---|---|---|---|---|
| 283 | `Text` | `string` | `text,omitempty` | the full-text query |
| 286 | `Filters` | `map[string]string` | `filters,omitempty` | field filters such as protocol, indexer or quality |
| 289 | `Limit` | `int32` | `limit,omitempty` | caps the number of releases returned |
| 292 | `Offset` | `int32` | `offset,omitempty` | pages through the result set |

**Response — `schema.QueryResponse`** (`schema/index.go:298-311`), schema string
`"index.QueryResponse.v1"`:

| Line | Field | Go type | JSON tag | Doc |
|---|---|---|---|---|
| 301 | `Releases` | `[]Release` | `releases,omitempty` | the matching releases |
| 304 | `Total` | `int64` | `total,omitempty` | how many releases matched before paging |
| 307 | `Error` | `string` | `error,omitempty` | the failure message when the query failed |

### 5.4 `schema.Ref` (`pkg/events/schema/schema.go:43-64`)

```go
type Ref struct {
    Namespace string `json:"namespace,omitempty"`
    Name      string `json:"name"`
    UID       string `json:"uid,omitempty"`
}
func (r Ref) String() string // "<namespace>/<name>", or just Name when Namespace == ""
func (r Ref) Key() string    // == String(); the Clustarr-Key header value
```

### 5.5 The bus RPC primitives

`events.Requester` (`pkg/events/bus.go:151-162`), verbatim:

```go
// Requester is the micro-style request/reply half of the bus: a single reply
// per request, load-balanced across a queue group.
type Requester interface {
    // Request encodes in as JSON, sends it to subject and decodes the single
    // reply into out. It returns ErrNoResponders when nothing is serving the
    // subject and the context error on deadline.
    Request(ctx context.Context, subject string, in, out any) error

    // Serve registers h as a responder on subject inside queue group queue.
    // Handlers run until the bus is closed.
    Serve(subject, queue string, h func(ctx context.Context, data []byte) ([]byte, error)) error
}
```

Key implementation facts (`pkg/events/natsbus/natsbus.go`):

- `DefaultRequestTimeout = 45 * time.Second` (`:44`) — it bounds a `Request`
  with no context deadline (`:449`) **and** bounds the server handler's ctx
  (`:371-375`). This is the "min(deadline, 45s)" of §5, implemented.
- `Serve` handlers receive **raw bytes**, not an `Envelope`. There is **no
  `Clustarr-Schema` header on the RPC path** — decode straight into the struct
  (`:388`).
- Trace **is** propagated: `Request` stamps `HeaderTrace` (`:455-461`), `Serve`
  extracts it into a synthetic envelope and runs `hooks.RunAfterReceive`
  (`:376-386`). Handlers get a trace-continuing ctx for free.
- A handler error becomes a reply with headers `Nats-Service-Error` (the error
  string) and `Nats-Service-Error-Code: 500` (`:389-394`), which the client
  surfaces as an error from `Request`.

### 5.6 The existing caller — `app/catalog/worker/search`

`app/catalog/worker/search/rpc.go:38-45`, verbatim:

> SearchRPC is the federated-search half of clustarr.rpc.indexarr.search that
> this package depends on. **indexarr (Phase D) MUST serve that subject with
> exactly schema.SearchRequest -> schema.SearchResponse, already pinned in
> pkg/events/schema/index.go**; this package adds no new payload type, only this
> narrow client interface plus a fake for tests.

```go
type SearchRPC interface {
    Search(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error)
}
func NewBusSearchRPC(r events.Requester) SearchRPC   // rpc.go:56
```

- `noRespondersRetryAfter = 15 * time.Second` (`rpc.go:36`) — on
  `events.ErrNoResponders` the worker returns `events.Retry(15s, …)`. That is
  today's behaviour with no server ("before Phase D, not built at all",
  `rpc.go:35`).
- `SearchDeadline = 45 * time.Second` (`request.go:31`), set into
  `DeadlineMillis` (`request.go:69`).
- `BuildSearchRequest` (`request.go:65-102`) populates: `Kind`, `Limit`,
  `DeadlineMillis`, `UserInvoked`, `Year`, `IndexerRefs`, `Categories`
  (either `Search.spec.categories` or `newznab.ByKind(kind)` expanded),
  `IDs` (`tmdb`/`imdb` for movies, `tvdb` for episodes), `Season`, `Episode`.
- **Anime convention, verbatim** (`request.go:59-64`):
  > schema.SearchRequest (pkg/events/schema/index.go) has Season and Episode but
  > no Absolute field, unlike spec §8.2's "tvdb plus season/episode/absolute".
  > For an anime episode this function puts the absolute number in Episode and
  > leaves Season nil, which is how *arr indexers key anime releases (absolute
  > numbering, no season token). The generated payload type wins over the spec's
  > literal wording.
- `FakeSearchRPC` (`rpc.go:74-97`) exists and is exported for tests.

`app/catalog/worker/search/worker.go:544` `releaseInfos([]schema.Release)
[]commonv1.ReleaseInfo` — the search path only consumes `Release.Info`; the
parsed fields matter for the RSS path (§6).

---

## 6. Question 2 — the release firehose

### 6.1 Subject

`pkg/events/subjects.go:189-193`, verbatim:

```go
// ReleaseSubject builds clustarr.rel.<protocol>.<indexerName>.<newznabTop>,
// the RSS fan-out subject indexarr publishes parsed releases on.
func ReleaseSubject(protocol, indexerName string, newznabTop int) string {
    return fmt.Sprintf("clustarr.rel.%s.%s.%d", tok(protocol), tok(indexerName), newznabTop)
}
```

- `tok` (`subjects.go:412-426`): empty → `"_"`; `.`, space, tab, `*`, `>`, `/`,
  `\`, and any rune `< 0x20` or `0x7f` → `-`; everything else passes through.
- `newznabTop` is the **1000-aligned parent** category id.
  `newznab.CategoryID.Parent()` (`pkg/newznab/category.go:186-191`) returns
  `(c/1000)*1000` for standard ids and the id itself for custom ids
  (`>= CustomCategoryOffset`, i.e. `>= 100000`, `category.go:230`).
- Spec §5 row (`design.md:558`): ``clustarr.rel.<protocol>.<indexerName>.<newznabTop>``
  → payload `index.Release.v1` (common.ReleaseInfo + parsed), producer→consumer
  "indexarr → catalogarr-rss-matcher".
- **No production caller exists yet.** `ReleaseSubject` is referenced only from
  `pkg/events/events_test.go:193` and `pkg/events/membus/membus_test.go:93,97`.

### 6.2 Deduplication id

`pkg/events/subjects.go:373-379`, verbatim:

```go
// MsgIDForRelease builds the deduplication ID for an RSS release:
// sha1("<indexerName>:<guid>"). Re-reading the same RSS page does not
// republish releases already seen.
func MsgIDForRelease(indexerName, guid string) string {
    sum := sha1.Sum([]byte(indexerName + ":" + guid))
    return hex.EncodeToString(sum[:])
}
```

Spec §5 headers row (`design.md:535`) confirms: "`Nats-Msg-Id` (… RSS:
`sha1(<indexerName>:<guid>)` …)". §8.7 (`design.md:755`) adds the window: "new
rows → `CLUSTARR_RELEASES` (Msg-Id `sha1(indexer:guid)`, **2h Duplicates**)".

### 6.3 Payload — `schema.Release`

`pkg/events/schema/index.go:30-75`, schema string `"index.Release.v1"`. Struct
doc (`:30-31`): "Release is one parsed indexer release fanned out on
`clustarr.rel.<protocol>.<indexerName>.<newznabTop>`."

| Line | Field | Go type | JSON tag | Doc |
|---|---|---|---|---|
| 34 | `Info` | `commonv1.ReleaseInfo` | `info` | the release as the indexer reported it, already normalised |
| 37 | `ParsedTitle` | `string` | `parsedTitle,omitempty` | the cleaned title the parser extracted |
| 40 | `Year` | `int32` | `year,omitempty` | the year parsed from the title |
| 43 | `Seasons` | `[]int32` | `seasons,omitempty` | the season numbers the release covers |
| 46 | `Episodes` | `[]int32` | `episodes,omitempty` | the episode numbers the release covers |
| 49 | `Absolute` | `[]int32` | `absolute,omitempty` | absolute episode numbers, for anime |
| 52 | `AirDate` | `*time.Time` | `airDate,omitempty` | the air date parsed from a daily-series title |
| 55 | `FullSeason` | `bool` | `fullSeason,omitempty` | marks a complete season pack |
| 58 | `MultiSeason` | `bool` | `multiSeason,omitempty` | marks a pack spanning more than one season |
| 61 | `Special` | `bool` | `special,omitempty` | marks a special or extra |
| 64 | `Kind` | `commonv1.MediaKind` | `kind,omitempty` | the media kind the classifier assigned to the title |
| 68 | `Hints` | `map[string][]string` | `hints,omitempty` | codec, HDR, audio and container tokens the parser recognised, keyed by hint name |
| 71 | `FetchedAt` | `time.Time` | `fetchedAt` | when indexarr read the release from the indexer (**no `omitempty`**) |

`commonv1.ReleaseInfo` (`api/common/v1alpha1/release_types.go:70-180`) — the
full nested payload indexarr must fill:

| Line | Field | Type | JSON | Markers / notes |
|---|---|---|---|---|
| 73 | `GUID` | `string` | `guid,omitempty` | indexer-scoped unique identifier |
| 77 | `IndexerRef` | `string` | `indexerRef,omitempty` | **the name of the Indexer object** |
| 81 | `IndexerName` | `string` | `indexerName,omitempty` | display name at search time |
| 85 | `Title` | `string` | `title,omitempty` | raw release title as published |
| 89 | `Protocol` | `Protocol` | `protocol,omitempty` | |
| 93 | `SizeBytes` | `int64` | `sizeBytes,omitempty` | |
| 109 | `PublishedAt` | `*metav1.Time` | `publishedAt,omitempty` | **pointer on purpose** — see the 12-line doc at `:95-108`: a value is unpersistable and "absence is genuinely different from a date"; substituting `now` or the zero time both corrupt usenet ranking |
| 113 | `DownloadURL` | `string` | `downloadURL,omitempty` | |
| 117 | `MagnetURL` | `string` | `magnetURL,omitempty` | |
| 121 | `InfoHash` | `string` | `infoHash,omitempty` | |
| 125 | `InfoURL` | `string` | `infoURL,omitempty` | release details page |
| 129 | `Seeders` | `*int32` | `seeders,omitempty` | torrent only |
| 133 | `Leechers` | `*int32` | `leechers,omitempty` | torrent only |
| 138 | `IndexerFlags` | `[]string` | `indexerFlags,omitempty` | **`+kubebuilder:validation:items:Enum=freeleech;halfleech;neutralleech;doubleupload;internal;exclusive;scene`** |
| 142 | `Categories` | `[]int32` | `categories,omitempty` | Newznab/Torznab category IDs |
| 146 | `Quality` | `Quality` | `quality,omitempty` | |
| 150 | `Revision` | `Revision` | `revision,omitempty` | |
| 154 | `ReleaseGroup` | `string` | `releaseGroup,omitempty` | |
| 158 | `Edition` | `string` | `edition,omitempty` | |
| 162 | `Languages` | `[]string` | `languages,omitempty` | |
| 166 | `ReleaseType` | `ReleaseType` | `releaseType,omitempty` | |
| 170 | `FormatScore` | `int32` | `formatScore,omitempty` | |
| 174 | `MatchedFormats` | `[]string` | `matchedFormats,omitempty` | |
| 179 | `IDs` | `map[string]string` | `ids,omitempty` | well-known keys `tmdb`/`imdb`/`tvdb` (`release_types.go:62-66`) |

`IndexerFlag*` constants (`release_types.go:50-59`): `freeleech`, `halfleech`,
`neutralleech`, `doubleupload`, `internal`, `exclusive`, `scene`.

### 6.4 Who consumes it, and the `Envelope.Key` convention — **CONFIRMED**

`app/catalog/worker/rssmatcher` is the consumer, durable
`events.ConsumerCatalogRSSMatcher = "catalogarr-rss-matcher"`
(`subjects.go:74`), on `StreamReleases` with filter `FilterAllReleases =
"clustarr.rel.>"` (`subjects.go:52`).

`Handler.Subscription()` (`handler.go:117-126`) reads the tuning from
`events.Default().Consumer(events.ConsumerCatalogRSSMatcher)` rather than
restating it.

**The key convention, verbatim** (`app/catalog/worker/rssmatcher/handler.go:167-174`):

```go
// Every catalogarr consumer splits the envelope key on "/" for its
// namespace, and indexarr must follow the same convention on
// clustarr.rel.> -- "<namespace>/<indexerName>". Guessing a namespace
// instead would fan one indexer's releases across the whole cluster.
ns, _, ok := strings.Cut(env.Key, "/")
if !ok || ns == "" {
    return events.Discard("rssmatcher: envelope key is not <namespace>/<indexerName>",
        fmt.Errorf("key=%q", env.Key))
}
```

**Confirmed: `Envelope.Key` MUST be `"<namespace>/<indexerName>"`.** Note:

- `strings.Cut` requires a literal `/`. A key with no `/` is **discarded to the
  DLQ**, not retried.
- `Envelope.Key`'s own doc (`pkg/events/envelope.go:92`) says "the
  `<namespace>/<name>` of the owning custom resource", and `schema.Ref.Key()`
  (`schema/schema.go:64`) renders exactly that — but the second segment here
  must be the **indexer name**, matching the `Indexer` object's
  `metadata.name`, because `ReleaseInfo.IndexerRef` is "the name of the
  Indexer the release came from" (`release_types.go:75-77`).
- **Ambiguity to resolve in the plan:** `ReleaseSubject`'s second token is
  `indexerName` and `ReleaseInfo` carries *both* `IndexerRef` (object name) and
  `IndexerName` (display name). `rssmatcher` logs `rel.Info.IndexerRef`
  (`handler.go:175`) and `decisionOptions` looks up indexer priority by
  `rel.Info.IndexerRef` (`resolve.go:216`). **Use the object name
  (`metadata.name`) for both the envelope key's second segment and
  `ReleaseInfo.IndexerRef`**; put the display name in `ReleaseInfo.IndexerName`.
  The subject token is cosmetic/routing only.

### 6.5 Which `schema.Release` fields the matcher actually reads

From `app/catalog/worker/rssmatcher/match.go` and `handler.go` — indexarr must
populate all of these or matching silently fails:

| Field | Read at | Used for |
|---|---|---|
| `rel.Kind` | `match.go:45`, `handler.go:175` | dispatch to `matchMovie` / `matchSeries`; anything else matches nothing |
| `rel.Info.IDs["tmdb"]` | `match.go:59` | movie id match |
| `rel.Info.IDs["tvdb"]` | `match.go:87` | series id match |
| `rel.ParsedTitle` + `rel.Year` | `match.go:65`, `:93` | `TitleYearKey` fallback |
| `rel.MultiSeason`, `rel.Seasons` | `match.go:131-134` | a multi-season or multi/zero-season release resolves **no episodes** |
| `rel.Episodes` | `match.go:145` | per-episode match |
| `rel.FullSeason` | `match.go:156` | season-pack → whole season |
| `rel.AirDate` | `match.go:163-165` | daily-series match against `Episode.status.airDate` |
| `rel.Info` (whole) | `handler.go:255` | `decision.Evaluate([]commonv1.ReleaseInfo{rel.Info}, …)` |
| `rel.Info.IndexerRef` | `resolve.go:216` | indexer priority lookup in `decision.Options` |

`TitleYearKey` (`rssmatcher/index.go:103-109`) is
`release.CleanTitle(title) + "|" + strconv.Itoa(int(year))`, and its doc
(`index.go:96-102`) is explicit that `CleanTitle` — not `Normalize` — is the
comparison function. **So `ParsedTitle` must be the parser's raw
`ParsedRelease.Title`, not a pre-cleaned string**: the matcher applies
`CleanTitle` itself. Passing an already-cleaned title is harmless only because
`CleanTitle` is idempotent — do not rely on that; pass `parsed.Title`.

### 6.6 Trace propagation on the firehose

`rssmatcher/handler.go:157-159`, verbatim:

```go
// Extract before Start, so this span continues indexarr's RSS poll rather
// than beginning a new trace per release.
ctx = tracing.Extract(ctx, env)
ctx, span := tracing.Start(ctx, "rssmatcher.Handler.Handle")
```

**indexarr must call `tracing.Inject(ctx, env)` before every `Publish`**, the
same way `app/catalog/worker/grab/perform.go:364` does, or the RSS leg of every
trace is orphaned.

### 6.7 Envelope template for a published release

Following `app/catalog/worker/grab/perform.go:355-366`:

```go
env := &events.Envelope{
    ID:     events.MsgIDForRelease(indexerName, guid),   // subjects.go:376
    Type:   "index.Release",                             // coarse type, mirrors "catalog.ReleaseEvent"
    Schema: schemaName,                                  // from schema.Encode -> "index.Release.v1"
    Source: "indexarr@" + version.String(),
    Key:    ns + "/" + indexerObjectName,                // REQUIRED shape, §6.4
    Time:   now,
    Data:   data,
}
tracing.Inject(ctx, env)
bus.Publish(ctx, events.ReleaseSubject(protocol, indexerName, newznabTop), env,
    events.WithMsgID(env.ID))
```

`schema.Encode(p Payload) (schema string, data []byte, err error)` —
`schema/schema.go:68`. `schema.Decode(schema string, data []byte, out Payload)
error` — `schema/schema.go:79`, with an empty schema skipping the check.

---

## 7. Question 3 — the SQLite FTS5 release index

### 7.1 What the spec requires (design.md:621)

Verbatim: "**Release index:** `app/indexer/releaseindex.Store` interface; SQLite
(modernc.org/sqlite, WAL, FTS5 on `title_norm, grp`) on PVC `clustarr-index`
(RWO, 5Gi), columns from `pkg/release` (source, resolution, modifier, codec,
hdr, audio, languages, group, year, season, episode, ids), `expires_at` sweep
every 10 min (72h), readiness tied to the DB open; Postgres FTS is a Store
swap."

Broken out:

- **Package:** `app/indexer/releaseindex`, exporting a `Store` interface.
- **Engine:** SQLite via `modernc.org/sqlite`; **WAL**; **FTS5 over
  `title_norm` and `grp`**.
- **Volume:** PVC `clustarr-index`, RWO, 5Gi.
- **Columns from `pkg/release`:** source, resolution, modifier, codec, hdr,
  audio, languages, group, year, season, episode, ids.
- **Retention:** `expires_at` column, sweep **every 10 minutes**, window
  **72h**.
- **Readiness:** tied to the DB being open.
- **Uniqueness:** `UNIQUE(indexer, guid)` (design.md:621, RSS worker clause).
- **Writers:** the search service upserts *every hit*; the RSS worker inserts
  *new rows*.

### 7.2 ADR-0003 — the Store interface's four methods

`docs/adr/0003-release-index-sqlite-fts5.md:20-27`, verbatim:

> A SQLite database with an FTS5 virtual table over release titles, using the
> `modernc.org/sqlite` pure-Go driver, on an RWO PersistentVolume mounted by the
> single indexarr replica. **All access goes through a `Store` interface —
> `Upsert`, `Search`, `Prune`, `Stats`** — so the engine is replaceable without
> touching callers.
>
> indexarr is a single writer by construction (see ADR-0004's one-writer
> principle applied to this component). That is what allows SQLite to be used
> without WAL contention across pods, and it is enforced by deployment shape,
> not by convention.

`:14-16`: "The index is **a cache, not a system of record**. … Losing the index
costs a re-sync, nothing more."

`:41-43` — why not cgo, verbatim:

> **cgo SQLite (`mattn/go-sqlite3`).** Faster, but drags cgo into the indexarr
> image, which is the one service that currently builds as a distroless static
> binary. The pure-Go driver keeps that property; the performance difference is
> irrelevant at our row counts.

`:53-61` consequences: while indexarr restarts, **search returns errors and
catalogarr retries with backoff rather than failing a grab**; "FTS5 tokenisation
is not release-title-aware out of the box, so **title normalisation happens
before insert**, and changing that normalisation means rebuilding the index —
cheap, because it is a cache."

`:63`: "Backups are unnecessary."

### 7.3 Retention numbers cross-checked

| Source | Value |
|---|---|
| §6.2 (`design.md:621`) | `expires_at` sweep **every 10 min**, **72h** |
| `CLUSTARR_RELEASES` stream MaxAge (`design.md:542`, `topology.go:407`) | **72h** |
| `clustarr-search-cache` KV TTL (`design.md:610`, `topology.go:590`) | **35m** |
| research `indexers.md:401` (pre-decision) | default 24h for RSS/basic, 2h for seeders/peers freshness |

The 72h of the spec and the stream agree; the research note's 24h/2h is from the
Postgres sketch that ADR-0003 superseded. **Use 72h.**

### 7.4 `go.mod` — what is and is not present

Read in full at `go.mod:1-131`.

**There is no SQLite driver in `go.mod` at all.** Specifically absent:

- `modernc.org/sqlite` — **absent**, direct and indirect
- `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory` (its
  transitive deps) — **absent**
- `mattn/go-sqlite3` — absent
- `github.com/blevesearch/bleve/v2` — absent
- `github.com/jackc/pgx/v5` — absent
- `github.com/anacrolix/torrent` (for `metainfo.ParseMagnetUri`, which
  `indexers.md:330` recommends for infohash extraction from magnets) — **absent**

**Present and directly usable by indexarr** (`go.mod:6-46`):

| Module | Version | Why indexarr needs it |
|---|---|---|
| `github.com/PuerkitoBio/goquery` | v1.13.0 | Cardigann HTML selectors (M6) |
| `github.com/antchfx/xmlquery` | v1.5.1 | Cardigann XML responses |
| `github.com/tidwall/gjson` | v1.19.0 | Cardigann JSON responses |
| `github.com/goccy/go-yaml` | v1.19.2 | Cardigann definition parsing |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 | Cardigann schema.json v11 validation |
| `github.com/Masterminds/sprig/v3` | v3.3.0 | Cardigann template funcs |
| `github.com/dlclark/regexp2` | v1.12.0 | TRaSH custom formats |
| `github.com/moistari/rls` | v0.6.0 | `pkg/release` hint tags |
| `github.com/nats-io/nats.go` | v1.53.1 | bus |
| `golang.org/x/time` | v0.16.0 | `rate.Limiter` per indexer host |
| `golang.org/x/net` | v0.59.0 | `proxy` (SOCKS), `html` |
| `github.com/hashicorp/golang-lru/v2` | v2.0.7 | per-pod caps/cookie caches |
| `github.com/jonboulle/clockwork` | v0.5.0 | injectable clock for backoff tests |

**Also note `hack/deps/deps.go` keeps `x/net/proxy` alive with no importer yet**
(remaining-work.md:1072): "`hack/deps/deps.go` still keeps `mimetype`,
`sprig/v3` and `x/net/proxy` alive (no importer yet); prune each when its phase
lands." indexarr's proxy work (M6) is `x/net/proxy`'s intended importer.

**Plan obligation:** `modernc.org/sqlite` must be added by **one serial
`go get`**, before any parallel agent starts. CLAUDE.md: "Never run `go get` or
`go mod tidy` from parallel agents. They corrupt `go.mod`." There is also an
outstanding `go mod tidy -diff` failure on `main`
(remaining-work.md:1054, :1136) — fold both into the same serial run.

**Image constraint:** `modernc.org/sqlite` is pure Go, so the distroless static
image (`design.md:40`) and `readOnlyRootFilesystem: true`
(`config/manager/indexarr.yaml:119`) both hold. SQLite temp files need a
writable `TMPDIR`; the pod already mounts an `emptyDir` at `/tmp`
(`indexarr.yaml:125-126,132-133`).

---

## 8. Question 4 — caps, health and backoff, concretely

### 8.1 Caps

**Where it lives:** `Indexer.status.caps` — `*Caps` (`indexer_types.go:273`),
with `Modes map[string][]string`, `Categories []Category` (MaxItems=200),
`LimitsMax int32`, `LimitsDefault int32`, `SupportsRawSearch bool`
(`:224-247`). `Indexer.status.protocol` and `.privacy` are "resolved from the
definition" (`:263-269`).

**Who fills it:** the `indexer` controller — "probe caps" (design.md:621).

**How it is used:** the search service's selection predicate — "caps support the
mode/categories" (design.md:621), and "id params gated by caps; fallback
`t=search&q=`" (same line). Research `indexers.md:332`: "Skip indexers whose caps
cannot serve the request (categories, mode, id params) exactly like Prowlarr's
`SupportedCategories`."

**The wire type it comes from:** `pkg/torznab.Caps` (`pkg/torznab/caps.go:83-91`):

```go
type Caps struct {
    ServerTitle   string
    LimitsDefault int
    LimitsMax     int
    Modes         map[SearchMode]Searching
    Categories    []newznab.Category
    Tags          map[string]string // flag name -> description
}
type Searching struct {                       // caps.go:76-80
    Available       bool
    SupportedParams []string
    SearchEngine    string // "raw" when the indexer accepts free-text q
}
func (c Caps) Supports(mode SearchMode, param string) bool   // caps.go:93
func ParseCaps(r io.Reader) (Caps, error)                    // caps.go:156
func (c *Client) Caps(ctx context.Context) (Caps, error)     // client.go:169
```

`torznab.SearchMode` values (`caps.go:41-47`): `ModeSearch = "search"`,
`ModeTVSearch = "tvsearch"`, `ModeMovieSearch = "movie"`,
`ModeMusicSearch = "music"`, `ModeAudioSearch = "audio"` (spec alias of music),
`ModeBookSearch = "book"`.

> **Note the vocabulary mismatch.** The CRD's `Caps.Modes` doc says the keys are
> "search, tv-search, movie-search, ..." (`indexer_types.go:226-227`, and
> identically in `IndexerDefinition`'s `CapsSummary`, `:61-62`), while
> `torznab.SearchMode` uses `search`, `tvsearch`, `movie`, `music`, `audio`,
> `book` — the actual Torznab wire values. **The Torznab values are correct**;
> the doc comments are wrong. `Caps.Modes` has no enum marker, so nothing
> enforces either. Pick `torznab.SearchMode`'s strings and say so.

`SupportsRawSearch` maps from `Searching.SearchEngine == "raw"`.
`Caps.Categories []Category{ID, Name, Sub []SubCategory}` maps from
`[]newznab.Category` (`pkg/newznab/category.go:36-49`).

### 8.2 Health / escalation backoff

**Spec §6.2 (design.md:621), verbatim:** "mirror KV health/limits into status
with Prowlarr's escalation table
`[0,60,300,900,1800,3600,10800,21600,43200,86400]s` + 15-min startup grace".

**Research `indexers.md:338`, the verified Prowlarr algorithm, verbatim:**

> `ProviderStatusServiceBase`: `EscalationBackOff.Periods = [0, 60, 300, 900,
> 1800, 3600, 10800, 21600, 43200, 86400]` seconds. `RecordFailure(id,
> minimumBackOff)`: increments `EscalationLevel` (capped at
> `Periods.Length-1`) unless in the 15-minute startup grace
> (`MinimumTimeSinceStartup`), and keeps incrementing until the period ≥
> `minimumBackOff`; `DisabledTill = now + Periods[level]`. `RecordSuccess`:
> level−1, clear `DisabledTill`. Status row: `ProviderId, InitialFailure,
> MostRecentFailure, EscalationLevel, DisabledTill` + `IndexerStatus`-specific
> `LastRssSyncReleaseInfo, Cookies, CookiesExpirationDate`.

The table has **10 entries**, so `EscalationLevel` is capped at **9**. Seconds →
human: 0s, 1m, 5m, 15m, 30m, 1h, 3h, 6h, 12h, 24h.

**CRD fields that hold it:**

| Concept | Field | Line |
|---|---|---|
| current step | `status.escalationLevel int32` | `indexer_types.go:277` |
| cooldown end | `status.disabledUntil *metav1.Time` | `:281` |
| run start | `status.initialFailureAt *metav1.Time` | `:285` |
| last failure | `status.lastFailureAt *metav1.Time` | `:289` |
| message | `status.lastFailure string` | `:293` |
| condition | `IndexerConditionHealthy = "Healthy"` | `:34` |

**`RecordSuccess` / `RecordFailure`** are named in the search-service clause of
§6.2 (design.md:621) as the functions the fan-out calls per indexer.

**Emission:** §5 (`design.md:559`) — `clustarr.evt.index.indexer.disabled|
recovered|limited.<uid>` carrying `index.IndexerEvent.v1`, indexarr → history.
Research `indexers.md:341` adds: "expose the same four numbers in
`Indexer.status` (`queriesInWindow`, `grabsInWindow`, `escalationLevel`,
`disabledUntil`) and **emit K8s Events on escalation**."

`schema.IndexerEvent` (`pkg/events/schema/index.go:77-100`), schema string
`"index.IndexerEvent.v1"`:

| Line | Field | Type | JSON | Doc |
|---|---|---|---|---|
| 81 | `IndexerRef` | `Ref` | `indexerRef` | the Indexer the event is about |
| 84 | `Action` | `string` | `action` | one of disabled, recovered or limited |
| 87 | `Reason` | `string` | `reason,omitempty` | explains the transition |
| 90 | `Until` | `*time.Time` | `until,omitempty` | when a disable or limit expires |
| 93 | `Failures` | `int32` | `failures,omitempty` | the consecutive failure count that triggered a disable |
| 96 | `At` | `time.Time` | `at` | when the transition happened |

Subject builder (`subjects.go:195-199`):

```go
// IndexerEventSubject builds
// clustarr.evt.index.indexer.<disabled|recovered|limited>.<uid>.
func IndexerEventSubject(action, uid string) string {
    return fmt.Sprintf("clustarr.evt.index.indexer.%s.%s", tok(action), tok(uid))
}
```

Action constants already exist (`subjects.go:138-140`): `ActionDisabled =
"disabled"`, `ActionRecovered = "recovered"`, `ActionLimited = "limited"`.

### 8.3 Limits (query / grab windows)

**CRD:** `Indexer.spec.limits *Limits{QueryLimit, GrabLimit *int32; Unit
enum{day,hour} = day}` (`indexer_types.go:63-77`, `:142`).
Observed counters: `status.queriesInWindow int32` (`:297`),
`status.grabsInWindow int32` (`:301`). Condition
`IndexerConditionRateLimited = "RateLimited"` (`:36`), "True while the indexer's
query or grab limit is exhausted."

**KV bucket** — `events.BucketIndexerLimits = "clustarr-indexer-limits"`
(`subjects.go:98`). Topology (`topology.go:587`):

```go
b(BucketIndexerLimits, 2*24*time.Hour, "Query and grab timestamp rings."),
```

Spec §5 KV table (`design.md:607`): keys `<indexer-uid>.query`, `.grab`
**timestamp rings (CAS)**, TTL 2d, "QueryLimit/GrabLimit windows".

Bucket defaults (`topology.go:575-581`): `History: 1`, `Storage: StorageFile`,
`Replicas: 3`, `LimitMarkerTTL: 5 * time.Minute`.

**Prowlarr's semantics** (`indexers.md:339`), verbatim:

> `IndexerLimitService`: window = 1 h (`LimitsUnit.Hour`) or 24 h; `AtQueryLimit`
> counts History events `IndexerQuery` + `IndexerRss` ≥ `QueryLimit`;
> `AtDownloadLimit` counts `ReleaseGrabbed` ≥ `GrabLimit`;
> `CalculateRetryAfter…` = seconds until the oldest event in the window ages
> out.

Note `IndexerQuery` **+ `IndexerRss`** both count toward `QueryLimit`.

**Grab accounting.** §5 (`design.md:555`) marks
`clustarr.evt.catalog.release.grabbed|rejected.<target-uid>` as
"catalogarr → history, **indexarr (grab accounting)**". §8.3 (`design.md:747`):
"`grabarr` records the grab against `clustarr-indexer-limits` via
`evt.catalog.release.grabbed` consumed by indexarr." §5's own
`rpc.indexarr.download` row (`design.md:574`) says indexarr "**counts
GrabLimit**" there. See §12.3 — these three are not consistent.

`schema.ReleaseEvent` (`pkg/events/schema/catalog.go:85-122`, schema string
`"catalog.ReleaseEvent.v1"`) carries `IndexerRef *Ref` (`:99`) and `Indexer
string` (`:96`), so it *is* consumable for accounting. It is published today by
`app/catalog/worker/grab/perform.go:365` with envelope `Key: ns + "/" +
target.Name` (`perform.go:360`) — i.e. keyed by the **media item**, not the
indexer.

### 8.4 Per-request pacing

`spec.requestDelay` default `"2s"` (`indexer_types.go:148`), "raised to the
definition's requestDelay when that is larger" (`:144-145`).
`spec.timeout` default `"30s"` (`:153`) is the per-indexer
`context.WithTimeout` in the fan-out (design.md:621).

Research `indexers.md:340`: "Per-request pacing: `RateLimit` (2 s default /
`requestDelay`) keyed **per indexer host**; 429 → `Retry-After` honoured (≥ 1
h)."

`CLAUDE.md` convention: "**The caller owns rate limiting.** A library package
accepts an injected limiter (`torznab.WithRateLimit`, …) and never defaults one
on; the controller holds one limiter per host." `torznab.WithRateLimit(r
rate.Limit, burst int) ClientOption` exists at `pkg/torznab/client.go:55`.

`torznab.Error` (`pkg/torznab/errors.go:52-58`) already carries `RetryAfter
time.Duration` "set from the Retry-After header on 429", plus
`Code ErrorCode`, `Description`, `HTTPStatus`. Torznab codes
(`errors.go:32-47`): 100 IncorrectCredentials, 101 AccountSuspended,
102 InsufficientPrivileges, 103 RegistrationDenied, 104 RegistrationsClosed,
200 MissingParameter, 201 IncorrectParameter, 202 NoSuchFunction,
203 FunctionNotAvailable, 300 NoSuchItem, **500 RequestLimitReached**,
**501 DownloadLimitReached**, 900 Unknown, 910 APIDisabled.

500/501 are the natural `RateLimited` / `Limited` signals.

### 8.5 Sessions

`Indexer.status.sessionSecretRef string` (`indexer_types.go:317`). §6.2: "owned
session Secret". KV bucket `events.BucketIndexerSessions =
"clustarr-indexer-sessions"` (`subjects.go:97`), TTL **30d**, description
"Cardigann cookies and JWTs." (`topology.go:586`). §5 (`design.md:606`): keys
`<indexer-uid>` cookies/JWT, "Cardigann logins (**mirrored into owned
Secret**)".

`spec.secretRef` recognised keys (`indexer_types.go:116`): `apikey`, `username`,
`password`, `cookie`, `passkey`, `rss_key`.

Condition `IndexerConditionAuthenticated` (`indexer_types.go:32`).

### 8.6 Search result cache

`events.BucketSearchCache = "clustarr-search-cache"` (`subjects.go:101`), TTL
**35m**, "Raw indexer search results." (`topology.go:590`). §5
(`design.md:610`): key `<indexer-uid>.<sha256(request)>`, "Jackett-style
raw-result cache". Research `indexers.md:403` agrees: key
`<indexerId>/<sha256(canonical JSON of SearchRequest)>`, value compressed
`[]Release`.

> Key characters must survive `events.KVKeyToken` (`pkg/events/kvkey.go:47`) /
> `ValidKVKey` (`:91`). A uid and a hex digest are safe; a raw indexer *name* is
> not. Use the **uid**, as the spec says.

---

## 9. Question 5 — the RSS worker

### 9.1 What it does (design.md:621, verbatim)

> **RSS worker:** `t=search` empty q per Indexer, insert new rows (UNIQUE(indexer,
> guid)), publish each new row to `CLUSTARR_RELEASES`.

### 9.2 Schedule

**Trigger:** the `indexer` controller schedules it — §6.2: "schedule RSS via
`WithScheduleAt` on `work.indexarr.rss`". §5 subject table (`design.md:569`):

> | `clustarr.work.indexarr.rss.normal.<indexer-uid>` (scheduled) |
> `index.RssTask.v1` | indexarr ctrl → rss worker |

**Interval:** `Indexer.spec.rssInterval metav1.Duration` default **`"15m"`**
(`indexer_types.go:138`). §8.7 (`design.md:755`): "indexarr schedules
`work.indexarr.rss` per Indexer". Research `indexers.md:390` confirms the
intent: "RSS polling of every indexer every ~15 min = `t=search` with empty
`q`".

**Gating:** `spec.enableRss *bool` default `true` (`indexer_types.go:123`).

**Subject builder** (`subjects.go:281-284`):

```go
// WorkRSSSubject builds clustarr.work.indexarr.rss.normal.<indexer-uid>.
func WorkRSSSubject(indexerUID string) string {
    return "clustarr.work.indexarr.rss.normal." + tok(indexerUID)
}
```

**Scheduling mechanics** (`subjects.go:296-317`) — `ScheduleSubject(target)`
inserts a `"sched"` token after the service, so
`clustarr.work.indexarr.rss.normal.<uid>` is held on
`clustarr.work.indexarr.sched.rss.normal.<uid>`. Its doc, verbatim:

> A scheduled message is stored on the holding subject and the broker
> republishes it to target when the schedule fires, so the two must differ or
> the message would re-trigger itself. … It therefore stays inside the same work
> stream while matching no consumer's filters … Only `clustarr.work.*` subjects
> can be scheduled, because they are the only streams with AllowMsgSchedules.

`ScheduleSubject` requires ≥5 dot-separated tokens starting `clustarr.work.`
(`:309`); `WorkRSSSubject` produces exactly 6, so it qualifies.
`events.WithScheduleAt(t time.Time) PublishOption` — `pkg/events/bus.go:64`.

**Dedup id for the task:** `events.MsgIDForObject(uid, generation, task)` →
`"<uid>:<generation>:<task>"` (`subjects.go:369-371`), per §5's header rule.

### 9.3 Task payload — `schema.RssTask`

`pkg/events/schema/index.go:102-118`, schema string `"index.RssTask.v1"`. Struct
doc (`:102-104`): "RssTask asks an RSS worker to poll one indexer. It is
published with a schedule matching the indexer's RSS interval. Subject:
`clustarr.work.indexarr.rss.normal.<indexer-uid>`."

| Line | Field | Type | JSON | Doc |
|---|---|---|---|---|
| 107 | `IndexerRef` | `Ref` | `indexerRef` | the Indexer to poll |
| 110 | `Categories` | `[]int32` | `categories,omitempty` | limits the poll to these Newznab category IDs |
| 114 | `Since` | `*time.Time` | `since,omitempty` | the publish time of the newest release already seen, **so the worker can stop paging early** |

### 9.4 Consumer tuning — already declared

`events.ConsumerIndexRSS = "indexarr-rss"` (`subjects.go:85`).
`topology.go:532-538`:

```go
{
    Name: ConsumerIndexRSS, Stream: StreamWorkIndexarr,
    Filters: []string{FilterIndexRSS},          // "clustarr.work.indexarr.rss.>"
    AckWait: 120 * s, MaxDeliver: 4,
    BackOff:       []time.Duration{1 * m, 5 * m, 15 * m},
    MaxAckPending: 4,
},
```

Matches the spec §5 table row (`design.md:592`): `indexarr-rss`, WORK_INDEXARR,
`…rss.>`, 120s, 4, 1m/5m/15m, 4, no heartbeat.

### 9.5 What it publishes

Per new row: one `schema.Release` on
`events.ReleaseSubject(protocol, indexerName, newznabTop)` with
`events.MsgIDForRelease(indexerName, guid)` — see §6. Then it must update
`Indexer.status.lastRssAt`, `.lastRssNewCount` and `.indexedReleases`
(`indexer_types.go:305,309,313`).

### 9.6 Definitions-sync sibling (M6 scope, plumbing already present)

`schema.DefinitionsSync` (`schema/index.go:120-135`), schema string
`"index.DefinitionsSync.v1"`. Subject: `clustarr.work.indexarr.definitions.
normal.sync` — a **constant**, not a builder (`subjects.go:286-287`):

```go
// WorkDefinitionsSubject is the single definitions-sync work subject.
const WorkDefinitionsSubject = "clustarr.work.indexarr.definitions.normal.sync"
```

| Line | Field | Type | JSON | Doc |
|---|---|---|---|---|
| 125 | `Source` | `string` | `source,omitempty` | the definitions repository or bundle to sync from. **Empty means the built-in bundle.** |
| 128 | `Revision` | `string` | `revision,omitempty` | the definition-set revision to move to. **Empty means latest.** |
| 131 | `Force` | `bool` | `force,omitempty` | re-applies definitions even when the revision is unchanged |

Consumer `events.ConsumerIndexDefinitions = "indexarr-definitions"`
(`subjects.go:86`), `topology.go:539-545`: filter `FilterIndexDefs =
"clustarr.work.indexarr.definitions.>"`, AckWait 300s, MaxDeliver 3, BackOff
5m/30m, MaxAckPending 1. Matches `design.md:593`.

---

## 10. Question 6 — already-shipped `pkg/events` infrastructure for indexarr

Everything in this section **exists today** and must be used, not reinvented.

### 10.1 Stream

```go
// pkg/events/subjects.go:33
StreamWorkIndexarr = "CLUSTARR_WORK_INDEXARR"
// pkg/events/subjects.go:55
FilterWorkIndexarr = "clustarr.work.indexarr.>"
```

`topology.go:419`: `work(StreamWorkIndexarr, FilterWorkIndexarr, 256*MiB)`.
The `work` helper (`topology.go:372-384`) gives every work stream:

```go
Retention:         RetentionWorkQueue,
Storage:           StorageFile,
Discard:           DiscardOld,      // NOT DiscardNew -- see below
MaxBytes:          256 * MiB,
Duplicates:        time.Hour,
Replicas:          3,
AllowMsgSchedules: true,
```

**`DiscardOld`, not the spec's `DiscardNew`** — the reason is documented at
`topology.go:365-371`, verbatim:

> The design asks for DiscardNew as well, so a full queue refuses new tasks
> instead of dropping queued ones. nats-server rejects that pairing outright
> ("message scheduling cannot use discard new"), so these streams use DiscardOld
> and are sized with enough headroom that the limit is an alarm, not a routine
> event. Publishers still handle `ErrQueueFull`, which remains reachable on any
> stream an operator configures with DiscardNew.

`Topology.Validate()` enforces this pairing (`topology.go:269-273`) and that
every WorkQueue stream allows schedules (`:264-268`) and that
`MaxDeliver > len(BackOff)` (`:295-299`).

The **release** stream (indexarr's publish target), `topology.go:400-411`:

```go
{
    Name:        StreamReleases,          // "CLUSTARR_RELEASES", subjects.go:30
    Description: "Parsed indexer releases fanned out to the RSS matcher.",
    Subjects:    []string{FilterAllReleases},  // "clustarr.rel.>", subjects.go:52
    Retention:   RetentionLimits,
    Storage:     StorageFile,
    Discard:     DiscardOld,
    MaxAge:      72 * time.Hour,
    MaxBytes:    4 * GiB,
    Duplicates:  2 * time.Hour,
    Replicas:    3,
},
```

### 10.2 Durable consumers

| Constant | Value | Line | Stream | Filter | AckWait | MaxDeliver | BackOff | MaxAckPending |
|---|---|---|---|---|---|---|---|---|
| `ConsumerIndexRSS` | `indexarr-rss` | `subjects.go:85` | `CLUSTARR_WORK_INDEXARR` | `clustarr.work.indexarr.rss.>` | 120s | 4 | 1m, 5m, 15m | 4 |
| `ConsumerIndexDefinitions` | `indexarr-definitions` | `subjects.go:86` | `CLUSTARR_WORK_INDEXARR` | `clustarr.work.indexarr.definitions.>` | 300s | 3 | 5m, 30m | 1 |

(`topology.go:532-545`.) Read them with
`events.Default().Consumer(events.ConsumerIndexRSS)` and call
`spec.Subscription()` (`topology.go:104-115`) — the pattern
`rssmatcher.Handler.Subscription()` uses (`handler.go:117-126`), so the tuning
lives in one place.

> **Inconsistency already flagged in the plan** (remaining-work.md:1112): the
> catalogarr subscriptions read `events.Default()` while `app/import/run.go`
> reads `o.BusTopology()`. "Benign only because `ForSingleNode` never touches
> `Consumers` — an invariant nothing enforces." Pick one and be consistent.

Downstream consumer indexarr must feed:
`ConsumerCatalogRSSMatcher = "catalogarr-rss-matcher"` (`subjects.go:74`) on
`StreamReleases`, filter `clustarr.rel.>`, AckWait 30s, MaxDeliver 6, BackOff
1s/5s/30s/2m/10m, MaxAckPending 256 (`topology.go:441-447`).

### 10.3 KV buckets

| Constant | Value | Line | TTL | Description | topology.go |
|---|---|---|---|---|---|
| `BucketIndexerSessions` | `clustarr-indexer-sessions` | `subjects.go:97` | 30d | "Cardigann cookies and JWTs." | `:586` |
| `BucketIndexerLimits` | `clustarr-indexer-limits` | `subjects.go:98` | 2d | "Query and grab timestamp rings." | `:587` |
| `BucketSearchCache` | `clustarr-search-cache` | `subjects.go:101` | 35m | "Raw indexer search results." | `:590` |

All three get `History: 1`, `Storage: StorageFile`, `Replicas: 3`,
`LimitMarkerTTL: 5m` (`topology.go:575-581`). `LimitMarkerTTL`'s doc
(`topology.go:126-128`): "how long tombstones for TTL-expired keys are kept. A
non-zero value is required for per-key TTL (KV `WithTTL`) to work."

Bucket names may not contain dots — enforced at `topology.go:320-326`.

### 10.4 Subjects and builders indexarr owns

| Builder / const | Signature or value | Line |
|---|---|---|
| `ReleaseSubject` | `(protocol, indexerName string, newznabTop int) string` → `clustarr.rel.<p>.<n>.<top>` | `subjects.go:191` |
| `IndexerEventSubject` | `(action, uid string) string` → `clustarr.evt.index.indexer.<action>.<uid>` | `subjects.go:197` |
| `WorkRSSSubject` | `(indexerUID string) string` → `clustarr.work.indexarr.rss.normal.<uid>` | `subjects.go:282` |
| `WorkDefinitionsSubject` | const `clustarr.work.indexarr.definitions.normal.sync` | `subjects.go:287` |
| `ScheduleSubject` | `(target string) (string, error)` — inserts `.sched.` | `subjects.go:307` |
| `MsgIDForRelease` | `(indexerName, guid string) string` → `sha1(name:guid)` hex | `subjects.go:376` |
| `MsgIDForObject` | `(uid string, generation int64, task string) string` | `subjects.go:369` |
| `DLQSubject` | `(service, task, id string) string` | `subjects.go:331` |
| `RPCIndexSearch/Download/Query` | the three RPC subjects | `subjects.go:156-158` |
| `QueueGroupIndexarr` | `"indexarr"` | `subjects.go:162` |
| `KVKeyToken` / `ValidKVKey` | KV key escaping — **use for every KV key segment** | `kvkey.go:47`, `:91` |

Action constants available (`subjects.go:129-152`): `ActionDisabled`,
`ActionRecovered`, `ActionLimited`, `ActionSynced`, `ActionGrabbed`, … .

### 10.5 Error / settlement helpers

`events.Retry(after, err)` (`errors.go:84`), `events.Discard(reason, err)`
(`errors.go:107`), `events.Settle(err, attempt, sub)` (`errors.go:150`),
`events.DeadLetter(m, durable, reason)` (`errors.go:175`),
`events.Backoff(schedule, attempt)` (`errors.go:220`),
`events.DefaultBackoff = 30s` (`errors.go:234`).
`events.ErrNoResponders`, `ErrQueueFull`, `ErrKeyExists`,
`ErrRevisionMismatch`, `ErrKeyNotFound`, `ErrClosed` (`errors.go:29`).

### 10.6 Metrics already defined for indexarr

`pkg/obs/metrics/domain.go:125-151` — "Indexer telemetry, used by indexarr":

| Symbol | Metric | Type | Labels | Line |
|---|---|---|---|---|
| `metrics.IndexerQueryDuration` | `clustarr_indexer_query_duration_seconds` | histogram | `indexer`, `function` | `:129-133` |
| `metrics.IndexerQueriesTotal` | `clustarr_indexer_queries_total` | counter | `indexer`, `outcome` | `:138-141` |
| `metrics.IndexerReleasesReturned` | `clustarr_indexer_releases_returned` | histogram | `indexer` | `:146-150` |

`durationBucketsShort` "covers sub-second to roughly a minute: indexer…"
(`domain.go:43`).

**`function` is the Torznab `t=` mode** (search/tvsearch/movie/…), bounded.
`indexer` must be the object name, never a title or release name (CLAUDE.md;
amendment A2.3 `:258`).

### 10.7 RBAC that exists today

`config/rbac/role.yaml` is a **single shared Role**. Grep results:

- `index.clustarr.io` → `indexers`, verbs `get;list;watch` only
  (`config/rbac/role.yaml:127-133`). That grant exists for app/catalog/grabarr
  readers.
- `secrets` is granted (`role.yaml:10`).
- **`indexerdefinitions` and `indexerproxies` are absent. `indexers/status` is
  absent. `indexers` has no `update`/`patch`.**

So the plan must add `+kubebuilder:rbac` markers next to each new reconciler
(§10 of the spec, `design.md:783`: "Each service gets its own ClusterRole
generated by `controller-gen rbac` markers next to each reconciler") and re-run
`make manifests`. `TestChartRBACMatchesTheGeneratedRole` compares the chart copy
byte-for-byte (remaining-work.md:1058), so the chart must be regenerated in the
same change.

Also carried (remaining-work.md:1095): "the single shared Role grants that to
every service … Split the Role per service, or keep it and document the grant
where it is granted."

### 10.8 Deployment / chart facts

`config/manager/indexarr.yaml`:

- ServiceAccount `indexarr` (`:14`); Service `indexarr` ClusterIP with ports
  `http: 8080 → targetPort http` and `metrics: 8443` (`:26-35`).
- `replicas: 1`, `strategy.type: Recreate` (`:44-46`).
- `terminationGracePeriodSeconds: 60` (`:56`) — the AckWait floor for any
  consumer indexarr runs, exactly as documented for importarr
  (`topology.go:501-510`). `indexarr-rss` uses AckWait 120s, which **exceeds**
  the grace period; see §12.7.
- args `["indexarr", "--role", "all", "--nats-single-node"]` (`:72`).
- env `CLUSTARR_INDEX_PATH=/var/lib/clustarr/index/releases.db` (`:84-85`);
  the flag default lives in `cmd/clustarr/flags.go:48` (`indexPathEnv =
  "CLUSTARR_INDEX_PATH"`), asserted in `cmd/clustarr/deploy_args_test.go:290`.
- containerPorts `http: 8080`, `metrics: 8443`, `health: 8081` (`:86-95`).
- readinessProbe `/readyz` on `health`, with the comment (`:103-104`): "§13:
  readiness is tied to the SQLite DB being open (plus a JetStream ping)."
- `readOnlyRootFilesystem: true` (`:119`); volumes: PVC `clustarr-index` at
  `/var/lib/clustarr/index` (`:123-131`), emptyDir at `/tmp` (`:125,132`).
- Resources: 100m/256Mi requests, 1 CPU/1Gi limits (`:110-116`).

`config/default/pvc.yaml:31-48`: `clustarr-index`, RWO, 5Gi, storageClassName
deliberately unset.

`charts/clustarr/values.yaml:222-239`: `indexarr.enabled: true`,
`replicas: 1`, `facade.service.{type: ClusterIP, port: 8080}`, same resources.
`storage.index.{existingClaim, storageClass, size: 5Gi}` (`values.yaml:120-125`).
`charts/clustarr/templates/deployments.yaml:69-76` renders args
`["indexarr", "--role", "all"]`.

---

## 11. Supporting `pkg/` API already built (Phase B) — use, do not rewrite

### 11.1 `pkg/torznab`

| Symbol | Signature | File:line |
|---|---|---|
| `SearchMode` + 6 consts | `search`, `tvsearch`, `movie`, `music`, `audio`, `book` | `caps.go:38-47` |
| `Searching` | `{Available bool; SupportedParams []string; SearchEngine string}` | `caps.go:76` |
| `Caps` | `{ServerTitle string; LimitsDefault, LimitsMax int; Modes map[SearchMode]Searching; Categories []newznab.Category; Tags map[string]string}` | `caps.go:83` |
| `Caps.Supports` | `(mode SearchMode, param string) bool` | `caps.go:93` |
| `ParseCaps` | `(r io.Reader) (Caps, error)` | `caps.go:156` |
| `Client` / `NewClient` | `NewClient(baseURL, apikey string, opts ...ClientOption) (*Client, error)` | `client.go:86,94` |
| `WithTimeout` / `WithRateLimit` / `WithProxy` / `WithHTTPClient` | `ClientOption`s; `WithProxy(dial func(ctx, network, addr) (net.Conn, error))` | `client.go:46,55,65,78` |
| `defaultTimeout` | `30 * time.Second` | `client.go:40` |
| `maxResponseBodyBytes` | `8 << 20` (8 MiB) | `client.go:162` |
| `ErrResponseTooLarge` | sentinel | `client.go:166` |
| `Client.Caps` / `Client.Search` | `Caps(ctx) (Caps, error)`; `Search(ctx, q Query) ([]Release, error)` | `client.go:169,194` |
| `Query` | `{Type SearchMode; Q string; Categories []newznab.CategoryID; Season *int; Episode string; …}` | `query.go:31` |
| `Query.Values` / `Query.Validate` | `Values(apikey string) url.Values`; `Validate(caps Caps) error` | `query.go:45,103` |
| `Release` | torrent+usenet wire release; see below | `release.go:34` |
| `ParseItem` / `ParseResults` | `(r io.Reader) (Release, error)` / `([]Release, error)` | `release.go:137,146` |
| `WriteCaps` / `WriteResults` / `WriteError` | facade writers (M6) | `write.go:42,131,277` |
| `Error` / `ErrorCode` / `ParseError` | codes 100–910 incl. 500/501 | `errors.go:29-74` |

`torznab.Release` fields (`release.go:34-77`): `Title, GUID, Link, CommentURL,
PubDate, Size, Description, Categories`; torrent: `Seeders, Leechers, Peers,
Grabs, Files *int32`, `InfoHash, MagnetURL string`, `DownloadVolumeFactor,
UploadVolumeFactor, MinimumRatio *float64`, `MinimumSeedTime *int64` (seconds);
usenet/common: `Group, Poster string`, `UsenetDate *time.Time`, `Password, NFO
*int32`, `Info string`; plus `IDs map[string]string` and `Attrs
map[string][]string`.

> Carried task (remaining-work.md:1067), verbatim: "**Phase D:** map Torznab
> `DownloadVolumeFactor`/`UploadVolumeFactor` to `ReleaseInfo.IndexerFlags`
> (freeleech, halfleech, doubleupload) so TRaSH `IndexerFlag` conditions fire;
> `torznab.wireCaps.Limits` stays typed (a malformed caps doc is a typed
> error)."

### 11.2 `pkg/newznab`

| Symbol | Signature | File:line |
|---|---|---|
| `CategoryID` | `int32` | `category.go:33` |
| `Category` / `SubCategory` | `{ID, Name, Sub}` / `{ID, Name}` | `category.go:42,36` |
| `Tree()` | `[]Category` | `category.go:174` |
| `CategoryID.Parent()` | `(c/1000)*1000`, or `c` when `>= CustomCategoryOffset` | `category.go:186` |
| `Expand(ids)` | parent → parent + all subs; custom ids pass through; order preserved, dupes dropped | `category.go:196` |
| `Custom(trackerID string)` | `100000 + uint16(sha1(trackerID)[:2])` | `category.go:230` |
| `ByKind(kind)` | movie→2000, series/episode→5000, artist/album→3000, audiobook→3030, author/book→7020, comic/issue→7030; **anime 5070 is not reachable from a MediaKind — it is a search-time overlay from `Indexer.spec.animeCategories`** | `category.go:240`, doc `:236-239` |
| `CategoryMapper.Kind(ids)` | `(commonv1.MediaKind, bool)` | `mapper.go:32` |

### 11.3 `pkg/cardigann` (M6, but built)

`Definition` (`definition.go:151`), `Load([]byte) (*Definition, error)`
(`:367`), `Validate(data []byte) error` against the embedded v11 schema
(`schema.go:66`, schema JSON at `pkg/cardigann/schema.json`),
`Engine` (`engine.go:43`) with `Caps(def) (Capabilities, error)` (`:53`),
`Login(ctx, def, cfg) (*Session, error)` (`login.go:95`),
`Search(ctx, def, cfg, query Query) ([]torznab.Release, error)` (`search.go:185`),
`Download(ctx, def, cfg, link string) (io.ReadCloser, error)` (`download.go:39`).
Error types: `CloudflareChallengeError` (`engine.go:62`), `LoginError`
(`login.go:46`), `CaptchaRequiredError` (`login.go:55`).

### 11.4 `pkg/release`

| Symbol | Signature | File:line |
|---|---|---|
| `Options` | `{Kind commonv1.MediaKind; SeriesType string /* "standard"|"daily"|"anime" */}` | `types.go:27` |
| `Hints` | `{Codec, HDR, Audio []string; Channels string; Streaming []string; Container string}` | `types.go:41` |
| `ParsedRelease` | 24 fields; see below | `types.go:92` |
| `Parse` | `(title string, o Options) (*ParsedRelease, error)` | `parse.go:39` |
| `ParseKind` | `(title string, kind commonv1.MediaKind) (*ParsedRelease, error)` | `parse.go:98` |
| `ParsePath` | `(path string, o Options) (*ParsedRelease, error)` | `parse.go:115` |
| `ClassifyKind` | `(title string) commonv1.MediaKind` | `classify.go:97` |
| `Normalize` | `(title string) string` — **display only** | `normalize.go:30` |
| `CleanTitle` | `(title string) string` — **the equality function** | `normalize.go:53` |
| `MatchTitle` | `(p *ParsedRelease, cands []TitleCandidate) (best int, score float64)` | `match.go:37` |
| `Age` | `(publishedAt, now time.Time) (days, hours int32, minutes int64)` | `age.go:26` |
| `SizePerMinuteCentiMB` | `(sizeBytes int64, runtimeMinutes int32) int64` | `age.go:43` |

`ParsedRelease` (`types.go:92-116`): `Title, Titles, Year, Quality, Revision,
Languages, Group, Hash, Edition, Seasons, Episodes, Absolute, AirDate,
FullSeason, Partial, MultiSeason, Special, ReleaseType, Music, Book, Comic,
Hints, IDs`.

**No converter from `torznab.Release` + `release.ParsedRelease` →
`commonv1.ReleaseInfo` / `schema.Release` exists anywhere in the repo.** That
mapping is net-new indexarr code, and it is the single highest-leverage pure
function in D1 (testable without a cluster, per CLAUDE.md's conventions).

Mapping notes for that function:

- `ParsedRelease.Seasons/Episodes/Absolute` are `[]int`; `schema.Release`'s are
  `[]int32`.
- `ParsedRelease.Year` is `int`; `schema.Release.Year` is `int32`.
- `ParsedRelease.Hints` is a struct; `schema.Release.Hints` is
  `map[string][]string` "keyed by hint name" (`schema/index.go:66-68`). The key
  vocabulary is unspecified — **pick and document** (`codec`, `hdr`, `audio`,
  `channels`, `streaming`, `container` are the obvious ones, matching the field
  names at `release/types.go:42-47`). Note `Channels` and `Container` are
  scalars that must become one-element slices.
- `schema.Release.ParsedTitle` = `ParsedRelease.Title` (raw), **not**
  `CleanTitle` output — see §6.5.
- `schema.Release.Kind` = `release.ClassifyKind(title)` or the pinned
  `Options.Kind`.
- `ReleaseInfo.FormatScore` / `MatchedFormats` are **left zero by indexarr**;
  `pkg/decision.Evaluate` computes them (`app/catalog/worker/grab/perform.go:270-272`:
  "Decision.Release already carries the resolved FormatScore and MatchedFormats
  -- pkg/decision.Evaluate writes both onto it before scoring").
- `ReleaseInfo.PublishedAt` is `*metav1.Time` and must stay nil when the
  indexer reported no pubDate — see the 12-line doc at `release_types.go:95-108`.

### 11.5 `pkg/ratelimit`

`Config` (`limiter.go:29`), `Limiter` (`:38`), `New(defaults Config) *Limiter`
(`:47`), `Backoff` (`backoff.go:28`), `CircuitBreaker` / `BreakerConfig` /
`BreakerState` / `NewCircuitBreaker(cfg, clock clockwork.Clock)`
(`breaker.go:28,50,59,75`).

---

## 12. Ambiguities, self-contradictions, and spec-vs-code conflicts

Phase C's lesson was that each of these costs a fix round. Eight found.

### 12.1 `DefaultIndexPath` contradicts every manifest — **live conflict**

`app/indexer/run.go:52` sets `DefaultIndexPath = "/index/releases.db"`, but:

- `config/manager/indexarr.yaml:84-85` sets
  `CLUSTARR_INDEX_PATH=/var/lib/clustarr/index/releases.db`
- the PVC is mounted at `/var/lib/clustarr/index` (`indexarr.yaml:124`)
- `readOnlyRootFilesystem: true` (`:119`) makes `/index` **unwritable**
- `cmd/clustarr/deploy_args_test.go:290` asserts the env-var value, so the env
  var is the operative default in a deployed pod

Only the compiled-in fallback is wrong, and only for someone running the binary
with no env var and no flag — e.g. `clustarr all` on a dev box. Decide: change
the constant to `/var/lib/clustarr/index/releases.db`, or document that the
constant is a never-used fallback. Do not leave two answers.

### 12.2 Facade port: `:9696` in code, `8080` everywhere else — **live conflict**

`app/indexer/run.go:56`: `DefaultFacadeBindAddress = ":9696"` (Prowlarr's port).
But `config/manager/indexarr.yaml:28-30,87-89` and
`charts/clustarr/values.yaml:229` both use **8080**, and the Service's
`targetPort` is the named port `http` → containerPort 8080. Nothing passes a
`--facade-bind-address` flag (no such flag is wired in `cmd/clustarr`; grep
found only the field, `app/indexer/run.go:96-97`). As shipped, the facade would
bind 9696 while the Service routes 8080 — the facade would be unreachable.
The facade is M6, so this can be fixed in D1 cheaply or deferred **with a
recorded decision**.

### 12.3 Grab accounting has three inconsistent owners and **no consumer**

- §5 subject table (`design.md:555`): `evt.catalog.release.grabbed` →
  "catalogarr → history, **indexarr (grab accounting)**"
- §5 RPC table (`design.md:574`): `rpc.indexarr.download` → "grabarr → indexarr
  (uses session cookies/passkeys, **counts GrabLimit**)"
- §8.3 (`design.md:747`): "**`grabarr` records the grab** against
  `clustarr-indexer-limits` via `evt.catalog.release.grabbed` **consumed by
  indexarr**" — a sentence that names grabarr as the recorder and indexarr as
  the consumer of an event catalogarr actually publishes.

And **there is no durable consumer for indexarr on `CLUSTARR_EVENTS`**:
`defaultConsumers()` (`topology.go:436-570`) has exactly one consumer on
`StreamEvents`, `catalogarr-history` (`:494-500`). If indexarr is to consume
`clustarr.evt.catalog.release.grabbed.>`, a new `ConsumerSpec` (and a
`Consumer*` constant) must be added to `pkg/events/topology.go` and
`subjects.go`. That is a change to shipped shared infrastructure — call it out
as its own task.

The simplest consistent reading, and the one the RPC table supports: **indexarr
increments `GrabLimit` itself inside `rpc.indexarr.download`**, and the event
consumption is redundant. But a grab whose `DownloadSource` is `torrentURL` /
`magnetURL` / `nzbURL` never touches `rpc.indexarr.download`
(`api/download/v1alpha1/download_types.go:235`), so those grabs would go
uncounted. Decide explicitly and write it down.

### 12.4 `rpc.indexarr.query` has no caller and no CRD field to select it

§5 (`design.md:575`) says the callers are "Search CR (query mode), Torznab
facade". But:

- `SearchSpec` (`api/catalog/v1alpha1/search_types.go:131-173`) has
  `Query *string`, `MediaRef`, `Kinds`, `IndexerRefs`, `Categories`, `Limit`,
  `Grab`, `Override`, `TTL` — **no mode/fresh switch** that would route to the
  index instead of the live fan-out.
- `grep` finds **zero** references to `events.RPCIndexQuery` outside
  `pkg/events/subjects.go:158`.
- The Torznab facade is M6.

So `rpc.indexarr.query` in M2 is a server with no client. Either serve it anyway
(cheap, and the e2e can exercise it directly), or defer it to M6 with the
facade — but §16's M2 line (`design.md:840`) explicitly names
"`rpc.indexarr.search|download|query`", so **serving it is in M2 scope**. The
"query mode" CRD field is the part that does not exist.

### 12.5 `Caps.Modes` key vocabulary is documented wrong

`indexer_types.go:226-227` and `indexerdefinition_types.go:61-62` both say the
keys are "search, tv-search, movie-search, ...". The real Torznab values, which
`pkg/torznab` implements and `torznab.Caps.Supports` compares against, are
`search`, `tvsearch`, `movie`, `music`, `audio`, `book`
(`pkg/torznab/caps.go:41-47`). There is no enum marker on `Caps.Modes`, so
nothing catches the divergence. **Use `torznab.SearchMode`'s strings** and fix
the two doc comments in the same change, or the caps-gating predicate will
silently never match.

### 12.6 One field manager, two writer paths — the Phase C trap, again

§2 (`design.md:42`) lists **`indexarr`** and no `indexarr-worker`.
`pkg/k8s/FieldManager.Validate()` (`fieldmanager.go:178`) rejects any name not
in that list. But indexarr has two status-writing paths onto the *same object*:

- the `indexer` reconciler → `conditions`, `protocol`, `privacy`, `caps`,
  `observedGeneration`, `sessionSecretRef`, and the escalation fields
- the RSS worker and the search service → `lastRssAt`, `lastRssNewCount`,
  `indexedReleases`, `queriesInWindow`, `grabsInWindow`, and also the escalation
  fields (`RecordSuccess`/`RecordFailure` live in the search fan-out per §6.2)

CLAUDE.md's hard-won rule applies verbatim: "**Server-side apply replaces a
field manager's ownership set on every apply — it does not merge.** Any field
that manager previously sent and now omits is *released*, which reads as 'reset
to zero' on the object." Two paths under one manager name will release each
other's fields — exactly the `catalogarr-metadata` / `catalogarr-grab` failure
(`design.md:42`, `pkg/k8s/fieldmanager.go:65-94`).

Three ways out, pick one **before writing the controller**:
1. Funnel every status write through one goroutine/reconciler that builds one
   complete apply configuration (possible because indexarr is one replica and
   one process).
2. Add `indexarr-worker` to §2, `pkg/k8s.FieldManagers()` and the spec, and
   split the fields explicitly.
3. Keep one manager and make every single apply a complete declaration of all
   16 status fields.

CLAUDE.md also warns: "Tests miss all three unless they act on an object that
**already has status**." Any envtest here must start from a populated status.

### 12.7 `indexarr-rss` AckWait (120s) exceeds the pod's grace period (60s)

`topology.go:535` sets `AckWait: 120 * s` for `ConsumerIndexRSS`;
`config/manager/indexarr.yaml:56` sets `terminationGracePeriodSeconds: 60`.
The importarr consumers document the rule this violates
(`topology.go:501-510`), verbatim:

> AckWait is 60s on all three, which is the floor set by
> `terminationGracePeriodSeconds: 60` … A worker that is SIGTERMed must be able
> to finish or give up an in-flight message inside the grace period, or the pod
> is killed mid-task and the message is only redelivered after AckWait expires.
> Any unit of work that can outlast 60s … must send in-progress acks rather than
> have its AckWait raised past the grace period, and the manifest and this value
> must be changed together.

An RSS poll of a slow indexer can plausibly exceed 60s. `ConsumerIndexRSS` has
`Heartbeat: 0` (unset, `topology.go:532-538`), so there is no in-progress ack
today. Either raise the grace period to ≥120s in `config/manager/indexarr.yaml`
and the chart, or drop AckWait to 60s and heartbeat. Note this AckWait matches
spec §5's table (`design.md:592`), so changing it is a spec edit.

### 12.8 Research note §10 recommends Postgres; ADR-0003 chose SQLite

`docs/research/indexers.md:396` marks Postgres FTS "**best fit** for a
distributed K8s service" and `:399-404` sketches the whole Postgres schema,
TTL policy (24h/2h) and a `release_cache` DDL. That note **predates the
decision**. `docs/adr/0003-release-index-sqlite-fts5.md` and `design.md:621`,
:796, :868 all pin SQLite FTS5 (modernc) on RWO, with Postgres named as the
documented scale-out path behind the `Store` interface. **The ADR wins.** Mine
the research note for the *column list*, the *dedup-at-query-time rule*
(`indexers.md:402`) and the *query-cache key* (`:403`), not for the engine or
the 24h/2h TTLs.

### Smaller items worth a line in the plan

- **`IndexerDefinition` is Cluster-scoped** (`indexerdefinition_types.go:118`)
  while `Indexer.spec.definitionRef` is a bare `*string`
  (`indexer_types.go:89`) resolved from a namespaced object. That is consistent
  (cluster-scoped lookup by name), but note the asymmetry against
  `spec.proxyRef`, which is explicitly "an IndexerProxy **in the same
  namespace**" (`:155`).
- **`IndexerDefinitionStatus.Caps` is a value, not a pointer**
  (`indexerdefinition_types.go:112`), unlike `IndexerStatus.Caps *Caps`
  (`indexer_types.go:273`). Two different nil semantics for the same concept.
- **`SearchOutcome.ElapsedMillis int64` → `IndexerOutcome.DurationMs int32`** —
  clamp on the catalogarr side (§5.1).
- **`SearchRequest` has no `Absolute` field** and no "fresh/bypass-cache" flag,
  though §8.2 describes "tvdb plus season/episode/absolute". The absolute case
  is already resolved by convention at
  `app/catalog/worker/search/request.go:59-64` (absolute goes in `Episode`,
  `Season` nil). **The generated payload type wins.**
- **No production code calls `events.ReleaseSubject` yet** — only tests
  (`pkg/events/events_test.go:193`, `pkg/events/membus/membus_test.go:93,97`).
  D1 is its first caller.
- **Spec §5's stream table omits `CLUSTARR_WORK_IMPORTARR`**
  (remaining-work.md:1128): "the implemented topology wins; bring the table up
  to date." Same class of drift; do not treat the table as exhaustive.
- **`clustarr_search_indexer_duration_seconds{indexer,outcome}`** in §13
  (`design.md:804`) does not exist. The amendment's A2.3 (`amendment:269-271`)
  replaced it with `clustarr_indexer_query_duration_seconds{indexer,function}`
  + `clustarr_indexer_queries_total{indexer,outcome}` +
  `clustarr_indexer_releases_returned{indexer}`, which is what
  `pkg/obs/metrics/domain.go:125-151` implements. **The amendment wins.**

---

## 13. Where the amendment overrides the base design

The amendment (`docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md`)
says almost nothing about indexarr directly. Three places matter:

1. **Metrics (A2.3, `amendment:250-287`) overrides §13's metric list**
   (`design.md:804`). The three indexer metrics above are authoritative, and
   `:258` restates the cardinality rule: "**Never** label by media title, file
   path or release name; those are unbounded."
2. **Readiness (A2.4, `amendment:289-295`) refines §13.** Verbatim: "`/healthz`
   is liveness and must not depend on anything external, or a broker blip
   restarts every pod. `/readyz` includes the dependencies the service genuinely
   needs: NATS connectivity for every service that consumes work, **the index
   volume for `indexarr`**, `/data` writability for services that touch files."
   Note the wording is "the index **volume**"; §6.2/§13 say "the DB **open**";
   the manifest comment (`indexarr.yaml:103-104`) and `run.go:185-187` both say
   "the SQLite handle being open". **Use the DB handle** — it subsumes the
   volume.
3. **A4's milestone table (`amendment:424-431`) does not touch M2.** M2 is
   unchanged by the amendment. (A4 moves import lists and completed-download
   import to `importarr` at M3/M6, and stages the UI — none of it indexarr.)

`amendment:236` also names indexarr work as a span site: "Spans wrap: every
`Reconcile` call, every work-queue handler, **every outbound HTTP call to an
indexer** or metadata provider, every ffmpeg invocation, and every filesystem
import."

Nothing in the amendment contradicts §6.2. Where §13 and A2.3/A2.4 disagree on
metric names and readiness wording, **A2 wins**.

---

## 14. Testing obligations

### §14 (design.md:808-813) — the clauses that name indexarr

- Unit/golden (`:808`): "pkg/cardigann (whole bundled corpus parses in CI; 1337x
  HTML + UNIT3D JSON fixtures vs cardigann-go/Prowlarr oracle; 25 filters; date
  translator), pkg/torznab (XML round-trips, error codes)" — **done in Phase B.**
- Engine integration, build-tagged (`:811`): "**SQLite store (dedup, FTS, TTL
  sweep)**" — new in D1.
- E2E (`:812`): "chart with NATS R1, hostPath RWX, **fixture Torznab server** +
  seeder pod (30 s H.264 clip with embedded English subtitle) … apply
  providers/**Indexer**/DownloadClient/RootFolder/Movie". Scenarios 2–4:
  "**RSS upgrade** (old file in `.recycle`), failed download → blocklisted →
  redownload, idempotency rerun".
- CI gates (`:813`): "**Cardigann corpus sync + schema validation**".

### Phase-plan obligations (remaining-work.md)

`:875-879`, verbatim:

> - **D1 — `indexarr` (M2).** Generic Torznab/Newznab, caps, health, backoff,
>   the SQLite FTS5 release index, the RSS worker, and the three RPC verbs
>   `rpc.indexarr.search|download|query`. **Lands e2e scenarios 2, 3 and 4.**
>   This is what makes Phase C's search path live: `app/catalog/worker/search`
>   already calls `rpc.indexarr.search` and today reaches no server.

`:887-888`: "The fixture image grows in the phase that needs it: **the
Torznab/Newznab fixture indexer in D1**, the seeder and NNTP stub in D2."

Scenario texts (`:968-973`):

> 2. **RSS upgrade.** A better release appears in RSS → upgrade grabbed and
>    imported → the old file is in `.recycle`.
> 3. **Failed download → Blocklist → redownload.**
> 4. **Idempotency.** `kubectl rollout restart` of every controller mid-flight,
>    then a rerun of scenario 1's inputs: no duplicate Download, TranscodeJob or
>    SubtitleRequest; no duplicate files.

> **Note:** scenarios 2 and 3 as written require *grabarr* (D2) to complete a
> download and importarr to import. D1 alone cannot make "imported" true. The
> plan must either scope D1's slice of 2/3/4 down to the parts that end at
> `Download` creation, or accept that 2 and 3 land in D2. The split note
> (`:880-883`) assigns "scenarios 1 (through import) and 6" to D2, which
> reinforces that 2 and 3 cannot fully pass in D1. **Flag and resolve.**

Existing e2e harness (`test/e2e/`): `main_test.go`, `helpers_test.go`,
`libraryscan_test.go`, `mediafile_test.go`, `series_test.go`.
Existing fixtures (`test/fixtures/`): `seed/`, `tmdbstub/`, `tvdbstub/`, with
`main.go`, `root.go`, `seed_cmd.go`, `tmdbstub_cmd.go`, `tvdbstub_cmd.go`.
A Torznab fixture is net-new.

### Carried Phase-D registration debt that will bite indexarr

From `remaining-work.md:1101-1108`:

- ":1103" The registration guard's `runnableServices` list is hand-maintained.
- ":1104" The guard only catches types with **both** `Start` and
  `NeedLeaderElection`; a bare `manager.RunnableFunc` — "precisely the shape
  that deadlocked every catalogarr and importarr rollout" — is **not** caught.
  indexarr's search responder, RSS worker, store sweeper and facade are all new
  runnables. **Use `k8s.EveryReplica`** (as `rssmatcher.SetupWithManager` does,
  `handler.go:138`), never a bare `RunnableFunc`.
- ":1106" "controller-runtime's controller-name registry is **process-global**,
  so `clustarr all` works only because catalogarr's and importarr's controller
  names are disjoint … **Phase D's new controllers must keep them disjoint or
  `clustarr all` stops starting.**" Name them `indexer`, `indexerdefinition`,
  `indexerproxy` — none collide with existing names.
- ":1113" The guard also cannot see an inline `k8s.EveryReplica` closure.
- ":1059" "Still outstanding: **the release index for `indexarr`** (Phase D) …
  reporting ready early lets the controller hand an engine work it would
  double-download."

### CLAUDE.md gates that apply

- `make test` (not `go test ./...`) — "`pkg/crdcheck` and the envtest suites
  skip silently when `KUBEBUILDER_ASSETS` is unset."
- `make build`, never bare `go build`.
- GPL-3.0 header from `hack/boilerplate.go.txt` on every new file.
- `slog` through context via `pkg/obs/logging.FromContext`; no package-level
  logger.
- Spans on every `Reconcile`, work handler and outbound provider call.
- Every HTTP response body read through a cap with an `ErrResponseTooLarge`
  sentinel — `pkg/torznab` already does this (`client.go:162-166`).
- `+kubebuilder:validation:MaxItems` on every status list — `Caps.Categories`
  and `Category.Sub` already have 200.
