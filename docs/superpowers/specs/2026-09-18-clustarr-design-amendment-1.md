# Clustarr — Design Amendment 1

Status: accepted, 2026-09-18. Amends `2026-09-18-clustarr-design.md`.

The project brief gained three requirements after the base design was approved:
a separate importer service, a defined observability stack, and a web UI. Two of
them reverse decisions in the base design, so they are recorded here rather than
edited silently into it. Where this amendment and the base design disagree, this
amendment wins.

| Change | Base design said | Now |
|---|---|---|
| Importer | Library scanning, import lists and completed-download import were workers inside `catalogarr` | Their own service, `importarr` |
| Web UI | Explicit non-goal for v1 | In scope: `ui`, server-rendered with templ |
| Observability | One short section: metrics, probes, a trace header | A defined stack: slog through context, OpenTelemetry spans, a documented metric catalogue |

---

## A1. importarr

### A1.1 Why it is separate

The brief asks for a service that reconciles media already on disk into custom
resources, and reconciles import lists into custom resources on a schedule. The
base design had both as workers inside `catalogarr`, and all three judges flagged
`catalogarr` as the overloaded component. Splitting them out is the fix the
judges asked for, so the requirement and the review agree.

`importarr` owns **every path by which something enters the library**, which is
the coherent boundary:

1. **Library rescan.** Walk a `RootFolder`, identify media files, and upsert the
   matching catalog resource plus a `MediaFile` for each file found.
2. **Import lists.** On a schedule, fetch wanted items from Trakt, Plex and the
   rest, and upsert catalog resources for them.
3. **Completed-download import.** Move or hardlink a finished download out of the
   download directory into its root folder, then create the `MediaFile`.

All three end the same way: files land in a root folder and resources appear in
the catalog. `catalogarr` keeps what is genuinely its own — the media domain
model, metadata, and release decisions.

### A1.2 Naming hazard

"Import" means two different things in the *arr world: importing a finished
download into the library, and import lists of things you want. Both now live in
`importarr`, which removes the ambiguity rather than creating it. The base
design's `app/catalog/worker/importer` is **deleted**; its logic moves to
`app/import/worker/fileimport`. Nothing named "importer" remains in `catalogarr`.

### A1.3 Ownership

Controller ownership moves; the API groups do not. `ImportList` and
`ImportExclusion` stay in `catalog.clustarr.io` because they are catalog-domain
data, but their sole controller-writer becomes `importarr`. This keeps the
single-writer rule intact without regenerating 28 CRDs.

| Resource | Group | Controller owner |
|---|---|---|
| `ImportList` | `catalog.clustarr.io` | `importarr` (was `catalogarr`) |
| `ImportExclusion` | `catalog.clustarr.io` | `importarr` (was `catalogarr`) |
| `LibraryScan` | `catalog.clustarr.io` | `importarr` (**new kind**) |
| `MediaFile` | `catalog.clustarr.io` | `importarr` creates; `catalogarr` still reconciles it |
| `Download.status.import` | `download.clustarr.io` | `importarr` (was `catalogarr`) |

`MediaFile` needs care: it was a `catalogarr`-owned resource and the single-writer
rule forbids two controllers writing one object's status. The split is by
**field manager, on disjoint fields**, which the base design already permits
within a service and now permits across these two:

- `importarr` creates `MediaFile` and owns `status.file` (path, size,
  fingerprint) and `status.probe` (the ffprobe result).
- `catalogarr` owns `status.quality`, `status.formatScore` and the conditions,
  because those are decisions, not observations.

Both write through `pkg/k8s.PatchStatus` with their own field manager, so the
apiserver enforces the split instead of convention.

> **Erratum (2026-09-18, Phase C).** The two bullets above describe fields that
> were never shipped. `MediaFileStatus` is flat — `observedGeneration`,
> `conditions`, `probeHash`, `probedAt`, `mediaInfo`, `sidecars`, `transcode` —
> with no `file` and no `probe`; and `quality`, `formatScore`, `revision`,
> `releaseType`, `matchedFormats` and `profileHash` are all **spec** fields,
> frozen at import, exactly as the base design's §8.4 says. The error survived
> from Phase A because nothing reconciled yet, so no code ever tried to write
> the named fields.
>
> The intent stands and the discipline is unchanged; only the boundary moves.
> The real split is **spec versus status**: `importarr` creates the resource and
> owns `MediaFileSpec`; `catalogarr` is the sole writer of all of
> `MediaFileStatus`, and takes over `spec.sizeBytes`, `spec.modTime` and
> `spec.original` once it incorporates a transcode swap (those fields' own doc
> comments and §8.5). Both still write with their own field manager, and the
> two-writer envtest this amendment demands still gates it — against the real
> split.
>
> **Gap fix X5a (ruling R-11), 2026-09-23:** catalogarr also takes over
> `spec.path` when a transcode lands under a new name and the source is gone
> (a container change, or an explicit `spec.outputPath`), in the same apply as
> the other three. A `replaceSource=false` result is not a swap and claims no
> spec field. After a transcode the rescan never re-applies `MediaFileSpec`; it
> reports changed bytes only through the `catalog.clustarr.io/observed-fingerprint`
> annotation (base design §8.5).

