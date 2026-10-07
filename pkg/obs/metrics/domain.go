/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package metrics

// durationBucketsLongMax is the minimum acceptable top boundary for
// durationBucketsLong, in seconds (3 days). Guarded by
// TestDurationBucketsLongCoverDaysNotHours in metrics_test.go so the
// ceiling cannot silently regress back to a few hours.
const durationBucketsLongMax = 259200

// Bucket sets shared by histograms whose observations land far outside
// prometheus.DefBuckets' 5ms-10s range. Each is documented at its use site
// below; they live here so the same boundaries are visible in one place.
var (
	// durationBucketsLong covers operations that run from a few seconds to
	// several days: downloads (a starved torrent can sit for days) and
	// transcodes (a large remux on a loaded CPU tier can run most of a
	// day). The top boundary must stay at or above durationBucketsLongMax
	// (3 days) or histogram_quantile silently clamps a slow/stuck tail to
	// the last finite bucket instead of reporting it — precisely the
	// failure mode the download_duration_seconds and
	// transcode_duration_seconds rows in docs/observability.md warn about.
	durationBucketsLong = []float64{
		1, 5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400, 28800,
		43200, 86400, 172800, 259200,
	}

	// durationBucketsShort covers sub-second to roughly a minute: indexer
	// queries and work-queue handlers, which are expected to be fast and
	// where a slow tail matters at fine granularity.
	durationBucketsShort = []float64{
		.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60,
	}

	// countBuckets covers small non-negative counts, such as how many
	// releases a single indexer query returned.
	countBuckets = []float64{0, 1, 2, 5, 10, 20, 50, 100, 200, 500}

	// ratioBuckets covers a 0-1.5 fraction, such as transcoded output size
	// relative to input size.
	ratioBuckets = []float64{
		0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0, 1.2, 1.5,
	}
)

// Download telemetry, used by grabarr.
var (
	// DownloadBytesTotal counts bytes downloaded, by protocol and client.
	DownloadBytesTotal = newCounterVec(
		"clustarr_download_bytes_total",
		"Total bytes downloaded, by protocol and client.",
		"protocol", "client",
	)

	// DownloadSpeedBytes is the current download speed in bytes per
	// second, by protocol and client.
	DownloadSpeedBytes = newGaugeVec(
		"clustarr_download_speed_bytes_per_second",
		"Current download speed in bytes per second, by protocol and client.",
		"protocol", "client",
	)

	// DownloadsActive is the number of downloads currently in progress, by
	// protocol and client.
	DownloadsActive = newGaugeVec(
		"clustarr_downloads_active",
		"Number of downloads currently active, by protocol and client.",
		"protocol", "client",
	)

	// DownloadDuration is how long a download takes from start to a
	// terminal outcome, by protocol and outcome.
	DownloadDuration = newHistogramVec(
		"clustarr_download_duration_seconds",
		"Duration of downloads in seconds, by protocol and outcome.",
		durationBucketsLong,
		"protocol", "outcome",
	)

	// DownloadsCompleted counts downloads that reached a terminal state,
	// by protocol and outcome.
	DownloadsCompleted = newCounterVec(
		"clustarr_downloads_completed_total",
		"Total downloads completed, by protocol and outcome.",
		"protocol", "outcome",
	)
)

// Import telemetry, used by importarr.
var (
	// ImportFilesTotal counts files that entered the library, by kind
	// (movie, episode, track, ...), mode (hardlink, copy, move) and
	// outcome.
	ImportFilesTotal = newCounterVec(
		"clustarr_import_files_total",
		"Total files imported into the library, by kind, mode and outcome.",
		"kind", "mode", "outcome",
	)

	// ImportUnmatchedTotal counts files the scanner could not attribute to
	// any resource, by kind and reason. This is the manual-work signal:
	// LibraryScan.status.unmatched entries, aggregated.
	ImportUnmatchedTotal = newCounterVec(
		"clustarr_import_unmatched_total",
		"Total scanner guesses that failed, by kind and reason.",
		"kind", "reason",
	)
)

