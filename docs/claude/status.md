# Clustarr record: Status and history

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## Status

Pre-alpha, and it reconciles. Since the manager/agent split (2026-10-06/07,
below) the services run as one `cmd/manager`, one `cmd/agent` per domain,
`cmd/ui`, `cmd/markers` and `cmd/transcode`. Until then every service --
`catalog`, `import`, `index`, `grab`, `transcode`, `caption` and `ui` --
registered every controller, worker and server behind its role flags of one
`clustarr` binary, and `clustarr all` stood every service up in one process
(controller roles only for `grab` and `transcode`, so it never downloaded or
transcoded). The paragraphs below are history in the order it happened, and
keep the names of their time.

M0 (done): the 29 CRDs across five groups, `pkg/events` (NATS and in-memory
behind one contract suite), `pkg/k8s`, the binary, manifests, chart and images.

Phase A (done): `pkg/obs` (`logging`, `tracing`, `metrics`, `obs.Bootstrap`),
`docs/observability.md`, the `LibraryScan` kind and `import` skeleton,
`pkg/pipeline` and the `ui` skeleton; all services wired into the binary.

Phase B (done): the library layer, thirteen pure-Go packages (only
`api/common/v1alpha1` shared types; `pkg/quality` alone reads catalog CRD
types). `release` (rls-based parser for every kind, batch ranges, id tokens,
130-title corpus); `quality` (vendored TRaSH corpus, all 2,791 regexes compile
under regexp2, 66 embedded formats proven byte-identical, 13 built-in profiles,
Match/Score, FromCRD, upgrade decision); `naming` (four dialects); `mediainfo`
(two-call ffprobe, HDR/DV, ProbeHash, MovieHash); `transcode` (argv goldens,
runner, cleanup, verify); `torznab`/`newznab`; `cardigann` (v11, 25 filters,
five logins, HTML/JSON/XML); `subtitles` (Bazarr scoring, cue-aware
post-processing, OpenSubtitles, Gestdown, embedded); `metadata` (six clients);
`importlist` (five lists, Dedupe, ApplySyncLevel); `fsops`/`ratelimit`.
Fixtures under `test/data/<pkg>/`, no network in tests, ffmpeg tests skip
without it.