### A1.4 New kind: LibraryScan

A short-lived resource, like `Search`, so a scan is observable with `kubectl` and
so progress survives a restart.

```go
// LibraryScanSpec asks importarr to walk a root folder and reconcile what it finds.
type LibraryScanSpec struct {
    // RootFolderRef names the RootFolder to scan.
    RootFolderRef commonv1.LocalRef `json:"rootFolderRef"`

    // Mode selects how much of the tree is examined. Incremental skips files
    // whose size and mtime match a known MediaFile fingerprint.
    // +kubebuilder:validation:Enum=full;incremental
    // +kubebuilder:default=incremental
    Mode ScanMode `json:"mode,omitempty"`

    // Subpath restricts the scan to one path beneath the root folder: a
    // directory, whose tree is walked, or a single file, which is the only
    // file visited. A file subpath together with the
    // catalog.clustarr.io/import-target annotation is how a file an earlier
    // scan left unmatched is assigned to an item by hand.
    // +optional
    Subpath string `json:"subpath,omitempty"`

    // DryRun reports what would change without creating or updating anything.
    // +optional
    DryRun bool `json:"dryRun,omitempty"`

    // TTLSecondsAfterFinished deletes the scan once it has settled.
    // +kubebuilder:default=3600
    TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// LibraryScanStatus reports progress. All lists are capped per the §4 legend.
type LibraryScanStatus struct {
    Phase          ScanPhase          `json:"phase,omitempty"`
    StartedAt      *metav1.Time       `json:"startedAt,omitempty"`
    FinishedAt     *metav1.Time       `json:"finishedAt,omitempty"`
    FilesSeen      int64              `json:"filesSeen,omitempty"`
    FilesMatched   int64              `json:"filesMatched,omitempty"`
    ItemsCreated   int64              `json:"itemsCreated,omitempty"`
    ItemsUpdated   int64              `json:"itemsUpdated,omitempty"`
    FilesSkipped   int64              `json:"filesSkipped,omitempty"`
    // Unmatched lists files the scanner could not attribute, newest first.
    // +kubebuilder:validation:MaxItems=200
    Unmatched      []UnmatchedFile    `json:"unmatched,omitempty"`
    Conditions     []metav1.Condition `json:"conditions,omitempty"`
}
```

`RootFolder` gains `spec.scanSchedule` (a cron expression, optional) so periodic
rescans are declarative; `importarr` creates a `LibraryScan` per tick.

### A1.5 Matching files to items

The scanner is the one place that guesses, so it must never guess silently. For
each candidate file:

1. Parse the path and filename with `pkg/release`, and probe with
   `pkg/mediainfo`.
2. Look for an embedded identifier first: the `{imdbid-tt...}` or `{tmdbid-...}`
   token the naming scheme writes into folder names, then an `.nfo` sidecar.
3. Otherwise resolve by normalized title plus year against existing catalog
   items, then against the metadata gateway.
4. On a confident match, upsert the item and create the `MediaFile`. On an
   ambiguous or failed match, record it in `status.unmatched` with the reason and
   the candidates considered. **Never** create a speculative item from a low
   confidence match.

Unmatched files are surfaced in the UI for manual resolution rather than being
silently dropped, which is the failure mode that makes library imports painful in
the existing tools.

### A1.6 Process topology

| Deployment | Roles | Replicas |
|---|---|---|
| `importarr` | controllers (`ImportList`, `ImportExclusion`, `LibraryScan`, `RootFolder` schedule) | 1, leader-elected |
| `importarr-worker` | `work.importarr.scan`, `work.importarr.list`, `work.importarr.fileimport` consumers | 2, scalable |

Both mount the RWX `/data` volume. The worker uses the media image because it
hardlinks, probes and moves files.

