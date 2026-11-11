# Structured logging (clustarr) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every clustarr line is one JSON record of the contract's shape, its level set per deployment and per subsystem from a ConfigMap that is re-read without a restart, and every third-party writer is bridged with its level mapped.

**Architecture:** `pkg/obs/logging` gains a handler that owns the level logic (a floor plus dotted-prefix overrides held in one `Levels`), the level names, UTC time and the `service` group; `logging.Subsystem` names a unit on the context. A file watcher re-reads `/etc/clustarr/logging.yaml` every 10 s. `obs.Bootstrap` and the two standalone binaries build the root through one `logging.Root`, which also drains `log` and `slog.Default`, and bridges controller-runtime, klog, NATS, anacrolix, FFmpeg and (through a fork of its binding) ONNX Runtime. The chart and kustomize render one ConfigMap mounted in every pod, the manager stamping it onto the pods it builds.

**Tech Stack:** Go 1.27, `log/slog`, `github.com/go-logr/logr`, `k8s.io/klog/v2`, `sigs.k8s.io/yaml`, `github.com/spf13/pflag`, Helm, kustomize.

**Spec:** `docs/superpowers/specs/2026-11-10-structured-logging-design.md` (the contract). cluster-plex's own plan, `cluster-plex/docs/superpowers/plans/2026-11-10-structured-logging.md`, ports Tasks 1 and 2 of this one and must start after they land.

## Global Constraints

- Module `github.com/mediactl/clustarr`, Go 1.27, the GPL-3.0 header from `hack/boilerplate.go.txt` on every new Go file.
- Level names exactly `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`, `FATAL`; `logging.LevelTrace = slog.Level(-8)`, `logging.LevelFatal = slog.Level(12)`. Below DEBUG is TRACE, DEBUG up to INFO is DEBUG, above ERROR+3 is FATAL; never `DEBUG-4`.
- Keys: `time` (RFC 3339 with nanoseconds, UTC), `level`, `source` (only with `addSource`), `msg`, `service.namespace`, `service.name`, `service.version`, `service.instance.id`, `subsystem`, then the logger's own attributes in the order they were added, then the record's. The one error key is `err`.
- `service.namespace` is `clustarr`. `service.name`: `manager`, `agent-<domain>`, `engine-<DownloadClient name>` for the two engine domains, `ui`, `markers`, `transcode`. The tracer's resource carries the same `service.name`, plus `service.namespace` and `service.version`.
- `service.version` is `pkg/version.Version` (already set by `-X` in the Makefile and both Dockerfiles). `service.instance.id` is `$POD_NAME`, else `os.Hostname()`.
- Defaults: `level: info`, `format: json`, `addSource: false`, no overrides. Config file `/etc/clustarr/logging.yaml`, top key `logging:`, polled every 10 s (`logging.DefaultPollInterval`). A missing file is not an error.
- Precedence for `level` and `subsystems`: the file, then flags (`--log-level`, `--log-subsystem-level name=level`, repeatable), then `LOG_LEVEL`. `format` and `addSource` resolve the same way but only at start. `subsystems` from the file replace the flags' map whole.
- An override applies to its subsystem and every subsystem below it by dotted segment: `livetv` covers `livetv.tuner`, never `livetvx`.
- `cmd/manager` and `cmd/ui` link no ffgo and no purego (`TestManagerNeverLinksADynamicLoader`, `TestUINeverLinksADynamicLoader`); the ONNX fork is linked by `cmd/markers` and the caption agent only.
- Third-party gaps: upstream's release where it has the fix, else a `mediactl/` fork rebased on the upstream tag with the fix as its own commit and the PR opened upstream; never a wrapper or an in-tree patch.
- Shared checkout: commit with a pathspec (`git commit -m ... -- <paths>`), `git add` new files first, never `git stash`, never a worktree, never `go get` or `go mod tidy` beside another agent, never push. No envtest runs until the final verification.

## Review Focus

1. **A ConfigMap mounted with `subPath` never receives an update.** Every mount of the logging ConfigMap must be a directory mount at `/etc/clustarr`, or the live knob silently does nothing. Pinned in Task 8 (`TestTheLoggingConfigIsADirectoryMount`).
2. **A DownloadClient in a namespace without the ConfigMap.** The engine pod must still start, on the defaults. Pinned in Task 8 (the runtime volume is `optional: true`; `TestRuntimePodsMountTheLoggingConfigOptionally`).
3. **A half-written or invalid file during a ConfigMap swap.** The last good settings stay and one WARN is written per bad content, not one every 10 s. Pinned in Task 2 (`TestWatchFileKeepsTheLastGoodSettingsOnABadFile`).
4. **An override whose name is a string prefix but not a dotted segment.** `livetv: warn` must not quiet `livetvx`. Pinned in Task 1 (`TestOverridesMatchByDottedSegment`).
5. **An `ffmpeg: debug` override taken back out.** FFmpeg's C-side level must return to warning, or every pod keeps paying for verbose logging. Pinned in Task 4 (`TestTheFFmpegOverrideRaisesAndRestoresTheCSideLevel`).

---

### Task 1: The record, the levels and subsystems