// Indexer telemetry, used by indexarr.
var (
	// IndexerQueryDuration is how long an indexer query takes, by indexer
	// and function (search, caps, ...).
	IndexerQueryDuration = newHistogramVec(
		"clustarr_indexer_query_duration_seconds",
		"Duration of indexer queries in seconds, by indexer and function.",
		durationBucketsShort,
		"indexer", "function",
	)

	// IndexerQueriesTotal counts indexer queries, by indexer and outcome
	// (ok, error, rate_limited, banned, ...).
	IndexerQueriesTotal = newCounterVec(
		"clustarr_indexer_queries_total",
		"Total indexer queries, by indexer and outcome.",
		"indexer", "outcome",
	)

	// IndexerReleasesReturned is the distribution of how many releases a
	// single query returned, by indexer.
	IndexerReleasesReturned = newHistogramVec(
		"clustarr_indexer_releases_returned",
		"Number of releases returned per indexer query, by indexer.",
		countBuckets,
		"indexer",
	)

	// IndexerReleasesDropped counts releases the local index refused, by
	// indexer. An RSS poll drops them rather than failing the whole page --
	// relindex validates a batch before it opens its transaction, so one
	// malformed row would otherwise lose every good release beside it -- and
	// this is the only signal that it happened.
	//
	// There is deliberately no reason label: the reasons are few but the
	// operator question is "is this indexer sending me garbage", which the
	// rate alone answers, and the log line carries the reason per row.
	IndexerReleasesDropped = newCounterVec(
		"clustarr_indexer_releases_dropped_total",
		"Total releases dropped because the local index would refuse them, by indexer.",
		"indexer",
	)
)

// Search telemetry, used by catalogarr's release decision pipeline.
var (
	// SearchDecisionsTotal counts release accept/reject decisions, by kind,
	// decision and reason. This is the metric behind the most common
	// support question: why was this release rejected.
	SearchDecisionsTotal = newCounterVec(
		"clustarr_search_decisions_total",
		"Total release search decisions, by kind, decision and reason.",
		"kind", "decision", "reason",
	)

	// MetadataCacheHitsTotal counts metadata gateway cache lookups. The tier
	// is the whole cardinality budget: never label this by title or id.
	MetadataCacheHitsTotal = newCounterVec(
		"clustarr_metadata_cache_hits_total",
		"Total metadata cache lookups, by tier and outcome.",
		"tier", "outcome",
	)
)

// Transcode telemetry, used by squasharr.
var (
	// TranscodeJobsActive is the number of transcode Jobs currently
	// running, by tier (cpu, gpu).
	TranscodeJobsActive = newGaugeVec(
		"clustarr_transcode_jobs_active",
		"Number of transcode jobs currently active, by tier.",
		"tier",
	)

	// TranscodeDuration is how long a transcode takes, by tier, resolution
	// and outcome.
	TranscodeDuration = newHistogramVec(
		"clustarr_transcode_duration_seconds",
		"Duration of transcodes in seconds, by tier, resolution and outcome.",
		durationBucketsLong,
		"tier", "resolution", "outcome",
	)

	// TranscodeSpeedRatio is encode speed relative to real time (1.0 = real
	// time, 2.0 = twice as fast as playback), by tier.
	TranscodeSpeedRatio = newGaugeVec(
		"clustarr_transcode_speed_ratio",
		"Encode speed relative to real time (1.0 = real time), by tier.",
		"tier",
	)

	// TranscodeSizeRatio is output size relative to input size, by tier and
	// resolution — the space actually saved, the point of the service.
	TranscodeSizeRatio = newHistogramVec(
		"clustarr_transcode_size_ratio",
		"Output size relative to input size, by tier and resolution.",
		ratioBuckets,
		"tier", "resolution",
	)
)

// Subtitle telemetry, used by captionarr.
var (
	// SubtitleFetchesTotal counts subtitle fetch attempts, by provider,
	// language and outcome.
	SubtitleFetchesTotal = newCounterVec(
		"clustarr_subtitle_fetches_total",
		"Total subtitle fetch attempts, by provider, language and outcome.",
		"provider", "language", "outcome",
	)

	// ProviderQuotaRemaining is the remaining daily quota for a subtitle
	// provider, by provider.
	ProviderQuotaRemaining = newGaugeVec(
		"clustarr_provider_quota_remaining",
		"Remaining provider quota for the day, by provider.",
		"provider",
	)
)

