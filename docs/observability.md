# Observability

This is an operator's guide to running Clustarr: what each service exposes, what
the metrics mean, how a single trace follows a movie through five services, and
what `/healthz` and `/readyz` actually check before Kubernetes routes traffic or
restarts a pod.

It documents the stack defined in the design amendment, §A2
(`docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md`). Where
this document and the amendment ever disagree, the amendment is the source of
truth and this file is stale — file an issue.

## Status: what is wired today

Clustarr is pre-alpha, but as of Phase C the catalog and import services do
reconcile, so parts of this stack now have real callers. The rest is still a
working library waiting for its first one. Read this table before you spend a
day debugging a collector that is receiving exactly what it should: nothing.

| Piece | Today | Lands in |
|---|---|---|
| Structured logging, `--log-*` flags, logger in context | **Wired.** Every service builds its logger in `pkg/obs.Bootstrap` and controller-runtime's own output is bridged onto the same stream. | — |
| `/metrics` endpoint and the 22 registered series | **Wired.** `pkg/obs/metrics.Register` runs once per process, so every series is exposed and scrapeable. Phase C increments three of the domain series — `clustarr_import_unmatched_total`, `clustarr_metadata_cache_hits_total` and `clustarr_search_decisions_total`; the rest still read `0`. | M1–M5, as each controller lands |
| `TracerProvider`, OTLP exporter, `--tracing-*` flags | **Wired.** `pkg/obs/tracing.Setup` installs the provider and the W3C propagator. | — |
| Spans around `Reconcile`, work handlers, provider calls, `ffmpeg` | **Partly.** 72 production `tracing.Start` call sites: 42 in the `pkg/` provider clients (metadata, subtitles, torznab, importlist) and 30 across `catalogarr`/`importarr` — every worker handler, the metadata RPC and gateway, and six of the nine catalog controllers. **The Movie, Series and Episode reconcilers have none**, so the three busiest reconcile loops are still invisible to a trace. `ffmpeg` runs are unspanned. | M1–M5, with the code each span wraps |
| Trace propagation across the bus (`Clustarr-Trace`) | **Wired inside `pkg/events`.** Both `natsbus.Bus` and `membus.Bus` take an `events.Hooks{BeforePublish, AfterReceive}` through a `WithHooks` constructor option and call it around every publish and every receive, before the handler runs; a nil hook (the default) is a no-op, so a bus built with none behaves exactly as before. `pkg/obs.BusHooks()` returns `events.Hooks{BeforePublish: tracing.Inject, AfterReceive: tracing.Extract}` unchanged — `pkg/obs/bootstrap_test.go`'s `TestBusHooksCarryOneTraceAcrossPublishAndConsume` proves a span started before a publish is the parent of the span the consumer starts after receiving, across an in-memory bus. **True in production as of Phase C:** `pkg/k8s.ConnectBus` takes `k8s.WithBusHooks(obs.BusHooks())` from every service's `Run`, and an AST guard over each `run.go` fails the build if a service stops passing them. `pkg/k8s` deliberately does **not** import `pkg/obs` — `Inject`/`Extract` read the otel globals `obs.Bootstrap` installs, so a default inside `ConnectBus` would be a silent no-op whenever bootstrap order slipped. The hooks cover publish, receive and both RPC legs. | Done (M1, amendment §A4). |
| `/healthz`, `/readyz` and the `ping` check | **Wired** on every service. | — |
| Per-role readiness checks | **Partly.** The JetStream ping (every service), `catalogarr`'s informer-cache sync and `importarr`'s `/data` present-and-writable check exist; the rest of the table below is design. Every readiness runnable is non-leader-elected, so a non-leader replica can reach Ready — otherwise every rollout deadlocks. Still missing: `indexarr`'s release index and `grabarr`'s torrent re-attach. | M2–M5 |

Everything below is written in the present tense where it is built and marked
where it is not.

## Endpoints

Every `clustarr <service>` process serves the same three endpoints, on the same
ports, regardless of role:

| Port | Path | Purpose |
|---|---|---|
| `:8443` (TLS) | `/metrics` | controller-runtime's built-in metrics plus every collector below, behind the authentication/authorization filter so a `ServiceMonitor` with a bearer token can scrape it. |
| `:8081` | `/healthz` | Liveness. Always just a ping — see [Readiness contract](#readiness-and-liveness). |
| `:8081` | `/readyz` | Readiness. Ping plus whatever external dependency the role genuinely needs. |

The umbrella chart's `ServiceMonitor` overlay scrapes `/metrics` on every
workload; there is nothing per-service to configure.

## Metric catalogue

All 22 series below are registered by `pkg/obs/metrics.Register`, called once
per process against controller-runtime's registry — one `/metrics` endpoint,
one place the catalogue is defined, one place a cardinality regression would be
caught (`pkg/obs/metrics/metrics_test.go` fails the build if a label named
`title`, `path`, `file`, `release`, `name`, `url` or `query` is ever added: those
are unbounded by media library size or by request content, and a Prometheus
series per movie title is how a `/metrics` endpoint takes down its own pod).

Every name carries the `clustarr_` prefix and Prometheus's base units
(bytes, seconds); counters end in `_total`.

| Metric | Type | Labels | What it signals | Example query |
|---|---|---|---|---|
| `clustarr_download_bytes_total` | counter | `protocol`, `client` | Rate collapsing toward zero while `clustarr_downloads_active` stays nonzero means transfers have stalled without failing outright. | `sum by (protocol) (rate(clustarr_download_bytes_total[5m]))` |
| `clustarr_download_speed_bytes_per_second` | gauge | `protocol`, `client` | A sustained drop below the client's usual speed points at ISP/seedbox throttling or a saturated engine pod. | `avg by (client) (clustarr_download_speed_bytes_per_second)` |
| `clustarr_downloads_active` | gauge | `protocol`, `client` | Approaching the ~500 torrents/pod budget (§12) means the torrent StatefulSet needs another replica. | `sum by (protocol) (clustarr_downloads_active)` |
| `clustarr_download_duration_seconds` | histogram | `protocol`, `outcome` | A rising p95 usually means grabs are stuck, not just slow — check `downloads_active` alongside it. | `histogram_quantile(0.95, sum by (le, protocol) (rate(clustarr_download_duration_seconds_bucket[15m])))` |
| `clustarr_downloads_completed_total` | counter | `protocol`, `outcome` | A rising `failed` share signals a bad indexer, a blocked client, or a release that never had seeders. | `sum(rate(clustarr_downloads_completed_total{outcome="failed"}[15m])) / sum(rate(clustarr_downloads_completed_total[15m]))` |
| `clustarr_import_files_total` | counter | `kind`, `mode`, `outcome` | A shift from `mode="hardlink"` to `mode="copy"` on files that used to hardlink means a RootFolder now lives on a different filesystem than the download directory — every import just got slower and doubled disk usage. | `sum by (mode) (rate(clustarr_import_files_total[1h]))` |
| `clustarr_import_unmatched_total` | counter | `kind`, `reason` | Any sustained rate is the manual-work signal directly: the scanner is refusing to guess, so someone has to look at `LibraryScan.status.unmatched`. | `sum by (reason) (increase(clustarr_import_unmatched_total[1d]))` |
| `clustarr_indexer_query_duration_seconds` | histogram | `indexer`, `function` | An indexer's p95 climbing toward the 45s search deadline (§8.2) means its queries are about to start timing out. | `histogram_quantile(0.95, sum by (le, indexer) (rate(clustarr_indexer_query_duration_seconds_bucket[15m])))` |
| `clustarr_indexer_queries_total` | counter | `indexer`, `outcome` | A spike in `outcome="error"` or `"rate_limited"` for one indexer usually means an expired session cookie or a ban, not a code problem. | `sum by (outcome) (rate(clustarr_indexer_queries_total{indexer="myindexer"}[15m]))` |
| `clustarr_indexer_releases_returned` | histogram | `indexer` | A drop toward zero for one indexer while others stay normal means that indexer is dead, mis-categorised, or logged out — not that nothing is being released this week. | `histogram_quantile(0.5, sum by (le, indexer) (rate(clustarr_indexer_releases_returned_bucket[1h])))` |
| `clustarr_indexer_releases_dropped_total` | counter | `indexer` | An RSS poll drops rows the release index would refuse rather than losing the whole page with them. A sustained non-zero rate for one indexer means its feed is sending titles that normalise to nothing or items with no GUID — the releases are real, and you are not seeing them. | `sum by (indexer) (rate(clustarr_indexer_releases_dropped_total[1h]))` |
| `clustarr_search_decisions_total` | counter | `kind`, `decision`, `reason` | The top support question, answered directly: break down `decision="reject"` by `reason` to see why nothing is grabbing. | `topk(5, sum by (reason) (increase(clustarr_search_decisions_total{decision="reject"}[1d])))` |
| `clustarr_metadata_cache_hits_total` | counter | `tier`, `outcome` | Whether the metadata gateway is sparing the upstream providers. A hit ratio falling toward zero means the cache TTL or the refresh heuristic is wrong, and the provider rate limits will be next to complain. | `sum by (tier) (rate(clustarr_metadata_cache_hits_total{outcome="hit"}[1h])) / sum by (tier) (rate(clustarr_metadata_cache_hits_total[1h]))` |
| `clustarr_transcode_jobs_active` | gauge | `tier` | Sustained equality with the `--slots` budget (§12) means the transcode queue is backing up and needs more CPU/GPU slots, not just patience. | `sum by (tier) (clustarr_transcode_jobs_active)` |
| `clustarr_transcode_duration_seconds` | histogram | `tier`, `resolution`, `outcome` | A jump for `tier="gpu"` specifically usually means encodes silently fell back to software — check `transcode_speed_ratio` for the same tier. | `histogram_quantile(0.95, sum by (le, tier, resolution) (rate(clustarr_transcode_duration_seconds_bucket[1h])))` |
| `clustarr_transcode_speed_ratio` | gauge | `tier` | Below `1.0` on `tier="gpu"` means the encode is slower than real time — hardware contention, thermal throttling, or an unintended software path. | `avg by (tier) (clustarr_transcode_speed_ratio)` |
| `clustarr_transcode_size_ratio` | histogram | `tier`, `resolution` | The point of the service, so drift toward `1.0` (no savings) matters: sources already HEVC-encoded, or a profile's CRF set too conservatively. | `histogram_quantile(0.5, sum by (le, resolution) (rate(clustarr_transcode_size_ratio_bucket[1d])))` |
| `clustarr_subtitle_fetches_total` | counter | `provider`, `language`, `outcome` | A spike in `outcome="error"` or `"throttled"` for one provider means its daily quota is gone or its API changed shape. | `sum by (provider, outcome) (rate(clustarr_subtitle_fetches_total[1h]))` |
| `clustarr_provider_quota_remaining` | gauge | `provider` | The useful alert fires here, before fetches start failing: falling toward zero predicts the `subtitle_fetches_total{outcome="throttled"}` spike above. | `min by (provider) (clustarr_provider_quota_remaining)` |
| `clustarr_work_queue_pending` | gauge | `stream`, `consumer` | Sustained growth means the consumer is falling behind producers — this is also the KEDA scaling trigger (§12) when autoscaling is enabled. | `sum by (consumer) (clustarr_work_queue_pending)` |
| `clustarr_work_handled_total` | counter | `consumer`, `outcome` | A rising `outcome="discard"` rate is the dead-letter signal: messages are landing in `CLUSTARR_DLQ` instead of completing. | `sum by (consumer) (rate(clustarr_work_handled_total{outcome="discard"}[15m]))` |
| `clustarr_work_duration_seconds` | histogram | `consumer` | A rising p99 for one consumer often correlates with AckWait redeliveries for that same consumer — check `work_handled_total{outcome="retry"}`. | `histogram_quantile(0.99, sum by (le, consumer) (rate(clustarr_work_duration_seconds_bucket[15m])))` |
| `clustarr_reconcile_errors_total` | counter | `controller` | Any sustained rate means a controller is failing repeatedly; it is usually visible here before it is visible on the affected resources' conditions. | `sum by (controller) (rate(clustarr_reconcile_errors_total[15m])) > 0` |

controller-runtime's own metrics (`controller_runtime_reconcile_errors_total`,
`workqueue_depth`, `rest_client_requests_total`, …) are served on the same
endpoint and are worth alerting on too; `clustarr_reconcile_errors_total` is
the domain-level complement, not a replacement.