*As built:* only `importarr-worker` mounts `/data`; the controller Deployment
does not, which is why the recycle-bin sweeper (`fileimport.RecycleSweeper`,
`RootFolder.spec.recycleBin.cleanupDays`, gap fix X7a) runs on the worker role.
The rescan attributes files under a series root folder to Series and Episodes
(gap fix X7a). A series folder carrying a TheTVDB id no Series has creates that
Series, pinned to the folder (`spec.folder`) and with neither add-time search,
as rule 4 above upserts a movie; until the Series controller fans out its
episodes its files are counted as awaiting them, not listed as unmatched, and a
later scan attributes them. A `RootFolder` schedule tick waits while an earlier
scan of the same folder is still Pending or Running (CronJob's `Forbid`).
`status.filesSkipped` counts only recorded media files the scan did not write;
parts, extras, samples and non-media files are counted by kind in the worker's
KV progress and rendered into the Ready condition's message.

Stream `CLUSTARR_WORK_IMPORTARR`, work-queue retention, subjects
`work.importarr.scan.<rootfolder>`, `work.importarr.list.<importlist>`,
`work.importarr.fileimport.<download>`. A scan of a large library is chunked by
directory so one message is not a multi-hour unit of work.

---

## A2. Observability

Three packages under `pkg/obs`, used by every service. None of this is optional:
a distributed pipeline that cannot be traced is not debuggable.

### A2.1 Logging: slog through context

```go
package logging

// FromContext returns the logger stored in ctx, or a discard logger.
func FromContext(ctx context.Context) *slog.Logger

// NewContext returns a copy of ctx carrying logger.
func NewContext(ctx context.Context, logger *slog.Logger) context.Context

// With returns a context whose logger has the given attributes attached.
func With(ctx context.Context, args ...any) context.Context

// New builds the root logger from Options{Level, Format json|text, AddSource}.
func New(opts Options) *slog.Logger
```

No package-level logger, no global default, no logger fields on structs: the
logger travels in the context, which is the dependency-injection pattern the
brief asks for. Handlers and reconcilers enrich it once at entry, so every line
below carries the request's identity without being passed it explicitly.

controller-runtime speaks `logr`, not `slog`. Bridge once at startup with
`logr.FromSlogHandler(handler)` and hand that to `ctrl.SetLogger`, so
controller-runtime's own output joins the same stream in the same format.
The reverse bridge, `logr.ToSlogHandler`, is available where a library hands back
a `logr.Logger`.

Every log line emitted while handling a traced operation includes `trace_id` and
`span_id`, added by the context enrichment, so logs and traces join without a
correlation step.

### A2.2 Tracing: OpenTelemetry

```go
package tracing

// Setup installs a TracerProvider exporting over OTLP and returns a shutdown func.
func Setup(ctx context.Context, opts Options) (shutdown func(context.Context) error, err error)

// Start begins a span and returns a context carrying both the span and a logger
// enriched with its ids.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span)
```

Spans wrap: every `Reconcile` call, every work-queue handler, every outbound HTTP
call to an indexer or metadata provider, every ffmpeg invocation, and every
filesystem import.

Propagation across NATS uses the `Clustarr-Trace` header that already exists in
`pkg/events` and already carries a W3C `traceparent`. The bus gains an injector
on publish and an extractor on receive, so a trace that starts at "user wants
this movie" continues through search, grab, download, import, transcode and
subtitle fetch as one trace across five services. This is the single most
valuable thing the observability stack buys: the base design's event flow is
otherwise very hard to follow in production.

Sampling defaults to parent-based with a configurable ratio; errors are always
sampled.

### A2.3 Metrics: Prometheus

Registered into controller-runtime's registry so one `/metrics` endpoint serves
both the controller-runtime built-ins and the domain metrics. Exposed per service
and scraped by the `ServiceMonitor` overlay.

Naming follows the Prometheus conventions: `clustarr_` prefix, base units
(bytes, seconds), `_total` on counters, and labels of bounded cardinality.
**Never** label by media title, file path or release name; those are unbounded.

| Metric | Type | Labels | Why it matters |
|---|---|---|---|
| `clustarr_download_bytes_total` | counter | `protocol`, `client` | Throughput and volume over time |
| `clustarr_download_speed_bytes_per_second` | gauge | `protocol`, `client` | The live speed the brief asks for |
| `clustarr_downloads_active` | gauge | `protocol`, `client` | Queue occupancy |
| `clustarr_download_duration_seconds` | histogram | `protocol`, `outcome` | How long grabs take, and stalls |
| `clustarr_downloads_completed_total` | counter | `protocol`, `outcome` | Success and failure rates |
| `clustarr_import_files_total` | counter | `kind`, `mode`, `outcome` | Files entering the library, by hardlink/copy/move |
| `clustarr_import_unmatched_total` | counter | `kind`, `reason` | Scanner guesses that failed, the manual-work signal |
| `clustarr_indexer_query_duration_seconds` | histogram | `indexer`, `function` | Which indexers are slow |
| `clustarr_indexer_queries_total` | counter | `indexer`, `outcome` | Failures, rate limits and bans |
| `clustarr_indexer_releases_returned` | histogram | `indexer` | Whether an indexer is actually useful |
| `clustarr_indexer_releases_dropped_total` | counter | `indexer` | Releases the local index refused, so a garbage feed is visible rather than silently thinned (added in Phase D1) |
| `clustarr_search_decisions_total` | counter | `kind`, `decision`, `reason` | Why releases are rejected, the top support question |
| `clustarr_metadata_cache_hits_total` | counter | `tier`, `outcome` | Whether the gateway is actually sparing the providers (added in Phase C) |
| `clustarr_transcode_jobs_active` | gauge | `tier` | CPU versus GPU occupancy |
| `clustarr_transcode_duration_seconds` | histogram | `tier`, `resolution`, `outcome` | Encode cost per class of file |
| `clustarr_transcode_speed_ratio` | gauge | `tier` | Encode speed against real time |
| `clustarr_transcode_size_ratio` | histogram | `tier`, `resolution` | The space actually saved, the point of the service |
| `clustarr_subtitle_fetches_total` | counter | `provider`, `language`, `outcome` | Provider health and quota burn |
| `clustarr_provider_quota_remaining` | gauge | `provider` | Daily limits before they bite |
| `clustarr_work_queue_pending` | gauge | `stream`, `consumer` | Backpressure, and the KEDA scaling input |
| `clustarr_work_handled_total` | counter | `consumer`, `outcome` | Throughput and dead-letter rate |
| `clustarr_work_duration_seconds` | histogram | `consumer` | Which handlers are slow |
| `clustarr_reconcile_errors_total` | counter | `controller` | Alongside controller-runtime's own |

`docs/observability.md` documents this catalogue, the trace layout, and example
queries. The brief asks for useful metrics to be discovered and documented, so
the table above is the starting point and the document is where it grows.

### A2.4 Health and readiness

Already required by the base design; made concrete. `/healthz` is liveness and
must not depend on anything external, or a broker blip restarts every pod.
`/readyz` includes the dependencies the service genuinely needs: NATS
connectivity for every service that consumes work, the index volume for
`indexarr`, `/data` writability for services that touch files.

*As built:* `catalogarr` also waits for its informer caches, grabarr's engines
for re-attach, and the `ui` for its cache sync **and** its first completed
projection round (gap fix X14), so a fresh ui pod never reports Ready while it
would serve empty pages. Every readiness runnable runs on every replica, not
only the leader.

---

## A3. ui

### A3.1 Shape

A server-rendered Go web application: `github.com/a-h/templ` v0.3.1020 for
templates compiled to Go, `github.com/axadrn/shadcn-templ` v1.13.2 for the
component set, Tailwind for styling, htmx for interaction, and server-sent events
for live updates. No separate frontend build pipeline beyond the templ generator
and Tailwind, and no client-side framework: the data model is Kubernetes
resources, and HTML over the wire suits that far better than a JSON API plus a
single-page app.

Directory `ui/`, binary role `clustarr ui`, distroless static image, one
Deployment, replicas configurable because it holds no authoritative state.

### A3.2 Where its data comes from

Two sources, both already present:

1. **A controller-runtime cache** over the catalog, download, transcode and
   subtitle groups, giving an in-memory, always-current view of every resource
   without polling the apiserver per request.
2. **NATS progress subjects** for the sub-second numbers that are deliberately
   not in resource status, because writing download speed to etcd every second
   would be abusive. `progress.download.<id>` and `progress.transcode.<id>`
   already exist in the base design.

The UI **never writes status**, holds no field manager, and owns no CRD. User
actions create or patch spec: "search now" creates a `Search`; "rescan" creates a
`LibraryScan`; "monitor this" patches `spec.monitored`. Everything the UI can do,
`kubectl` can do, which keeps the GitOps story honest.

> **Note (2026-09-23, Phase G, Task G3-5).** "Holds no field manager" is
> superseded by Phase G ruling R2 (`docs/superpowers/plans/2026-09-23-phase-g-parity.md`):
> the UI's spec edits are made as field manager `clustarr-ui`
> (`pkg/k8s.ManagerUI`, restated as `ui/actions.FieldManager`), so an audit of
> `managedFields` can tell a user's edit from a controller's. It holds that
> manager on **spec only** — never on status, which it still never writes.
> Every UI write lives in `ui/actions`: "search now" and "rescan" create; the
> Unmatched page's manual assignment creates an annotated `LibraryScan`; "monitor
> this" and the Settings page's forms are JSON merge patches of a few spec
> fields. `cmd/clustarr`'s start envtest performs a UI action through a running
> `clustarr ui` and asserts `clustarr-ui` owns the spec leaf and nothing on
> status. The rest of this paragraph stands.

### A3.3 The pipeline projection

The brief's pipeline page needs one stage per item, but an item's progress is
spread across resources owned by five services. A new package `pkg/pipeline`
computes that projection and is the UI's only view of it:

```go
// Stage is a coarse, ordered position in the end-to-end flow.
type Stage string

const (
    StageMetadataSearching Stage = "MetadataSearching"
    StageMetadataFound     Stage = "MetadataFound"
    StageMetadataSynced    Stage = "MetadataSynced"
    StageReleaseSearching  Stage = "ReleaseSearching"
    StageReleaseSelected   Stage = "ReleaseSelected"
    StageDownloading       Stage = "Downloading"
    StageDownloaded        Stage = "Downloaded"
    StageImporting         Stage = "Importing"
    StageImported          Stage = "Imported"
    StageSubtitleSearching Stage = "SubtitleSearching"
    StageSubtitleFound     Stage = "SubtitleFound"
    StageSubtitleFetching  Stage = "SubtitleFetching"
    StageSubtitleDone      Stage = "SubtitleDone"
    StageTranscoding       Stage = "Transcoding"
    StageTranscodeDone     Stage = "TranscodeDone"
    StageComplete          Stage = "Complete"
    StageFailed            Stage = "Failed"
    StageBlocked           Stage = "Blocked"
)

// Entry is one row on the pipeline page.
type Entry struct {
    Ref        commonv1.ObjectRef
    Kind       commonv1.MediaKind
    Title      string
    Stage      Stage
    Percent    int32          // -1 when the stage has no meaningful percentage
    ETA        *time.Duration
    Detail     string         // e.g. the indexer being queried, or the encoder tier
    Since      time.Time
    Failure    string         // populated only in StageFailed or StageBlocked
}

// Project derives the current stage of one item from its resources.
func Project(item client.Object, related Related) Entry
```

`Project` is pure: resources in, an `Entry` out. That makes the trickiest part of
the UI unit-testable without a cluster or a browser, which matters because stage
derivation is where this kind of page usually rots.

The mapping from the brief's stages to observable state is explicit: metadata
stages come from the item's `MetadataReady` condition and `status.metadata`;
release stages from an active `Search` and `status.lastSearchResult`; download
stages from the owning `Download` phase and its progress subject; import from
`Download.status.import`; subtitle stages from `SubtitleRequest` phases; and
transcode from `TranscodeJob` phase plus its progress subject.

### A3.4 Pages

| Page | Content | Live updates |
|---|---|---|
| Pipeline | One row per in-flight item, with its stage, a progress bar and an estimate. Filter by kind and stage. | SSE per row |
| Library | Every collected and monitored item as a poster grid with a collection-status indicator, a detail modal, and a toolbar for filtering and bulk operations (monitor, unmonitor, search, delete) | SSE on status change |
| Downloads | The queue per client, with speed and estimated completion per item, and actions to pause, remove and blocklist | SSE per item |
| Import lists | Each list, its schedule, last sync, item counts, and the Trakt device-code flow when authorization is pending | Poll on sync |
| Settings | Root folders, quality profiles, indexers, download clients, providers and profiles, each rendered from the corresponding resources with an edit form that patches spec | None |
| Unmatched | Files the scanner could not attribute, with candidates and a manual-assign action | SSE on scan |

The unmatched page is not in the brief but falls directly out of A1.5: a scanner
that refuses to guess needs somewhere to put what it could not resolve.

Cover art is fetched by the metadata gateway and cached on the `/data` volume,
then served by the UI from disk. The UI never calls a metadata provider itself,
so provider rate limits stay owned by the one component that already manages
them.

> **Note (2026-09-24, M7 Task B3).** Superseded by ADR-0011
> (`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md`
> §B). This paragraph was never built past the sentence: what actually
> shipped, in Phase G and the gap fixes, was hotlinking `status.metadata.images`
> URLs straight into `<img src>`. Cover art now lives in a JetStream object
> store bucket (`clustarr-artwork`), not a `/data` disk cache: the metadata
> gateway is the sole writer of provider-fetched `original` objects, a
> renderer (`catalogarr --role artwork`) is the sole writer of rating-badge
> `overlay` objects, and the UI serves both at
> `GET /art/{kind}/{uid}/{type}` over a read-only NATS connection it did not
> hold before — it still never mounts `/data` and still never writes
> status. Every template-facing image URL, and the hotlinks above, now point
> at `/art`.

### A3.4a Components and theme (as built, 2026-09-23)

The UI's shared widgets come from shadcn-templ (`components.json` at the
module root; `shadcn-templ add <component>` writes into `ui/components/`,
whose `utils` package they share, and `make templ` regenerates them). The
theme is `ui/theme/plex.json`, a shadcn registry item holding Clustarr's
Plex look -- Plex Gold `#e5a00d` as the primary, Plex's dark gray `#282a2d`
for surfaces on a darker page, 4px corners, the Open Sans face -- with the
light and dark token sets identical, since Plex has one look. It was
initialised with preset code `b1Fk1KzVQ` (style vega, base zinc, theme amber,
radius small, Noto Sans, lucide) and the token file applied on top:
`shadcn-templ apply --preset <raw URL of ui/theme/plex.json>`, because `init`
reads a URL as a component registry, not a theme. The tokens live in
`ui/static/input.css` (`:root` and `.dark`), which `make css` compiles.

