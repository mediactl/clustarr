# Clustarr

A distributed, event-driven media stack for Kubernetes: controllers and CRDs that
manage a media collection end to end, from wanting something through finding,
downloading, importing, transcoding and subtitling it.

Custom resources are the interface for what a person declares: `kubectl get
movies,series,artists` and every settings kind. What a controller creates --
MediaFile, Episode, Album, Book, Audiobook, Issue -- is a row in Postgres, the
**library store** (ADR-0024, `pkg/library`), with the same Go types, spec and
status; `kubectl` does not reach those. An item's grabs are its
`status.downloads[]`, a file's transcode and subtitles its MediaFile's status,
and the web UI is a view over both stores.

**This file is the short core.** It is loaded into every session and every
subagent, so it holds only what most tasks need. The detailed records live in
`docs/claude/` (table below): read the one for the area you are about to touch,
`grep -n '^\*\*\|^- \*\*' <file>` first, then the section. **Record new decisions,
dated owner rulings and incident notes in the topic file, never here**; add a line
here only when it is a rule most tasks need. Keep this file under 300 lines.
A code comment or commit that cites `CLAUDE.md, "<section>"` means that section's
file in `docs/claude/` (they were this file's sections until 2026-11-17).

## Read before designing anything

- `docs/superpowers/specs/2026-09-18-clustarr-design.md` — the design of record.
  Field-level CRD definitions, event subjects, data flows, milestones.
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` — adds
  `import`, observability and the UI. **Where the two disagree, the amendment wins.**
- The unify branch's designs (2026-10-06/07), each with an "as built" record:
  `2026-10-06-manager-agent-split-design.md` (ADR-0018, ADR-0017),
  `2026-10-06-mediafile-remediation-loop-design.md` (ADR-0016),
  `2026-10-07-agents-report-over-nats-design.md` (ADR-0019) and
  `2026-10-07-unbranded-names-design.md` (Wave U; its §6 maps every old name).
- `2026-11-26-library-store-design.md` (ADR-0024): the library kinds as rows in
  Postgres; its §9 is what was built.
- `docs/adr/` — why NATS, why GPL-3.0, why one manager, and so on.
  `docs/adr/README.md` is the index and the supersede lifecycle.
- `docs/research/*.md` — nine verified research notes with verified API shapes.
  Long: `grep -n '^#'` first. Prefer them over guessing.

The spec is authoritative for names, fields and behaviour. Do not invent fields;
do not drop them.

## Topic records (read before working in the area)

| File | Read it before touching |
| --- | --- |
| `docs/claude/overview.md` | the full Services tables (each binary's contents, the images and what they pack), the Commands notes, tool versions and fork tags |
| `docs/claude/transcoding.md` | `app/transcode`, `pkg/transcode`, the standard and its versions, encoder tiers, admission, the Transcoded / cutoff / baseline rules in `pkg/decision` and `importplan`, probing |
| `docs/claude/livetv.md` | `app/livetv`, `pkg/iptv`, the tuner and relay, lineups, guides, Plex DVR blocks, cluster-plex's Live TV half |
| `docs/claude/ui.md` | anything under `ui/`: pages, the look, htmx/portal/morph rules, settings forms, the Plex provider (`ui/plex`), search and ranking rules the pages show, anime and audio policy, collections and hubs |
| `docs/claude/music-books.md` | Artist/Album/Author/Book/Comic/Issue/Audiobook: containers, leaf phases, attribution, naming, root scans, NFO |
| `docs/claude/invariants.md` | the full text of the invariants summarised below, with their guard tests |
| `docs/claude/gotchas.md` | before debugging anything that "cannot happen": server-side apply, NATS/JetStream, usenet and torrent engines, caches and the row cache, object stores, ffgo, envtest, Helm, shared-checkout git |
| `docs/claude/conventions.md` | logging subsystems, metrics, memory limits, the `pkg/` conventions in full |
| `docs/claude/status.md` | what was built when (M0 through the manager/agent split and after), and what is next |

## Services

One Go module, five binaries (ADR-0018):

| Binary | Image | Runs |
| --- | --- | --- |
| `cmd/manager` | `clustarr` (static, distroless) | the control plane (ADR-0019): every reconciler under one lease and one cache (`app/<svc>/manager`), the MediaFile remediation loop (`app/remediation`, ADR-0016), task admission and routing (`app/dispatch`), intake (`app/intake`), the NATS topology, the agents' HPAs (`app/autoscale`), object-store maintenance, the Live TV tuner |
| `cmd/agent --domain <d>` | `native` (cgo) | one domain per process, never leader-elected, read-only to Kubernetes: `catalog`, `events`, `metadata`, `import`, `index`, `caption`, `torrent-engine`, `usenet-engine`, and the optional `trailers` in its own image (ADR-0023) (`app/<svc>/agent`) |
| `cmd/ui` | `clustarr` | the web UI and the Plex provider |
| `cmd/markers` | `native` | segment detection on ffgo and ONNX Runtime |
| `cmd/transcode` | `native` | transcode pool and graft Jobs on ffgo |

| Dir | Owns |
| --- | --- |
| `app/catalog/` | media domain, metadata gateway, release decisions, artwork |
| `app/import/` | root-folder rescan, import lists, completed-download import |
| `app/indexer/` | indexer aggregation, Cardigann engine, release index |
| `app/grab/` | download clients: torrent and usenet engines |
| `app/transcode/` | transcode to HEVC/MP4 on worker pools and graft Jobs; admission |
| `app/caption/` | subtitles |
| `app/remediation/` | the MediaFile remediation loop: file and item keys, planners, item stages |
| `app/dispatch/`, `app/intake/` | task admission and routing; intake of candidates, scan observations, advisories |
| `app/autoscale/`, `app/maintenance/` | agents' HPAs and External Metrics; object-store maintenance |
| `app/segments/`, `app/livetv/` | markers' analysis; Live TV controllers and tuner |
| `ui/` | server-rendered web UI (templ + htmx + SSE), `/art`, the Plex provider; never writes status |

API groups: `{catalog,index,download,transcode,subtitle}.clustarr.io/v1alpha1`, and
`clustarr.io/v1alpha1` for Live TV. Module `github.com/mediactl/clustarr`. Licence
GPL-3.0 (keep the header from `hack/boilerplate.go.txt` on every file).

**Names.** The domain names -- `catalog`, `import`, `index`, `grab`, `transcode`,
`caption`, `markers` -- are the only internal names. No per-service *arr brand
anywhere outside the dated records; `make brand-gate` fails on one. `clustarr`
is the product name and stays.

## Invariants — do not break these

Full text and guard tests: `docs/claude/invariants.md`.

- **A library kind is a row, read and written through the seams a custom
  resource is** (ADR-0024): the manager's cache and client and
  `pkg/k8s.PatchStatus`, wired to the store (`pkg/library/ctrlwire`,
  `readwrite`). Never a hand `INSERT`/`UPDATE`, never a client-go or dynamic
  client for one, never a new CRD for a kind a controller creates. Its schema
  is its CRD file under `pkg/library/schemas/` (`make manifests` puts it there);
  a validation rule on one is also written in `pkg/library/validate.go`. No
  agent links `pkg/library/readwrite`. The tables are a contract with cluster-plex.
- **The manager is the only writer of every CR and Secret** (ADR-0019). Agents,
  markers and transcode pods write no Kubernetes object; a worker answers in a
  record (`pkg/records`, compare-and-swap) or on the intake streams. Exceptions:
  PlexCollection status + finalizer and PlexHubLayout status are cluster-plex's.
- **One controller-writer per resource.** MediaFile is split spec (import) versus
  status (`catalog`, through the remediation loop), each under its own field manager.
- **All status writes go through `pkg/k8s.PatchStatus`** (server-side apply, named
  field manager). `.Status().Update()` / `.Status().Patch()` are banned outside `pkg/k8s`.
- **Every apply is a complete declaration of what that manager owns** — a field
  it stops sending is released (reads as reset). One apply per manager per
  reconcile; early returns re-assert known status; re-`Get` before applying after
  slow work. `ForceOwnership` is always on, so an over-claim is silent.
- **No `float32`/`float64` in `api/`.** Use `resource.Quantity` or scaled integers.
- **Cap every status list** with `+kubebuilder:validation:MaxItems`.
- **The manager couples through Kubernetes watches; agents through NATS only.**
  Only the manager ensures the NATS topology; subscriptions bind, never create.
- **One domain per agent process**; every container names its binary; no image
  ships `ffmpeg`, `ffprobe` or `par2`; no production code starts a process
  (except the usenet engine's `par2` until par2go lands, and the optional
  trailers agent's `yt-dlp`, ADR-0023).
- **The scanner never guesses.** Unattributable files go to `status.unmatched`.
- **The UI never writes status** and owns no CRD; every write is a spec patch or
  create in `ui/actions` (`TestUINeverWrites`); it holds a bus connection for reads
  plus three metadata requests only; `cmd/manager` and `cmd/ui` never link ffgo or purego.
- **Artwork objects have two writers split by variant** (`original` and its
  twin `compact`: metadata gateway; `overlay` and `overlay-compact`: renderer);
  no object links. A regular poster's bytes never change for the compact one.

## Commands

```bash
make generate      # deepcopy + apply configurations
make manifests     # CRDs + one RBAC ClusterRole per identity into config/; a library kind's CRD goes to pkg/library/schemas/
make build         # bin/manager, bin/ui, bin/agent (cgo), bin/markers, bin/transcode (never bare `go build` for a release)
make native-assets # FFmpeg 9 libs + libffshim, ONNX Runtime, test-only CLIs
make chart-deps    # vendor the chart's nats and cloudnative-pg tarballs
make test          # unit + envtest + native suites (sets KUBEBUILDER_ASSETS and friends)
make lint          # golangci-lint v2
make brand-gate    # no *arr brand outside the dated records
make css           # ui/static/app.css from input.css (pinned Tailwind CLI)
make templ         # regenerate *_templ.go (pinned templ v0.3.1118; never the templ on PATH)
make run-dev       # manager, ui and one agent per domain against your kubeconfig
make e2e           # end-to-end suite against the kind cluster
```

Tools live in `$(go env GOPATH)/bin`. `go.mod` replaces ffgo and
onnxruntime-purego with `mediactl/` forks at published tags.
**A third-party gap is never patched in this tree**: take the upstream release
with the fix, else a `mediactl/` fork rebased on the upstream tag with the PR opened.

## Working in this checkout

- **Other sessions share this checkout, its index and the scratchpad.** Commit
  only with a pathspec: `git commit -m '...' -- <paths>` (`git add` new files
  first). Never `git stash`, `git reset --hard`, bare `git commit`, or a push from
  a subagent. Find commits by subject, not SHA. No worktrees; work on `main`.
- **Never run `go get` or `go mod tidy` from parallel agents.**
- **`go test ./...` passing does not mean the CRDs are valid**: envtest suites
  skip silently without `KUBEBUILDER_ASSETS`. `make test` is the gate; a hand-run
  envtest also needs `CLUSTARR_PG_ASSETS=$(go env GOPATH)/bin/pg-assets` (the
  library kinds are rows in an embedded Postgres, `test/librarytest`), and `helm dependency build
  charts/clustarr` in a fresh clone. A test's own manager or client on an envtest
  config is `librarytest.NewManager` / `librarytest.NewClient`, never
  `ctrl.NewManager` / `client.New`: those cannot see a library kind.
- **Episodes and MediaFiles are read with SQL**: talos was cut over on
  2026-11-26 (`docs/runbooks/library-store-cutover.md`) and `kubectl` no
  longer knows the six library kinds. `kubectl -n clustarr-system exec
  clustarr-postgres-2 -c postgres -- psql -U postgres -d clustarr`, then
  `select name, body->'spec', status from library.mediafile where ...`. Never
  a hand `UPDATE`: a repair is a spec patch through the ui or the manager's client.
- **Generated files** (`ui/static/app.css`, `*_templ.go`, `config/`, apply
  configurations) are built from the whole tree: regenerate from a clean state
  and commit them with their sources.
- **A UI markup or styling change is checked in a browser**, not only by tests
  (`docs/claude/ui.md`).
- **A test built from a fixture shaped like the answer cannot fail**: build inputs
  through the real producer, and falsify (revert the fix, watch the test fail).
- **After fixing a class of bug, grep for the other copies** of the thing you fixed.
- A guard test named with † in the records is planned, not yet written; review
  holds that invariant until it lands.

## Code conventions

- GPL-3.0 header on every Go file.
- Logging is `slog` carried through `context` (`pkg/obs/logging.FromContext`); no
  package-level logger. Every entry point names its subsystem first
  (`logging.Subsystem(ctx, "<svc>.<pkg>")`, a literal in `logging.Subsystems`).
  An error's key is `err`.
- Spans wrap every `Reconcile`, work handler, outbound provider call and ffmpeg run.
- Metrics use the `clustarr_` prefix and base units and are listed in
  `docs/observability.md`. **Never label by title, path or release name.**
- Every binary imports `automemlimit`; never add `automaxprocs`.
- Tests: table-driven, testify, fixtures under `test/data/`; pure functions where
  the logic is tricky.
- `pkg/`: the caller owns rate limiting (inject the limiter); every HTTP body is
  read through a cap; provider errors expose sentinels; crash-safe writes go
  through `pkg/fsops`; a copy onto `/data` uses `fsops` (`O_DIRECT`), never `io.Copy`.

## Status

Pre-alpha, and it reconciles: one `cmd/manager`, one `cmd/agent` per domain,
`cmd/ui`, `cmd/markers`, `cmd/transcode`; live on the owner's talos cluster.
History, what is deployed versus only built, and what is next: `docs/claude/status.md`.
**Nothing is finished until it is proven end to end on a kind cluster** (`test/e2e`,
`hack/e2e.sh`); unit, envtest and integration suites do not substitute for it.
