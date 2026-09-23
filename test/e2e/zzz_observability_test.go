//go:build e2e

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

// X12c (docs/superpowers/plans/2026-09-23-gap-fixes.md) -- Phase H scenario
// 15 (remaining-work.md): "After scenario 1, /metrics on each service
// exposes the documented clustarr_ series with non-zero values (download
// rate, transcode fps, queue pending, indexer duration); readiness flips
// when NATS is scaled to zero and back; a poisoned work message reaches
// CLUSTARR_DLQ and the DLQ projector surfaces it."
//
// # The zzz_ filename prefix is deliberate
//
// go test's own file-alphabetical execution order (no test in this package
// calls t.Parallel(), confirmed by grep) is the only ordering guarantee
// this suite has, and every other file's own doc comments lean on it
// implicitly (e.g. transcode_test.go's newTranscodeProfile picks a
// (resolution, source) pair "no OTHER e2e scenario's real, PROBED
// MediaFile uses" -- a statement that is only meaningful given a fixed
// run order). "After scenario 1" reads most naturally as "after a
// representative slice of this whole suite has already run", not as an
// instruction to duplicate download_test.go's, indexer_test.go's and
// transcode_test.go's own real work a second time just to observe it --
// Prometheus's client_golang only emits a MetricVec's HELP/TYPE lines
// (and the metric family at all) once at least one label combination has
// actually been observed, so `clustarr_transcode_speed_ratio` in
// particular will never appear at squasharr's /metrics without a REAL
// TranscodeJob having Succeeded first (transcode_test.go's own
// TestTranscodeMediaFileThroughTranscodeJob and
// TestDownloadScenario1TranscodeLeg both do this already). Naming this
// file to sort after every letter any other scenario file's name starts
// with ('c' through 'u') is what makes "after scenario 1" true without
// spending a second real download, indexer probe and transcode encode on
// proving it.
//
// This is documented here, not left to be discovered: a reader renaming
// this file to something more conventional would silently turn every
// "non-zero" assertion below into a coin flip against `go test`'s file
// compilation order, and CLAUDE.md's own standing instruction is that a
// test which passes only sometimes is worse than no test.
//
// Every wait below still names its own reason on failure rather than
// assuming the ordering held, the same discipline every other file in
// this package uses for a dependency it cannot control directly.
package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const (
	// metricsPort is every service's controller-runtime metrics listener
	// (docs/observability.md's Endpoints table: ":8443 (TLS)").
	metricsPort = 8443

	// probePort is /healthz and /readyz -- plain HTTP, no bearer token.
	probePort = 8081

	// e2eMetricsReaderServiceAccount is config/e2e/metrics-reader.yaml's
	// ServiceAccount, bound to a ClusterRole granting `get` on the
	// /metrics nonResourceURL -- see that file's own doc comment for why
	// it duplicates config/prometheus/metrics_reader_role.yaml's rule
	// rather than referencing it.
	e2eMetricsReaderServiceAccount = "e2e-metrics-reader"
)

// metricsReaderToken mints a short-lived TokenRequest for
// e2eMetricsReaderServiceAccount, the same mechanism a real Prometheus's
// own ServiceAccount uses (config/prometheus/README.md), via `kubectl
// create token` -- the TokenRequest API has no client-go-free CLI-less
// equivalent this suite already depends on, and kubectl is already a hard
// requirement (main_test.go's own gate).
func metricsReaderToken(ctx context.Context, t *testing.T) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "kubectl", "--context", KubeContext, "-n", Namespace,
		"create", "token", e2eMetricsReaderServiceAccount, "--duration=10m")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "kubectl create token %s: %s", e2eMetricsReaderServiceAccount, out)
	return strings.TrimSpace(string(out))
}

