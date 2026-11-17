# Clustarr

A distributed, event-driven media stack for Kubernetes: controllers and CRDs that
manage a media collection end to end, from wanting something through finding,
downloading, importing, transcoding and subtitling it.

Custom resources are the interface. `kubectl get movies,series,mediafiles` is
the UI -- an item's grabs are its `status.downloads[]`, a file's transcode and
subtitles its MediaFile's status -- and the web UI is a view over the same resources.

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
| `cmd/agent --domain <d>` | `native` (cgo) | one domain per process, never leader-elected, read-only to Kubernetes: `catalog`, `events`, `metadata`, `import`, `index`, `caption`, `torrent-engine`, `usenet-engine` (`app/<svc>/agent`) |
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
  (except the usenet engine's `par2` until par2go lands).
- **The scanner never guesses.** Unattributable files go to `status.unmatched`.
- **The UI never writes status** and owns no CRD; every write is a spec patch or
  create in `ui/actions` (`TestUINeverWrites`); it holds a bus connection for reads
  plus three metadata requests only; `cmd/manager` and `cmd/ui` never link ffgo or purego.
- **Artwork objects have two writers split by variant** (`original`: metadata
  gateway; `overlay`: renderer); no object links.

## Commands

```bash
make generate      # deepcopy + apply configurations
make manifests     # CRDs + one RBAC ClusterRole per identity into config/
make build         # bin/manager, bin/ui, bin/agent (cgo), bin/markers, bin/transcode (never bare `go build` for a release)
make native-assets # FFmpeg 9 libs + libffshim, ONNX Runtime, test-only CLIs
make chart-deps    # vendor the chart's nats and cloudnative-pg tarballs
make test          # unit + envtest + native suites (sets KUBEBUILDER_ASSETS and friends)
make lint          # golangci-lint v2
make brand-gate    # no *arr brand outside the dated records
make css           # ui/static/app.css from input.css (pinned Tailwind CLI)
make templ         # regenerate *_templ.go (pinned templ v0.3.1020; never the templ on PATH)
make run-dev       # manager, ui and one agent per domain against your kubeconfig
make e2e           # end-to-end suite against the kind cluster
```

Tools live in `$(go env GOPATH)/bin`. `go.mod` replaces ffgo, anacrolix/torrent,
onnxruntime-purego and controller-runtime with `mediactl/` forks at published tags.
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
  envtest also needs `KUBE_FEATURE_ClientsAllowCBOR=false KUBE_FEATURE_ClientsPreferCBOR=false`
  and `helm dependency build charts/clustarr` in a fresh clone.
- **Never list every Episode or MediaFile with kubectl on talos** (each page past
  the first is read from etcd). Read by name, or filter on the server with
  `--field-selector`/`-l` and `--chunk-size=0`.
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
