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
    re-asserting the other's fields; then a genuine double-claim is a loud
    apiserver conflict instead of silent data loss.

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
  limiter (`torznab.WithRateLimit`, the metadata and OpenSubtitles clients) and
  never defaults one on; the controller holds one limiter per host.
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

Next: `grabarr` and `importarr`'s file-import worker (**Phase D2**, M3
downloads and import) and the first UI slice (**Phase D3**) are in flight →
M4 transcode → M5 subtitles → M6 parity, import lists and non-video
inventory, then **Phase H: end-to-end proof on kind**. Phase detail, and the
list Phase C and D1 carried forward, are in
`docs/superpowers/plans/2026-09-18-remaining-work.md`; milestone detail is in
the spec's §16 and amendment §A4.

**Nothing is finished until it is proven end to end on a kind cluster.** Every
phase from C onward lands its milestone's scenarios in `test/e2e` (real CRs,
real controllers, real NATS, real files under `/data`, in-cluster fixture
services, no Internet) and keeps `hack/e2e.sh` green; Phase H audits that every
scenario in the plan exists and passes. Unit, envtest and build-tagged
integration suites do not substitute for it.