// Object stores (artwork design §B.5 as amended 2026-10-07), labelled by
// bucket (clustarr-artwork, clustarr-fingerprints) and, for artwork, by
// variant (original, overlay).
var (
	// ObjectOrphanChunksPurgedTotal counts chunk subjects natsbus's
	// PurgeOrphanChunks purged, by bucket: the full copies two racing Puts
	// of one name leak, which no meta names.
	ObjectOrphanChunksPurgedTotal = newCounterVec(
		"clustarr_object_orphan_chunks_purged_total",
		"Total orphaned object-store chunk subjects purged, by bucket.",
		"bucket",
	)

	// ObjectOrphanBytesPurgedTotal counts the bytes those purges freed
	// (estimated from each subject's last chunk), by bucket.
	ObjectOrphanBytesPurgedTotal = newCounterVec(
		"clustarr_object_orphan_bytes_purged_total",
		"Total bytes of orphaned object-store chunks purged, by bucket.",
		"bucket",
	)

	// ArtworkObjects is the artwork bucket's objects at the reaper's last
	// sweep, by variant and metadata version ("none" for an object stored
	// before metadata existed): the backfill's progress.
	ArtworkObjects = newGaugeVec(
		"clustarr_artwork_objects",
		"Artwork objects at the last reaper sweep, by variant and metadata version.",
		"variant", "meta_version",
	)

	// ArtworkAuditTasksTotal counts the fetch and render tasks the reaper's
	// audit published, by variant and reason (missing, digest, meta).
	ArtworkAuditTasksTotal = newCounterVec(
		"clustarr_artwork_audit_tasks_total",
		"Total artwork repair and backfill tasks the reaper's audit published, by variant and reason.",
		"variant", "reason",
	)
)

// Work-queue telemetry, shared by every NATS consumer across services.
var (
	// WorkQueuePending is the backlog of a durable consumer, by stream and
	// consumer: NumPending + NumAckPending, the autoscaling input. The
	// manager's leader sets it every 30 s (app/autoscale/extmetrics.
	// QueueGauge) from the same ConsumerInfo the External Metrics API
	// serves as clustarr_consumer_lag.
	WorkQueuePending = newGaugeVec(
		"clustarr_work_queue_pending",
		"Pending messages in the work queue, by stream and consumer.",
		"stream", "consumer",
	)

	// ConsumerPending is a durable's NumPending: undelivered messages
	// matching its filters (split §9.0 as amended 2026-10-07). Set beside
	// WorkQueuePending by QueueGauge.
	ConsumerPending = newGaugeVec(
		"clustarr_consumer_pending",
		"Messages matching a durable's filters not yet delivered (NumPending), by stream and consumer.",
		"stream", "consumer",
	)

	// ConsumerAckPending is a durable's NumAckPending: delivered and not
	// settled -- in a handler, waiting out a delayed nak, or lapsed.
	ConsumerAckPending = newGaugeVec(
		"clustarr_consumer_ack_pending",
		"Messages delivered and not settled (NumAckPending), by stream and consumer.",
		"stream", "consumer",
	)

	// ConsumerWaiting is a durable's NumWaiting: open pull requests, idle
	// capacity, never part of the lag.
	ConsumerWaiting = newGaugeVec(
		"clustarr_consumer_waiting",
		"Open pull requests on a durable (NumWaiting), idle capacity, by stream and consumer.",
		"stream", "consumer",
	)

	// ConsumerMaxAckPending is a durable's MaxAckPending: its cap on
	// unsettled deliveries across every process.
	ConsumerMaxAckPending = newGaugeVec(
		"clustarr_consumer_max_ack_pending",
		"A durable's cap on unsettled deliveries across every process (MaxAckPending), by stream and consumer.",
		"stream", "consumer",
	)

	// StreamFillRatio is a stream's stored bytes over its MaxBytes, by
	// stream: the manager's leader sets it every 30 s (QueueGauge) from
	// STREAM.INFO. A single-node memory stream is DiscardOld, so at 1 it
	// is dropping its oldest messages, which lag cannot show (S10).
	StreamFillRatio = newGaugeVec(
		"clustarr_stream_fill_ratio",
		"A stream's stored bytes over its MaxBytes, by stream; 1 means a DiscardOld stream is dropping its oldest messages.",
		"stream",
	)

	// WorkHandledTotal counts work items a consumer finished handling, by
	// consumer and outcome (ok, retry, discard, ...).
	WorkHandledTotal = newCounterVec(
		"clustarr_work_handled_total",
		"Total work items handled, by consumer and outcome.",
		"consumer", "outcome",
	)

	// WorkDuration is how long a single work item takes to handle, by
	// consumer.
	WorkDuration = newHistogramVec(
		"clustarr_work_duration_seconds",
		"Duration of work handling in seconds, by consumer.",
		durationBucketsShort,
		"consumer",
	)

	// BusMutedTotal counts InProgress and Nak calls the bus did not send
	// because their delivery had lapsed and been redelivered, by durable
	// and op (in_progress, nak).
	BusMutedTotal = newCounterVec(
		"clustarr_bus_muted_total",
		"Settlements a lapsed delivery attempted and the bus muted, by durable and op.",
		"durable", "op",
	)

	// BusLapsedHandlers is how many of a subscription's handlers have lapsed
	// and still run, by durable: at most its slots (split §9.3).
	BusLapsedHandlers = newGaugeVec(
		"clustarr_bus_lapsed_handlers",
		"Handlers past their delivery's deadline that still run, by durable.",
		"durable",
	)

	// BusSaturated is 1 while a subscription's lapsed handlers hold its
	// every slot, by durable: it fetches nothing then, and the bus liveness
	// check fails once that outlasts the handler budget (S1).
	BusSaturated = newGaugeVec(
		"clustarr_bus_saturated",
		"1 while lapsed handlers hold every slot of a subscription, by durable.",
		"durable",
	)

	// NATSAsyncErrorsTotal counts a NATS connection's asynchronous errors by
	// kind: a slow consumer dropping messages (a Serve responder, a KV watch,
	// a Fetch inbox), a permission violation, a disconnect with an error, or
	// anything else. Never labelled by subject: subjects carry media keys.
	NATSAsyncErrorsTotal = newCounterVec(
		"clustarr_nats_async_errors_total",
		"Asynchronous NATS connection errors, by kind.",
		"kind",
	)
)

