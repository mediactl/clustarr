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

  A related trap, same apply, different mechanism: **`WithConditions` appends**
  rather than replacing, so setting conditions both in a shared `baseStatus`
  helper and again at the call site is rejected outright with `duplicate
  entries for key [type="Ready"]`. Set conditions in exactly one place per
  apply.

  Tests miss all three unless they act on an object that **already has status**.
  A test that creates a blank object, triggers the path and asserts cannot
  observe a release, because there was nothing to release. Exercise the failure
  path against an object already in its steady state.
- **Never run `go get` or `go mod tidy` from parallel agents.** They corrupt
  `go.mod`. Add every dependency serially up front, then tell workers not to touch
  it.
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

Pre-alpha. **Nothing reconciles yet** — every `setupControllers` and
`setupWorkers` is still an empty registration point.

M0 (done): the 29 CRDs across five groups, `pkg/events` (NATS and in-memory
behind one contract suite), `pkg/k8s`, the binary, manifests, chart and images.

Phase A (done): `pkg/obs` (`logging`, `tracing`, `metrics`, `obs.Bootstrap`),
`docs/observability.md`, the `LibraryScan` kind and `importarr` skeleton,
`pkg/pipeline` and the `ui` skeleton; all services wired into the binary. The
tracing helpers still have no production call sites (see the doc's banner).

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
`importlist` (five lists, Dedupe, ApplySyncLevel); `fsops`/`ratelimit`. Fixtures
under `testdata/<pkg>/`, no network in tests, ffmpeg tests skip without it.

Next: **Phase C** (M1 catalog core and library rescan, plus wiring trace
propagation into `pkg/events`), then M2 indexers → M3 downloads, import and
the first UI slice → M4 transcode → M5 subtitles → M6 Prowlarr parity, import
lists and non-video inventory, and finally **Phase H: end-to-end proof on
kind**. Phase detail is in `docs/superpowers/plans/2026-09-18-remaining-work.md`;
milestone detail is in the spec's §16 and amendment §A4.

**Nothing is finished until it is proven end to end on a kind cluster.**
Every phase from C onward lands its milestone's scenarios in `test/e2e`
(real CRs, real controllers, real NATS, real files under `/data`, in-cluster
fixture services, no Internet) and keeps `hack/e2e.sh` green; Phase H audits
that every scenario in the plan exists and passes. Unit, envtest and
build-tagged integration suites do not substitute for it.