**Files:**
- Create: `pkg/obs/logging/level.go`, `pkg/obs/logging/handler.go`, `pkg/obs/logging/subsystem.go`
- Modify: `pkg/obs/logging/logging.go` (Options, New), `pkg/obs/logging/context.go` (unchanged API)
- Test: `pkg/obs/logging/handler_test.go`, `pkg/obs/logging/level_test.go`, `pkg/obs/logging/subsystem_test.go`, golden `pkg/obs/logging/testdata/record.golden.jsonl`

**Interfaces:**
- Produces:
  - `const LevelTrace, LevelFatal slog.Level`
  - `func ParseLevel(s string) (slog.Level, error)`: slog's spellings, case-insensitive, plus `trace` and `fatal`.
  - `func LevelName(l slog.Level) string`
  - `type Service struct{ Namespace, Name, Version, InstanceID string }`
  - `type Levels struct{...}` with `NewLevels(floor slog.Level, overrides map[string]slog.Level) *Levels`, `(*Levels).Set(floor slog.Level, overrides map[string]slog.Level)`, `(*Levels).For(subsystem string) slog.Level`, `(*Levels).OnChange(fn func()) (cancel func())`. Safe for concurrent use; `For` is a lock-free read of an atomic snapshot.
  - `type Handler struct{...}`: `NewHandler(w io.Writer, format string, addSource bool, svc Service, levels *Levels) *Handler`; implements `slog.Handler`; `(*Handler).WithSubsystem(name string) *Handler`.
  - `Options` gains `Subsystems map[string]slog.Level`, `ConfigPath string`, `Service Service`; `New(opts Options) *slog.Logger` keeps its signature.
  - `func Subsystem(ctx context.Context, name string) context.Context`
  - `func SubsystemLogger(l *slog.Logger, name string) *slog.Logger`
  - `func LevelsOf(l *slog.Logger) *Levels`: nil when `l` is not this package's.
  - `func Watch(ctx context.Context, subsystem string, at slog.Level, fn func(enabled bool)) (cancel func())`: calls `fn` once now and again on every change of whether `subsystem` logs at `at`.

- [ ] **Step 1: Write the failing tests**

`TestRecordMatchesTheGolden`: build a `Handler` writing to a buffer with `Service{"clustarr","manager","1.2.3","manager-0"}`, levels floor `LevelTrace`; for each of the six levels, `h.WithSubsystem("livetv.tuner").WithAttrs([trace_id, span_id, namespace, name, kind])` handles a `slog.NewRecord(time.Date(2026,11,10,18,20,17,104000000,time.FixedZone("CST",-6*3600)), lvl, "tuner started", 0)` with attrs `err`=`errors.New("boom")` and `ratingKey`=`42`; compare to the golden, six lines, each:

```json
{"time":"2026-11-11T00:20:17.104Z","level":"INFO","msg":"tuner started","service":{"namespace":"clustarr","name":"manager","version":"1.2.3","instance.id":"manager-0"},"subsystem":"livetv.tuner","trace_id":"…","span_id":"…","namespace":"clustarr-system","name":"argon-tv","kind":"IPTVProvider","err":"boom","ratingKey":42}
```

`TestLevelNames`: `-9 TRACE`, `-8 TRACE`, `-5 TRACE`, `-4 DEBUG`, `-1 DEBUG`, `0 INFO`, `4 WARN`, `8 ERROR`, `11 ERROR`, `12 FATAL`, `16 FATAL`.

`TestParseLevel`: `trace`→-8, `TRACE`→-8, `debug`→-4, `Info`→0, `WARN`→4, `warning` is an error, `error`→8, `fatal`→12, `INFO+2`→2, `""` is an error.

`TestOverridesMatchByDottedSegment`: floor INFO, overrides `{"livetv": WARN, "livetv.tuner": DEBUG}`: `For("livetv.tuner")`=DEBUG, `For("livetv.tuner.relay")`=DEBUG, `For("livetv.relay")`=WARN, `For("livetv")`=WARN, `For("livetvx")`=INFO, `For("")`=INFO.

`TestSubsystemIsOneKeyAndTheInnerWins`: `Subsystem(Subsystem(ctx,"remediation"),"remediation.transcode")` logs one line with exactly one `subsystem` key, `remediation.transcode`, gated by that name's level.

`TestWithKeepsTheSubsystem`: `logging.With(Subsystem(ctx,"livetv.tuner"), "trace_id","x")` logs `subsystem` before `trace_id`, and a DEBUG line is dropped under `livetv: warn`.

`TestGroupsReplay`: `l.WithGroup("g").With("a",1)` under a subsystem renders `"subsystem":"x","g":{"a":1,…}`.

`TestTextFormatCarriesTheSameAttributes`: `format: "text"` renders `level=INFO`, `service.name=manager`, `subsystem=…`.

`TestWatchSeesAnOverride`: `Watch(ctx,"ffmpeg",slog.LevelDebug,fn)` calls `fn(false)` at once; `levels.Set(INFO, {"ffmpeg": DEBUG})` calls `fn(true)`; setting back calls `fn(false)`; `cancel()` stops further calls.