**Own components (as built, 2026-09-24).** shadcn's base style has parts
shadcn-templ's registry lacks; those are written by hand under
`ui/components/<name>/`, the way the vendored ones are: one templ part per
tsx export, the `data-slot` names and class strings verbatim,
`data-tui-<name>-*` attributes for a script in the same directory that
`shadcn-templ bundle` packs into the one bundle, Base UI's public state
contract (`data-open`/`data-closed`, `data-starting-style`/`data-ending-style`,
`data-popup-open`, the `--popup-*` variables), and tests beside them, since
nothing overwrites them. `navigationmenu` is Base UI's NavigationMenu: a
hover (after the root's delay) or a click opens an item, its content moves
into the popup's viewport, floating ui places the positioner against the
trigger, a switch slides the popup and the contents cross in
`data-activation-direction`, the arrows walk the triggers and carry an open
menu along, Escape, an outside press, focus leaving and a close-on-click
link close. `scrollarea` is Base UI's ScrollArea: the viewport scrolls
natively with its bar hidden, each thumb is the viewport's share of the
content (never under 16px) and drags, a track press centres the thumb
under the pointer, a wheel over the bar scrolls the viewport, and
`data-hovering`, `data-scrolling`, `data-has-overflow-*` and
`data-overflow-*-start/end` mark the parts. Two traps met building them:
the tsx's `data-activation-direction=left:` variant shorthand compiles to
nothing under the pinned Tailwind (the bracket form
`data-[activation-direction=left]:` is the same selector), and a browser
keeps `/static/app.css` from an earlier load, so a page checked against a
rebuilt stylesheet needs a cache-busting query or a hard reload.

**Library cards (as built, 2026-09-23).** A library card is shadcn-templ's
`item`: the whole tile is the link to the detail page (`Href`), the poster
is the item's media at poster ratio (2:3, never a square crop), the title
with its year is the item's title, kind and quality profile its
description, and monitored, phase and on-disk are `badge`s -- the phase
badge keeping the amber warning and emerald done tones the earlier tests
guard (`phaseTone`). The grid is an `item.Group` with the responsive column
classes. Every data attribute the tests and the e2e suite key on stays on
the card element. The rest of the library follows the same shape: the
detail header is an item (poster media, title with year, kind and profile,
badges, and the monitor, search and refresh forms as `button`s in the
item's actions slot), each season header is a muted item that its toggle
swaps in place, and episode, album and book rows are small items in an
item group, each toggle an hx-post targeting `closest [data-slot=item]`.
templ writes an item's attributes in sorted order, so the tests find an
element by one attribute and assert the rest on it rather than matching
an attribute sequence. The pipeline, downloads and unmatched rows, and
the download client cards, are items too: the stage or phase badge keeps
its meaning colour (`stageTone`, `downloadPhaseTone`), the progress bar is
one shared `progressBar` whose fill carries `data-progress`, and the
unmatched row's manual-assign form is the item's footer. The theme's Open
Sans is self-hosted: the latin and latin-ext variable woff2 subsets Google
Fonts serves for v44 live under `ui/static/fonts` with the OFL, are
embedded and served under `/static/fonts`, and `input.css` declares their
`@font-face` rules with each subset's unicode-range, so no page fetches
its text face from a third party and a script neither subset covers falls
through to the system faces.

**Radarr's look (as built, 2026-09-24).** The chrome and the library grid
follow Radarr's, from shadcn-templ components: a `sidebar` with every page
(collapsible to icons, the active entry derived from the page title), a
`breadcrumb` trail in the inset's header (Library › tab › item › season),
the library's tabs as the `tabs` component whose triggers navigate through
htmx (`hx-get` with `hx-push-url` and an `hx-select` of the library page,
so the tab strip, the rows and their stream are replaced together and the
URL follows), and an A–Z bar as a vertical `button-group` down the right
edge: a link per letter that has titles, `?jump=<letter>`, which the
handler answers with a redirect to the page where that letter's titles
begin, and a disabled button per letter that has none. A library card is
Radarr's poster tile: the whole tile the link, the poster filling an
`aspect-ratio` box at 2:3 with the title as a `tooltip` rather than
printed text (a card without art prints the title in the box instead), a
status stripe under it (`data-status`: green on disk, amber on disk below
the cutoff, blue in flight, red monitored and missing, gray unmonitored),
and two centred footer lines, the monitored state and the quality profile.
The grid is `repeat(auto-fill, minmax(160px, 1fr))`. The component script
bundle `shadcn-templ add` writes under `ui/static/js` is embedded and
loaded once from the layout head; its scripts bind by delegation and
observe the DOM, so htmx swaps keep them working.

**Toolbar, sort and filter (as built, 2026-09-24).** Above the grid sits
Radarr's toolbar row: the actions on the left (one "Rescan" `button` per
RootFolder, the form it always was) and on the right a Sort and a Filter
menu, each a `dropdown-menu` of links. The view is three query parameters
the page and its stream parse alike and apply through
`projection.Arrange` before paging: `?sort` (`title`, the default, `year`,
`profile` or `status`, the stripe's state with missing first), `?dir=desc`
(choosing the current sort again flips it, as Radarr does) and `?filter`,
Radarr's presets (`all`, `monitored`, `unmonitored`, `missing` = monitored
with nothing on disk, `wanted` = missing and released, `cutoff-unmet`).
Defaults are left off the URL, so a link to the default view is the plain
page URL and two links to one view are byte-identical (`url.Values`
order). The view rides every link on the page -- the pager and its
page-size links, `sse-connect`, the A–Z bar and the menus themselves --
through `paging.Request.Params`; the A–Z bar shows only under the title
sort, where it means something. Each menu entry swaps `#library-page`
through htmx the way the tabs do, so the stream reconnects in the new
view and the URL follows.

**Item page and the page toolbar (as built, 2026-09-24).** An item's
page follows Radarr's movie page. The toolbar row every library page now
carries (`ui/views/toolbar.templ`) is Radarr's: a full-width bar under
the header of icon-over-label ghost `button`s, actions on the left and
view controls on the right; the library page's holds one "Rescan" per
RootFolder and the Sort and Filter menu triggers, an item's page holds
"Refresh & Scan" and "Search <kind>". "Refresh & Scan" posts the refresh
form with `scan=true`, which the handler turns into the refresh
annotation plus a LibraryScan of the RootFolder restricted to the item's
own folder (`actions.RescanPath`; an item with nothing on disk gets the
refresh alone). Below the toolbar the hero: the item's fanart behind
everything, the poster, Radarr's bookmark beside the title as the
monitored toggle, previous and next arrows to the neighbours on the tab,
the certification `badge`, year, runtime and provider links (TMDB or
TVDB, IMDb), then the facts (path, status with its stripe, quality
profile, size, original language, a series' network, genres) and the
overview; then, for a movie, its file as a `table` (path relative to the
movie's folder, video codec, first audio track, size, languages,
quality, release group, matched formats and score, from the MediaFile's
spec and probe), its extra files (sidecar subtitles, or Radarr's "No
extra files to manage." as an `empty`) and its alternative titles as a
`table`. Everything is read from what the item already carries -- the
Movie, Series, Artist or Author and, for a movie, the MediaFile named by
`status.fileRef` -- so what the metadata does not gather (ratings,
studio, cast, crew) and the actions that do not exist (interactive
search, rename, manage files, history, edit, delete) are not on the page.
Series, artist and author pages share the toolbar and the hero.

**Top bar, infinite scroll and the A–Z bar (as built, 2026-09-24).** The
library's tabs (Movies, TV, Music, Books) sit in the top bar of every
page, as Radarr's top nav, with the sidebar trigger; each trigger swaps
`#page-body` -- the breadcrumb row beneath the bar and the page -- through
htmx and pushes the URL, so the sidebar and the bar stay put. An item's
page marks its tab. The library tabs no longer page: `#library-rows`
renders the window `?page=N&per=M&pages=K` (K pages from page N,
`paging.Request.Pages`, one by default) and ends in a sentinel that,
once scrolled into view (`hx-trigger="revealed"`), fetches the same
window one page wider and swaps `#library-rows` whole -- the wider grid,
the next sentinel and the stream element, which reconnects for the wider
window so every live frame carries everything on screen; the sentinel
goes once the window reaches the end. A jump lands on the letter's page
and scrolls on from there in both directions: a sentinel above the grid
(`data-load-prev`) fetches the window one page earlier (the start moves
back a page, the span grows by one) on a `loadprev` event the tracker
script fires when the reader scrolls up at the top of the page, holding
the first card on screen in place across the prepend; a click does the
same. The A–Z bar is fixed to the right edge of
the screen, from beneath the top bar, the breadcrumb row and the toolbar
(all three sticky) to the bottom of the viewport, outside the grid's flow,
its letters sharing the height, as Radarr's; a vendored tracker
(`ui/static/jump.js`) places a thin thumb along it in proportion to the
items on screen over the whole list (from `#library-rows`' `data-offset`
and `data-total`, so it is stable across the pages infinite scroll loads)
on scroll and after every htmx swap, as Radarr's position mark, and hides
the document's native scrollbar while the bar is on the page. The
pipeline, downloads and unmatched pages keep the pager. The thumb drags like a native scrollbar's (2026-09-24): a 12px grab
strip down the bar's left edge draws the 2px line, a press on it follows
the pointer from where it took hold, the grid follows the thumb (its top
over the bar's height, times the total, is the title put under the
toolbar), a title outside the loaded window is reached the way a letter
click reaches one after a short pause, and letting go asks at once.

**Pagination (as built, 2026-09-23).** The pipeline, downloads, unmatched
and library pages each show one window of rows: `?page=N&per=M`, 1-based,
`per` defaulting to 50 and capped at 500, a page past the end clamping to
the last one (`ui/paging`, pure Go). Each page's `sse-connect` URL carries
the same parameters and the stream slices every push to that window, so a
live update redraws only the page in view; the pager (shadcn-templ's
pagination component: previous, first, neighbours, gaps, last, next, plus
"showing a to b of N" and the page-size links) rides the streamed fragment
so its counts stay live. Ordering is the projection's own, except on the
library, where the toolbar's view above is applied first. The Downloads
page's client cards stay outside the window.

### A3.5 Authentication

Out of scope for v1, and stated rather than left implied. The UI binds inside the
cluster and is expected to sit behind whatever ingress authentication the
operator already runs. It ships with no login, so its Service must not be
exposed directly. `docs/` and the chart's notes both say so.

**As built (2026-09-23).** The mode is chosen explicitly rather than implied.
`clustarr ui --auth-mode` (`clustarr all --ui-auth-mode`) has no default:
`ui.Options.Validate` refuses an empty or unknown mode and `ui.Run` validates
before it binds, so a process started without one exits at once having served
nothing, and under `clustarr all` that failure stops every service in the
process. `anonymous` is the only mode: every request is served without a
login, the startup line names the mode and repeats the ingress-authentication
requirement, and it stays a warning. The chart's `ui.auth.mode` defaults to
`anonymous`, so a Helm install chooses on the operator's behalf, and its schema
enumerates the modes so an unknown one fails `helm template` instead of
crash-looping a pod; `config/manager/ui.yaml` passes `--auth-mode=anonymous`
for the same reason. A second mode (a forwarded-user header from an
authenticating proxy is the natural one) slots into the same flag.

---

## A4. Effect on the milestones

`importarr` and the observability packages are foundational, so they move early.
The UI depends on resources that only exist once the services behind them work,
so it lands in stages rather than all at once.

| Milestone | Change |
|---|---|
| M0 | Add `pkg/obs` (logging, tracing, metrics) and wire it into every service's startup. Add the `importarr` and `ui` skeletons to the binary and the manifests. |
| M1 | Add the `LibraryScan` kind. `importarr` controllers and the rescan worker land here, because scanning an existing library is how anyone gets started. Trace propagation across the bus is exercised end to end. |
| M3 | Completed-download import moves to `importarr`. The first UI slice ships: the pipeline page and the downloads page, which is what makes the end-to-end demo legible. |
| M4 | Transcode metrics and the transcode rows on the pipeline page. |
| M5 | Subtitle metrics and subtitle stages on the pipeline page. |
| M6 | Import lists move to `importarr`. The library, import lists, settings and unmatched pages complete the UI. |

## A5. Risks this adds

- **Two writers on `MediaFile`.** Split by field manager on disjoint fields and
  enforced by the apiserver, but it is the first cross-service status split in the
  design and the base design deliberately had only one. It needs an envtest that
  proves neither manager clobbers the other's fields.
- **The pipeline projection drifts.** Stage derivation reads conditions and
  phases owned by five services; a change to any of them can silently degrade the
  page to a wrong stage. Mitigated by `Project` being pure and table-tested, with
  a test case per stage transition.
- **templ and shadcn-templ are young.** templ v0.3.x and shadcn-templ v1.13.x are
  both pre-2.0 and move quickly. Pin both, and keep the generated Go committed so
  a generator change cannot break a build unattended.
- **Trace volume.** A span per reconcile across six controllers is a lot of
  spans. Parent-based sampling with a low default ratio, always sampling errors.
- **Scope.** This amendment adds a service, a UI with six pages and a full
  telemetry stack to a build that was already seven milestones. It is a real
  increase, not a refinement.
