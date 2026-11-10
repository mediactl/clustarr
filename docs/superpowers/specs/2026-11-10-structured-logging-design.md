# Structured logging: one contract for clustarr and cluster-plex

Date: 2026-11-10. Owner's brief: "slog style, production grade log handling via
dependency injection, subsystems, json compatible (loki labels), but
configurable formatting, global log level selection, proper context handling,
etc. This should be applied to both clustarr and cluster-plex. In cluster-plex,
manage Plex's log files via trimming, redirection to our logs (in the correct
format with labelling), and automated deletion of the log files." Then:
"Investigate best practices, labels within our capi resources, etc. They should
be relatively standard."

This document is the contract both repositories implement and clustarr's own
changes. cluster-plex's adoption and the Plex log files are
`cluster-plex/docs/superpowers/specs/2026-11-10-structured-logging-design.md`,
which refers back here for the contract.

## 1. Intent

Every line either product writes is one JSON record of one shape, carrying the
same few fields with the same names, so a person in Grafana (VictoriaLogs,
LogsQL) can ask "every ERROR from the Live TV tuner across all clustarr pods
in the last hour" without knowing which binary, package or library wrote it,
and can read Plex's own log beside cluster-plex's in the same view. The level
is one setting per deployment, changeable without a restart, with per-subsystem
overrides. No logger is global; every writer, third-party libraries included,
is behind the one handler.

### Why now

- clustarr is already slog-through-context (1,160 call sites) but has no
  subsystem convention, no live level control, an unbridged klog, and
  third-party noise setting its level: on 2026-11-10 the torrent engine wrote
  125k WARN lines in a day (anacrolix's hasher) and NATS 80k with no level.
- cluster-plex is a mix: `slog.SetDefault` plus 17 `slog.Default()`
  fallbacks, logger struct fields in 14 packages, four binaries each with a
  bare `slog.NewJSONHandler(os.Stdout, nil)` (nothing below Info ever leaves
  a pod), client-go's reflector errors as klog text, and the shim's stderr
  relayed at INFO whatever its own `[ERROR]` tag says.
- Plex's `Logs/` (a per-pod emptyDir) is read by nobody and vanishes with the
  pod; its content never reaches the store.

## 2. What the standards say, and what we take from them

Investigated 2026-11-10: the OpenTelemetry log data model and service
resource attributes, Loki's label best practices, VictoriaLogs' key concepts
and vlagent, Kubernetes' recommended labels, and our own charts and CAPI
resources.

- **Labels (streams) identify the source instance and are bounded.** Loki:
  "don't add a `level` dynamic label", never trace or request ids.
  VictoriaLogs: stream fields are `container`, `pod`, `namespace`; "never
  associate high-cardinality fields with log streams"; `level` and `trace_id`
  are ordinary fields, and a dedicated field is faster to filter than `_msg`.
  **Taken:** the stream labels stay what vlagent sets in cluster-bootstrap
  (`kubernetes.container_name`, `kubernetes.pod_name`,
  `kubernetes.pod_namespace`). Nothing in this design changes vlagent.
- **Our pods already carry the Kubernetes recommended labels**, and vlagent
  copies every pod label onto every entry: `app.kubernetes.io/name`,
  `instance`, `component` (both charts), `part-of` and `version` (clustarr).
  The CAPI side adds `cluster.x-k8s.io/cluster-name` and the HelmChartProxy
  name on the HelmReleaseProxy, not on pods. **Taken:** the log line does not
  repeat what the pod labels already say (namespace, pod, component); it adds
  what only the process knows. Two gaps are fixed: the pods clustarr builds at
  runtime (download engines) carry `component` but no `name`, and `version`
  is the chart's `0.1.0`, not the build.
- **OpenTelemetry names the process's identity** `service.name` (required,
  the same across replicas), `service.version` (the build), and
  `service.instance.id` (unique per instance). Its log record is a
  timestamp, a severity, a body, attributes and the trace context. **Taken:**
  those three service fields as a `service` group on every line, severities
  as slog's names, `trace_id`/`span_id` as fields.
- **The store already parses our JSON.** vlagent reads `msg` as `_msg` and
  `time` as `_time`, flattens nested objects to dotted keys
  (`service.name`), and marks `output_stream`. **Taken:** slog's own keys
  stay; a nested `service` group arrives as `service.name` and so on.

## 3. The record

Every line is one JSON object. Keys, in the order slog writes them:

| Key | Value | Who sets it |
| --- | --- | --- |
| `time` | RFC 3339 with nanoseconds, UTC | the handler |
| `level` | `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`, `FATAL` | the call site; the handler names slog's numeric levels (below DEBUG is TRACE, above ERROR is FATAL), never `DEBUG-4` |
| `msg` | the message, a short sentence without values in it | the call site |
| `service.namespace` | the product: `clustarr` or `cluster-plex` (OpenTelemetry's grouping of related services; it disambiguates the two managers without inventing prefixed names) | the root logger, once; the tracer's resource gets the same attribute |
| `service.name` | the process within it: clustarr's `manager`, `agent-<domain>`, `ui`, `markers`, `transcode`, `engine-<client>`; cluster-plex's `manager`, `proxy`, `video-worker`, `remux-worker`, `maintenance`, `guide-build`. The same string the process gives OpenTelemetry as its tracing service name, so logs and traces share it; clustarr's tracing names are these already, and cluster-plex's `clusterplex-manager` becomes `manager` under its namespace (nothing in cluster-bootstrap, the charts or the dashboards keys on the old name, checked 2026-11-10) | the root logger, once |
| `service.version` | the build: `pkg/version.Version` in clustarr (git describe, ldflags); cluster-plex gains the same | the root logger, once |
| `service.instance.id` | the pod name, else the hostname | the root logger, once |
| `subsystem` | the unit that wrote the line, dotted, lowercase: `remediation.transcode`, `livetv.tuner`, `plex.supervisor`, `pms`, `pms.plugin`, `shim`, `client-go`, `ffmpeg` (the full list is each repo's `logging.Subsystems` table, which a test holds every call site to) | `logging.Subsystem` |
| `err` | an error's text; the one error key in both repos (clustarr splits 1,287 `err` to 1,091 `error` today, cluster-plex uses `error`) | the call site |
| `trace_id`, `span_id` | the active span's, hex | the tracing enrichment, as clustarr does today |
| `namespace`, `name`, `kind` | the Kubernetes object the line concerns; controller-runtime's bridge already emits `namespace`, `name`, `controller`, `reconcileID` and they stay as they are | the reconciler's entry enrichment |
| `source` | `file:line` of the call, only when `addSource` is on | the handler |

Anything else is the call site's own attribute, named in lowerCamel as today
(`ratingKey`, `sectionKey`, `pid`). Never a title, path or release name as a
*key*; as a value is fine. A message is a sentence, not a format string:
`"put back what Plex held differently"` with `"fields", out.PutBack`, never
`fmt.Sprintf` into `msg`.

The text format (`format: text`) is slog's `TextHandler` with the same
attributes, for a terminal, and is never what a pod runs.

## 4. Levels, and where they come from

**Global floor plus per-subsystem overrides.** A record is written when its
level is at or above the most specific override for its subsystem, else the
floor. Overrides match by dotted prefix: `livetv` covers `livetv.tuner`;
`livetv.tuner: debug` beside `livetv: warn` gives the tuner debug and the rest
of Live TV warn.

**Sources, in precedence:**

1. The logging config file, when present and readable, for `level` and
   `subsystems`. It is re-read on change (its mtime polled every 10 s; a
   mounted ConfigMap's symlink swap lands within the kubelet's sync period,
   about a minute), and a bad file keeps the last good settings and logs one
   WARN. **This is the live knob:** a values change becomes a ConfigMap
   change and reaches every pod with no restart, and `kubectl edit configmap`
   is the emergency flip (Helm does not correct that drift until the next
   upgrade).
2. Flags: `--log-level`, `--log-format`, `--log-add-source`,
   `--log-subsystem-level name=level` (repeatable), `--log-config <path>`
   (default `/etc/clustarr/logging.yaml` in clustarr, the `logging:` section of
   `/etc/clusterplex/config.yaml` in cluster-plex).
3. Environment: `LOG_LEVEL`, `LOG_FORMAT`, same spellings, for `make run-dev`
   and the test rigs.

`format` and `addSource` are read at start only; changing them is a rollout.
The file:

```yaml
logging:
  level: info
  format: json
  addSource: false
  subsystems:
    livetv.tuner: debug
    pms: warn
    grab.torrent.anacrolix: error
```

Level names accept slog's spellings case-insensitively plus `trace` and
`fatal`.

**Third-party writers are bridged, each with its level mapped and a subsystem
named**, never left to their own output:

| Writer | Bridge | Subsystem | Mapping |
| --- | --- | --- | --- |
| controller-runtime (logr) | `logr.FromSlogHandler` as today | `controller-runtime`, its own `logger` attr kept | V(0) INFO, V(1+) DEBUG, Error ERROR |
| client-go (klog) | `klog.SetLogger` on the same logr | `client-go` | as logr; `UnhandledError` ERROR |
| anacrolix/torrent | `ClientConfig.Slogger` as today | `grab.torrent.anacrolix` | anacrolix's own levels; the owner's override can quiet it |
| nats.go handlers (error, disconnect, reconnect) | the handlers log | `events.nats` | error ERROR, disconnect WARN, reconnect INFO |
| Go `log` (http.Server.ErrorLog and any library using it) | `slog.SetDefault(root)` in Bootstrap, and `ErrorLog: slog.NewLogLogger(h, LevelWarn)` where a struct takes one | `stdlog`, or the struct's own | INFO by default, WARN for ErrorLog |
| FFmpeg through ffgo (`pkg/ffruntime/log.go`) | the one trampoline and its router as today: the C-side level stays warning, the eight known decode-noise messages stay demoted to DEBUG, the once-a-minute repeat count and the 10/s bucket stay (built after the markers pod drowned in "Duplicate POC" lines on 2026-10-15). What changes is the record's shape: subsystem `ffmpeg`, the level names. An `ffmpeg: debug` override raises the C-side level to verbose for as long as it is set, through the same router, never around it | `ffmpeg` | panic/fatal FATAL, error ERROR (the router's collapse to WARN kept where it applies), warning WARN, info INFO, verbose DEBUG, debug/trace TRACE |
| ONNX Runtime | not bridged in this work: the purego binding (`shota3506/onnxruntime-purego`) binds `CreateEnv` only, not `CreateEnvWithCustomLogger`, and both envs are created at ORT's error level, so what reaches stderr is rare and is errors. Its stderr is exempt from the guards, named as such; binding the custom logger (a `mediactl/` fork, as ffgo's) is a follow-up, not a block on the sweep | `onnx`, when bridged | verbose TRACE, info INFO, warning WARN, error ERROR, fatal FATAL |
| Plex's files, stdout, stderr and the shim | the cluster-plex design | `pms…`, `shim` | that design's table |