// Controller telemetry, shared by every reconciler alongside
// controller-runtime's own controller_runtime_reconcile_errors_total.
var (
	// ReconcileErrorsTotal counts reconcile errors, by controller.
	ReconcileErrorsTotal = newCounterVec(
		"clustarr_reconcile_errors_total",
		"Total reconcile errors, by controller.",
		"controller",
	)
)

// Probe queue telemetry (spec 2026-10-06 §6.6). The probe's requests are
// counted as clustarr_record_requests_total{remediation="probe"}, which
// replaced the split's own probe request counter (loop spec 2026-10-06
// §4.12).
var (
	// ProbeDuration is how long the import domain's probe worker took per
	// task, by lane and outcome (probed, failed, transient, abandoned,
	// superseded).
	ProbeDuration = newHistogramVec(
		"clustarr_probe_duration_seconds",
		"Duration of MediaFile probes in seconds, by lane and outcome.",
		durationBucketsShort,
		"lane", "outcome",
	)
)

// Records protocol telemetry (loop spec 2026-10-06 §4.15). The manager alone
// counts these; pool, graft and markers pods export none. lane is high, low
// or none; op is get, request, withdraw, decode, publish or watch.
var (
	RecordRequestsTotal = newCounterVec(
		"clustarr_record_requests_total",
		"Total records-bucket requests the remediation loop wrote, by remediation and lane.",
		"remediation", "lane",
	)
	RecordIncorporationsTotal = newCounterVec(
		"clustarr_record_incorporations_total",
		"Total worker records the remediation loop incorporated, by remediation and record state.",
		"remediation", "state",
	)
	RecordTimeoutsTotal = newCounterVec(
		"clustarr_record_timeouts_total",
		"Total requests that outlived their remediation's request timeout, by remediation.",
		"remediation",
	)
	RecordErrorsTotal = newCounterVec(
		"clustarr_record_errors_total",
		"Total records-bucket operations that failed, by remediation and operation.",
		"remediation", "op",
	)
)

// Agent write refusals (ADR-0019 §9.2). pkg/k8s.ReadOnly counts every
// Kubernetes write it refuses, by verb (create, update, patch, apply,
// delete, deleteAllOf, or <subresource>/<verb>). Agents never write the
// Kubernetes API, so any value above zero is a bug.
var (
	AgentWriteRefusedTotal = newCounterVec(
		"clustarr_agent_write_refused_total",
		"Kubernetes writes an agent attempted and the read-only client refused (ADR-0019); any value above zero is a bug.",
		"verb",
	)
)

