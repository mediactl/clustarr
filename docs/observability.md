# Observability

This is an operator's guide to running Clustarr: what each process exposes, what the metrics
mean, how a single trace follows a movie from the manager through its agents, and what
`/healthz` and `/readyz` actually check before Kubernetes routes traffic or restarts a pod.

It documents the stack defined in the design amendment, §A2
(`docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md`), as the manager/agent
split (ADR-0018), the MediaFile fold (ADR-0016), agents that never write Kubernetes (ADR-0019)
and NATS client resilience (ADR-0020) built it on the unify branch (2026-10-07). Where this
document and the code disagree, the code wins and this file is stale -- fix it in the same
change.

## Processes

Five binaries, one service name each for traces (`--tracing-*`), and one `/metrics` endpoint
per process that has one:

| Process | `service.name` | `/metrics` | `/healthz`, `/readyz` |
|---|---|---|---|
| `manager` (`cmd/manager`) | `manager` | `:8443`, https, behind controller-runtime's authn/authz filter | `:8081` |
| `agent --domain <d>` (`cmd/agent`), one Deployment per domain and the download engines | `agent-<domain>` (`agent-catalog`, ..., `agent-torrent-engine`) | `:8443`, as the manager | `:8081` |
| `markers` (`cmd/markers`) | `markers` | `:8080`, plain http (`--metrics-bind-address`), with its probes on the same port | `:8080` |
| transcode pool and graft pods (`cmd/transcode`) | `transcode` | none: the manager records the transcode series from the records it incorporates | none (Jobs) |
| `ui` (`cmd/ui`) | `ui` | none: it registers no collectors | on its one HTTP address |

The chart's and `config/prometheus`'s ServiceMonitors scrape the manager and the agent
Deployments; `config/prometheus` adds markers. A scrape needs a bearer token whose subject may
`get /metrics` (`clustarr-metrics-reader`, see `config/prometheus/README.md`).

## Status: what is wired today

| Piece | Today |
|---|---|
| Structured logging, `--log-*` flags, logger in context | **Wired** in every binary; controller-runtime's own output is bridged onto the same stream. |
| `/metrics` and the catalogue below | **Wired.** `pkg/obs/metrics.Register` runs once per process. A few series are registered and never recorded yet; the catalogue says which. |
| `TracerProvider`, OTLP exporter, `--tracing-*` flags | **Wired** in every binary, one provider and one `service.name` per process. |
| Spans | **Wired.** 241 production `tracing.Start` call sites: every reconciler (the remediation loop's file and item keys as `remediation.Reconcile`, each planner's gather and plan as `remediation.<planner>.<phase>`), every work handler, every outbound provider and indexer call. FFmpeg runs in-process (ADR-0017), inside its handler's span. |
| Trace propagation across the bus (`Clustarr-Trace`) | **Wired.** Every binary builds its bus with `obs.BusHooks()` (`busconn.Connect(..., busconn.WithHooks(obs.BusHooks()))`; markers and transcode with the same `tracing.Inject`/`Extract` pair), so a trace crosses every publish, receive and both RPC legs. |
| `/healthz`, `/readyz` | **Wired** per process (below). |
| Alerts | **One shipped:** `ClustarrNATSSlowConsumer` (`config/prometheus/prometheusrule.yaml`, chart `metrics.prometheusRule.enabled`, ADR-0020 §3). Every other query below is a suggestion. |

## Metric catalogue

Every series below except the four marked otherwise is registered by
`pkg/obs/metrics.Register`, called once per process against controller-runtime's registry --
one place the catalogue is defined, one place a cardinality regression is caught
(`pkg/obs/metrics/metrics_test.go` fails the build if a label named `title`, `path`, `file`,
`release`, `name`, `url` or `query` is ever added: those are unbounded by library size or by
request content, and a series per movie title is how a `/metrics` endpoint takes down its own
pod). Each process exposes the whole catalogue; the "Recorded by" column says which process
moves a series.

Every name carries the `clustarr_` prefix and Prometheus's base units (bytes, seconds);
counters end in `_total`.

### Downloads, imports, indexers and search