### Adding a metric

The brief that produced this catalogue expects it to grow as real operation
surfaces new questions. A new series is one `newCounterVec` / `newGaugeVec` /
`newHistogramVec` call in `pkg/obs/metrics/domain.go` — it is registered and
guarded automatically because `Register` and the cardinality tests both walk
the same `all` slice. Add its row to the table above in the same change; an
undocumented metric is not discoverable by an operator who only reads this
file.

## Traces

Every service installs an OpenTelemetry `TracerProvider` at startup, exporting
over OTLP when `--tracing-enabled` is set. That part is built today.

The spans themselves are not: `pkg/obs/tracing.Start` has no production call
sites yet, so a collector pointed at a running Clustarr receives an empty
stream. As each milestone lands, spans **will** wrap:

- every controller `Reconcile` call (M1 onwards),
- every work-queue handler (M1 onwards),
- every outbound HTTP call to an indexer or a metadata provider (M1, M2),
- every `ffmpeg` invocation (M4), and
- every filesystem import (M3).

`tracing.Start` already enriches the logger carried by the context with
`trace_id` and `span_id`, so once a span wraps a code path, every log line
emitted underneath it carries both and a trace ID from a dashboard is also a
`grep` key against the structured logs.

### Propagation across services

Spans inside one process nest the ordinary way. What will make Clustarr's
traces worth opening is that they do not stop at a process boundary:
`pkg/events` defines a `Clustarr-Trace` header on the envelope, carrying a W3C
`traceparent`, and `pkg/obs/tracing.Inject`/`Extract` read and write it. A span
started in one service then becomes the parent of a span started by the service
that consumes the message it published — one trace, five services, zero
correlation IDs to thread through code by hand.

