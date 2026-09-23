# Clustarr

A distributed, event-driven media stack for Kubernetes: controllers and CRDs that
manage a media collection end to end, from wanting something through finding,
downloading, importing, transcoding and subtitling it.

Custom resources are the interface. `kubectl get movies,downloads,transcodejobs`
is the UI, and the web UI is a view over the same resources.

## Read before designing anything

- `docs/superpowers/specs/2026-09-18-clustarr-design.md` — the design of record.
  Field-level CRD definitions, event subjects, data flows, milestones.
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` — adds
  `importarr`, observability and the UI. **Where the two disagree, the amendment
  wins.**
- `docs/adr/0001..0008` — why NATS, why GPL-3.0, why Jobs for transcode, and so on.
- `docs/research/*.md` — nine verified research notes (TRaSH quality model,
  Prowlarr/Cardigann, queue comparison, torrent/usenet, controller-runtime,
  ffmpeg, Bazarr, metadata providers, naming). Long: `grep -n '^#'` first, then
  read the section. They contain verified API shapes — prefer them over guessing.

The spec is authoritative for names, fields and behaviour. Do not invent fields;
do not drop them.

## Services

One Go module, one cobra binary: `clustarr <service> --role <role>`.

| Dir | Owns |
| --- | --- |
| `catalogarr/` | Media domain (movies, series, music, books, comics, audiobooks), metadata gateway, release decisions |
| `importarr/` | Everything entering the library: root-folder rescan, import lists, completed-download import |
| `indexarr/` | Indexer aggregation, Cardigann engine, local release index (SQLite FTS5) |
| `grabarr/` | Download clients: torrent (anacrolix) and usenet engines |
| `squasharr/` | Transcode to HEVC 10-bit + AAC, as batch Jobs |
| `captionarr/` | Subtitles, Bazarr-equivalent, distributed |
| `ui/` | Server-rendered web UI (templ + htmx + SSE); never writes status |

API groups: `{catalog,index,download,transcode,subtitle}.clustarr.io/v1alpha1`.
Module `github.com/mediactl/clustarr`. Licence GPL-3.0 (lets us port *arr and
Bazarr logic verbatim — keep the header on every file).

## Invariants — do not break these

- **One controller-writer per resource.** The sole exception is `MediaFile`, and
  the split is **spec versus status**, not status versus status: `importarr`
  creates the resource and owns `MediaFileSpec` (the observed path, size and
  fingerprint, plus the quality, revision, formatScore, matchedFormats and
  releaseType frozen at import); `catalogarr` is the sole writer of all of
  `MediaFileStatus`, and additionally takes over `spec.sizeBytes`,
  `spec.modTime` and `spec.original` once it incorporates a transcode swap.
  Both write with their own field manager, so the apiserver enforces the split
  rather than convention. (Until Phase C this file claimed `importarr` owned
  `status.file`/`status.probe` and `catalogarr` `status.quality`/
  `status.formatScore`. No such fields exist — `MediaFileStatus` is flat and
  the decided fields are spec, frozen at import per spec §8.4. Nothing had
  reconciled yet, so no code ever contradicted the claim.)
- **All status writes go through `pkg/k8s.PatchStatus`** (server-side apply with a
  named field manager). `.Status().Update()` and `.Status().Patch()` are banned
  outside `pkg/k8s` and golangci-lint's forbidigo rule enforces it.
- **No `float32`/`float64` in `api/`.** controller-gen rejects them without
  `allowDangerousTypes`, and floats do not round-trip across API clients. Use
  `resource.Quantity` for decimals a human types (e.g. seed ratio `"2.0"`) and
  scaled integers for machine telemetry: `Milli` (thousandths, 23.976 fps →
  `23976`), `Centis` (hundredths), `Percent` (whole 0-100).
- **Kubernetes watches are the default coupling**; NATS is the exception, for
  rate-limited or long-running work, RPC, the release firehose and KV leases.
- **Cap every status list** with `+kubebuilder:validation:MaxItems`. Unbounded
  lists in status are how operators melt etcd.
- **The scanner never guesses.** Unattributable files go to
  `LibraryScan.status.unmatched` with the reason, never a speculative item.
- **The UI never writes status** and owns no CRD. User actions patch spec or
  create short-lived resources, so anything the UI does, `kubectl` can do.

## Commands

```bash
make generate      # deepcopy + apply configurations
make manifests     # CRDs + RBAC into config/
make build         # binary into bin/  (never bare `go build` — it drops a binary in the repo root)
make test          # unit + envtest, sets KUBEBUILDER_ASSETS
make lint          # golangci-lint v2
make kind-up       # local cluster with NATS, then: make install deploy
make e2e           # end-to-end suite against that kind cluster (test/e2e, tag e2e)
```

Tools live in `$(go env GOPATH)/bin`: `controller-gen` v0.22.0, `setup-envtest`,
`kustomize`, `golangci-lint-v2`. envtest assets are Kubernetes 1.37.0.

## Gotchas found the hard way

- **`make test` cannot pass in a fresh clone or a new git worktree until
  `helm dependency build` has run.** `charts/clustarr/charts/` holds three
  vendored dependency tarballs (`nats`, `nack`, `keda`) and is gitignored by
  `charts/clustarr/.gitignore`, so a clean checkout does not have them.
  `TestChartAndKustomizeAgreePerComponent` shells out to real `helm template`,
  which refuses with "found in Chart.yaml, but missing in charts/ directory"
  — and it reads as a chart/kustomize *disagreement*, which is a code defect,
  rather than as a missing local artifact. This cost a false red on a merge
  gate run in a throwaway worktree; the branch was fine. Either run
  `helm dependency build charts/clustarr` first or copy the `.tgz` files in.
- **`go test ./...` passing does not mean the CRDs are valid.** `pkg/crdcheck`
  and the envtest suites skip silently when `KUBEBUILDER_ASSETS` is unset. Only
  `make test` (or exporting the assets path) compiles the CEL rules against a real
  apiserver. A suite that finishes in milliseconds skipped.
- **controller-gen v0.22.0's `applyconfiguration` generator ignores output rules**
  — it writes next to the types regardless of `output:...:dir`. The Makefile
  documents the workaround.
- **Server-side apply replaces a field manager's ownership set on every apply —
  it does not merge.** Any field that manager previously sent and now omits is
  *released*, which reads as "reset to zero" on the object. This bit three
  different ways in Phase C alone, so treat every `PatchStatus` call as a
  complete declaration of everything that manager owns:
  - Two applies in one reconcile (spec, then labels): the second erased the
    first. Build one apply configuration instead.
  - A boolean stopped being sent once it was true: it flipped back to false.
    Keep sending it.
  - **An early-return path built a partial status** (conditions only) and
    returned: it wiped `Phase`, `Path`, and every rollup field the happy path
    had set. This is the dangerous one, because the early return is usually a
    transient failure — a full queue, a missing RootFolder — so a healthy
    object gets silently gutted by a blip.
  - **Two different components shared one field manager on one object.** The
    metadata gateway and the grab worker both write `Movie.status` as
    `catalogarr-worker`, so the grab released `status.metadata` — the movie
    dropped to `Phase=Pending` and the gateway refetched, on every grab. Give
    each writer its own manager (as `catalogarr-series` does) rather than
    re-asserting the other's fields.

  **A double-claim is silent, not loud — this file and a code comment both
  said otherwise and both were wrong.** `pkg/k8s.PatchStatus` and
  `pkg/k8s.Apply` append `client.ForceOwnership` unconditionally
  (`patch.go:88,121`), and there is no non-forcing path in the package.
  Forcing is what makes deliberate co-ownership work, so it is not a bug — but
  it means the apiserver never raises the conflict that distinct field managers
  were supposed to surface. The later applier simply takes the field. Proven by
  making one manager claim another's field and watching **every object-value
  assertion keep passing**. So a test that asserts on the object's values
  catches *under*-declaration only; an over-claim is visible in exactly one
  place, and that is where a test meaning to catch one has to look —
  `metadata.managedFields`, by manager name against the field paths it owns.

  And the reason this class keeps surviving review: **two managers can
  *co-own* a field.** SSA only needs force when their values differ, so while
  a second manager keeps applying the same value, a release by the first
  leaves the field standing and the bug is invisible. A "manager X released
  field Y" test that does not deliberately drop the co-owner reports a false
  pass — which is how the rollup release above survived three reviews.

  - **A sibling controller never got the fix its siblings did.** Movie and
    Series gained `reassertKnownStatus`; `mediafile`, built in the same wave,
    kept both partial early returns *and* a second unconditional status apply
    at the end of every reconcile. Once a rule goes in here, sweep every
    controller for it — the rule existing is not the same as the code obeying
    it.

  - **The same rule binds the main resource, not only status — and there the
    failure is a rejected write, not a silent reset.** A manager that applies
    `spec` must send every spec field it owns on every apply. D2-4's
    labels-only apply omitted `spec.clientRef`; the same manager released it,
    and the Download's "clientRef is immutable" CEL rule then rejected the
    write. G1-3's unmonitor path sent only `spec.monitored` under the manager
    that also creates Movies, releasing the required `tmdbID`,
    `qualityProfileRef` and `rootFolderRef`, so the apiserver refused it.
    Route every write from a manager through the one function that renders
    that manager's complete set, and override a field there, rather than
    building a second, narrower apply.

  **`reassertKnownStatus` has an exception.** `MediaFileStatus`' `Conditions`
  and `Sidecars` are lists whose generated `With*` methods **append**, so a
  reassert-then-overwrite helper doubles their entries. Seed a struct and
  render it once instead of copying the sibling pattern verbatim.

  A related trap, same apply, different mechanism: **`WithConditions` appends**
  rather than replacing, so setting conditions both in a shared `baseStatus`
  helper and again at the call site is rejected outright with `duplicate
  entries for key [type="Ready"]`. Set conditions in exactly one place per
  apply.

  Tests miss all three unless they act on an object that **already has status**.
  A test that creates a blank object, triggers the path and asserts cannot
  observe a release, because there was nothing to release. Exercise the failure
  path against an object already in its steady state.
- **A lost update is not an SSA release, and no release test can see one.**
  Phase D1 hit this three times in one wave. A handler reads the object, does
  slow work (an HTTP fetch, a four-page RSS poll), then applies a status seeded
  from that stale snapshot — silently rolling back whatever another writer
  under the same manager did meanwhile. The complete-declaration rule does not
  help: every field *is* declared, just with values from a minute ago. Worse
  than a rollback, a field that was nil when read and non-nil now is
  **cleared**, because a seed that emits a pointer field only when non-nil omits
  it — so a download verb re-enabled an indexer the search fan-out had just
  disabled, undoing the backoff protecting a failing tracker. Every
  release-regression test in the tree is blind to this by construction: the
  object it seeds from is the one it wrote, so there is no window. The
  distinguishing test interleaves a *real* second writer mid-operation; the
  rule is that any path which reads, works slowly, then applies must re-`Get`
  immediately before the apply. `indexarr/worker/rss/worker.go` does this with
  the comment "the poll closes the window".
- **Server-side apply tracks ownership per leaf inside a struct**, not for the
  sub-object as a whole. A renderer that sent only `caps.modes` released
  `caps.limitsMax`, `caps.limitsDefault`, `caps.supportsRawSearch` and
  `caps.categories` on every apply that did not re-probe — and the reconciler
  re-applies every 15 minutes while re-probing every 12 hours, so four of five
  fields zeroed within one tick of the probe that set them. The envtest that
  should have caught it asserted only that `status.caps` was non-nil, which a
  modes-only renderer satisfies. Assert every leaf, not the parent.
- **`metav1.Time` comes back from the apiserver in the replica's local
  timezone, so `.Year()` on it is wrong by a year near New Year.**
  `metav1.Time.UnmarshalJSON` converts to `time.Local` on every round-trip, so
  a release dated 1 January 00:30 UTC reads as the previous year on any host
  west of UTC — and a year feeds folder names and year-tolerance matching, so
  files land in the wrong folder. G2-3 found it in the Audiobook naming
  context; the same `.Year()` call was already in the Album controller and in
  four places in the non-video rescan and import paths. Always
  `.UTC().Year()` (and `.UTC()` before any other calendar field), and test it
  with a Jan 1 date on a west-of-UTC location.
- **Never run `go get` or `go mod tidy` from parallel agents.** They corrupt
  `go.mod`. Add every dependency serially up front, then tell workers not to touch
  it.
- **The git index is shared by every agent in the worktree, so `git add <path>`
  followed by a bare `git commit` is not a path-scoped commit.** It commits
  whatever else was already staged. This swept fourteen files of another agent's
  half-finished, non-compiling work into a docs commit, under a docs message.
  Staging your own path is not the scoping step -- the commit is. The only safe
  form while anyone else is working is a pathspec on the commit itself:
  `git commit -m '...' -- path/to/file`, which builds the commit from HEAD plus
  those paths and ignores the index for everything else. To undo a sweep, do
  **not** `git reset --hard` or `git stash` (both destroy the other agent's
  uncommitted work, and `stash` is process-global): move the branch ref with a
  compare-and-swap, `git update-ref refs/heads/<branch> <sha>^ <sha>`, which
  leaves the index and working tree exactly as they were.
- **A NATS KV key must match `^[-/_=\.a-zA-Z0-9]+$`, and nothing in the Go
  types enforces it.** Build every key through `events.KVKeyToken`; its
  escaping is injective on purpose, because a sanitiser that maps every
  illegal byte to one replacement collapses distinct ids onto one key and
  serves one item's state for another. This escaped twice: an illegal cache
  key made **every** metadata refresh fail forever, and an illegal exclusion
  key left the object **undeletable**, because the finalizer's `Delete`
  validates the key exactly as the `Put` did. Both times every suite that
  could have caught it ran against the in-memory bus, which has no key
  grammar — so the guard is a contract test against a real embedded server
  (`pkg/events/natsbus/kvkey_contract_test.go`), not a regex restated in a
  test file. Restating it is also how `ValidKVKey` shipped permissive:
  nats.go's gate is the character set **plus** no leading `.`, no trailing
  `.` and no `..`, and the first version implemented only the regex.
- **Use `github.com/dlclark/regexp2`, not stdlib `regexp`, for TRaSH patterns.**
  Go's RE2 rejects 157 of the 2791 custom-format regexes (backtracking,
  lookaround). Set `IgnoreCase` and a `MatchTimeout`.
- **Quality profiles are TRaSH-only and opinionated.** Built-in profiles ship as
  embedded data; custom-format editing is deliberately not exposed.

## Code conventions

- GPL-3.0 header from `hack/boilerplate.go.txt` on every Go file.
- Logging is `slog` carried through `context` (`pkg/obs/logging.FromContext`). No
  package-level logger, no logger struct fields. controller-runtime gets a
  `logr.FromSlogHandler` bridge at startup.
- Spans wrap every `Reconcile`, work handler, outbound provider call and ffmpeg
  run; propagation rides the `Clustarr-Trace` header already in `pkg/events`.
- Prometheus metrics use the `clustarr_` prefix and base units. **Never label by
  title, path or release name** — unbounded cardinality.
- Tests: table-driven, testify, fixtures under `testdata/`. Pure functions where
  the logic is tricky (`pkg/pipeline.Project`, `pkg/transcode.Plan`) so it is
  testable without a cluster.

### Conventions across `pkg/`

- **The caller owns rate limiting.** A library package accepts an injected
  limiter (`torznab.WithRateLimit`; the metadata, OpenSubtitles.com and
  Gestdown clients' `Config.Limiter`) and never defaults one on; the
  controller holds one limiter per host. `pkg/subtitles/providers/
  opensubtitlescom` shipped in Phase F with exactly the violation this rule
  warns against -- `Config.Limiter == nil` silently got `rate.NewLimiter(5,
  5)` -- so a caller with no Limiter configured got a private 5 req/s
  allowance per `Provider` instance instead of the shared budget captionarr's
  KV token bucket (`captionarr/throttle`) provides; fixed under ruling R3,
  alongside giving `providers/gestdown` the injection point it had never had.
- **Every HTTP response body is read through a cap** — a package-level max, an
  `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel.
- **Provider errors expose sentinels** (`metadata.ErrNotFound`,
  `subtitles.ErrNotFound`, …) so `errors.Is` works through `Unwrap`.
- **Filesystem writes that must survive a crash go through `pkg/fsops`**
  (`AtomicWrite` fsyncs the file and its parent and takes an explicit mode).

## Status

Pre-alpha, and it reconciles: `catalogarr` and `importarr` register every
controller and worker behind their role flags, and `clustarr all` still stands
every service up in one process.

M0 (done): the 29 CRDs across five groups, `pkg/events` (NATS and in-memory
behind one contract suite), `pkg/k8s`, the binary, manifests, chart and images.

Phase A (done): `pkg/obs` (`logging`, `tracing`, `metrics`, `obs.Bootstrap`),
`docs/observability.md`, the `LibraryScan` kind and `importarr` skeleton,
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
Fixtures under `testdata/<pkg>/`, no network in tests, ffmpeg tests skip
without it.

Phase C (done): M1 catalog core and library rescan — the first phase that
reconciles. `catalogarr` controllers for Movie, Series, Episode, MediaFile,
RootFolder, QualityProfile (a `Bootstrap` runnable seeds the 13 TRaSH
built-ins), DelayProfile, MetadataProvider and Search; the metadata gateway
(registry over Phase B's clients, RPC plus work queue, L1 memory + L2 NATS KV);
`pkg/decision` (`Evaluate`/`Rank`) over `quality.Profile`; the search, grab (KV
lease, delay profiles) and RSS-matcher workers and the wanted cron. `importarr`
controllers for LibraryScan, the RootFolder schedule and ImportExclusion, plus
the rescan worker under the never-guess rule. Traces now cross the bus in
production (`pkg/events` hooks; every `run.go` passes
`k8s.WithBusHooks(obs.BusHooks())` to `k8s.ConnectBus`, AST-guarded); RBAC
generates from controller markers, with a byte-for-byte test holding the
chart's copy to it; readiness is per-service and runs on every replica, not
only the leader; `test/e2e`, `config/e2e`, the fixture image and `hack/e2e.sh`
land scenarios 5, 7 and 8 on kind.

Phase D1 (done): M2 indexers — `indexarr` controllers for `Indexer`,
`IndexerDefinition` and `IndexerProxy`; the caps probe with its 12-hour
refresh (`capsTTL`, `indexarr/controller/indexer/controller.go`); the
health/backoff escalation ladder in `indexarr/status/health.go` (Prowlarr's
ten-rung `EscalationBackOff` periods, 0s to 24h, with a 15-minute startup
grace); the SQLite FTS5 release index (`pkg/relindex`, external-content FTS5,
pure-Go `modernc.org/sqlite`, no cgo); the federated search
(`indexarr/search`) with its dedupe (infohash, then per-indexer
`(indexer, guid)`) and the KV-backed query-limit window
(`clustarr-indexer-limits`); the RSS poll worker and release firehose
(`indexarr/worker/rss`); the three RPC verbs `rpc.indexarr.search|download|query`
that make `catalogarr/worker/search`'s calls reach a real server for the first
time; `indexarr/status` as the single declaration of each field manager's
owned set (`ControllerFields`/`WorkerFields`). E2E scenario 17
(`test/e2e/indexer_test.go`, against the Torznab fixture under
`test/fixtures/torznabstub/`) is written — Indexer health and caps, ranked
federated search, the release firehose, failure backoff — but has **never
been executed against a kind cluster**; that and `make e2e` generally are
deferred by explicit user instruction until D1–D3 implementation is complete,
not by oversight. Reconciles against envtest; not proven end to end.

Phase D2 (done): M3 downloads and import — `grabarr` and `importarr`'s
file-import worker. `pkg/download/torrent` over `anacrolix/torrent`
(idempotent `Add` on the infohash, `Resume`/`SetSeedCriteria` no-ops on an
already-satisfied state, anacrolix state mapped onto `download.Status`);
`pkg/download/usenet` over `Tensai75/nntp` plus pure-Go `javi11/rapidyenc` —
`javi11/nntppool` was ruled out as cgo-only (R9), so this package writes its
own bounded connection pool, 430-failover across configured servers,
pipelining and quota accounting, then PAR2-verifies/repairs (shelling out to
`par2cmdline-turbo`, no Go module) and unpacks with
`github.com/nwaples/rardecode/v2`. `grabarr/status` declares the two disjoint
field-manager sets on `Download.status` — `ControllerFields`
(`k8s.ManagerGrabarr`, nine fields) and `EngineFields`
(`k8s.ManagerGrabarrEngine`, twenty-three) — and `Patch` refuses any other
manager. `grabarr/controller/downloadclient` reconciles `DownloadClient` into
its engine workload (a `StatefulSet` per torrent client, a `Deployment` for
usenet), `DiskSpaceOK` and the blocklist sweep.
`grabarr/controller/download` picks a `ClientRef`, waits for `EngineReady`,
pins the choice into `status.engine` once and never recomputes it, and runs
the `removeDataOnDelete` finalizer. `grabarr/engine/torrent` and
`grabarr/engine/usenet` gate readiness on re-attach completing first (R4) and
write only `EngineFields` telemetry; each carries a level-driven `Reaper`
(`torrent.Reaper`, `usenet.Reaper`) that lists client transfers against
owned Downloads and removes orphans past a grace period, proved by deleting a
`Download` with the engine's watch deliberately not firing.
`importarr/worker/fileimport` consumes `ConsumerImportFile`
(`"importarr-fileimport"`) and creates `MediaFile`, writing only
`MediaFileSpec` per the spec/status split; it also settled the
`Download.status.import` ownership question three comments disagreed on,
onto `k8s.ManagerImportarr`. D2-4, D2-5 and D2-6 could not advance
`status.phase` past `Assigned` — it is `ControllerFields`, not something an
engine can write — so `grep -rn WorkFileImportSubject` found the subject
builder and the consumer but no publisher, and the phase→import handoff that
is D2's own gate could not complete until D2-8a
(`grabarr/controller/download`) added phase derivation from engine telemetry
(`Assigned`→`Queued`→`Downloading`→`Completed`→`Seeding`→`Imported`, plus
`Paused`/`Failed`/`Blocklisted`/`Removing`) and published exactly one
`schema.ImportTask` per completion. Separately, D2-8 found the grabarr
reconcilers, both engines, both reapers and the file-import worker
registered nowhere and wired them into `grabarr/run.go` and
`cmd/clustarr`, added `grabarr` to the registration guard's
`runnableServices`, and regenerated RBAC. A CEL defect found along the way
was Critical: the `Download` release-identity rule
(`api/download/v1alpha1/download_types.go`) compared five `+optional` fields
unguarded, so an absent one raised "no such key" and the apiserver rejected
the write outright — fatal for usenet, which never carries `infoHash` — for
every write after creation, including the status-only applies that are
grabarr's only way to report progress; fixed in `c5e86d5` by guarding every
comparison with `has()`, with `pkg/crdcheck/download_cel_test.go` as the
regression guard using a dynamic client so an absent `infoHash` stays absent
on the wire. E2E scenarios (`test/e2e/download_test.go`,
`test/e2e/import_test.go`: 1 through import, 2, 3, 4, 6) are **written and
never executed**, deferred by user instruction until D1–D3 are all in.
Reconciles against envtest; not proven end to end.

Phase D3 (done): the first UI slice — the pipeline and downloads pages over
real cluster state, streamed. `ui/reader.go`'s `NewClusterReader` builds a
standalone controller-runtime `cache.Cache` (no manager, no metrics or
leader-election port); `Options.Reader` staying nil is still legal and
renders empty pages. `/readyz` now gates on cache sync while `/healthz`
stays unconditional, and `config/rbac/ui_role.yaml` is a hand-written
`get,list,watch`-only role (no `*/status`) bound to the previously-unbound
`ServiceAccount ui`. `ui/projection`'s `Projection` replaces the old
per-connection 5s poll with one process-wide tick that lists `Download`,
`TranscodeJob`, `SubtitleRequest`, `Search` and `MediaFile`, indexes them by
owner through `metav1.OwnerReference` UID, projects `pipeline.Entry` and
`downloadv1.Download` rows, and fans both out through `Subscribe`/
`SubscribeDownloads` to every open SSE connection. `ui/views/downloads.templ`
renders all eleven `DownloadPhase` values plus a `DownloadClient` section
without panicking on a zero-value status; `GET /downloads` and
`GET /events/downloads` join the existing pipeline page and stream. The
never-writes invariant (CLAUDE.md) is now two enforced guards rather than an
accident of no RBAC grant existing (D3-4, `6597877`): `ui/guard_test.go`'s
`TestUINeverWrites` is an AST guard over every non-test file in `ui/`
rejecting a `client.Writer` selector (`Create`, `Update`, `Patch`, `Delete`,
`DeleteAllOf`, `Status`) on anything typed from controller-runtime's
`client` package, plus any import of `pkg/k8s`; `cmd/clustarr/ui_rbac_test.go`'s
`TestUIRoleGrantsOnlyReadVerbs` and `TestUIRoleChartMatchesConfig` assert
`ui_role.yaml`'s verbs are a subset of `{get,list,watch}` with no `/status`
resource, and hold the chart's copy byte-identical to it. E2E scenario 14
(`test/e2e/ui_test.go`: the pipeline and downloads pages) is **written and
never executed**, deferred the same way. Reconciles against envtest; not
proven end to end.

Next: Phase D is done (D1 indexers, D2 downloads and import, D3 the first UI
slice) → **M4 transcode** (`squasharr`) is next → M5 subtitles → M6 parity,
import lists and non-video inventory, then **Phase H: end-to-end proof on
kind**. Phase detail, and the list Phase C, D1 and D2 carried forward, are in
`docs/superpowers/plans/2026-09-18-remaining-work.md`; milestone detail is in
the spec's §16 and amendment §A4.

**Nothing is finished until it is proven end to end on a kind cluster.** Every
phase from C onward lands its milestone's scenarios in `test/e2e` (real CRs,
real controllers, real NATS, real files under `/data`, in-cluster fixture
services, no Internet) and keeps `hack/e2e.sh` green; Phase H audits that every
scenario in the plan exists and passes. Unit, envtest and build-tagged
integration suites do not substitute for it.