| Metric | Type | Labels | Recorded by | What it signals | Example query |
|---|---|---|---|---|---|
| `clustarr_download_bytes_total` | counter | `protocol`, `client` | registered, not recorded yet | Rate collapsing toward zero while transfers stay active means they stalled without failing. | `sum by (protocol) (rate(clustarr_download_bytes_total[5m]))` |
| `clustarr_download_speed_bytes_per_second` | gauge | `protocol`, `client` | registered, not recorded yet | A sustained drop below a client's usual speed points at throttling or a saturated engine pod. | `avg by (client) (clustarr_download_speed_bytes_per_second)` |
| `clustarr_downloads_active` | gauge | `protocol`, `client` | registered, not recorded yet | Approaching ~500 torrents per pod means the torrent engine needs another replica. | `sum by (protocol) (clustarr_downloads_active)` |
| `clustarr_download_duration_seconds` | histogram | `protocol`, `outcome` | registered, not recorded yet | A rising p95 usually means grabs are stuck, not slow. | `histogram_quantile(0.95, sum by (le, protocol) (rate(clustarr_download_duration_seconds_bucket[15m])))` |
| `clustarr_downloads_completed_total` | counter | `protocol`, `outcome` | registered, not recorded yet | A rising `failed` share signals a bad indexer, a blocked client or a release that never had seeders. | `sum(rate(clustarr_downloads_completed_total{outcome="failed"}[15m])) / sum(rate(clustarr_downloads_completed_total[15m]))` |
| `clustarr_engine_commands_total` | counter | `desired` (`present`, `absent`, `resync`), `outcome` (`published`, `republished`, `error`) | manager (downloads stage, ADR-0019) | `error` rising means engine commands are not reaching `CLUSTARR_WORK_ENGINE`; `republished` rising means an engine is not answering its commands. | `sum by (desired, outcome) (rate(clustarr_engine_commands_total[15m]))` |
| `clustarr_blocklist_calls_total` | counter | `op` (`block`, `unblock`), `outcome` (`applied`, `stale`, `error`) | manager | The release index's blocklist (ADR-0019): `error` means `clustarr.rpc.index.blocklist` failed and the block is retried; `stale` is a superseded call. | `sum by (op, outcome) (increase(clustarr_blocklist_calls_total[1d]))` |
| `clustarr_import_files_total` | counter | `kind`, `mode`, `outcome` | agent `import` | A shift from `mode="hardlink"` to `mode="copy"` means a RootFolder now lives on another filesystem than the download directory: every import got slower and doubled disk use. | `sum by (mode) (rate(clustarr_import_files_total[1h]))` |
| `clustarr_import_unmatched_total` | counter | `kind`, `reason` | manager (scan applier), agent `import` (rescan) | Any sustained rate is the manual-work signal: the scanner refuses to guess, so someone has to look at `LibraryScan.status.unmatched`. | `sum by (reason) (increase(clustarr_import_unmatched_total[1d]))` |
| `clustarr_intake_scan_total` | counter | `kind` (`present`, `missing`, `unmatched`, `orphanPart`), `outcome` (`applied`, `retried`, `discarded`, `unapplied`) | manager (scan intake) | What library scans observed and what the manager wrote of it; `discarded` rising means scan observations are malformed or stale. | `sum by (kind, outcome) (increase(clustarr_intake_scan_total[1d]))` |
| `clustarr_intake_candidates_total` | counter | `outcome` (`incorporated`, `pended`, `refused`, `timedOut`) | manager (candidate intake) | Grab candidates from the RSS matcher and interactive picks, by what their owner decided; `timedOut` means no owner pass settled one inside the 60 s budget. | `sum by (outcome) (rate(clustarr_intake_candidates_total[1h]))` |
| `clustarr_indexer_query_duration_seconds` | histogram | `indexer`, `function` | agent `index`; the manager's Indexer controller for caps probes and logins | An indexer's p95 climbing toward the 45 s search budget means its queries are about to time out. | `histogram_quantile(0.95, sum by (le, indexer) (rate(clustarr_indexer_query_duration_seconds_bucket[15m])))` |
| `clustarr_indexer_queries_total` | counter | `indexer`, `outcome` | agent `index`; the manager's Indexer controller for caps probes and logins | A spike in `error` or `rate_limited` for one indexer usually means an expired session cookie or a ban. | `sum by (outcome) (rate(clustarr_indexer_queries_total{indexer="myindexer"}[15m]))` |
| `clustarr_indexer_releases_returned` | histogram | `indexer` | agent `index` | A drop toward zero for one indexer while others stay normal means it is dead, mis-categorised or logged out. | `histogram_quantile(0.5, sum by (le, indexer) (rate(clustarr_indexer_releases_returned_bucket[1h])))` |
| `clustarr_indexer_releases_dropped_total` | counter | `indexer` | agent `index` | An RSS poll drops rows the release index would refuse rather than losing the page: a sustained rate means a feed sends titles that normalise to nothing or items with no GUID. | `sum by (indexer) (rate(clustarr_indexer_releases_dropped_total[1h]))` |
| `clustarr_search_decisions_total` | counter | `kind`, `decision`, `reason` | manager (grab stage), agents `catalog` (search) and `events` (RSS matcher) | The top support question: break `decision="reject"` down by `reason` to see why nothing grabs. Under `reason="grabplan"` the grab planner's verdicts include `overlapsLive` (a release covering an episode a live grab already covers), `seasonNotAired` and `tooManyEpisodes` (Wave S). | `topk(5, sum by (reason) (increase(clustarr_search_decisions_total{decision="reject"}[1d])))` |
| `clustarr_season_searches_total` | counter | `trigger` (`series`, `season`, `interactive`), `outcome` (`answered`, `skipped`, `failed`, `timedOut`, `paced`, `canceled`) | manager | A person's series and season searches (Wave S), once per season reaching a final phase. A rising `timedOut` or `failed` share means season tasks are not answered; `paced` means every indexer was at its pace both times. | `sum by (trigger, outcome) (increase(clustarr_season_searches_total[1d]))` |
| `clustarr_search_results_total` | counter | `band` (`approved`, `rejected`, `otherTitles`) | manager (Search controller) | Interactive answers' results by band (Wave S). An `otherTitles` share near one means text queries reach mostly noise. | `sum by (band) (increase(clustarr_search_results_total[1d]))` |
| `clustarr_metadata_cache_hits_total` | counter | `tier`, `outcome` | agent `metadata` | Whether the gateway spares the upstream providers; a hit ratio falling toward zero means the TTL or refresh heuristic is wrong. | `sum by (tier) (rate(clustarr_metadata_cache_hits_total{outcome="hit"}[1h])) / sum by (tier) (rate(clustarr_metadata_cache_hits_total[1h]))` |