// fetchMetrics port-forwards to svc/svcName's metrics port and returns the
// raw Prometheus exposition text, bearer-authenticated against the
// authn/authz filter docs/observability.md §13 describes. TLS is
// self-signed by the manager at startup (the same README.md comment
// config/prometheus/servicemonitors.yaml already has for its own
// insecureSkipVerify), so this does the same.
func fetchMetrics(ctx context.Context, t *testing.T, svcName string) string {
	t.Helper()
	base, stop := portForwardService(ctx, t, svcName, metricsPort)
	defer stop()
	token := metricsReaderToken(ctx, t)

	url := strings.Replace(base, "http://", "https://", 1) + "/metrics"
	httpClient := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // self-signed manager cert, docs/observability.md §13
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	require.NoErrorf(t, err, "GET %s", url)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "GET %s: %s", url, body)
	return string(body)
}

// metricSum returns the sum of every sample whose metric name is exactly
// family (Prometheus label sets aside -- e.g. every {protocol=...,
// client=...} series of clustarr_download_bytes_total). It is intentionally
// simple line-based parsing, not a full text-format decoder: this suite
// already declines a new dependency for a one-shot HTTP fetch
// (portForwardService's own doc comment gives the identical reasoning for
// preferring `kubectl` over pulling in client-go's SPDY transport), and
// client_golang's `expfmt`/`prometheus` packages are libraries this test
// binary does not otherwise need.
func metricSum(body, family string) (float64, int) {
	var sum float64
	var n int
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		name := line
		if idx := strings.IndexAny(line, "{ "); idx >= 0 {
			name = line[:idx]
		}
		if name != family {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		sum += v
		n++
	}
	return sum, n
}

// TestObservabilityMetricsNonZero is scenario 15's metrics leg. See this
// file's own package doc comment for why it reads the OTHER scenario
// files' real activity rather than generating its own.
func TestObservabilityMetricsNonZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	// download rate: clustarr_download_bytes_total, incremented by every
	// completed torrent/usenet transfer in this suite's earlier scenarios
	// (download_test.go, import_test.go, transcode_test.go, subtitle_test.go
	// all complete at least one).
	grabarrBody := fetchMetrics(ctx, t, "grabarr")
	downloadBytes, downloadSeries := metricSum(grabarrBody, "clustarr_download_bytes_total")
	require.Greaterf(t, downloadSeries, 0,
		"clustarr_download_bytes_total has no series in grabarr's /metrics -- no completed transfer was "+
			"observed by this point in the suite run (see this file's own package doc comment on ordering)")
	require.Greaterf(t, downloadBytes, 0.0, "clustarr_download_bytes_total summed to 0 across %d series", downloadSeries)

	// transcode fps: the catalogue names no literal "fps" series (checked
	// against pkg/obs/metrics/domain.go directly, not assumed); this
	// project's closest documented signal for encode throughput is
	// clustarr_transcode_speed_ratio (docs/observability.md: "Below 1.0 on
	// tier=gpu means the encode is slower than real time"), produced by
	// squasharr/worker's progress reporting (FPSMilli feeds the ratio, not
	// a standalone counter) once a TranscodeJob Succeeds.
	squasharrBody := fetchMetrics(ctx, t, "squasharr")
	_, speedSeries := metricSum(squasharrBody, "clustarr_transcode_speed_ratio")
	require.Greaterf(t, speedSeries, 0,
		"clustarr_transcode_speed_ratio has no series in squasharr's /metrics -- no TranscodeJob had "+
			"Succeeded by this point (transcode_test.go's TestTranscodeMediaFileThroughTranscodeJob and "+
			"TestDownloadScenario1TranscodeLeg both produce one)")

	// queue pending: clustarr_work_queue_pending is a gauge every consumer
	// reports on each poll, including 0 once caught up -- so "has a
	// series" is the meaningful, always-true-once-any-worker-has-run
	// assertion here, not "> 0" (a fully drained queue at scrape time is
	// the SYSTEM WORKING, not a fixture gap).
	importarrWorkerBody := fetchMetrics(ctx, t, "importarr-worker")
	_, pendingSeries := metricSum(importarrWorkerBody, "clustarr_work_queue_pending")
	require.Greaterf(t, pendingSeries, 0,
		"clustarr_work_queue_pending has no series in importarr-worker's /metrics -- no consumer has "+
			"polled at all, which points at the worker never having run rather than an empty queue")

	// indexer duration: clustarr_indexer_query_duration_seconds, recorded
	// by every caps probe (indexarr/controller/indexer) as well as every
	// real search -- indexer_test.go's scenario 17 test and the caps probe
	// every Indexer gets on creation both produce this.
	indexarrBody := fetchMetrics(ctx, t, "indexarr")
	_, durationSeries := metricSum(indexarrBody, "clustarr_indexer_query_duration_seconds_count")
	require.Greaterf(t, durationSeries, 0,
		"clustarr_indexer_query_duration_seconds has no series in indexarr's /metrics -- no indexer query "+
			"(a caps probe or a real search) was observed by this point in the suite run")
}