**The mechanism is wired; the production call site is not, yet.** `natsbus.Bus`
and `membus.Bus` both take an `events.Hooks{BeforePublish, AfterReceive}`
through a `WithHooks` constructor option and call it around every publish and
every receive — `BeforePublish` before the envelope is encoded onto the wire,
`AfterReceive` before the handler, with its returned context becoming the
handler's — so no publish or subscribe path can forget to propagate the
header once its bus is built with hooks installed. `pkg/obs.BusHooks()` wires
`Inject` and `Extract` into that shape unchanged, and
`pkg/obs/bootstrap_test.go` proves a trace crosses a publish and a consume
over an in-memory bus. What is still missing is the one place a real bus gets
built: `pkg/k8s.ConnectBus` calls `natsbus.New(nc)` with no `WithHooks` option,
so no running service passes `BusHooks()` to its bus yet, and today a trace
still ends at the process that started it in production. Amendment §A4
assigns wiring that call site, and so the first end-to-end exercise of one
trace, to M1.

Sampling defaults to parent-based with a configurable ratio; whatever the
ratio, a span that ends in an error is always sampled. An operator chasing one
failed grab does not need to hope it landed in the 1%.

That "always sampled" guarantee is enforced by the collector's tail sampling,
not by this SDK: `pkg/obs/tracing.Setup` installs a head sampler
(`ParentBased(TraceIDRatioBased(...))`) that decides at span *start*, before
the span has an outcome to look at, so the ratio it applies is the whole
guarantee this process can give on its own -- the collector must run a tail
sampling policy that keeps every trace containing an error for the promise
above to hold end to end.

`Setup` also installs at most one `TracerProvider` per process (a `sync.Once`
guard, exactly like `pkg/obs/metrics.Register`'s): under `clustarr all`, which
runs every service in one process for kind and development, every span from
every service therefore carries `service.name="clustarr"` rather than its own
name, because one process has one `TracerProvider` and there is nothing to
attribute a span to but the process as a whole. Run services separately (one
`clustarr <service>` per process, as every real Deployment does) for
per-service trace attribution.

### One trace, want to subtitles

This is the flow the tracing stack is built to make legible — the base
design's event flow (§8) is otherwise hard to follow in production because it
crosses five independently-scaled services. Reading it top to bottom is
reading one `traceparent` end to end.

It is the target, not today: none of the spans below exist yet. Amendment §A4
assigns the first end-to-end exercise of this trace to M1, and the **Status**
table above says which piece arrives when.

1. **Want** — a `Movie`/`Episode` becomes `Wanted`. `catalogarr`'s reconciler
   span publishes `work.catalogarr.search.<tier>.<mediaKey>`, carrying the
   trace.
2. **Search** — `catalogarr`'s search worker picks up the message (its handler
   span is a child of the publish span above), calls `rpc.indexarr.search`
   (`indexarr` runs its query span as a child of that RPC), and evaluates the
   results.
3. **Decide → grab** — still inside the search worker's span tree: `Evaluate`
   and `Rank` run as plain function calls (no HTTP, no NATS hop, so no new
   span), then a delay-profile decision either publishes
   `work.catalogarr.grab.normal.<mediaKey>` for a delayed grab, or creates the
   `Download` directly and grabs now.
4. **Download** — `grabarr`'s torrent or usenet engine consumes the grab and
   drives the transfer; its span covers the client call that starts the
   download. The multi-minute-to-multi-day transfer itself is not one long
   span — it is the `clustarr_download_duration_seconds` histogram above,
   bucketed out to 3 days so a starved torrent's tail is still measurable
   instead of being clamped into the last bucket — but the engine's periodic
   status reconciles remain part of the same trace via the `Download`'s
   owned events.
