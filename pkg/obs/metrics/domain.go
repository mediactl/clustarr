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

// Bucket sets shared by histograms whose observations land far outside
// prometheus.DefBuckets' 5ms-10s range. Each is documented at its use site
// below; they live here so the same boundaries are visible in one place.
var (
	// durationBucketsLong covers operations that run from a few seconds to
	// several hours: downloads and transcodes.
	durationBucketsLong = []float64{
		1, 5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 14400, 28800,
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

// Work-queue telemetry, shared by every NATS consumer across services.
var (
	// WorkQueuePending is the number of messages waiting to be handled, by
	// stream and consumer. This is the KEDA scaling input as well as the
	// backpressure signal for operators.
	WorkQueuePending = newGaugeVec(
		"clustarr_work_queue_pending",
		"Pending messages in the work queue, by stream and consumer.",
		"stream", "consumer",
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