### The remediation loop, records and probes (ADR-0016)

| Metric | Type | Labels | Recorded by | What it signals | Example query |
|---|---|---|---|---|---|
| `clustarr_remediation_passes_total` | counter | `kind`, `outcome` | manager | The loop's passes. `applied` is status writes; `unchanged` should dominate a quiet library; `conflict` or `stale` rising means another writer or a lagging cache; `paced` rising after a profile or ProbeVersion change is the bulk-write limiter doing its job; `error` is a transient fault that wrote nothing. | `sum by (outcome) (rate(clustarr_remediation_passes_total[5m]))` |
| `clustarr_remediation_planner_failures_total` | counter | `planner`, `reason` | manager | `error` or `panic` for one planner sets its `<Planner>PlannerError` condition on the files it failed for; `transient` rising for every planner at once points at NATS, the apiserver or `/data`. | `sum by (planner, reason) (rate(clustarr_remediation_planner_failures_total[1h]))` |
| `clustarr_remediation_planner_seconds` | histogram | `planner`, `phase` | manager | A planner's `gather` p99 near its deadline (15 s probe, 30 s subtitles, 10 s the rest) means its reads are about to fail transiently. | `histogram_quantile(0.99, sum by (le, planner, phase) (rate(clustarr_remediation_planner_seconds_bucket[5m])))` |
| `clustarr_remediation_effects_total` | counter | `planner`, `effect`, `outcome` | manager | The records and tasks the loop owes after its status lands: `cas_miss` is another writer winning a record (the next pass decides again); `transient` a bus or KV fault; `error` or `panic` sets the planner's condition on the next pass. | `sum by (planner, effect, outcome) (rate(clustarr_remediation_effects_total[1h]))` |
| `clustarr_remediation_io_calls_total` | counter | `op`, `outcome` | manager | `/data` health seen from the manager: `timeout` is a slow or hung mount, `saturated` every I/O worker busy, `breaker_open` five timeouts in 30 s, after which `/data` calls fail fast for a minute. | `sum by (op, outcome) (rate(clustarr_remediation_io_calls_total[5m]))` |
| `clustarr_record_requests_total` | counter | `remediation`, `lane` | manager | What the loop asks its workers for. `remediation="probe",lane="high"` well above the import rate means files change under the library or renames are not recognised; `lane="low"` spikes once after a `mediainfo.ProbeVersion` raise. Requests far above incorporations for one remediation mean its workers are not answering. | `sum by (remediation, lane) (rate(clustarr_record_requests_total[1h]))` |
| `clustarr_record_incorporations_total` | counter | `remediation`, `state` | manager | A rising `state="failed"` share for one remediation names a worker that cannot do its job. | `sum by (remediation, state) (rate(clustarr_record_incorporations_total[1h]))` |
| `clustarr_record_timeouts_total` | counter | `remediation` | manager | Any sustained rate means that remediation's workers are missing or wedged: requests outlive their timeout and are asked again. | `sum by (remediation) (increase(clustarr_record_timeouts_total[1h])) > 0` |
| `clustarr_record_errors_total` | counter | `remediation`, `op` | manager | `op="decode"` means something other than the records protocol wrote a records bucket; `get` and `request` rising together point at NATS. | `sum by (remediation, op) (rate(clustarr_record_errors_total[15m])) > 0` |
| `clustarr_probe_duration_seconds` | histogram | `lane`, `outcome` (`probed`, `failed`, `transient`, `abandoned`, `superseded`) | agent `import` | `abandoned` above zero means a file wedges the in-process prober; `transient` rising with a stable library points at the mount. | `sum by (outcome) (rate(clustarr_probe_duration_seconds_count[15m]))` |
| `clustarr_ffgo_abandoned_calls` | gauge | -- | agents `import`, `caption`; markers | In-process FFmpeg calls that outlived their context and still run (`pkg/ffruntime`, registered by `pkg/ffruntime/ffmetrics`). At 4 (`ffruntime.MaxAbandoned`) the process reports itself wedged and its `ffgo` liveness check restarts it. | `max by (pod) (clustarr_ffgo_abandoned_calls) > 0` |