// Dispatch admission (ADR-0019 §5.4, §5.5). The manager's leader sets both
// from app/dispatch's ledger; they are dashboards' and alerts', never an HPA
// metric: the HPA sees only work an agent can take.
var (
	// DispatchWaiting is how many CRs wait to dispatch to a durable: due,
	// not admitted (Budget, Paced, NoAgent, NoCapableAgent, ...), by
	// consumer.
	DispatchWaiting = newGaugeVec(
		"clustarr_dispatch_waiting",
		"Dispatches the manager has not yet admitted to a durable, by consumer.",
		"consumer",
	)
	// DispatchUnattended is 1 while a dispatched durable is unattended:
	// pending work with no pull open and no progress for two minutes, or no
	// agent of its domain present, by consumer.
	DispatchUnattended = newGaugeVec(
		"clustarr_dispatch_unattended",
		"1 while a dispatched durable has work and no agent taking it, by consumer.",
		"consumer",
	)
)

// Intake (ADR-0019 §4.3, §8.4): what the manager's leader-only intake
// consumers decided of what agents proposed.
var (
	// IntakeCandidatesTotal counts grab candidates by how their owner's pass
	// settled them: incorporated, pended, refused, or timedOut (no pass
	// settled it within the handler budget; redelivered).
	IntakeCandidatesTotal = newCounterVec(
		"clustarr_intake_candidates_total",
		"Grab candidates the manager's intake handled, by outcome.",
		"outcome",
	)
	// IntakeScanTotal counts library-scan observations by kind (present,
	// missing, unmatched, orphanPart) and outcome (applied, retried,
	// discarded, unapplied).
	IntakeScanTotal = newCounterVec(
		"clustarr_intake_scan_total",
		"Library-scan observations the manager's intake handled, by kind and outcome.",
		"kind", "outcome",
	)
)

// Task events (ADR-0019 §8.2): JetStream's nak and term advisories and its
// sampled ack metrics of every task the manager dispatches, read by the
// leader's advisory intake. consumer is a durable's name (a transcode pool's
// or an engine instance's included: bounded by profiles, classes and
// DownloadClients, never by an item).
var (
	// TaskEventsTotal counts advisories by consumer, kind (nak, term) and
	// outcome: recorded (delivery state written to the book), stale (a
	// newer dispatch exists), unresolved (no CR found), metricsOnly (a nak
	// between the first and the last).
	TaskEventsTotal = newCounterVec(
		"clustarr_task_events_total",
		"Nak and term advisories of dispatched tasks, by consumer, kind and outcome.",
		"consumer", "kind", "outcome",
	)
	// TaskAckDelay is the sampled time from delivery to ack, by consumer
	// (SampleFrequency: 10% of a dispatched durable's acks, all of an
	// engine's).
	TaskAckDelay = newHistogramVec(
		"clustarr_task_ack_delay_seconds",
		"Sampled delivery-to-ack time of dispatched tasks in seconds, by consumer.",
		durationBucketsLong,
		"consumer",
	)
	// TaskDeliveries is the sampled delivery count of an acked task, by
	// consumer.
	TaskDeliveries = newHistogramVec(
		"clustarr_task_deliveries",
		"Sampled deliveries an acked dispatched task took, by consumer.",
		[]float64{1, 2, 3, 4, 5, 8, 12, 16, 20},
		"consumer",
	)
)

// Grabs (ADR-0019 §6.7, §6.14): what the manager's downloads stage owed the
// engines and the release index's blocklist.
var (
	// BlocklistCallsTotal counts clustarr.rpc.indexarr.blocklist calls by op
	// (block, unblock) and outcome (applied, stale, error).
	BlocklistCallsTotal = newCounterVec(
		"clustarr_blocklist_calls_total",
		"Blocklist calls the manager made to the release index, by op and outcome.",
		"op", "outcome",
	)
	// EngineCommandsTotal counts engine commands the manager published, by
	// desired state (present, absent, resync) and outcome (published,
	// republished, error).
	EngineCommandsTotal = newCounterVec(
		"clustarr_engine_commands_total",
		"Engine commands the manager published, by desired state and outcome.",
		"desired", "outcome",
	)
)