Phase C (done): M1 catalog core and library rescan — the first phase that
reconciles. `catalog` controllers for Movie, Series, Episode, MediaFile,
RootFolder, QualityProfile (a `Bootstrap` runnable seeds the 13 TRaSH
built-ins), DelayProfile, MetadataProvider and Search; the metadata gateway
(registry over Phase B's clients, RPC plus work queue, L1 memory + L2 NATS KV);
`pkg/decision` (`Evaluate`/`Rank`) over `quality.Profile`; the search, grab (KV
lease, delay profiles) and RSS-matcher workers and the wanted cron. `import`
controllers for LibraryScan, the RootFolder schedule and ImportExclusion, plus
the rescan worker under the never-guess rule. Traces now cross the bus in
production (`pkg/events` hooks; every `run.go` passes
`k8s.WithBusHooks(obs.BusHooks())` to `k8s.ConnectBus`, AST-guarded); RBAC
generates from controller markers, with a byte-for-byte test holding the
chart's copy to it; readiness is per-service and runs on every replica, not
only the leader; `test/e2e`, `config/e2e`, the fixture image and `hack/e2e.sh`
land scenarios 5, 7 and 8 on kind.

Phase D1 (done): M2 indexers — `index` controllers for `Indexer`,
`IndexerDefinition` and `IndexerProxy`; the caps probe with its 12-hour
refresh (`capsTTL`, `app/indexer/controller/indexer/controller.go`); the
health/backoff escalation ladder in `app/indexer/status/health.go` (Prowlarr's
ten-rung `EscalationBackOff` periods, 0s to 24h, with a 15-minute startup
grace); the SQLite FTS5 release index (`pkg/relindex`, external-content FTS5,
pure-Go `modernc.org/sqlite`, no cgo); the federated search
(`app/indexer/search`) with its dedupe (infohash, then per-indexer
`(indexer, guid)`) and the KV-backed query-limit window
(`clustarr-indexer-limits`); the RSS poll worker and release firehose
(`app/indexer/worker/rss`); the three RPC verbs `rpc.index.search|download|query`
that make `app/catalog/worker/search`'s calls reach a real server for the first
time; `app/indexer/status` as the single declaration of each field manager's
owned set (`ControllerFields`/`WorkerFields`). E2E scenario 17
(`test/e2e/indexer_test.go`, against the Torznab fixture under
`test/fixtures/torznabstub/`) is written — Indexer health and caps, ranked
federated search, the release firehose, failure backoff — but has **never
been executed against a kind cluster**; that and `make e2e` generally are
deferred by explicit user instruction — first until D1–D3 were in, then
through E–G to Phase H — not by oversight. Reconciles against envtest; not
proven end to end.

Phase D2 (done): M3 downloads and import — `grab` and `import`'s
file-import worker. `pkg/download/torrent` over `anacrolix/torrent`
(idempotent `Add` on the infohash, `Resume`/`SetSeedCriteria` no-ops on an
already-satisfied state, anacrolix state mapped onto `download.Status`);
`pkg/download/usenet` over `Tensai75/nntp` plus pure-Go `javi11/rapidyenc` —
`javi11/nntppool` was ruled out as cgo-only (R9), so this package writes its
own bounded connection pool, 430-failover across configured servers,
pipelining and quota accounting, then PAR2-verifies/repairs (shelling out to
`par2cmdline-turbo`, no Go module -- since 2026-10-06 ADR-0017 makes it par2go
as a library, built once a par2go release can be required) and unpacks with
`github.com/nwaples/rardecode/v2`. `app/grab/status` declares the two disjoint
field-manager sets on `Download.status` — `ControllerFields`
(`k8s.ManagerGrab`, nine fields) and `EngineFields`
(`k8s.ManagerGrabEngine`, twenty-three then, twenty-five since gap fix
Y2 added `engineFailureReason` and `seedGoalReached`, twenty-six since Z1
added `healthPaused`) — and `Patch` refuses any other
manager. `app/grab/controller/downloadclient` reconciles `DownloadClient` into
its engine workload (a `StatefulSet` per torrent client, a `Deployment` for
usenet), `DiskSpaceOK` and the blocklist sweep.
`app/grab/controller/download` picks a `ClientRef`, waits for `EngineReady`,
pins the choice into `status.engine` once and never recomputes it, and runs
the `removeDataOnDelete` finalizer. `app/grab/engine/torrent` and
`app/grab/engine/usenet` gate readiness on re-attach completing first (R4) and
write only `EngineFields` telemetry; each carries a level-driven `Reaper`
(`torrent.Reaper`, `usenet.Reaper`) that lists client transfers against
owned Downloads and removes orphans past a grace period, proved by deleting a
`Download` with the engine's watch deliberately not firing.
`app/import/worker/fileimport` consumes `ConsumerImportFile`
(`"import-fileimport"`) and creates `MediaFile`, writing only
`MediaFileSpec` per the spec/status split; it also settled the
`Download.status.import` ownership question three comments disagreed on,
onto `k8s.ManagerImport`. D2-4, D2-5 and D2-6 could not advance
`status.phase` past `Assigned` — it is `ControllerFields`, not something an
engine can write — so `grep -rn WorkFileImportSubject` found the subject
builder and the consumer but no publisher, and the phase→import handoff that
is D2's own gate could not complete until D2-8a
(`app/grab/controller/download`) added phase derivation from engine telemetry
(`Assigned`→`Queued`→`Downloading`→`Completed`→`Seeding`→`Imported`, plus
`Paused`/`Failed`/`Blocklisted`/`Removing`) and published exactly one
`schema.ImportTask` per completion. Separately, D2-8 found the grab
reconcilers, both engines, both reapers and the file-import worker
registered nowhere and wired them into `app/grab/run.go` and
`cmd/clustarr`, added `grab` to the registration guard's
`runnableServices`, and regenerated RBAC. A CEL defect found along the way
was Critical: the `Download` release-identity rule
(`api/download/v1alpha1/download_types.go`) compared five `+optional` fields
unguarded, so an absent one raised "no such key" and the apiserver rejected
the write outright — fatal for usenet, which never carries `infoHash` — for
every write after creation, including the status-only applies that are
grab's only way to report progress; fixed in `c5e86d5` by guarding every
comparison with `has()`, with `pkg/crdcheck/download_cel_test.go` as the
regression guard using a dynamic client so an absent `infoHash` stays absent
on the wire. E2E scenarios (`test/e2e/download_test.go`,
`test/e2e/import_test.go`: 1 through import, 2, 3, 4, 6) are **written and
never executed**, deferred by user instruction to Phase H.
Reconciles against envtest; not proven end to end.

Phase D3 (done): the first UI slice — the pipeline and downloads pages over
real cluster state, streamed. `ui/reader.go`'s `NewClusterReader` builds a
standalone controller-runtime `cache.Cache` (no manager, no metrics or
leader-election port); `Options.Reader` staying nil is still legal and renders
empty pages. `/readyz` now gates on cache sync while `/healthz` stays
unconditional, and `config/rbac/ui_role.yaml` is a hand-written
`get,list,watch`-only role (no `*/status`) bound to the previously-unbound
`ServiceAccount ui` (at D3; Phase G adds exactly `ui/actions`' grants).
`ui/projection`'s `Projection` replaces the old per-connection 5s poll with
one process-wide tick that lists `Download`, `TranscodeJob`,
`SubtitleRequest`, `Search` and `MediaFile`, indexes them by owner through
`metav1.OwnerReference` UID, projects `pipeline.Entry` and
`downloadv1.Download` rows, and fans both out through `Subscribe`/
`SubscribeDownloads` to every open SSE connection. `ui/views/downloads.templ`
renders all eleven `DownloadPhase` values plus a `DownloadClient` section
without panicking on a zero-value status; `GET /downloads` and
`GET /events/downloads` join the existing pipeline page and stream. The
never-writes invariant (CLAUDE.md) is now two enforced guards rather than an
accident of no RBAC grant existing (D3-4, `6597877`): `ui/guard_test.go`'s
`TestUINeverWrites` is an AST guard over every non-test file in `ui/`
rejecting a `client.Writer` selector (`Create`, `Update`, `Patch`, `Delete`,
`DeleteAllOf`, `Status`) by method name on any receiver -- it is syntactic,
not type-checked, so it over-matches rather than tracing types back to
controller-runtime's `client` package -- plus any import of `pkg/k8s`;
`cmd/clustarr/ui_rbac_test.go`'s (now `test/guards/ui_rbac_test.go`'s) `TestUIRoleGrantsOnlyReadsAndActionWrites`
(named `TestUIRoleGrantsOnlyReadVerbs` until Phase G ruling R2 narrowed both
guards to admit `ui/actions`' writes) and `TestUIRoleChartMatchesConfig`
asserted `ui_role.yaml`'s verbs were a subset of `{get,list,watch}` with no
`/status` resource, and hold the chart's copy byte-identical to it (Phase G
below narrows both). E2E scenario 14 (`test/e2e/ui_test.go`: the pipeline and
downloads pages) is **written and never executed**, deferred the same way.
Reconciles against envtest; not proven end to end.

Phase E (done): M4 transcode — `transcode`. `app/transcode/status` declares the
disjoint sets on `TranscodeJob.status` — `ControllerFields`
(`k8s.ManagerTranscode`) and a worker-owned field set for `progress`,
`result` and `stderrTail` — plus `ProfileFields`/`PatchProfile`, and `Patch`
refuses any other manager (the transcode-worker-pools plan later retired the
worker manager; see below). `app/transcode/controller/transcodeprofile`
computes `status.hash` through `app/transcode/worker.ProfileSpec`, the same
conversion the worker renders from
(`TestStatusHashChangesWithEveryRenderField` fails if a render field stops
moving it), marks the loser of two overlapping selectors with an `Overlap`
condition, and creates one TranscodeJob per selected MediaFile not already
tagged `CLUSTARR_PROFILE=<profile>@<hash>`, named `<mediafile>-<hash[:8]>` so
a re-run creates nothing. `app/transcode/controller/transcodejob` plans from the
MediaFile's stored probe (a Dolby Vision `reject` or a container change lands
as `Skipped` with a reason, rulings R1 and R8), creates a suspended `batch/v1`
Job whose `podFailurePolicy` fails on exit codes 3 and 4 and ignores
`DisruptionTarget`, unsuspends through the pure `Admit` slot scheduler, and
owns the transcode metrics, observed once per terminal transition (R9).
`app/transcode/worker` runs in the Job pod: re-probe and refuse a changed source
(exit 3), encode, verify (exit 4), then hard-link the original into the
recycle bin (`fsops.RecycleLink`) and rename the output over the source, so
the library path is never empty; a retry after a crash past the swap finds the
`CLUSTARR_PROFILE` tag and exits 0.
`TestTranscodeWorkerExitCodeReachesTheProcess` runs the real `main` to prove
the exit codes survive; at Phase E the Job ran as its own `transcode-worker`
ServiceAccount with a generated role
(`config/rbac/transcode_worker_role.yaml`); `--worker-image`,
`--worker-image-cuda` (removed 2026-10-01, ADR-0015) and `--data-claim` are
threaded through config and chart. (The gap fixes reversed two of Phase E's rulings: a container change
and `replaceSource: false` are transcoded now, ruling R-11, below. The
transcode-worker-pools plan, 2026-09-23/24, replaced this per-TranscodeJob
Job with one long-lived pool Job per (TranscodeProfile, hardware class): see
`docs/superpowers/plans/2026-09-23-transcode-worker-pools.md` and design spec
§6.4/ADR-0009. The `transcode-worker` ServiceAccount and role above are gone;
the worker became a separate, credential-less binary, `cmd/transcode` since the
split.) Defects a
future reader must know: `maxOutputToSourcePercent` was an
`int32` defaulted to `1.0`, so **every real transcode would have exited 4**;
`policy.replaceSource`/`recycleBin` could not be set false from Go and
`activeDeadline`/`resources`/`scratch` never got their defaults from a Go
client (the typed-client gotcha above); `replaceSource: false` is now rejected
by CEL rather than silently ignored. E2E scenario 12
(`test/e2e/transcode_test.go`, against an HDR10 clip generated in the fixture
image; Dolby Vision skips with a named reason, since ffmpeg alone cannot
synthesise an RPU) and scenario 1's transcode leg (skipped on the `.bin`
fixture gap) are **written and never executed**, deferred by user instruction.
Reconciles against envtest, real libx265 encodes included; not proven end to
end.