`slog.SetDefault(root)` is the drain for libraries that log through
`log` or `slog.Default()`; it is never the way our own code reaches the
logger, and a guard test bans `slog.Default()` and `slog.New(` outside the
logging package in both repos.

## 5. Dependency injection and context

The logger is handed to code; code never reaches for a global.

- **clustarr keeps what it has:** the root logger built once in
  `obs.Bootstrap` from `logging.Options`, installed on the base context,
  read with `logging.FromContext(ctx)`, enriched with `logging.With` and now
  `logging.Subsystem(ctx, name)`, which returns a context whose logger
  carries `subsystem=name` and whose handler enforces that subsystem's
  level. A package's entry point (a reconciler's `Reconcile`, a worker's
  handler, a server's constructor) names its subsystem once; everything
  below inherits it through the context. No logger fields outside the
  engine clients and test fixtures that have them today, and those take a
  `*slog.Logger` the caller built from the context.
- **cluster-plex moves to the same package shape** (`pkg/logging`: `New`,
  `Options`, `FromContext`, `NewContext`, `With`, `Subsystem`, `Config`,
  the file watcher) and the same rule: a component that today holds a
  `Logger *slog.Logger` field keeps constructor injection, but the field is
  required (the constructor refuses nil; no `slog.Default()` fallback,
  no `log()` helper that invents one) and is built by the caller with
  `logging.Subsystem`; code that has a context carrying a logger reads it
  from there. The two repos cannot share the package (cluster-plex does not
  depend on the clustarr module, and should not for a logger), so the
  cluster-plex copy is held to the same API by name in its design; drift is
  acceptable, divergence of the record is not.
- **Context carries the span,** and the handler reads it: the
  `*Context` call forms (`log.InfoContext(ctx, …)`) are preferred where a
  context is at hand, and clustarr's tracing enrichment keeps stamping
  `trace_id`/`span_id` onto the context's logger at span start as today, so
  the non-context forms carry them too.

## 6. Configuration and installers

- **clustarr chart:** a `logging` values block (`level`, `format`,
  `addSource`, `subsystems`) rendered into one ConfigMap
  `<release>-logging` holding `logging.yaml`, mounted read-only at
  `/etc/clustarr/logging.yaml` in every pod the chart renders (manager, the
  agents, ui, markers' and the pools' templates) and in every pod the manager
  builds at runtime (download engines, transcode pools, graft Jobs), which
  the manager stamps from its own mount, held by the existing
  "what the installer creates and binds" tests. `config/` (kustomize) renders
  the same ConfigMap. `--log-config` defaults to that path and a missing
  file is not an error (local runs).