`TestLevelsOfAForeignLoggerIsNil`: `LevelsOf(slog.New(slog.DiscardHandler))` is nil, and `Subsystem` on a context carrying a foreign logger adds `subsystem` with `With` instead of failing.

- [ ] **Step 2: Run them to watch them fail**

Run: `go test ./pkg/obs/logging/ -run 'TestRecordMatchesTheGolden|TestLevelNames|TestParseLevel|TestOverridesMatchByDottedSegment|TestSubsystem|TestWith|TestGroups|TestTextFormat|TestWatch|TestLevelsOf' -count=1`
Expected: FAIL, undefined `NewHandler`, `LevelTrace`, `Subsystem`.

- [ ] **Step 3: Implement**

`level.go`: the constants, `ParseLevel`, `LevelName`, `Levels` (an `atomic.Pointer` to an immutable snapshot; `For` walks the name's dotted prefixes from longest to shortest). `handler.go`: `Handler` keeps the base JSON or text handler, the `Levels`, its subsystem and the replayable list of `WithAttrs`/`WithGroup` operations; its inner handler is rebuilt as base, then the `service` group, then `subsystem`, then the operations, whenever the subsystem changes, so the key order above holds and a subsystem is never written twice. `Enabled` compares with `levels.For(subsystem)`. A `ReplaceAttr` names the level with `LevelName` and turns `time` to UTC. `subsystem.go`: `Subsystem`, `SubsystemLogger`, `LevelsOf`, `Watch` (over `OnChange`). `logging.go`: `New` builds `NewHandler` from `Options`, `Levels` from `Level` and `Subsystems`.

- [ ] **Step 4: Run them to watch them pass, and the package's existing tests**

Run: `go test ./pkg/obs/logging/ -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/obs/logging/level.go pkg/obs/logging/handler.go pkg/obs/logging/subsystem.go pkg/obs/logging/*_test.go pkg/obs/logging/testdata/record.golden.jsonl
git commit -m "logging: the contract's record, levels by subsystem and Subsystem on the context (2026-11-10 structured-logging design §3, §4, §5)" -- pkg/obs/logging
```

---

### Task 2: Where the levels come from, and the live file

**Files:**
- Create: `pkg/obs/logging/config.go`, `pkg/obs/logging/watch.go`
- Modify: `pkg/obs/logging/logging.go` (`BindFlags`)
- Test: `pkg/obs/logging/config_test.go`, `pkg/obs/logging/watch_test.go`

**Interfaces:**
- Consumes: Task 1's `Levels`, `ParseLevel`.
- Produces:
  - `type FileConfig struct{ Level, Format string; AddSource *bool; Subsystems map[string]string }` with JSON tags `level`, `format`, `addSource`, `subsystems`.
  - `func ReadConfig(path string) (FileConfig, error)`: reads the file's `logging:` key with `sigs.k8s.io/yaml`; a missing file wraps `fs.ErrNotExist`.
  - `func Resolve(file FileConfig, haveFile bool, flags Options, changed func(flag string) bool, getenv func(string) string) (Options, error)`: the precedence of the Global Constraints.
  - `BindFlags(fs, opts)` adds `--log-subsystem-level` (repeatable `name=level`) and `--log-config` (default `/etc/clustarr/logging.yaml`) beside the existing three, and records the flag set so `Root` can ask what was changed.
  - `const DefaultPollInterval = 10 * time.Second`
  - `func WatchFile(ctx context.Context, path string, levels *Levels, flags Options, changed func(string) bool, getenv func(string) string, every time.Duration)`: blocks until `ctx` ends.

- [ ] **Step 1: Write the failing tests**

`TestResolvePrecedence`, a table: file `level: warn` beats flag `--log-level=debug` beats `LOG_LEVEL=error`; an unchanged flag loses to `LOG_LEVEL`; no file, no flag, no env gives INFO and json; file `subsystems` replaces the flags' `--log-subsystem-level` map whole; `LOG_FORMAT=text` with no file and no flag gives text; a file `level: loud` is an error naming the field.

`TestReadConfigReadsTheLoggingKey`: a file with `logging: {level: debug, subsystems: {pms: warn}}` and other top-level keys reads `Level "debug"`, `Subsystems {"pms":"warn"}`.

`TestReadConfigOfAMissingFile`: `errors.Is(err, fs.ErrNotExist)`.

`TestWatchFileAppliesAChange`: write `level: info`, start `WatchFile` at 10 ms, rewrite `level: debug`; within 1 s `levels.For("x")` is DEBUG.

`TestWatchFileFollowsAConfigMapSymlinkSwap`: lay the directory out as the kubelet does (`..2026_11_10_1/logging.yaml`, `..data -> ..2026_11_10_1`, `logging.yaml -> ..data/logging.yaml`); swap `..data` to a second timestamped directory holding `level: warn` with `os.Rename` of a temporary symlink; within 1 s the floor is WARN.

`TestWatchFileKeepsTheLastGoodSettingsOnABadFile`: from `level: debug`, write `level: [` and let ten polls pass: the floor stays DEBUG and exactly one WARN record with `subsystem: logging` and `err` was written; writing a second, different bad content writes a second WARN; writing a good file applies it.

`TestWatchFileKeepsTheLastGoodSettingsWhenTheFileGoes`: remove the file: the floor stays, one WARN.

- [ ] **Step 2: Run them to watch them fail**

Run: `go test ./pkg/obs/logging/ -run 'TestResolve|TestReadConfig|TestWatchFile' -count=1`
Expected: FAIL, undefined `Resolve`, `ReadConfig`, `WatchFile`.

- [ ] **Step 3: Implement**

`WatchFile` stats the path (following symlinks) each tick and re-reads only when size, mtime or the resolved target changed, applies only when the content's hash differs from the last applied, and remembers the hash of the last bad content so it warns once per content. It applies `Resolve`'s level and subsystems through `levels.Set`; format and addSource are never changed by it.

- [ ] **Step 4: Run them to watch them pass**

Run: `go test ./pkg/obs/logging/ -count=1 -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/obs/logging/config.go pkg/obs/logging/watch.go pkg/obs/logging/config_test.go pkg/obs/logging/watch_test.go
git commit -m "logging: levels from the file, flags and environment, the file re-read every 10 s (design §4)" -- pkg/obs/logging
```

---

### Task 3: One root per process, its identity, and the controller-runtime, klog and Go log drains

**Files:**
- Create: `pkg/obs/logging/root.go`
- Modify: `pkg/obs/bootstrap.go`, `pkg/obs/logging/bridge.go`, `pkg/obs/tracing/tracing.go` (resource attributes), `internal/cli/agent/run.go:59`, `internal/cli/manager/run.go:81`, `internal/cli/ui/command.go:136`, `cmd/markers/main.go:156-158`, `cmd/transcode/main.go:118,130-132`
- Test: `pkg/obs/logging/root_test.go`, `pkg/obs/bootstrap_test.go`, `pkg/obs/tracing/tracing_test.go`, `internal/cli/agent/run_test.go`

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces:
  - `func Root(ctx context.Context, opts Options, svc Service) (context.Context, *slog.Logger, error)`: resolves options (Task 2), builds the root, installs it on `ctx`, starts `WatchFile` under `ctx` when `opts.ConfigPath` is set, and calls `slog.SetDefault` with the root's `stdlog` subsystem logger. The only `slog.SetDefault` in the module.
  - `func StdLogger(ctx context.Context, subsystem string, level slog.Level) *log.Logger`: for `http.Server.ErrorLog` and its kind.
  - `func ServiceFor(name string) Service`: `Namespace "clustarr"`, `Version version.Version`, `InstanceID` from `POD_NAME` else the hostname.
  - `LogrBridge(l *slog.Logger) logr.Logger` keeps its signature; `obs.Bootstrap` passes it the `controller-runtime` subsystem logger for `crlog.SetLogger` and the `client-go` one for `klog.SetLogger` (`k8s.io/klog/v2` becomes a direct dependency; add it serially, nobody else running `go get`).
  - `agentServiceName(domain, engine string) string` in `internal/cli/agent`: `engine-<engine>` for `torrent-engine` and `usenet-engine`, else `agent-<domain>`.
  - The tracer's resource adds `service.namespace=clustarr` and `service.version`.

- [ ] **Step 1: Write the failing tests**

`TestRootStampsTheService`: `Root` with `Output` a buffer and `ServiceFor("manager")` under `POD_NAME=manager-7`; one `Info` line carries `service.namespace=clustarr`, `service.name=manager`, `service.instance.id=manager-7`, `service.version` = `version.Version`.

`TestRootDrainsGoLogAndSlogDefault`: after `Root`, `log.Print("x")` and `slog.Default().Info("y")` each write one record with `subsystem: stdlog`, level INFO.

`TestStdLoggerWritesAtItsLevel`: `StdLogger(ctx,"ui",slog.LevelWarn).Print("tls: bad")` is one WARN record, `subsystem: ui`.

`TestBootstrapBridgesControllerRuntimeAndKlog`: after `obs.Bootstrap` (under the existing `setLoggerOnce` test pattern), `ctrl.Log.Info("a")` is INFO `subsystem: controller-runtime`, `ctrl.Log.V(1).Info("b")` is DEBUG and dropped at the INFO floor, `ctrl.Log.Error(err,"c")` is ERROR with `err`; `klog.InfoS("d")` is INFO `subsystem: client-go`, `klog.ErrorS(err,"e")` is ERROR.

`TestTheTracerResourceNamesTheNamespaceAndVersion`: `tracing.Setup` with `ServiceName "manager"` yields a resource carrying `service.namespace=clustarr` and `service.version`.

`TestAgentServiceNames`: `("catalog","")`→`agent-catalog`, `("torrent-engine","qbt")`→`engine-qbt`, `("usenet-engine","nzb-0")`→`engine-nzb-0`.

- [ ] **Step 2: Run them to watch them fail**

Run: `go test ./pkg/obs/... ./internal/cli/agent/ -run 'TestRoot|TestStdLogger|TestBootstrapBridges|TestTheTracerResource|TestAgentServiceNames' -count=1`
Expected: FAIL, undefined `Root`, `StdLogger`, `agentServiceName`.

- [ ] **Step 3: Implement**

`obs.Bootstrap` calls `logging.Root` with `logging.ServiceFor(to.ServiceName)` before `tracing.Setup`. `cmd/markers` and `cmd/transcode` replace `logging.NewContext(ctx, logging.New(*lo))` with `logging.Root`; the graft path (`runGraft`) takes the root from the same call. `agentServiceName` feeds `to.ServiceName` in `internal/cli/agent/run.go`.

- [ ] **Step 4: Run them to watch them pass, and the binaries' own suites**

Run: `go test ./pkg/obs/... ./internal/cli/... ./cmd/... -count=1` and `go build ./...`
Expected: PASS; build clean. `go test ./test/guards/ -run 'Dynamic|Deps' -count=1` stays green (no new linkage in manager or ui).

- [ ] **Step 5: Commit**

```bash
git add pkg/obs/logging/root.go pkg/obs/logging/root_test.go
git commit -m "obs, cli, markers, transcode: one logging.Root per process stamps the service and drains controller-runtime, klog, log and slog.Default (design §3, §4)" -- pkg/obs internal/cli cmd/markers cmd/transcode go.mod go.sum
```

---

### Task 4: NATS, anacrolix, http.Server and FFmpeg

**Files:**
- Modify: `pkg/events/natsbus/natsbus.go:106,168-176,195`, `pkg/events/natsbus/asyncerr.go`, `pkg/download/torrent/client.go:256-258,900`, `app/autoscale/extmetrics/server.go:131` and the other three `http.Server{` sites, `pkg/ffruntime/log.go`, `pkg/ffruntime/load.go:109`
- Test: `pkg/events/natsbus/asyncerr_test.go`, `pkg/download/torrent/client_test.go`, `pkg/ffruntime/log_test.go`

**Interfaces:**
- Consumes: `Subsystem`, `SubsystemLogger`, `StdLogger`, `Watch` (Tasks 1, 3).
- Produces: `ffruntime.RouteLog(ctx context.Context)` replaces `RouteLog(l *slog.Logger)`: it takes the `ffmpeg` subsystem logger from `ctx` and calls `logging.Watch(ctx, "ffmpeg", slog.LevelDebug, setVerbose)`. Callers: `cmd/markers/main.go:186`, `cmd/transcode/main.go:154,237`, and wherever the agent's import and caption domains route today.

- [ ] **Step 1: Write the failing tests**

`TestNATSHandlersLogUnderEventsNats`: the connection's error handler writes ERROR, its disconnect handler WARN, its reconnect handler INFO, each with `subsystem: events.nats`; a natsbus built with no logger in its options takes the context's, never `slog.Default()`.

`TestAnacrolixLogsUnderItsSubsystem`: a torrent `Client` built from a context logger writes anacrolix's records with `subsystem: grab.torrent.anacrolix`, and a `grab.torrent.anacrolix: error` override drops a WARN from it.

`TestFFmpegRecordsCarryTheSubsystemAndNames`: a LogError line is WARN (the router's collapse, as today), a decode-noise line is DEBUG, LogFatal and LogPanic are FATAL, each `subsystem: ffmpeg` with `ffmpegLevel` and `message` as today; the repeat window and the 10/s bucket still hold (the existing tests).

`TestTheFFmpegOverrideRaisesAndRestoresTheCSideLevel`: with a fake for `ffgo.SetLogLevel` injected, `levels.Set(INFO, {"ffmpeg": DEBUG})` sets the C side to `LogVerbose` and routes LogWarning as WARN, LogInfo as INFO, LogVerbose as DEBUG, LogDebug and LogTrace as TRACE, still through the repeat window and the bucket; setting the override back sets `LogWarning` and stops routing anything above LogError.

- [ ] **Step 2: Run them to watch them fail**

Run: `go test ./pkg/events/natsbus/ ./pkg/download/torrent/ ./pkg/ffruntime/ -run 'TestNATSHandlers|TestAnacrolixLogs|TestFFmpegRecords|TestTheFFmpegOverride' -count=1`
Expected: FAIL. The ffruntime tests need the native environment: run them as `make test` does (`LD_LIBRARY_PATH`, `FFGO_SHIM_DIR`); a skip under those variables is a failure.

- [ ] **Step 3: Implement**

The router's verbose state is one bool under its lock; `setVerbose` changes it and the C-side level together. Each `http.Server` gets `ErrorLog: logging.StdLogger(ctx, <its subsystem>, slog.LevelWarn)`.

- [ ] **Step 4: Run them to watch them pass**

Run: the same command, then `go test ./pkg/events/... ./pkg/download/... ./pkg/ffruntime/... ./app/autoscale/... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "natsbus, torrent, ffruntime, http servers: third-party writers bridged under their subsystems; an ffmpeg debug override raises FFmpeg's own level and lowers it again (design §4)" -- pkg/events pkg/download/torrent pkg/ffruntime app/autoscale cmd internal ui
```

---

### Task 5: ONNX Runtime's logger, through a fork of its binding

**Files:**
- Fork: `github.com/mediactl/onnxruntime-purego`, branch `clustarr`, from upstream at `v0.0.0-20260315223538-8db8bd7424b2` (commit `8db8bd7424b2`); change `onnxruntime/env.go`, `onnxruntime/internal/api/api.go`, `onnxruntime/internal/api/v23/*.go`; test `onnxruntime/env_test.go`
- Modify: `go.mod` (replace), `pkg/segments/textdet/onnx.go:56-60`, `pkg/subtitles/ocr/ocr.go:95-99`
- Test: `pkg/subtitles/ocr/ocr_log_test.go`, `pkg/segments/textdet/onnx_log_test.go`

**Interfaces:**
- Produces, in the fork's `onnxruntime` package:
  - `type LogFunc func(severity LoggingLevel, category, logID, codeLocation, message string)`
  - `func (r *Runtime) NewEnvWithLogger(logID string, level LoggingLevel, fn LogFunc) (*Env, error)`: through `CreateEnvWithCustomLogger` (API slot 4), the C callback made once per process with `purego.NewCallback`, dispatching to `fn` by the `logger_param` it was given.
  - `func (e *Env) SetLogLevel(level LoggingLevel) error`: through `UpdateEnvWithCustomLogLevel` (slot 226).
- Produces, in clustarr: `ocr` and `textdet` create their env with `NewEnvWithLogger(…, LoggingLevelWarning, fn)` where `fn` writes to the `onnx` subsystem logger (verbose TRACE, info INFO, warning WARN, error ERROR, fatal FATAL; `category`, `logID`, `codeLocation` as attributes), and `logging.Watch(ctx, "onnx", slog.LevelDebug, …)` switches the env between `LoggingLevelVerbose` and `LoggingLevelWarning`.

- [ ] **Step 1: Fork and branch**

```bash
gh repo fork shota3506/onnxruntime-purego --org mediactl --fork-name onnxruntime-purego --clone=false
git clone https://github.com/mediactl/onnxruntime-purego.git <scratchpad>/ort-fork
cd <scratchpad>/ort-fork && git checkout -b clustarr 8db8bd7424b2
```

Expected: the fork exists; `clustarr` sits at upstream's commit.

- [ ] **Step 2: Write the fork's failing test**

`TestNewEnvWithLoggerReceivesORTsLines` (skips without the library path the package's other tests use): an env at `LoggingLevelVerbose` whose `fn` appends; creating a session from the repository's test model yields at least one call with a non-empty `message` and severity `LoggingLevelVerbose` or `LoggingLevelInfo`; `SetLogLevel(LoggingLevelWarning)` then a second session yields no verbose call.

- [ ] **Step 3: Implement, run, commit, tag, push, open the PR**

Run: `go test ./onnxruntime/ -run TestNewEnvWithLogger -count=1` with `make native-assets`' ONNX Runtime on the library path; Expected: FAIL first, PASS after. Commit as one commit, tag `v0.0.0-clustarr.1`, push the branch and the tag, then `gh pr create -R shota3506/onnxruntime-purego --head mediactl:clustarr` with the commit's message as the body. The owner chose this PR (2026-11-10, "1+2").

- [ ] **Step 4: Write clustarr's failing tests**

`TestORTSeverityNames` (no library): the adapter maps the five ORT severities to TRACE, INFO, WARN, ERROR, FATAL under `subsystem: onnx`, with `category`, `logID`, `codeLocation`.

`TestTheOnnxOverrideReachesORT` (native): with `onnx: trace` set, building the OCR reader writes at least one TRACE record under `subsystem: onnx`; with it removed, a second build writes none.

- [ ] **Step 5: Replace the module and implement**

```bash
go mod edit -replace github.com/shota3506/onnxruntime-purego=github.com/mediactl/onnxruntime-purego@v0.0.0-clustarr.1
go mod tidy
```

A comment above the replace names the fork's one commit, the upstream PR, and says to drop the replace at the first upstream release carrying it, as the torrent and controller-runtime replaces do.

- [ ] **Step 6: Run and commit**

Run: `go test ./pkg/subtitles/ocr/ ./pkg/segments/textdet/ -count=1` in the native environment; `go test ./test/guards/ -run 'Dynamic' -count=1`.
Expected: PASS; manager and ui still link no purego.

```bash
git commit -m "ocr, textdet: ONNX Runtime's own lines reach the onnx subsystem through the mediactl/onnxruntime-purego fork's custom logger (design §4; owner 2026-11-10, the fork and the upstream PR)" -- go.mod go.sum pkg/subtitles/ocr pkg/segments/textdet
```

Add the fork's line to CLAUDE.md's Commands paragraph beside the torrent and controller-runtime forks, in the same commit.

---

### Task 6: `err`, and no global logger

**Files:**
- Create: `test/guards/logging_test.go`
- Modify: every production file the guards name (805 `"error"` keys at the start; `ui` 35, `pkg/events/natsbus` 7, `pkg/download/usenet` 7, and so on)

**Interfaces:**
- Consumes: `productionFiles`, `importAlias`, `isSelector`, `isIdent` in `test/guards/astutil_test.go`.

- [ ] **Step 1: Write the failing guards**

`TestNoErrorAttributeKey`: no string literal `"error"` at a key position in a call of `Debug`, `Info`, `Warn`, `Error`, `Log`, `LogAttrs`, `With`, their `…Context` forms, `slog.String`, `slog.Any` or `logging.With`. The message names file and line.

`TestNoGlobalLogger`: `slog.Default()`, `slog.New(` and `slog.SetDefault` appear only in `pkg/obs/logging`; `test/` and `hack/` are outside production files.

`TestNoStdlibLogOrStderrPrints`: no `log.Print*`, `log.Fatal*`, `log.Panic*`, and no `fmt.Fprint*(os.Stderr, …)` in production code, except `internal/cli`'s `Main` error print, `cmd/*/main.go` before the logger exists, and `pkg/par2child` (a child process whose stderr its parent reads), each named in the test with its reason.

- [ ] **Step 2: Run them to watch them fail**

Run: `go test ./test/guards/ -run 'TestNoErrorAttributeKey|TestNoGlobalLogger|TestNoStdlibLog' -count=1`
Expected: FAIL, listing the sites.

- [ ] **Step 3: Sweep**

Change each listed key to `"err"`, by top-level directory, one commit each, with the guards as the list; a prometheus label value or a JSON field named `error` is not a log key and the guard does not list it. Remove the remaining `slog.Default()` fallbacks (Task 4 took natsbus and torrent).

- [ ] **Step 4: Run them to watch them pass**

Run: `go test ./test/guards/ -count=1` and `go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit** each directory's sweep with a pathspec, the guard file with the first.

---

### Task 7: Subsystems at every entry point

**Files:**
- Create: `pkg/obs/logging/subsystems.go` (the table)
- Modify: the entry points of every production package that logs (149 packages call `logging.FromContext` today)
- Test: `test/guards/logging_test.go` (add)

**Interfaces:**
- Produces: `var Subsystems = []string{…}`, sorted, every name the module passes to `Subsystem` or `SubsystemLogger`.

The naming rule, which the table records: a package's subsystem is its path below `app/` with `/` read as `.` and the segments `controller`, `worker`, `agent` and `manager` dropped (`app/livetv/tuner` → `livetv.tuner`, `app/catalog/worker/search` → `catalog.search`, `app/transcode/controller/pool` → `transcode.pool`, `app/remediation` → `remediation`); `ui` and `ui/plex` are `ui` and `ui.plex`; a `pkg/` package with its own entry point takes its service's name (`pkg/events/natsbus` → `events.nats`, `pkg/download/torrent` → `grab.torrent`, its anacrolix logger `grab.torrent.anacrolix`, `pkg/download/usenet` → `grab.usenet`). Fixed by the spec: `controller-runtime`, `client-go`, `stdlog`, `events.nats`, `grab.torrent.anacrolix`, `ffmpeg`, `onnx`, `logging`.

An entry point is where a unit's work starts: a `Reconcile` method, a bus handler registered in an `app/<svc>/agent` or `app/<svc>/manager` package, a remediation planner's entry, a leader runnable's `Start`, a server's constructor. It calls `ctx = logging.Subsystem(ctx, "<name>")` once; code below inherits it.

- [ ] **Step 1: Write the failing guard**

`TestEverySubsystemIsInTheTable`: every `logging.Subsystem` and `logging.SubsystemLogger` call passes a string literal; each literal is in `Subsystems`; each `Subsystems` entry is used at least once; and every method named `Reconcile` taking `(context.Context, reconcile.Request)` in production code calls `logging.Subsystem` in its body.

- [ ] **Step 2: Run it to watch it fail**

Run: `go test ./test/guards/ -run TestEverySubsystemIsInTheTable -count=1`
Expected: FAIL, the unnamed `Reconcile` methods listed.

- [ ] **Step 3: Sweep by service** (`catalog`, `import`, `index`, `grab`, `transcode`, `caption`, `remediation`, `dispatch`, `intake`, `autoscale`, `maintenance`, `segments`, `livetv`, `ui`, `pkg`), adding each name to the table as it is used, one commit per service.

- [ ] **Step 4: Run it to watch it pass, and the unit suites**

Run: `go test ./test/guards/ -count=1` and `go test ./app/... ./pkg/... ./ui/... -count=1` (no envtest assets set, so the envtest suites skip; they run once at the end)
Expected: PASS.

- [ ] **Step 5: Commit** per service with a pathspec.

---

### Task 8: The ConfigMap in every pod, and the recommended labels on the pods clustarr builds

**Files:**
- Create: `charts/clustarr/templates/logging-configmap.yaml`, `config/manager/logging.yaml`
- Modify: `charts/clustarr/values.yaml` (`logging`, `global.logging`), `charts/clustarr/values.schema.json` if present, `charts/clustarr/templates/_helpers.tpl` (`clustarr.workload` volumes and mounts; `clustarr.labels` version), `charts/clustarr/templates/deployments.yaml`, `charts/clustarr/templates/markers.yaml`, `config/manager/*.yaml`, `config/manager/kustomization.yaml`, `internal/cli/manager/run.go` (`--logging-configmap`), `app/grab/controller/downloadclient/workload.go` (`EngineRuntime.LoggingConfigMap`, labels), `app/transcode/controller/pool/template.go` (`Config.LoggingConfigMap`, version label)
- Test: `test/guards/logging_installers_test.go`, `app/grab/controller/downloadclient/workload_test.go`, `app/transcode/controller/pool/template_test.go`

**Interfaces:**
- Consumes: Task 2's path `/etc/clustarr/logging.yaml`.
- Produces: the ConfigMap `<release>-logging` (kustomize: `clustarr-logging`) with one key `logging.yaml`; the volume `logging` mounted read-only at `/etc/clustarr`; the manager flag `--logging-configmap <name>`, `EngineRuntime.LoggingConfigMap string`, `pool.Config.LoggingConfigMap string` (empty mounts nothing). Runtime pods get the volume with `optional: true`, `app.kubernetes.io/name: clustarr` and `app.kubernetes.io/version: <version.Version>`.

The chart's values: `global.logging` is the knob both charts read (Helm hands a subchart the parent's `global` and nothing else, so the design's "pass `logging` down as `global.logging`" is written by the owner as `global: logging:`); clustarr's own `logging` block overrides it key by key for clustarr's pods, its `subsystems` merged over the global's. Default: both empty, rendering `logging: {level: info, format: json, addSource: false}`.

- [ ] **Step 1: Write the failing tests**

`TestEveryPodMountsTheLoggingConfig`: in the chart's render and in kustomize's (`installtest.Rendered`), every Deployment, StatefulSet and Job template that runs a clustarr binary has a `configMap` volume naming the logging ConfigMap and a mount of it at `/etc/clustarr`.

`TestTheLoggingConfigIsADirectoryMount`: no such mount has a `subPath`.

`TestTheLoggingValuesRender`: `global.logging.level: debug` renders `level: debug`; adding `logging.subsystems: {livetv.tuner: trace}` keeps the global's other subsystems and adds that one; `logging.level: warn` beats `global.logging.level`.

`TestRuntimePodsMountTheLoggingConfigOptionally`: `buildStatefulSet`, `buildDeployment` and `pool.Template` with `LoggingConfigMap: "clustarr-logging"` mount it at `/etc/clustarr`, `optional: true`, no `subPath`; with it empty, no such volume.

`TestRuntimePodsCarryTheRecommendedLabels`: the engine pod templates and the pool template carry `app.kubernetes.io/name: clustarr`, `app.kubernetes.io/component`, `app.kubernetes.io/version` equal to `version.Version`; the StatefulSet's and Deployment's selectors are unchanged.

`TestTheChartVersionLabelIsTheImage`: `app.kubernetes.io/version` renders `image.tag` when set, else `appVersion`.

- [ ] **Step 2: Run them to watch them fail**

Run: `make chart-deps && go test ./test/guards/ ./app/grab/controller/downloadclient/ ./app/transcode/controller/pool/ -run 'Logging|RecommendedLabels|ChartVersionLabel' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

The manager passes `--logging-configmap` into both runtime configs. The pool's applied-template annotation changes, so every pool Job is replaced once at the rollout, as on any image change.

- [ ] **Step 4: Run them to watch them pass, and the installer guards**

Run: `go test ./test/guards/ -count=1`, `helm lint --strict charts/clustarr`
Expected: PASS, `TestChartAndKustomizeAgreePerComponent` included.

- [ ] **Step 5: Commit**

```bash
git add charts/clustarr/templates/logging-configmap.yaml config/manager/logging.yaml test/guards/logging_installers_test.go
git commit -m "chart, config, manager: one logging ConfigMap mounted in every pod, the runtime pods' optionally, and the recommended labels on the pods clustarr builds (design §2, §6)" -- charts config internal/cli/manager app/grab/controller/downloadclient app/transcode/controller/pool test/guards
```

---

### Task 9: Documentation and the final verification

**Files:**
- Modify: `docs/observability.md` (a Logging section: the record's keys, the levels, the knob, three LogsQL examples: `subsystem:livetv.tuner level:ERROR`, `service.version:"<v>"`, `trace_id:<id>`), `CLAUDE.md` (Code conventions' logging bullet: Subsystem at entry points, `err`, no `slog.Default`; the ONNX fork line from Task 5), `charts/clustarr/README.md` (the `global.logging` and `logging` values)

- [ ] **Step 1: Write the docs.**
- [ ] **Step 2: Run the whole verification once**

Run: `make generate manifests` (clean), `make build`, `make lint`, `make brand-gate`, `helm lint --strict charts/clustarr`, then `make test`.
Expected: all green; any red test named in the report with its output, pre-existing ones marked as such.

- [ ] **Step 3: Commit** the docs with a pathspec.

---

## Notes for the executor

- **Rulings already made in this plan:** the knob is `global.logging` (Helm cannot pass a parent block into a subchart's values); FFmpeg's error-level lines stay WARN as the router writes them today, FATAL is new, and the rest is shape; the engines' tracing name becomes `engine-<client>` with their log name, since the spec's table lists that name and says the two are one string.
- **Deploying it** is not part of this plan: the clustarr chart version bump restarts every clustarr pod, the pools are replaced once, and talos's `talos-values.yaml` needs no `logging:` for the defaults.