Phase F (done): M5 subtitles — `caption`. `app/caption/status` declares each
manager's set: `RequestControllerFields`/`RequestWorkerFields` behind
`PatchRequest`, `ProfileFields`, and `ProviderFields` behind a `PatchProvider`
that refuses the worker's manager — the SubtitleProvider controller is the
only writer of provider status (ruling R2), and workers record throttle, quota
and auth into the `clustarr-provider-throttle` KV bucket instead
(`app/caption/throttle`: a per-provider token bucket shared by every replica,
keys through `events.KVKeyToken`, contract-tested against a real embedded NATS
server). `SubtitleRequest.status.items` is split per leaf between the two
managers (R4), and the split needed a liveness protocol to work at all: an
item is live exactly when the controller has given it a `nextSearchAt`
(`status.IsLive`, `LiveItemKeys`); the controller creates and withdraws items,
the worker re-sends leaves only for live items and never creates one, and
`items[].state` became optional. Before that the worker re-sent every item, so
a withdrawn language could never be removed, and only `managedFields` caught
the controller claiming the worker's `state`. `pkg/subtitles` gained the
missing-subtitle `Plan` and right-to-left `SidecarName`/`ParseSidecar`;
`pkg/lang.Normalize` maps ffprobe's ISO 639-2 onto the profiles' BCP-47 — the
third instance of the language-vocabulary class, and it also fixed TVDB's
original language. The three controllers
(`app/caption/controller/subtitleprofile`, `subtitleprovider`,
`subtitlerequest`) and the fetch worker (`app/caption/worker/fetch` over
`app/caption/providerset`, never leader-elected) are registered behind
`caption`'s role flags; one `providerset.Validate` now serves controller
and worker (the two copies had drifted — a Gestdown provider with a missing
Secret reported Ready), and `app/caption/datapath` gives both one `/data`
mapping. Fixed along the way: the embedded provider claimed
hash-verifiability, so embedded tracks scored 0-1 and were never taken;
OpenSubtitles.com defaulted a private 5 req/s limiter and read JSON uncapped
(R3); a forced search was absorbed by the JetStream dedup window for up to an
hour until `events.MsgIDForForcedSubtitle` gave it an ID of its own — so fetch
MsgIDs now extend spec §6.5's `<uid>/<langKey>/<probeHash>` as a prefix, a
recorded deviation; and the `caption-worker` KEDA `ScaledObject` counted a
consumer that does not exist, so it never scaled
(`TestCaptionScaledObjectCountsTheRealFetchConsumers`; KEDA and the guard left
with the split, 2026-10-06, for the HPAs the manager owns). E2E scenario 13
(`test/e2e/subtitle_test.go`, against `test/fixtures/opensubtitlesstub` and
`gestdownstub`, both round-tripped through the real clients) and scenario 1's
subtitle leg (skipped on the `.bin` gap) are **written and never executed**,
deferred by user instruction. Reconciles against envtest; not proven end to
end.