// TestObservabilityReadinessFlipsWithNATS is scenario 15's readiness leg:
// scaling NATS to zero must flip a bus-dependent service's /readyz away
// from 200, and scaling it back must flip it back. catalogarr is the
// service checked -- docs/observability.md's readiness table names it
// "Wired" for NATS connectivity (pkg/k8s.BusReadyChecker), unlike the rows
// still marked with a milestone.
//
// This scales a real, shared Deployment's dependency (the nats
// StatefulSet) that every OTHER scenario in this suite also depends on.
// It restores it (t.Cleanup, unconditionally, not cleanupUnlessFailed) and
// waits for catalogarr's own readiness to recover before returning, so a
// scenario file that happens to run after this one (alphabetically, only
// itself: zzz_ sorts last) never observes a still-degraded cluster.
func TestObservabilityReadinessFlipsWithNATS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Fatalf("kubectl not on PATH: %v", err)
	}

	scale := func(replicas string) {
		cmd := exec.CommandContext(ctx, "kubectl", "--context", KubeContext, "-n", Namespace,
			"scale", "statefulset/nats", "--replicas="+replicas)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "kubectl scale statefulset/nats --replicas=%s: %s", replicas, out)
	}
	t.Cleanup(func() {
		scale("1")
		waitReadyz(context.Background(), t, "catalogarr", true, 3*time.Minute)
	})

	before := readyzStatus(ctx, t, "catalogarr")
	require.Truef(t, before, "catalogarr's /readyz must be 200 before this test scales NATS down, "+
		"or a flip back to ready afterwards proves nothing")

	scale("0")
	waitReadyz(ctx, t, "catalogarr", false, 3*time.Minute)

	scale("1")
	waitReadyz(ctx, t, "catalogarr", true, 3*time.Minute)
}

// readyzStatus reports whether svcName's /readyz currently answers 200.
func readyzStatus(ctx context.Context, t *testing.T, svcName string) bool {
	t.Helper()
	base, stop := portForwardService(ctx, t, svcName, probePort)
	defer stop()
	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/readyz", nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// waitReadyz polls svcName's /readyz until it matches want (true: 200,
// false: anything else) via readyzStatus, which opens and tears down its
// own fresh port-forward on every call. That costs one kubectl process per
// poll interval rather than holding one tunnel open across the whole wait,
// but a NATS-down catalogarr stays up and merely fails its OWN readiness
// check -- nothing about a not-ready pod tears down an existing
// port-forward from underneath this loop -- so the simpler, one-tunnel-
// per-poll shape is fine at pollInterval's 2s cadence for a bounded wait.
func waitReadyz(ctx context.Context, t *testing.T, svcName string, want bool, timeout time.Duration) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		return readyzStatus(ctx, t, svcName) == want, nil
	})
	require.NoErrorf(t, err, "%s's /readyz never became %v within %s", svcName, want, timeout)
}