- **cluster-plex chart:** `logging` under the manager's values, rendered as
  the `logging:` section of the existing `config.yaml` ConfigMap; the
  workers and proxy mount the same file. **One knob on talos:** cluster-plex
  is a subchart of the clustarr chart, so the clustarr chart passes its
  `logging` block down as `global.logging`, which the cluster-plex chart
  reads whenever its own `manager.logging` is unset; a cluster-plex chart
  installed alone keeps its own block. The owner's one `logging:` in
  `talos-values.yaml` then governs every pod of both products, and a
  cluster-plex-only override is still possible.
- **cluster-bootstrap:** `logging:` in `clustarr/talos-values.yaml` when the
  owner wants a level other than the default; no vlagent change.
- Defaults: `level: info`, `format: json`, `addSource: false`, no overrides.

## 7. What a person can do when this ships

- Filter in Grafana by `level`, `subsystem`, `service.name`,
  `service.version`, `trace_id`, `err`, and by the pod labels vlagent already
  attaches; find the build that wrote a line from the line itself.
- Turn one subsystem up to debug on talos by editing the values (through
  Terraform) or the ConfigMap by hand, and watch it take effect within a
  minute with no restart; turn it back the same way.
- Read Plex's own log lines, Plex's level and request ids intact, beside
  cluster-plex's, and never lose them to a pod restart (the cluster-plex
  design).
- Trust the level: WARN and ERROR counts mean what they say, since every
  third-party writer is mapped.

## 8. Non-goals

- The NATS server's and PostgreSQL's own container logs: not our binaries.
  (CNPG already writes JSON with a lowercase `level`.)
- Changing vlagent's stream fields or adding pod annotations as fields.
- Exporting logs over OTLP. VictoriaLogs collects stdout; the OTel log bridge
  can come later without changing a call site.
- Sampling or rate limiting of log lines.
- An HTTP endpoint or signal for the level: the config file covers it.
- Rewriting message texts. Only keys and levels change in this work.

## 9. Guards

Tests in each repo, named for what they refuse:

- no `slog.Default()`, no `slog.New(`, no `slog.SetDefault` outside the
  logging package and Bootstrap; no `log.Printf` family and no
  `fmt.Fprint*(os.Stderr` in production code except a CLI's usage error
  before the logger exists;
- no attribute key `"error"`; the key is `err`;
- every production subsystem name is in the repo's `Subsystems` table and
  every entry point names one (an AST scan of `logging.Subsystem` calls);
- a golden test of the JSON record for one line at each level, with the
  service group, subsystem, err, trace ids and a Kubernetes identity;
- per-subsystem override by prefix; the file re-read on change and a bad
  file kept out; flags and environment precedence;
- each third-party bridge: one line in at each of its levels, the level out;
- every pod the installers render and every pod the manager builds mounts
  the logging ConfigMap (clustarr) or the config file (cluster-plex).

## 9a. Calls made at the builder check (2026-11-10)

A fresh reviewer read both documents as a builder; its four questions were
settled here, as the author's calls, not the owner's:

1. **The FFmpeg router keeps its gates.** The bridge table's first draft
   read as "pass every FFmpeg level through"; that would undo the
   2026-10-15 fix. The table now says the gates stay and only the record's
   shape changes, with the override reaching the C-side level through the
   router.
2. **ONNX Runtime stays unbridged** until its binding carries the custom
   logger; exempt from the guards by name.
3. **`service.namespace` plus `service.name`** instead of renamed, prefixed
   service names: the tracing names clustarr already uses stay, cluster-plex's
   one tracer name changes, and nothing downstream keys on it.
4. **One `logging` knob on talos** through `global.logging` into the
   subchart; a standalone cluster-plex install keeps its own.

## 10. Left to the builder

File layout under `pkg/obs/logging`; the handler's implementation (a wrapper
around slog's JSON and text handlers that owns the level logic, the level
names and the UTC time, with `WithAttrs`/`WithGroup` carrying the subsystem);
the polling interval's exact value; the order of the sweep through clustarr's
call sites (by package, each a commit); whether the runtime-built pods take
the ConfigMap by a mount or by flags copied from the manager (a mount, unless
a Job's template cannot carry it).

## 11. Findings on the way, not in scope

- The torrent engine's 125k WARN lines a day read `finished hashing piece`
  with `err: short write` and `correct: true`: anacrolix reporting a short
  write while completing a piece, every piece, on the NFS data volume. That
  is a real warning to look at, not noise to hide; the override exists so it
  does not drown the rest meanwhile.
- cluster-plex's one-pod guard means the collections controller's two-pod
  case is unproven while Live TV is on (the rig proof of 2026-11-10).