Phase G (done): M6 parity, import lists, non-video inventory and the rest of
the UI. **Indexers.** A Cardigann definition is one more `Client` behind
`app/indexer/controller/indexer`'s `ClientCache.For` (ruling R5), so it inherits
the fan-out's dedupe, query-limit window and backoff; a `SearchBlock.Error`
match is now a `*cardigann.SearchError` (`ErrSearchFailed`) that escalates the
indexer instead of reading as "no results" (R6); sessions live in the
`clustarr-indexer-sessions` KV bucket (`SessionKey`, real-NATS contract test)
plus an owned `<indexer>-session` Secret; `IndexerProxy` http/socks5 is
applied; `cardigann.Engine` takes an injected limiter. `app/indexer/facade`
serves the Torznab facade (`/{indexer}/api`, `/{indexer}/download`,
`/search/api`) on `--facade-bind-address` (`:9696` under `clustarr all`) and
**fails closed** without a key from `--facade-api-key-secret` (created with
one random key if absent); a Search with `spec.query` runs through
`rpc.index.query`. Automatic search falls back to a title query:
`BuildSearchRequest` fills `SearchRequest.Text` from the resolved title and
`app/indexer/search` uses it only for an indexer that supports none of the
request's ids (`SearchOutcome.QueryMode` records which). That made live the
absence of any release-identity check — a text fallback could approve the
wrong film — so `pkg/decision/identity.go` now rejects `WrongItem` (ids decide
when both sides carry one; otherwise titles plus year ±1, or
season/episode/absolute/air-date) and `UnknownItem` when unevaluable, fed by
`search.MovieIdentity`/`EpisodeIdentity` at both construction sites. **Lists
and history.** `import` gains the ImportList controller and sync worker
(Trakt device flow with a `SecretTokenStore`, `status.auth` for the UI,
ImportExclusion respected); `catalog`'s `RoleHistory` starts the history
`Sink` and the `DLQProjector`, which per R1 applies a
`clustarr.io/dead-lettered` annotation under `clustarr-dlq-projector` and
emits an Event rather than writing anyone's status. **Non-video catalog.** The
metadata gateway is the sole writer of `status.metadata` on Artist, Album,
Author, Book, Audiobook and Comic; the Artist→Album, Author→Book (standalone
Book too), Comic→Issue and Audiobook controllers follow Series→Episode, and
only Comic→Issue uses `ManagerCatalogFanout`, since Issue, like Episode,
has no `status.metadata`. Rescan attributes non-video files to existing items
only; manual import is the `catalog.clustarr.io/import-target` and
`import-override` annotations on a Download (re-queued by
`fileimport.Retrigger`) or on a LibraryScan whose `spec.subpath` names the
file (manual assignment). **UI.** Every write lives in `ui/actions` — search
now, rescan, monitor, manual assign and eight Settings patches — as merge
patches of spec under `clustarr-ui`; `TestUINeverWrites` allows Create/Patch
there and nowhere else and bans every status write everywhere, and the role
guard holds `ui_role.yaml` to reads plus exactly `actions.Grants()`. The
library, unmatched, import-lists and settings pages ride the one projection
with no new ticker; Tailwind is a pinned standalone binary behind `make css`,
with `ui/static/app.css` committed and htmx vendored.
`TestBothUICommandsWireEveryUIOption` runs both ui commands and fails on any
unset option — the text guard it replaced missed that `/library`, `/unmatched`
and `/import-lists` rendered no rows in production. **G4-0** swept `api/` for
the typed-client defaulting trap and uncapped status lists and left two guards
behind (gotcha above). Defects found and fixed along the way: the non-video
built-in profiles listed tiers worst-first, so PDF was the best ebook and
every comic met cutoff; `pkg/fsops` knew only `.mkv` as video, so **every
`.mp4` movie was skipped**, and applied the 50 MiB sample floor to every kind
— classification is now per kind (`fsops.Classifier`), the floor is video-only
and set by `--sample-max-bytes`, and a size-suspected sample goes to
`status.unmatched` as `suspected_sample` instead of vanishing; every naming
preset left dangling separators for an empty optional token
(`"The Matrix (1999) - -RlsGrp"`, shipped in Phase C); ComicVine wants the
`4050-N` guid for a volume but a bare `N` in the issues filter, which every
fake accepted either way; Audiobook's region was hard-coded `us`; and a
re-scan erased a MediaFile's frozen import fields by re-applying
`MediaFileSpec` under the same manager. E2E scenarios 9, 10 and 11
(`test/e2e/importlist_test.go`, `cardigann_test.go`, `nonvideo_test.go`,
against `cardigannstub`, `httpproxystub`, `importliststub` and `nonvideostub`)
and the rest of 14 (`TestUILibraryImportListsSettingsAndUnmatchedPages`) are
**written and never executed**, deferred by user instruction; scenario 9 only
checked that the Trakt and Plex CRs are accepted, since neither client took a
base-URL override (the gap fixes added `--trakt-base-url`/`--plex-base-url`
and un-skipped both). Reconciles against envtest; not proven end to end.