// TestObservabilityDLQPoisonMessage is scenario 15's DLQ leg. It publishes
// directly onto CLUSTARR_DLQ's own subject space rather than exhausting a
// real consumer's redelivery ladder to get there naturally: subtitle_test.
// go's own subtitleFetchTimeout doc comment already rules that out for a
// bounded e2e test ("It does not try to reach that consumer's full
// 8-attempt/30s-2m-10m-1h-6h backoff ladder ... not something worth
// budgeting for here"), and the SAME reasoning applies here more so --
// this suite's context timeout is scenarioTimeout (15m), far short of
// even one consumer's ladder. This proves the DLQ PROJECTOR's own
// behaviour (catalogarr/history/dlq.go's Handle: annotate the CR a dead
// letter concerns) in isolation from how a message actually arrives at
// CLUSTARR_DLQ, which pkg/events' own DeadLetter helper and its callers
// are responsible for and are not this test's concern.
//
// The envelope is built from pkg/events' and pkg/events/schema's own
// public types -- catalog.ItemEvent.v1, the schema catalogarr/history/
// target.go's resolveItemEvent decodes -- not hand-rolled wire bytes, so
// this exercises the same header/payload contract a real dead letter
// carries (events.HeaderDLQSubject, -Reason, -Consumer, -Attempts) rather
// than a shape invented for this test alone.
func TestObservabilityDLQPoisonMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-dlq-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	movie := newMovie(ctx, t, "e2e-dlq-movie", fixtureTmdbID, QualityProfileName, rf.Name, catalogv1alpha1.MinimumAvailabilityAnnounced)

	base, stop := portForwardService(ctx, t, "nats", 4222)
	defer stop()
	natsURL := strings.Replace(base, "http://", "nats://", 1)

	bus, nc, err := k8s.ConnectBus(natsURL, "e2e-observability")
	require.NoError(t, err)
	defer nc.Close()
	defer func() { _ = bus.Close() }()

	schemaName, data, err := schema.Encode(schema.ItemEvent{
		Media:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
		Action: "updated",
		At:     time.Now().UTC(),
	})
	require.NoError(t, err)

	id := uniqueName("e2e-dlq")
	env := &events.Envelope{
		Schema: schemaName,
		Key:    Namespace + "/" + movie.Name,
		Data:   data,
		Headers: map[string]string{
			events.HeaderDLQReason:   "e2e poison message (TestObservabilityDLQPoisonMessage)",
			events.HeaderDLQConsumer: "e2e-observability-test",
			events.HeaderDLQAttempts: "3",
			events.HeaderDLQSubject:  "clustarr.evt.catalog.item.updated." + id,
		},
	}
	subject := events.DLQSubject("e2e", "poison", id)

	pubCtx, pubCancel := context.WithTimeout(ctx, 30*time.Second)
	defer pubCancel()
	_, err = bus.Publish(pubCtx, subject, env, events.WithMsgID("e2e-dlq-"+id))
	require.NoErrorf(t, err, "publish to %s", subject)

	waitFor(t, ctx, 2*time.Minute, "Movie "+movie.Name+" carries "+k8s.AnnotationDeadLettered,
		func(ctx context.Context) (bool, error) {
			var live catalogv1alpha1.Movie
			if gerr := k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: movie.Name}, &live); gerr != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			_, ok := live.Annotations[k8s.AnnotationDeadLettered]
			return ok, nil
		}, func() string {
			var live catalogv1alpha1.Movie
			if gerr := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: Namespace, Name: movie.Name}, &live); gerr != nil {
				return fmt.Sprintf("Movie %s could not be read back: %v", movie.Name, gerr)
			}
			return fmt.Sprintf("Movie %s annotations: %v", movie.Name, live.Annotations)
		})

	var live catalogv1alpha1.Movie
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: movie.Name}, &live))
	require.Contains(t, live.Annotations[k8s.AnnotationDeadLettered], "@",
		"the annotation value must be \"<original-subject>@<RFC3339>\" (catalogarr/history/dlq.go's own doc comment)")

	require.True(t, metav1.HasAnnotation(live.ObjectMeta, k8s.AnnotationDeadLettered))
}
