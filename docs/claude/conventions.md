# Clustarr record: Code conventions

Moved verbatim out of CLAUDE.md on 2026-11-17 (owner: keep CLAUDE.md to the rules most tasks need). CLAUDE.md is the
short core and says when to read this file; new decisions, dated owner rulings and incident notes for this area are
recorded here.

## Code conventions

- GPL-3.0 header from `hack/boilerplate.go.txt` on every Go file.
- Logging is `slog` carried through `context` (`pkg/obs/logging.FromContext`;
  `docs/superpowers/specs/2026-11-10-structured-logging-design.md`,
  `docs/observability.md`'s Logging section). No package-level logger, no
  logger struct fields, and `slog.Default()` is only the drain for
  third-party `log` output (`TestNoGlobalLogger`, `TestNoStdlibLogOrStderrPrints`).
  Every entry point names its subsystem as its first statement -- a
  `Reconcile`, a leader runnable's `Start`, a bus subscription, a server's
  constructor, a binary after its root -- `ctx = logging.Subsystem(ctx,
  "<svc>.<pkg>")`, the name a literal in the sorted `logging.Subsystems`
  table (`TestEverySubsystemIsInTheTable`; the naming rule is the table's doc).
  An error's key is `err`, never `error` (`TestNoErrorAttributeKey`). Levels
  come from `/etc/clustarr/logging.yaml` (the installers' logging ConfigMap,
  polled every 10 s; chart `global.logging` and `logging`), then set flags,
  then `LOG_LEVEL`/`LOG_FORMAT`; a per-subsystem override covers every
  subsystem below it by dotted prefix. controller-runtime (`logr`), client-go
  (`klog`), nats.go, anacrolix, FFmpeg (`pkg/ffruntime/ffroute`, errors as
  WARN) and ONNX Runtime (`pkg/ortlog`, through the `mediactl/onnxruntime-purego`
  fork) are bridged onto the same stream under their own subsystems.
  `service.name` is the process's tracing name: `manager`, `agent-<domain>`,
  `engine-<client>`, `ui`, `markers`, `transcode`.
- Spans wrap every `Reconcile`, work handler, outbound provider call and ffmpeg
  run; propagation rides the `Clustarr-Trace` header already in `pkg/events`.
- Prometheus metrics use the `clustarr_` prefix and base units; the catalogue
  is held to `docs/observability.md`'s tables (`TestCatalogueMatchesTheObservabilityDoc`,
  2026-11-10: a new series goes in the doc with it), and a label name is
  refused by whole word, never substring (`profile` is not `file`). **Never label by
  title, path or release name** — unbounded cardinality.
- **Every binary's Go memory limit is a share of its cgroup's** (owner,
  2026-11-03): each `cmd/*/main.go` has `import _
  "github.com/KimMachineGun/automemlimit"` (`TestEveryBinaryImportsAutomemlimit`),
  which reads the container's memory limit before `main` and takes 90% of
  it, or `AUTOMEMLIMIT`'s share. Only the processes whose FFmpeg, par2 or
  ONNX Runtime allocates outside the Go heap name a share: agent-import,
  agent-caption and the transcode pool and graft pods 0.60, markers 0.50 (OD42);
  no installer or pod the manager builds sets a `GOMEMLIMIT` byte count, and
  an explicit one (the chart's `goMemLimit`) wins. The ui may name a share too (chart `ui.automemlimit`, unset by default;
  owner, 2026-11-16): a ui's resting size is whatever its share allows, and
  talos's three pods sat at 1.3 GiB of 1.5 GiB over a 480 MiB live heap.
  Profiled the same day: every component's class merge (`utils.CN`) is
  remembered (`ui/components/utils/cn_memo.go`; a guide page's 9,000 cells
  merged one ~1 KB class string each, 230 MB a load), and
  `filter.Matches` folds nothing for a rule of another group. A compressed
  row cache was measured and NOT built for the ui: the projection and the
  Plex index list every Episode and MediaFile zero-copy every 5 s, which a
  row cache would turn into a decode of each. GOMAXPROCS is the
  runtime's own since Go 1.25 (the cgroup's CPU limit, rounded up, at least
  2, followed when it changes): never add `go.uber.org/automaxprocs`, which
  rounds down to 1 on the 500m and 1-CPU pods and stops that
  (`TestNoModuleSetsGOMAXPROCS`).
- Tests: table-driven, testify, fixtures under `test/data/`. Pure functions where
  the logic is tricky (`pkg/pipeline.Project`, `pkg/transcode/standard.Plan`) so it is
  testable without a cluster.

### Conventions across `pkg/`

- **The caller owns rate limiting.** A library package accepts an injected
  limiter (`torznab.WithRateLimit`; the metadata, OpenSubtitles.com and
  Gestdown clients' `Config.Limiter`) and never defaults one on; the
  controller holds one limiter per host. `pkg/subtitles/providers/
  opensubtitlescom` shipped in Phase F with exactly the violation this rule
  warns against -- `Config.Limiter == nil` silently got `rate.NewLimiter(5,
  5)` -- so a caller with no Limiter configured got a private 5 req/s
  allowance per `Provider` instance instead of the shared budget caption's
  KV token bucket (`app/caption/throttle`) provides; fixed under ruling R3,
  alongside giving `providers/gestdown` the injection point it had never had.
- **Every HTTP response body is read through a cap** — a package-level max, an
  `io.LimitReader(body, max+1)` and an `ErrResponseTooLarge` sentinel.
- **Provider errors expose sentinels** (`metadata.ErrNotFound`,
  `subtitles.ErrNotFound`, …) so `errors.Is` works through `Unwrap`.
- **Filesystem writes that must survive a crash go through `pkg/fsops`**
  (`AtomicWrite` fsyncs the file and its parent and takes an explicit mode).
- **A copy across filesystems writes its destination with `O_DIRECT`**
  (`fsops.copyFile`: `MoveAtomic`'s EXDEV fallback, `CopyDir`,
  `HardlinkOrCopy`), 64 MiB per write, the source's pages dropped behind
  the read. /data is NFS, and a buffered copy onto it leaves the data in
  the page cache under writeback, charged to the pod with no per-cgroup
  throttling: on 2026-10-15 a buffered 3 GiB copy held 1.9 GiB in
  writeback and crawled at 8.3 MB/s, and transcode pool and usenet engine
  pods were OOM-killed moving outputs and releases from local scratch.
  Never move a large file onto /data with `io.Copy` or `os.WriteFile`.
  Its buffers are sized to the file (`copyBuffers`: up to four 64 MiB
  chunks, no more than the file fills) and mapped outside the Go heap,
  unmapped when the copy ends (2026-11-03: four 64 MiB Go buffers per copy,
  a sidecar's too, were most of a transcode pool worker's ~290 MiB heap).