Gap fixes (done, 2026-09-23): plan `docs/superpowers/plans/2026-09-23-gap-fixes.md`,
tasks X1-X16 in four waves — W0 every API change serially, W1a/W1b code under
strict file ownership, W2 wiring and RBAC, W3 docs — closing the carried list
except what the spec defers. **API** (X1, X15): `CutoffUnevaluated` on Movie
and Episode (an unresolved profile no longer reads `CutoffUnmet`);
`IndexerProxy.spec.port` required; `ReleaseDecision` moved to
`api/common/v1alpha1` so controller-gen generates Search's apply configuration
and the hand-written one is gone; `fieldRecording`; all nine image types;
`IssueStatus.cutoffMet`, `pendingGrab` and state `delayed`; `MediaRef.track`;
`TranscodeProfileSpec.maxConcurrent`; `hdrOffset`/`minimumSeeders`/
`cleanupDays` as pointers with `*OrDefault` accessors; `ReleaseInfo.alsoOn`;
`MovieMetadata.secondaryYear`; `replaceSource=false` admitted;
`k8s.MarkDeadLettered`/`DeadLetteredAnnotationChanged`, folded into the
`DeadLettered` condition of all sixteen annotatable kinds; ImportList CEL
admitting only the kinds its provider can yield;
`IndexerDefinition.status.replaces`. **Catalog and search:**
`status.activeDownloadRef` has one writer, the item's reconciler, derived from
its owned non-terminal Downloads (`rollup.ActiveDownload`,
`rollup.DownloadNonTerminal`) on all six grabbable kinds, and
`rollup.DownloadOverlay` reads every Download phase before Imported as
Downloading; the grab path has one source resolver shared with Search-CR grabs
(`app/catalog/worker/grab/downloads.ResolveSource`), a double-grab guard that
lists Downloads through the uncached APIReader, reclaimable and re-enterable
leases, CAS status writes, and grabs albums, books, audiobooks and issues,
which automatic search now covers too (text queries after Lidarr, Readarr and
Mylar); `decision.Identity` gains `SecondaryYear`, `FullSeason` on a
single-episode search, TheXEM scene mappings (`pkg/metadata/scenemap`) and
non-video rules; ReleaseTitle custom formats read the release or file name;
the RSS matcher keys series as Sonarr does; Series airings get a writer;
`history.Replayer` serves `clustarr.io/replay`; item, media-file, indexer,
download and transcode events all have producers. **Metadata:** eight new
clients (coverart, fanart, hardcover, metron, mangadex, anilist, kitsu,
animelists) that reach Ready, every client read through `metadata.ReadBody`/
`CappedTransport`, the real `tmdb.SearchMovies`/`musicbrainz.SearchArtists`,
`Album.Releases`, Open Library editions, ComicVine status, artwork and
resolver enrichment of `status.metadata`, Lidarr's album release selection
(`selectedReleaseID` is the Album reconciler's leaf), Kapowarr issue numbers
and injective Issue names. **Release and quality:** `release.TitleNorm` on
both sides of the release index (non-Latin search works), Radarr's
ReleaseGroupParser, MULTi is not a language, an untagged release takes the
item's original language, non-video qualities on Lidarr's music ladder,
multi-episode names per Sonarr. **Indexers:** every field the Cardigann schema
decodes, XML queried with CSS, cookie and form logins that work,
session-expiry re-login, `app/indexer/proxy` (selector, FlareSolverr, SOCKS4;
an empty selector matches nothing and ambiguity fails closed),
`minimumSeeders`, RSS pages and direct grabs counted into their windows,
`alsoOn`. **Downloads:** torrent file selection by `spec.target.keys`, usenet
priority classes, `Item.AddedAt`, and an engine finalizer so the controller
removes files only after the engine lets go. **Import:** best file first per
single-file item, episode and series-root imports, honest rescan counters,
redelivery resume, `fileimport.RecycleSweeper`, `removeAndDelete` through the
recycle bin, `automaticAdd=false`. **Transcode:** one output rule
(`app/transcode/worker.OutputPath`), `status.plan` equal to the worker's argv,
per-profile `maxConcurrent`, Job pods with the Deployments' securityContext,
`--intel-render-groups`, the Intel QSV/VAAPI runtime in the media image.
**Subtitles:** SubDL and SubSource clients, Bazarr pooling, an effective
`embedded.extract`, one shared OpenSubtitles login, sidecar mode from the
RootFolder. **Wiring** (X14, X16): one generated ClusterRole per identity
(`RBAC_ROLES`), proven by a start envtest that runs every service as its own
ServiceAccount under only its role; a registration guard that derives the
services and catches bare-`Start` runnables, unadded `EveryReplica`s and
duplicate controller names; UMASK applied by the binary; engine pods with a
ServiceAccount (`--engine-service-account`), namespace, NATS URL, UMASK,
`GOMEMLIMIT` and the Deployments' securityContext; `--trakt-base-url`,
`--plex-base-url`, `--ui-bind-address`; ui `/readyz` after the first
projection round. **The Cardigann corpus** (`7b6fc4a`): the project owner
added Prowlarr's Cardigann definitions on 2026-09-23 — 752 files packed by
`hack/pack-cardigann` into `app/indexer/bundle/embedded/definitions.zip`
(`go:embed`, read through `archive/zip` as an `fs.FS`), 749 of which load;
index applies them at startup (`--cardigann-bundled`, default true), and
`--cardigann-definitions-dir` (chart: the index service's `cardigann.*` values) replaces them.
**Found and fixed beyond the list:** engine pods had no ServiceAccount,
namespace or NATS address and could not have started on any cluster; grab
leases were never released, so every item could be grabbed exactly once; the
RSS title fallback had never matched a series without an id; the generic
`.torrent` fetch bypassed the Indexer's proxy, passkey included; TMDB and
ComicVine API keys leaked into error strings; every HDR10 remux with non-AAC
audio failed (`-vf` beside stream copy, exit 234) and every 8-bit QSV encode
failed (exit 218); `fsops.IsPart` missed a transcode's `<stem>.part.<ext>`;
a multi-file download gave one item several MediaFiles; `removeDataOnDelete`
was a no-op for every torrent; HTML `case` keys, XML definitions (a panic)
and cookie logins were all broken for the whole corpus; MusicBrainz release
statuses never matched; an album listing one recording twice made a status the
apiserver would have rejected; ImportList `Synced` reset once its checkpoint expired.
**Rulings:** R-1 subtitle sync, Whisper and chunked transcoding stay
spec-deferred and CEL-forced off; R-2 usenet dedupe stays per indexer; R-3 an
automatic single-episode search rejects a full-season pack (Sonarr); R-4
subtitles pool every provider, Bazarr-style; R-5 one writer of
`activeDownloadRef`, the reconciler; R-6 each engine's finalizer gates the
controller's file removal, with a 10-minute timeout for a gone engine; R-7
`SecondaryYear` exact or ±1 of `Year`; R-8 only three `omitempty` scalars
become pointers; R-9 `IndexerProxy.spec.port` required; R-10 an unyieldable
import-list kind is refused at admission; R-11 `replaceSource=false` and the
container change are implemented; R-12 confirmation-only items closed with
evidence, or fixed where the evidence said so; R-13 vendor the Cardigann corpus only under a compatible licence —
superseded by the owner's addition of the corpus above. E2E: X12c made the
download fixtures serve real media, deployed the seeder and NNTP stubs, and
wrote scenarios 15 and 16 (16's Helm and `clustarr all` legs skip); every
scenario is still **written and never executed**. Reconciles against envtest
(W2 gate green at `732c4a1`); not proven end to end.

Gap fixes Y1-Y3 (done, 2026-09-23): the four failure-handling behaviours the
spec named and nothing built (spec §4.4, §5, §8.3 now say "as built").
**Hung-handler DLQ** (Y1, `bd6227e`): natsbus watches JetStream's
`MAX_DELIVERIES` advisory per subscription, in the queue group
`clustarr-dlq-watch-<durable>`, and copies a message whose final delivery
lapsed on AckWait under the in-process path's Msg-Id, deleting a work-queue
original afterwards; membus sweeps the same case. A non-work consumer's dead
letter is named by its durable (`clustarr.dlq.catalog.redownload.<id>`);
before, two event consumers shared one DLQ subject. `events.KV.DeleteRevision`
(`1ab8ce2`) is a revision-checked delete (`ErrRevisionMismatch`; refuses
revision 0, which JetStream reads as unconditional). **Failure reasons, seed
goal, blocklisting** (Y2, `813145b`, `56c10c1`, `0b607c0`): engines report
through `status.engineFailureReason` (torrent `stalled` after
`spec.torrent.stallTimeout`, default 24h; `diskFull`; `writeError`; usenet
`missingArticles`, `encrypted`, `diskFull`, `writeError`, and `timeout` after
the opt-in `downloadTimeout`) and `status.seedGoalReached`; the controller
records a terminal `failureReason`, labels a release fault blocklisted before
publishing `failed`, leaves a local fault (`diskFull`, `writeError`) Failed
and unlabelled, and fires `seedGoalMet`; engines remove a Failed or
Blocklisted transfer. A server that could not be asked (bad credentials,
refused connection) is `ErrProvidersUnavailable` and retries, not
`missingArticles`. `importRejected` is read off `status.import` through
`downloadv1alpha1.ImportMessageEveryFileRejected`, one constant both services
name. **Redownload** (Y3, `dd1ea96`): the `catalog-redownload` durable on
`failed` and `blocklisted` frees the grab lease (`grab.FreeLeases`,
revision-checked) and publishes one `redownload` search per monitored target
under a per-Download Msg-Id, none for a local fault (Radarr raises no
DownloadFailedEvent for one) or a failure older than 24h; the grab records
`grabbedBy=redownload`. The carried items, including the owner's policy call
on `importRejected` blocklisting at once, are under *Downloads and events* in
the remaining-work plan.

Z-wave (done, 2026-09-23; Z1-Z6 plus a final pass, reports in
`.superpowers/sdd/2026-09-23-gap-fixes/`): every fixable item left open after
the gap fixes. Downloads read `healthAction` (`pause` holds a job with
`status.healthPaused` until `spec.paused` is toggled) and `removeCompleted`,
persist seed counters, blocklist `payloadMismatch`, reach a live transfer
through `download.Client.SetPriority`, and roll the engine when a start-time
setting or a usenet provider Secret's data changes (Secrets read by name with
`get` alone, no watch, so within 5 minutes). natsbus runs `MaxInFlight`
handlers, `CLUSTARR_ADVISORIES` captures MAX_DELIVERIES advisories durably,
retries wait exactly their backoff, and the never-used consumers, subjects and
search-cache bucket are pruned. Engines and the transcode worker write 1 Hz
telemetry into `clustarr-progress`. RSS matches non-video releases and
alternate titles; searches send scene numbering. The open list is the
unchecked items under "Still open after the gap fixes" in the remaining-work
plan.

M7 (done, 2026-09-24): index/artwork/ratings/Plex
(`docs/superpowers/specs/2026-09-24-index-artwork-ratings-plex-design.md`).
**A** `pkg/relindex` gains `OpenPostgres` behind the same `Store` contract
(`storetest.Run` runs both engines), selected by `--index-dsn`
(`CLUSTARR_INDEX_DSN`, ADR-0010); a DSN turns on leader election for every
index controller and the retention sweep, since more than one replica may
now run; the chart and kustomize gained an optional CloudNativePG `Cluster`
(`postgres.enabled`/`config/postgres`) as a kept normal resource, never a
hook (a ruling reversing this design's own hook sentence and the plan's R2,
gotchas above), and `nack` left the chart. **B** a JetStream object store
bucket, `clustarr-artwork` (5Gi, file storage — `ForSingleNode` had to learn
every object store belongs there, not only KV and streams, gotchas above),
holds provider-fetched `original` artwork and `spec.artwork` overrides, one
writer per variant (the metadata gateway; the renderer, `catalog --role
artwork`); `ui` gained a read-only NATS connection and serves it at `/art`,
dropping every provider hotlink (ADR-0011); both installers give the ui
`NATS_URL` (the first cut did not, so every image request hung — the
`TestEveryNATSDialingDeploymentCarriesNATSURL` guard). **C**
`status.metadata.ratings` from TMDB for **movies only** — series carry no
ratings in M7: TMDB declares its source for movies only, TVDB (the series
provider) supplies none, and MDBList and OMDb, declared as fallthrough, were
not built (ruling R5: no API keys at hand). MDBList has since landed
(2026-09-24, `pkg/metadata/clients/mdblist`, built against responses
recorded from the live API): every source for movies and series, with an
optional second key spent once the first's daily quota is out; OMDb still
reports `Ready=False, InvalidSpec`;
`OverlayProfile` and `pkg/overlay` composite Kometa-style rating badges
onto posters, rendered by the same `--role artwork` worker for Movie and
Series only, bounded to 2 concurrent renders by default and drawn at most
2000px wide (`MaxRenderWidth`). **D** `ui/plex` serves Plex's Custom
Metadata Provider protocol at `/plex/movies` and `/plex/tv` (`--plex-provider`,
`--external-url`/`CLUSTARR_EXTERNAL_URL`; ADR-0012), read-only over the
existing projection cache (one index build per 5 s, `projection.IndexTTL`),
gated 503 with no external URL configured.
Reconciles against envtest, with real Postgres (`make pg-assets`) and real
NATS object-store round trips included; e2e scenario 18
(`test/e2e/plex_test.go`) is written and, like every scenario since Phase C,
has never run on kind — its thumb-fetch leg skips by name rather than
asserting, since `config/e2e` has no egress and no in-cluster fixture serves
image bytes yet. Every deferred item the build surfaced is under "M7 carried
items" in `docs/superpowers/plans/2026-09-18-remaining-work.md`.

Live TV phase 1 (done on branch `livetv`, 2026-10-07): the two CRDs,
`pkg/iptv`, the controllers, the tuner and relay in the manager, blocks and
ports, the installers and scenario 19 (written, not run); see **Live TV**
above. Reconciles against envtest; not proven end to end.

Manager/agent split (2026-10-06/07, as built;
`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`, ADR-0018,
ADR-0017): `cmd/clustarr` and `clustarr all` are gone. `cmd/manager` runs every
reconciler under one lease (`manager.clustarr.io`) and one cache, registered by
`app/<svc>/manager`; `cmd/agent --domain <d>` runs one worker domain per process,
registered by `app/<svc>/agent`; `cmd/ui`, `cmd/markers` and `cmd/transcode`
stand alone (`internal/cli` holds the command trees, `make run-dev` the local
processes). Two images replace the controller, media and transcoder images:
`clustarr` (distroless static: manager, ui) and `native` (FROM scratch: agent,
markers, transcode), every Go binary UPX-packed (owner, 2026-10-07). Agents
scale on HPAs the manager owns, driven by `clustarr_consumer_lag` from its own
External Metrics API (`app/autoscale`, `docs/autoscaling.md`), and KEDA is
gone. The manager alone ensures the NATS topology; subscriptions bind, never
create, and fetch only for free slots (Waves 4c, 4f). Probing, segment
decoding and embedded subtitle extraction run in-process on ffgo
(`pkg/mediainfo/native`, `pkg/segments/decode`, `embedded/native`, behind
`pkg/ffruntime`); no image ships ffmpeg, ffprobe or par2. Not yet: par2 as a
library (Wave 9 waits on a par2go release Go's module tooling accepts, so the
usenet engine still execs `par2` and a set that needs repair fails as a local
fault in the native image); tests that run no ffmpeg (Wave 6b's
`internal/testmedia` and goldens and W10.9's guard are in the test batch).
`go.mod` replaced ffgo with the `../ffgo-unify` worktree until 2026-10-27
(OD18), when its branch was rebased onto the fork's master and the replace
moved to the published `v0.0.0-clustarr.14` tag.

MediaFile remediation loop (ADR-0016, 2026-10-06/07;
`docs/superpowers/specs/2026-10-06-mediafile-remediation-loop-design.md`):
SubtitleRequest, TranscodeJob and AudioGraft fold into MediaFile
`status.subtitles`, `status.transcode` and `status.graft`, planned and written
by one remediation loop in the manager (`app/remediation`, controller
`mediafile`, file and item keys) that replaces the MediaFile, Movie, Episode,
Album, Book, Audiobook, Issue, Series, Comic, rename, TranscodeJob, AudioGraft
and SubtitleRequest controllers; workers answer through records (`pkg/records`,
Durable KV); transcode and graft admission is one ledger
(`transcode-admission`); the `transcode-results` and `catalog-segments-result`
durables and the `catalog-markers` and caption-worker field managers are gone.

Agents never write Kubernetes (ADR-0019, waves A1-A7, 2026-10-07;
`docs/superpowers/specs/2026-10-07-agents-report-over-nats-design.md`): the
manager is the control plane -- admission and routing (`app/dispatch`, agent
presence in `pkg/presence`), the intake of candidates, scan observations and
task advisories (`app/intake`) -- and the only writer of every CR and Secret.
The Download kind is deleted: a grab is an entry in its owner's
`status.downloads[]`, run by the item key's grab and downloads stages
(`app/catalog/grabplan`, `app/grab/lifecycle`), carried out by seq-fenced
engine commands (`CLUSTARR_WORK_ENGINE`) and reported in transfer and engine
records; imports are two-phase (inspect, execute) with the manager
materialising MediaFiles (`app/import/importplan`); the blocklist is a table in
the release index (`clustarr.rpc.index.blocklist`); the grab leases, the pending
KV and the redownload consumer are gone. Scans report observations the
manager's scan applier writes; import lists report snapshots the ImportList
controller applies; the index agent reports health outcomes and KV sessions the
Indexer controller turns into status and Secrets; the metadata gateway answers
in `clustarr-item-metadata`, and `status.artwork` and `status.overlay` are
rendered from the artwork objects' metadata.

Wave U (2026-10-07; `docs/superpowers/specs/2026-10-07-unbranded-names-design.md`):
every internal *arr name became its domain name, cold turkey (the transcode
service's packages moved to `app/transcode`), and `make brand-gate` holds it; the apply
configurations moved to `api/applyconfiguration/<group>/<version>`
(`hack/flatten-applyconfiguration.sh`, run by `make generate`). Wave N
(ADR-0020, ADR-0021): `busconn.Shutdown` drains every binary's bus last,
pending limits on every core subscription, `clustarr_nats_dropped_messages`
and the `ClustarrNATSSlowConsumer` PrometheusRule, `ClassifyRequestError` at
every RPC caller (no responders reads as the domain's `NoAgent`), a chunk size
per object store and the manager's orphan purge over every store, reconnect
backoff with jitter, a `NATS_URL` pool and a named reconnect buffer, and
fingerprint metadata. Wave S (2026-10-07): series and season search, banded
interactive results and pack preference (the UI section above).

Cold turkey (owner, 2026-10-07): one release and no migration code -- no
adoption of the four removed kinds, no legacy names, no rollback tooling; 27
CRDs remain. A running 0.4.x install moves over once, by hand
(`docs/runbooks/unify-cold-cutover.md`), with the owner's OK. Everything since
the split is **built, not deployed**, and was built implementation-first: the
tests and guards each wave named are written by the test batch
(`.superpowers/unify/progress.md` lists them), and until then `go vet ./...`
fails on the test packages the waves broke.

Next: the unified topology ADR-0013 deferred is built (ADR-0018 supersedes
ADR-0013; the 2026-09-24 unified-manager design is superseded by the split
design): land reconcilers in their service's `app/<svc>/manager` package and
workers in its `app/<svc>/agent` domain, never in a per-service Deployment.
The **test batch** comes first: it writes every deferred test and guard, then
runs `make test` and `make lint` green. Then Wave 9 (par2go in a re-exec'd child
of the agent) once a par2go release can be required, and the cold cutover of
kind-cluster-plex with the owner's OK. Then **Phase H: end-to-end proof on
kind**. Only Phase C's scenarios (5, 7 and 8) have ever run on kind; every
scenario written since — 1-4, 6, 9-18 — never has. Phase H runs them, builds the
Helm deploy path scenario 16 needs, extends scenario 1's trace check across the
manager and its agents, and owns the open items left under "Carried defects" in
`docs/superpowers/plans/2026-09-18-remaining-work.md`. Milestone detail is in
the spec's §16 and amendment §A4.

**The library store (ADR-0024), live on talos since 2026-11-26 11:06Z; not
proven on kind.** MediaFile, Episode, Album, Book, Audiobook and Issue are
rows in Postgres (`pkg/library`; design
`docs/superpowers/specs/2026-11-26-library-store-design.md`, §9 for what was
built and what was not). talos was cut over on the owner's word ("deploy to
the talos cluster ... Ensure the migations are properly run", then "Do it" for
the delete): 33,814 MediaFiles, 27,693 Episodes, 6,890 Albums and 148 Books
copied, chart 0.6.18 with clustarr and native `sha-710ef86` and cluster-plex
`7148cac` (chart 0.6.12), Helm rev 273, and the six CRDs deleted at 12:01Z.
What was done and what differed from the plan is the last section of
`docs/runbooks/library-store-cutover.md`. The kind run before it was stopped
at 31 of 41 scenarios on the owner's word (13 passed; commit b7625e55 lists
the failures and which of them were read); no scenario has run since with
the fixes, so the suite is still owed.

**Nothing is finished until it is proven end to end on a kind cluster.** Every
phase from C onward lands its milestone's scenarios in `test/e2e` (real CRs,
real controllers, real NATS, real files under `/data`, in-cluster fixture
services, no Internet) and keeps `hack/e2e.sh` green; Phase H audits that every
scenario in the plan exists and passes. Unit, envtest and build-tagged
integration suites do not substitute for it.