5. **Import** — `importarr`'s fileimport worker consumes
   `work.importarr.fileimport.<download>` once the `Download` reaches
   `Completed`/`Seeding`; its span covers probing, naming and the
   hardlink-or-copy/move. (Amendment §A1.2 moved this out of `catalogarr`.)
6. **Transcode** — `squasharr`'s `transcodeprofile` controller reacts to the
   new `MediaFile.status.probeHash` (a Kubernetes watch, not a NATS hop, so
   this leg's parent is the import span's trace context carried on the
   resource rather than a message header) and creates a `TranscodeJob`; the
   worker's `ffmpeg` span is the expensive one.
7. **Subtitles** — `captionarr` reacts to the same `probeHash` change (after
   the transcode's re-probe) and fetches subtitles; its provider-call spans
   close the trace.

A trace that stops early is as informative as one that completes: a trace with
no span past step 2 means nothing matched; one that stops at step 4 means the
download never finished; one that stops at step 5 means the file is sitting in
`status.import.state=blocked` waiting for an operator.

## Readiness and liveness

`/healthz` and `/readyz` are wired by `pkg/k8s.AddProbes`, which always adds a
`ping` check to both — liveness never depends on anything external, because a
JetStream blip must not restart every controller in the cluster at once. Every
service adds `/readyz` checks on top of that ping for the dependencies its role
genuinely needs, matching §A2.4 (amendment) and §13 (base design).

The **Status** column says which of these a running pod performs today; the
rest land with the code whose dependency they guard.

| Dependency | Checked by | Status | Why it is readiness, not liveness |
|---|---|---|---|
| NATS connectivity + JetStream ping (`pkg/k8s.BusReadyChecker`) | Every service that consumes work: `catalogarr`, `indexarr`, `grabarr`, `squasharr`, `captionarr`, `importarr` | **Wired** | A NATS outage should pull the pod out of Service rotation, not restart it — a controller whose Kubernetes watches are still healthy has useful reconcile work to do regardless. |
| Release index volume open (SQLite FTS5) | `indexarr` | M2 | The local index is how `indexarr` answers queries fast; if it failed to open, the pod cannot do its job even though the process is alive. |
| `/data` writability | `importarr` today; `grabarr` (engines), `squasharr` (worker) and `captionarr` (worker) as each starts touching files | **Wired for `importarr`** (`importarr.DataReadyChecker`); M3–M5 for the rest | An RWX volume that has gone read-only (a common CephFS/NFS failure mode) must stop new work without crash-looping the pod. |
| Torrent engine re-attach complete | `grabarr` torrent engines | M3 | An engine restart has to re-attach every persisted torrent from `/data/torrents/.state/*.torrent` before it is safe to accept new adds; readiness gates that, not liveness. |

A pod that is unready but alive keeps its existing work going (an engine keeps
seeding, a controller keeps watching) while Kubernetes stops routing new
traffic and a `Deployment` rollout waits — which is the whole point of
splitting the two checks instead of using one `/health` endpoint for both.

## Exposure

Every endpoint above sits behind the authentication/authorization filter
(`/metrics`) or is meant to be reached only by Kubernetes itself
(`/healthz`, `/readyz`). `ui` is the one service designed to answer real
user traffic, and it carries two different exposure postures on the same
process:

- The web UI itself (every page, `/events/*`) ships with no login of its
  own — `--auth-mode=anonymous` is the only mode — and **must sit behind
  ingress authentication** (design amendment §A3.5); nothing in the binary
  or the installers adds that for you.
- The Plex Custom Metadata Provider (`/plex/movies`, `/plex/tv`, design
  spec §D; `docs/research/plex-metadata-provider.md`), mounted on the same
  `ui` process, is **unauthenticated by the Plex protocol itself** — there
  is no token, header or query parameter it will ever accept — and it
  answers the *entire* catalog, matched or images included, to anyone who
  can reach it. It is strictly more sensitive than the UI's own pages and
  must **never** sit behind a public ingress, whether or not that ingress
  has its own authentication in front of the rest of `ui`. See
  `charts/clustarr/README.md`'s "Plex provider" section for how to point it
  at PMS from inside a private network only, and the chart's `ui.plex.*`
  values and `values.yaml` ingress comments.