### Transcode and subtitles

| Metric | Type | Labels | Recorded by | What it signals | Example query |
|---|---|---|---|---|---|
| `clustarr_transcode_jobs_active` | gauge | `tier` | manager (admission) | Files transcoding per class; sustained equality with the `--slots` budget means the queue needs more CPU or GPU slots, not patience. | `sum by (tier) (clustarr_transcode_jobs_active)` |
| `clustarr_transcode_duration_seconds` | histogram | `tier`, `resolution`, `outcome` | manager (the loop, from the worker's record) | A jump for a GPU class usually means encodes fell back to software -- check the speed ratio. | `histogram_quantile(0.95, sum by (le, tier, resolution) (rate(clustarr_transcode_duration_seconds_bucket[1h])))` |
| `clustarr_transcode_speed_ratio` | gauge | `tier` | manager | Below 1.0 on a GPU class means slower than real time: contention, thermal throttling or an unintended software path. | `avg by (tier) (clustarr_transcode_speed_ratio)` |
| `clustarr_transcode_size_ratio` | histogram | `tier`, `resolution` | manager | The point of the service: drift toward 1.0 means sources already HEVC or a quality set too conservatively. | `histogram_quantile(0.5, sum by (le, resolution) (rate(clustarr_transcode_size_ratio_bucket[1d])))` |
| `clustarr_transcode_backlog_files` | gauge | `profile` | manager (admission) | Eligible files beyond a profile's `--job-window` that admission holds in memory (`TranscodeProfile.status.waitingFiles`): a profile edit or a `standard.Version` raise makes it jump, and it should drain at the pace the slots allow. | `sum by (profile) (clustarr_transcode_backlog_files)` |
| `clustarr_transcode_ledger_corrections_total` | counter | -- | manager | A resync's quorum read corrected an admission ledger entry: an observation the ledger missed. Any sustained rate is a bug to report. | `increase(clustarr_transcode_ledger_corrections_total[1h])` |
| `clustarr_transcode_dispatches_total` | counter | `class`, `outcome` (`published`, `republished`, `unclaimed`, `withdrawn`, `lost`) | manager | `unclaimed` or `lost` rising means a pool is not pulling or its workers are dying. | `sum by (class, outcome) (rate(clustarr_transcode_dispatches_total[1h]))` |
| `clustarr_subtitle_fetches_total` | counter | `provider`, `language`, `outcome` | agent `caption` | A spike in `error` or `throttled` for one provider means its daily quota is gone or its API changed shape. | `sum by (provider, outcome) (rate(clustarr_subtitle_fetches_total[1h]))` |
| `clustarr_provider_quota_remaining` | gauge | `provider` | manager (SubtitleProvider controller) | The useful alert fires here, before fetches start failing. | `min by (provider) (clustarr_provider_quota_remaining)` |
| `clustarr_segments_analyzed_total` | counter | `result`, `source` | markers (its own registry) | Files analyzed, by result and the source of their strongest segment. | `sum by (result) (increase(clustarr_segments_analyzed_total[1d]))` |
| `clustarr_segments_stage_seconds` | histogram | `stage` | markers | Which analysis stage is slow. | `histogram_quantile(0.95, sum by (le, stage) (rate(clustarr_segments_stage_seconds_bucket[1h])))` |
| `clustarr_segments_dnn_available` | gauge | -- | markers | 0 means the text detector did not load on ONNX Runtime, so weak segments get no OCR pass. | `min(clustarr_segments_dnn_available) == 0` |

### Work queues, the bus and autoscaling

| Metric | Type | Labels | Recorded by | What it signals | Example query |
|---|---|---|---|---|---|
| `clustarr_work_queue_pending` | gauge | `stream`, `consumer` | manager's leader, every 30 s | A durable's lag (`NumPending + NumAckPending`). It is produced by the manager, and `clustarr_consumer_lag` (the same reading, served to the HPAs by the External Metrics API, `docs/autoscaling.md`) is the autoscaling input. Sustained growth means consumers fall behind producers. | `sum by (consumer) (clustarr_work_queue_pending)` |
| `clustarr_consumer_pending` | gauge | `stream`, `consumer` | manager's leader | Messages matching a durable's filters not yet delivered. | `clustarr_consumer_pending{consumer="import-fileimport"}` |
| `clustarr_consumer_ack_pending` | gauge | `stream`, `consumer` | manager's leader | Delivered and unsettled: in a handler, waiting out a delayed nak, or lapsed. Equal to `clustarr_consumer_max_ack_pending` with waiting pulls means the durable is stalled behind long delayed naks. | `clustarr_consumer_ack_pending >= clustarr_consumer_max_ack_pending` |
| `clustarr_consumer_waiting` | gauge | `stream`, `consumer` | manager's leader | Open pull requests: idle capacity, never part of the lag. | `sum by (consumer) (clustarr_consumer_waiting)` |
| `clustarr_consumer_max_ack_pending` | gauge | `stream`, `consumer` | manager's leader | A durable's cap on unsettled deliveries across every process (the topology's, which only the manager writes). | `clustarr_consumer_max_ack_pending` |
| `clustarr_stream_fill_ratio` | gauge | `stream` | manager's leader, from `STREAM.INFO` | Stored bytes over `MaxBytes`. A single-node memory stream is discard-old, so at 1 it drops its oldest messages, which lag cannot show. Alert at 0.8. | `max by (stream) (clustarr_stream_fill_ratio) > 0.8` |
| `clustarr_work_handled_total` | counter | `consumer`, `outcome` | agent `catalog`, for `catalog-artwork-render` only | A rising `outcome="discard"` rate is the dead-letter signal for overlay renders. For dispatched tasks the signal is `clustarr_task_events_total{kind="term"}` and the DLQ projector's `clustarr.io/dead-lettered` annotation. | `sum by (consumer) (rate(clustarr_work_handled_total{outcome="discard"}[15m]))` |
| `clustarr_work_duration_seconds` | histogram | `consumer` | agent `catalog`, for `catalog-artwork-render` only | A rising p99 for one consumer often correlates with AckWait redeliveries -- check `outcome="retry"`. | `histogram_quantile(0.99, sum by (le, consumer) (rate(clustarr_work_duration_seconds_bucket[15m])))` |
| `clustarr_bus_lapsed_handlers` | gauge | `durable` | the consuming process | Handlers past their delivery's deadline that still run, at most the subscription's slots. | `max by (pod, durable) (clustarr_bus_lapsed_handlers) > 0` |
| `clustarr_bus_saturated` | gauge | `durable` | the consuming process | 1 while lapsed handlers hold every slot: the subscription fetches nothing, and the `bus` liveness check fails once that outlasts the handler budget. | `max by (pod, durable) (clustarr_bus_saturated) == 1` |
| `clustarr_bus_muted_total` | counter | `durable`, `op` (`in_progress`, `nak`) | the consuming process | Settlements a lapsed delivery attempted and the bus muted, because the broker keys them by stream sequence and they would reach the redelivered copy. | `sum by (durable, op) (rate(clustarr_bus_muted_total[1h]))` |
| `clustarr_nats_async_errors_total` | counter | `kind` | every process with a bus | A NATS connection's asynchronous errors: `slow_consumer` (a responder, a KV watch or a fetch inbox dropped messages), permissions, disconnects. Any `slow_consumer` increase fires `ClustarrNATSSlowConsumer`. | `sum by (namespace, pod) (increase(clustarr_nats_async_errors_total{kind="slow_consumer"}[5m])) > 0` |
| `clustarr_nats_dropped_messages` | gauge | `subscription` | every process with a bus | Messages nats.go dropped from one core subscription's buffer, by its bounded name (an RPC verb or a subscribed pattern, never a message subject; ADR-0020 §3). Non-zero in steady state means the pending limits are too small or a callback does work it should hand off. | `max by (pod, subscription) (clustarr_nats_dropped_messages) > 0` |
| `clustarr_dispatch_waiting` | gauge | `consumer` | manager's leader | Dispatches the manager has not admitted to a durable (`Budget`, `Paced`, `NoAgent`, `NoCapableAgent`, `EngineNotReady`, `Rebuilding`; ADR-0019). A dashboard and alert series, never an HPA metric (`docs/autoscaling.md`). | `sum by (consumer) (clustarr_dispatch_waiting)` |
| `clustarr_dispatch_unattended` | gauge | `consumer` | manager's leader | 1 while a dispatched durable has work and no agent taking it: no pull open and no progress for two minutes, or no agent of its domain present (`pkg/presence`). Recorded with a `DispatchUnattended` Event. | `max by (consumer) (clustarr_dispatch_unattended) == 1` |
| `clustarr_task_events_total` | counter | `consumer`, `kind` (`nak`, `term`), `outcome` (`recorded`, `stale`, `unresolved`, `metricsOnly`) | manager's leader (advisory intake) | JetStream's nak and term advisories of dispatched tasks. `unresolved` rising means advisories for objects that no longer exist. | `sum by (consumer, kind, outcome) (rate(clustarr_task_events_total[1h]))` |
| `clustarr_task_ack_delay_seconds` | histogram | `consumer` | manager's leader | Sampled delivery-to-ack time of dispatched tasks (10% of a dispatched durable's acks, every engine's). | `histogram_quantile(0.95, sum by (le, consumer) (rate(clustarr_task_ack_delay_seconds_bucket[1h])))` |
| `clustarr_task_deliveries` | histogram | `consumer` | manager's leader | Sampled deliveries an acked task took; a high median means handlers are failing and retrying. | `histogram_quantile(0.5, sum by (le, consumer) (rate(clustarr_task_deliveries_bucket[1h])))` |
| `clustarr_agent_write_refused_total` | counter | `verb` | agents | Kubernetes writes an agent attempted and its read-only client refused (`k8s.ReadOnly`, ADR-0019). Any value above zero is a bug. | `sum by (pod, verb) (increase(clustarr_agent_write_refused_total[1h])) > 0` |
| `clustarr_reconcile_errors_total` | counter | `controller` | registered, not recorded yet; use controller-runtime's `controller_runtime_reconcile_errors_total` | -- | `sum by (controller) (rate(controller_runtime_reconcile_errors_total[15m])) > 0` |

### Object stores and artwork (ADR-0011, ADR-0021)

| Metric | Type | Labels | Recorded by | What it signals | Example query |
|---|---|---|---|---|---|
| `clustarr_object_orphan_chunks_purged_total` | counter | `bucket` | manager (object-store maintenance) | Chunk subjects of full copies two racing `Put`s of one name leaked, which no metadata names, purged. A steady rate means concurrent writers of one object. | `sum by (bucket) (increase(clustarr_object_orphan_chunks_purged_total[1d]))` |
| `clustarr_object_orphan_bytes_purged_total` | counter | `bucket` | manager | The bytes those purges freed (estimated from each subject's last chunk). | `sum by (bucket) (increase(clustarr_object_orphan_bytes_purged_total[1d]))` |
| `clustarr_artwork_objects` | gauge | `variant`, `meta_version` | manager (artwork reaper) | Artwork objects at the last sweep, by variant (`original`, `overlay`) and metadata version (`none` for an object stored before metadata existed): a metadata backfill's progress. | `sum by (variant, meta_version) (clustarr_artwork_objects)` |
| `clustarr_artwork_audit_tasks_total` | counter | `variant`, `reason` | registered, not recorded: the reaper's status audit retired with ADR-0019 (A5), the item keys plan fetches and renders themselves | -- | -- |

controller-runtime's own metrics (`controller_runtime_reconcile_errors_total`,
`workqueue_depth`, `rest_client_requests_total`, ...) are served on the same endpoint by the
manager and every agent, and are worth alerting on too.

### Adding a metric

A new series is one `newCounterVec` / `newGaugeVec` / `newHistogramVec` call in
`pkg/obs/metrics/domain.go` -- it is registered and guarded automatically because `Register`
and the cardinality tests both walk the same `all` slice. Add its row to the tables above in
the same change; an undocumented metric is not discoverable by an operator who only reads this
file.

## Traces

Every binary installs an OpenTelemetry `TracerProvider` at startup, exporting over OTLP when
`--tracing-enabled` is set, with its own `service.name`: `manager`, `agent-<domain>`,
`markers`, `transcode` or `ui` (OD5). Spans wrap every reconcile, every work handler, every
outbound HTTP call to an indexer or a metadata provider, and every import. `tracing.Start`
enriches the logger carried by the context with `trace_id` and `span_id`, so a trace ID from a
dashboard is also a `grep` key against the structured logs.

### Propagation across processes

`pkg/events` carries a W3C `traceparent` in the `Clustarr-Trace` header, and
`pkg/obs/tracing.Inject`/`Extract` read and write it. Every binary builds its bus with
`events.Hooks{BeforePublish: tracing.Inject, AfterReceive: tracing.Extract}` (`obs.BusHooks()`
through `busconn.WithHooks`), so a span started in one process becomes the parent of the span
the consuming process starts -- one trace across the manager and its agents, with no
correlation IDs threaded by hand. `pkg/obs/bootstrap_test.go` proves a trace crosses a publish
and a consume over an in-memory bus.

Sampling defaults to parent-based with a configurable ratio. That "an error is always sampled"
is the collector's job, not this SDK's: `tracing.Setup` installs a head sampler
(`ParentBased(TraceIDRatioBased(...))`) that decides at span start, before the span has an
outcome, so the collector must run a tail-sampling policy that keeps every trace containing an
error for the promise to hold end to end.

### One trace, want to subtitles

This is the flow the tracing stack is built to make legible. Reading it top to bottom is reading
one `traceparent` end to end, across the manager and four agent domains.

1. **Want.** A Movie or Episode becomes wanted. The manager's item key (`remediation.Reconcile`)
   runs its search stage, which admits and publishes a search task to `catalog-search-high` or
   `-normal`, carrying the trace.
2. **Search.** The `catalog` agent's search worker takes the task (its handler span is a child of
   the publish), calls `clustarr.rpc.index.search` (the `index` agent's query span is a child of
   that RPC) and answers in `clustarr-searches`.
3. **Decide and grab.** The record wakes the item key; its grab stage runs the decision engine,
   the delay profile and the keep-best pending grab, and writes a grab entry. The downloads stage
   publishes an engine command to `CLUSTARR_WORK_ENGINE`.
4. **Download.** The engine (`agent --domain torrent-engine|usenet-engine`) executes the command
   and reports in `clustarr-transfers`; the multi-day transfer itself is not one long span.
5. **Import.** The downloads stage dispatches an inspect and then an execute task to the `import`
   agent (`clustarr.work.import.fileimport.*`); the manager creates the MediaFile.
6. **Probe, transcode and subtitles.** The remediation loop's file key plans a probe
   (`CLUSTARR_WORK_PROBE`, answered by the `import` agent in `clustarr-probes`), then a transcode
   (dispatched to a pool, answered in `clustarr-transcodes`) and subtitle fetches (answered by
   the `caption` agent in `clustarr-subtitles`). A record wakes the loop through a KV watch, not a
   message header, so the loop's pass after a record starts a new trace.

A trace that stops early is as informative as one that completes: one with no span past step 2
means nothing matched; one that stops at step 4 means the transfer never finished.

## Readiness and liveness

`/healthz` and `/readyz` are wired by `pkg/k8s.AddProbes` in the manager and the agents, which
always adds a `ping` check to both -- liveness never depends on anything external, because a
JetStream blip must not restart every pod at once. Readiness adds what the process genuinely
needs; liveness adds only what a restart cures.

| Process | `/readyz` checks | `/healthz` checks |
|---|---|---|
| manager | `ping`, `jetstream` (connected and JetStream answering), `cache` (informer caches synced) | `ping`, `bus` |
| every agent | `ping`, `jetstream`, `cache` | `ping`, `bus` |
| agent `import` | plus `import.data`: `/data` present and writable | plus `ffgo` |
| agent `caption` | plus `caption.data` | plus `ffgo` |
| agent `index` | plus `index.releaseindex`: the release index open | |
| agent `torrent-engine` | plus `torrent-engine.reattach`: every persisted transfer re-attached | |
| agent `usenet-engine` | plus `usenet-engine.reattach` | |
| markers | green once NATS is connected and its subscription bound | fails only on a bus wedged by handlers that ignore their context |
| ui | cache sync and the first completed projection round | always |
| transcode pods | none (Jobs) | none |

- **`bus`** (ADR-0020, split §3.3 as amended): handlers that ignore their context have held a
  subscription's every slot past its handler budget, which only a restart cures.
- **`ffgo`**: four in-process FFmpeg calls (`ffruntime.MaxAbandoned`) outlived their context and
  still run; the process is wedged.
- **`/data` writability** runs in the background (`k8s.DataReadyChecker`): `/readyz` answers
  from the last finished write, re-probes once it is 30 s old (at once after a failure), and
  fails on a write error or a write still running after 60 s -- a write inline in the 1 s probe
  failed readiness whenever NFS was slow (2026-10-05). An RWX volume gone read-only stops new
  work without crash-looping the pod.

Every readiness check runs on every replica, not only the leader, so a non-leader manager can
reach Ready and a rollout does not deadlock. A pod that is unready but alive keeps its existing
work going (an engine keeps seeding) while Kubernetes stops routing new traffic and a rollout
waits.

## Exposure

Every endpoint above sits behind the authentication/authorization filter (`/metrics` on
`:8443`), is plain http on a pod port (markers' `:8080`), or is meant to be reached only by
Kubernetes itself (`/healthz`, `/readyz`). `ui` is the one process designed to answer real user
traffic, and it carries two different exposure postures on the same process:

- The web UI itself (every page, `/events/*`) ships with no login of its own --
  `--auth-mode=anonymous` is the only mode -- and **must sit behind ingress authentication**
  (design amendment §A3.5); nothing in the binary or the installers adds that for you.
- The Plex Custom Metadata Provider (`/plex/movies`, `/plex/tv`, `docs/research/plex-metadata-provider.md`),
  mounted on the same `ui` process, is **unauthenticated by the Plex protocol itself** -- there
  is no token, header or query parameter it will ever accept -- and it answers the *entire*
  catalog, images included, to anyone who can reach it. It is strictly more sensitive than the
  UI's own pages and must **never** sit behind a public ingress, whether or not that ingress has
  its own authentication in front of the rest of `ui`. See `charts/clustarr/README.md`'s "Plex
  provider" section for how to point it at PMS from inside a private network only.

The External Metrics API the manager serves on `:6443` accepts only front-proxied requests from
the kube-apiserver (`docs/autoscaling.md`).
