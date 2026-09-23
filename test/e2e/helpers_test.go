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

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/test/fixtures/seed"
	"github.com/mediactl/clustarr/test/fixtures/torznabstub"
)

// fixtureDirName is the directory under $CLUSTARR_DATA_DIR that hack/e2e.sh
// seeds the probe clip into. It is deliberately dot-prefixed so it sits
// beside, not inside, the media roots a scenario walks.
const fixtureDirName = ".e2e-fixtures"

// pollInterval is how often every scenario-level waitFor re-checks. It is
// slower than a controller's own requeue on purpose: the suite is measuring
// convergence, not latency.
const pollInterval = 2 * time.Second

// scanCompletedTimeout is DERIVED, not chosen. Do not round it down.
//
// A LibraryScan completes when importarr's rescan worker acks the scan task,
// so the wait has to cover ConsumerImportScan's redelivery ladder
// (pkg/events/topology.go: AckWait 60s, MaxDeliver 4, BackOff 30s/2m/10m),
// not just the walk. A missed ack is ordinary on a single kind node running
// thirteen pods at 25m CPU requests -- an evicted or restarted worker loses
// its in-flight message and the task only comes back when AckWait expires:
//
//	attempt 1 delivered at   0s, AckWait expires at   60s, backoff 30s
//	attempt 2 delivered at  90s, AckWait expires at  150s, backoff  2m
//	attempt 3 delivered at 270s, AckWait expires at  330s, backoff 10m
//	attempt 4 delivered at 930s  (MaxDeliver, last chance)
//
// A round 5 minutes -- which is what this was, and which produced exactly one
// unexplained timeout at ~308s during Task C11's verification -- sits between
// the third delivery and its completion, so two missed acks read as a hang.
// Seven minutes clears the third attempt's delivery at 270s with margin for
// the walk itself, the controller's 3s progress poll and this suite's own 2s
// poll. Covering the FOURTH attempt would mean waiting past 930s, which
// exceeds every per-scenario context here and most of `make e2e`'s 30-minute
// budget; a scan that has missed three acks is a broken cluster, and the
// failure message now says so.
//
// Secondary, and not covered by any timeout: in single-node mode the
// clustarr-progress KV bucket is memory storage, so a NATS restart loses the
// worker's checkpoint entirely. The scan then sits Running until the
// LibraryScan controller's own noProgressTimeout (30 minutes) fails it --
// far beyond anything this suite waits for, and correctly so.
const scanCompletedTimeout = 7 * time.Minute

// scenarioTimeout bounds one whole scenario. It is larger than
// scanCompletedTimeout so that an individual wait, not the context, is what
// expires first in the common case -- a wait's timeout prints the object's
// state, and naming the stalled predicate is the whole point. It is sized for
// ONE pathological scan plus everything else nominal, not for several: a run
// where two scans each burn the full ladder is a broken cluster, and one
// clear diagnosis beats five truncated ones inside `make e2e`'s 30 minutes.
const scenarioTimeout = 15 * time.Minute

// dataDirOrEmpty returns $CLUSTARR_DATA_DIR without failing the test binary,
// so TestMain can print a clear message instead of a panic when it is unset.
func dataDirOrEmpty() string { return os.Getenv("CLUSTARR_DATA_DIR") }

// dataDir is dataDirOrEmpty for use after TestMain has already verified it
// is set and usable.
func dataDir() string {
	d := dataDirOrEmpty()
	if d == "" {
		panic("e2e: CLUSTARR_DATA_DIR unset after TestMain's gate passed -- this is a harness bug, not a test failure")
	}
	return d
}

// hostPath converts a path as the CLUSTER sees it under /data into the path
// on the machine running `go test`, via the same hostPath mount
// hack/kind.sh's create_cluster wires up. It is the only bridge between the
// two views of the library, and every scenario plants through it.
func hostPath(clusterPath string) string {
	rel := strings.TrimPrefix(clusterPath, "/data")
	return filepath.Join(dataDir(), rel)
}

// uniqueName returns "<prefix>-<5 lowercase alnum chars>", short enough to
// stay under Kubernetes' 63-character name limit for every prefix this suite
// uses and collision-safe enough that a rerun never trips over the previous
// run's leftovers.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, rand.String(5))
}

var (
	sampleClipOnce  sync.Once
	sampleClipBytes []byte
	sampleClipErr   error
)

// sampleClip returns the clip hack/e2e.sh seeds to
// <dataDir>/.e2e-fixtures/tiny.mkv via `clustarr-e2e-fixtures seed`. Every
// planted file that is meant to BECOME a MediaFile is a copy of these bytes,
// so catalogarr's MediaFile controller probes real container and stream data
// with real ffprobe, never a mock.
func sampleClip(t *testing.T) []byte {
	t.Helper()
	sampleClipOnce.Do(func() {
		sampleClipBytes, sampleClipErr = os.ReadFile(filepath.Join(dataDir(), fixtureDirName, seed.ClipName))
	})
	if sampleClipErr != nil {
		t.Fatalf("sampleClip: %v (did hack/e2e.sh run `clustarr-e2e-fixtures seed`?)", sampleClipErr)
	}
	return sampleClipBytes
}

// plantMedia writes a copy of the seeded probe clip at hostAbsPath and
// registers a t.Cleanup to remove it. Use it for every file that is expected
// to become a MediaFile: it is real, probeable media and it is over
// pkg/fsops's sample threshold.
func plantMedia(t *testing.T, hostAbsPath string) {
	t.Helper()
	plantBytes(t, hostAbsPath, sampleClip(t))
}

// plantFiller writes a file that pkg/fsops classifies as ClassMedia -- the
// right extension and comfortably over the 50 MiB sample threshold -- but
// whose contents are never read, because nothing will ever probe it. Use it
// for the files a scenario expects to end up UNMATCHED: copying 57 MiB of
// real video for a file the scanner is supposed to refuse is wasted I/O, and
// a sparse file makes the same point in a few microseconds.
//
// It deliberately does NOT use a small file. pkg/fsops.IsSample flags any
// media-extension file under 50 MiB as a promotional sample, and the rescan
// worker skips samples before matching ever runs -- so a small "unmatchable"
// file would prove nothing at all.
func plantFiller(t *testing.T, hostAbsPath string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(hostAbsPath), 0o775),
		"plantFiller: mkdir %s", filepath.Dir(hostAbsPath))
	f, err := os.OpenFile(hostAbsPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
	require.NoError(t, err, "plantFiller: create %s", hostAbsPath)
	cleanupUnlessFailed(t, func() { _ = os.Remove(hostAbsPath) })
	// A Matroska EBML magic prefix, then a hole: fsops classifies on
	// extension and size only, so this is enough to be ClassMedia, and the
	// magic makes a stray file recognisable in a diagnostic dump.
	_, err = f.Write([]byte{0x1A, 0x45, 0xDF, 0xA3})
	require.NoError(t, err, "plantFiller: write header")
	require.NoError(t, f.Truncate(seed.MinMediaBytes+4096), "plantFiller: size %s", hostAbsPath)
	require.NoError(t, f.Close())
}

// plantBytes writes content at hostAbsPath, creating parent directories, and
// registers a t.Cleanup to remove it -- even though every scenario also
// removes its RootFolder's whole directory, this keeps a failed setup from
// leaking files into the next run. Mode 0664/0775 matches §11's UMASK 002:
// the pods reading these files run as uid/gid 1000, and hostPath volumes
// ignore fsGroup.
func plantBytes(t *testing.T, hostAbsPath string, content []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(hostAbsPath), 0o775),
		"plantBytes: mkdir %s", filepath.Dir(hostAbsPath))
	require.NoError(t, os.WriteFile(hostAbsPath, content, 0o664),
		"plantBytes: write %s", hostAbsPath)
	cleanupUnlessFailed(t, func() { _ = os.Remove(hostAbsPath) })
}

// waitFor polls check every pollInterval until it returns true, or fails the
// test with desc after timeout. Controllers here use RequeueAfter, never
// sleep (CLAUDE.md); this is the test-side mirror of that patience.
//
// details, when given, is called only on failure and appended to the
// message. "context deadline exceeded" on its own says nothing about WHY
// convergence stalled, and by the time hack/e2e.sh dumps the cluster the
// objects may already be gone, so the last observed state has to be captured
// here or not at all.
func waitFor(t *testing.T, ctx context.Context, timeout time.Duration, desc string, check func(context.Context) (bool, error), details ...func() string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, check)
	if err == nil {
		return
	}
	msg := fmt.Sprintf("timed out waiting for %s: %v", desc, err)
	for _, d := range details {
		msg += "\n  last observed: " + d()
	}
	t.Fatal(msg)
}

// cleanupUnlessFailed registers fn to run at the end of a PASSING test only.
// A failing scenario deliberately leaves its objects and planted files
// behind: hack/e2e.sh dumps the cluster after `go test` returns, which is
// after every t.Cleanup has already run, so tearing down on failure destroys
// the evidence the dump exists to collect. Everything this suite creates is
// uniquely named and rooted at a unique path, so leftovers from a failed run
// cannot collide with the next one.
func cleanupUnlessFailed(t *testing.T, fn func()) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("test failed: leaving resources in place for hack/e2e.sh's diagnostics dump")
			return
		}
		fn()
	})
}

// describeScan renders one LibraryScan's phase, counters and conditions for a
// failure message.
func describeScan(key client.ObjectKey) func() string {
	return func() string {
		var live catalogv1alpha1.LibraryScan
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("LibraryScan %s could not be read back: %v", key.Name, err)
		}
		out := fmt.Sprintf("LibraryScan %s mode=%s phase=%q filesSeen=%d filesMatched=%d filesSkipped=%d itemsCreated=%d unmatched=%d",
			key.Name, live.Spec.Mode, live.Status.Phase, live.Status.FilesSeen, live.Status.FilesMatched,
			live.Status.FilesSkipped, live.Status.ItemsCreated, len(live.Status.Unmatched))
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}

// managedFieldsTouch reports whether entry's FieldsV1 touches every one of
// dottedPaths (for example "spec.path", "status.mediaInfo"), by walking the
// "f:"-prefixed structured-merge-diff field set. Scenario 8 uses it to prove
// the two-writer MediaFile split from the apiserver's own bookkeeping rather
// than by re-deriving it from which fields happen to be non-zero.
func managedFieldsTouch(entry metav1.ManagedFieldsEntry, dottedPaths ...string) (bool, error) {
	if entry.FieldsV1 == nil {
		return false, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(entry.FieldsV1.GetRawBytes(), &raw); err != nil {
		return false, fmt.Errorf("managedFieldsTouch: decode FieldsV1: %w", err)
	}
	for _, dotted := range dottedPaths {
		cur := raw
		ok := true
		for _, seg := range strings.Split(dotted, ".") {
			next, has := cur["f:"+seg]
			if !has {
				ok = false
				break
			}
			nm, isMap := next.(map[string]any)
			if !isMap {
				break // leaf field ("f:x": {}) -- present, nothing deeper to walk
			}
			cur = nm
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// newRootFolder creates a RootFolder rooted at a per-run unique path under
// /data, waits for the RootFolder controller to mark it Ready (the
// LibraryScan controller refuses to dispatch a scan until it is), and
// registers cleanup of both the object and the directory.
//
// defaults.qualityProfileRef is always set: importarr's rescan worker
// reports CodeNoQualityProfile instead of creating a Movie when the root
// folder names none, so a RootFolder without one can never discover
// anything.
func newRootFolder(ctx context.Context, t *testing.T, prefix string, kind catalogv1alpha1.RootFolderKind, subdir string) *catalogv1alpha1.RootFolder {
	t.Helper()
	clusterPath := "/data/media/" + subdir + "/" + uniqueName(prefix)
	// The directory must exist before the RootFolder does: its controller
	// probes the path for accessibility and free space.
	require.NoError(t, os.MkdirAll(hostPath(clusterPath), 0o775), "create root folder directory")

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix), Namespace: Namespace},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: clusterPath,
			Kind: kind,
			Defaults: catalogv1alpha1.RootDefaults{
				QualityProfileRef: QualityProfileName,
			},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, rf))
	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), rf)
		_ = os.RemoveAll(hostPath(clusterPath))
	})

	waitFor(t, ctx, 2*time.Minute, "RootFolder "+rf.Name+" Ready", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.RootFolder
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(rf), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, catalogv1alpha1.RootFolderConditionReady), nil
	})
	return rf
}

// runScan creates a LibraryScan for rf, waits for it to reach Completed and
// returns the finished object. A scan that reaches Failed fails the test
// immediately with whatever it recorded as unmatched, which is almost always
// the useful half of the diagnosis.
func runScan(ctx context.Context, t *testing.T, rf *catalogv1alpha1.RootFolder, mode catalogv1alpha1.ScanMode) catalogv1alpha1.LibraryScan {
	t.Helper()
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-scan"), Namespace: Namespace},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: mode},
	}
	require.NoError(t, k8sClient.Create(ctx, scan))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), scan) })
	return waitForScanCompleted(ctx, t, client.ObjectKeyFromObject(scan))
}

// waitForScanCompleted polls one LibraryScan until it reports Completed and
// returns it.
func waitForScanCompleted(ctx context.Context, t *testing.T, key client.ObjectKey) catalogv1alpha1.LibraryScan {
	t.Helper()
	var done catalogv1alpha1.LibraryScan
	waitFor(t, ctx, scanCompletedTimeout, "LibraryScan "+key.Name+" Completed", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.LibraryScan
		if err := k8sClient.Get(ctx, key, &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		if live.Status.Phase == catalogv1alpha1.ScanPhaseFailed {
			return false, fmt.Errorf("LibraryScan %s reached Failed; conditions=%+v unmatched=%+v",
				key.Name, live.Status.Conditions, live.Status.Unmatched)
		}
		done = live
		return live.Status.Phase == catalogv1alpha1.ScanPhaseCompleted, nil
	}, describeScan(key))
	return done
}

// mediaFilesUnder lists every MediaFile whose spec.path sits under
// clusterPath. Scenarios scope their assertions this way rather than by the
// owning item's name: two scenarios that plant the same TMDB id share one
// Movie object, and a count taken across the namespace would see both.
func mediaFilesUnder(ctx context.Context, t *testing.T, clusterPath string) []catalogv1alpha1.MediaFile {
	t.Helper()
	var list catalogv1alpha1.MediaFileList
	require.NoError(t, k8sClient.List(ctx, &list, client.InNamespace(Namespace)))
	var out []catalogv1alpha1.MediaFile
	prefix := strings.TrimSuffix(clusterPath, "/") + "/"
	for _, mf := range list.Items {
		if strings.HasPrefix(mf.Spec.Path, prefix) {
			out = append(out, mf)
		}
	}
	return out
}

// isConditionTrue is the tiny read-only half of pkg/k8s's condition helpers,
// repeated here so the suite never imports a writer helper by accident.
func isConditionTrue(conds []metav1.Condition, condType string) bool {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Scenario 17: the fixture indexer, federated search and the release firehose.
// ---------------------------------------------------------------------------

// indexerReadyTimeout covers an Indexer's first reconcile: one caps fetch
// against a Service on the same node. It races no redelivery ladder at all --
// the reconcile is a watch-driven controller-runtime reconcile, not a queue
// task -- so the only things to cover are the fixture pod's own readiness
// (probe period 5s, up to a few periods on a loaded node), indexarr's
// per-request Timeout (Indexer.spec.timeout, default 30s) and a
// controller-runtime rate-limited requeue after a first failure (the default
// workqueue starts at 5ms and doubles, so two failures cost milliseconds, not
// minutes). Two minutes is roughly four times the worst realistic case and
// short enough that a genuinely broken fixture is reported before the suite
// has burned its budget.
const indexerReadyTimeout = 2 * time.Minute

// searchCompletedTimeout is bounded by the CONTROLLER, not by the queue:
// catalogarr/controller/search's SearchRunningTimeout (5 minutes) fails a
// Search that has sat in Running that long, so no wait past it can ever
// observe a Completed that was not already going to arrive.
//
// Within that window the task rides ConsumerCatalogSearchHigh (AckWait 120s,
// MaxDeliver 5, BackOff 30s/2m/10m):
//
//	attempt 1 delivered at   0s, AckWait expires at 120s, backoff 30s
//	attempt 2 delivered at 150s, AckWait expires at 270s, backoff  2m
//	attempt 3 delivered at 390s  -- already past SearchRunningTimeout
//
// so one lost ack is survivable and two are not, by construction. Six minutes
// clears SearchRunningTimeout plus the reconciler's own requeue and this
// suite's 2s poll; it deliberately does NOT reach for attempt 3, which the
// controller has already given a verdict on.
const searchCompletedTimeout = 6 * time.Minute

// firehoseTimeout covers a release travelling indexarr's RSS poll ->
// CLUSTARR_RELEASES -> catalogarr's rss-matcher -> a status write. It races
// TWO ladders in series.
//
// ConsumerIndexRSS (AckWait 60s, Heartbeat 30s, MaxDeliver 4, BackOff
// 1m/5m/15m):
//
//	attempt 1 at 0s, expires 60s, backoff 1m -> attempt 2 at 120s
//
// ConsumerCatalogRSSMatcher (AckWait 30s, MaxDeliver 6, BackOff
// 1s/5s/30s/2m/10m), measured from the release's publish:
//
//	attempt 1 at   0s   attempt 2 at  31s   attempt 3 at  66s
//	attempt 4 at 126s   attempt 5 at 276s   attempt 6 at 906s
//
// Covering one lost RSS delivery (120s) plus the matcher's FIFTH delivery
// (276s) plus the decide-and-apply round trip and this suite's 2s poll is
// 120 + 276 + ~20 = 416s. Seven minutes sits past that and far short of the
// matcher's sixth attempt at 120 + 906 = 1026s, which would exceed every
// per-scenario context here.
//
// It equals scanCompletedTimeout by coincidence, not by copying: that one is
// derived from ConsumerImportScan. Changing either ladder changes only its
// own constant.
const firehoseTimeout = 7 * time.Minute

// escalationTimeout covers driving a failing indexer far enough up Prowlarr's
// backoff ladder to be disabled for longer than [quietWindow].
//
// The driver is the RSS poll at the scenario's rssInterval of 1 minute, and
// the ladder's first step is a ZERO-length disable
// (indexarr/status.escalationTable's [0, 1m, 5m, ...]), so reaching a
// 5-minute window takes three failing polls: level 0 -> 1 (a 1m window),
// then, when that window expires, level 1 -> 2 (a 5m window). That is ~180s.
// One of those polls can lose its delivery and come back on ConsumerIndexRSS's
// second attempt, 120s after its schedule; with the apply and this suite's 2s
// poll that is ~320s. Six minutes sits past it and short of that consumer's
// third delivery at 480s.
//
// It does NOT include indexarr's startup grace; see
// requireIndexarrEscalationObservable, which waits that out separately and
// says so.
const escalationTimeout = 6 * time.Minute

// quietWindow is how long scenario 17 watches a disabled indexer's request
// log for a query that must not arrive. It is two rssInterval periods: a
// worker that ignored the backoff would poll within one, and a second covers
// a poll whose delivery slipped. A longer window proves nothing extra, only
// eats the scenario budget -- and it must stay well inside the ladder's
// 5-minute step, or the indexer legitimately comes back mid-check.
const quietWindow = 2 * time.Minute

// torznabRequestLogPath is where test/fixtures/torznabstub appends its JSONL
// request log, as the HOST sees it. The stub writes it at
// /data/.e2e-fixtures/torznab/requests.jsonl inside the cluster; both names
// are the same file through kind's hostPath mount.
func torznabRequestLogPath() string {
	return filepath.Join(dataDir(), fixtureDirName, seed.TorznabDirName, seed.RequestLogName)
}

// readTorznabRequests returns every request the fixture indexer logged at or
// after t0. It is the ONLY way this suite can observe what indexarr asked the
// indexer: the test process cannot reach a ClusterIP Service, so the fixture
// writes onto the shared /data volume instead.
//
// The since filter is what makes an assertion live rather than merely
// consistent. Phase C's lesson was that scaling a stub to zero was NOT enough
// to prove a metadata assertion was hitting it -- a warm cache served the same
// answer -- and only forcing cold caches distinguished the two. Here there is
// no equivalent doubt to resolve by deletion: a request logged after the
// scenario started can only have been issued during this run. Both clocks are
// the host kernel's (kind's node shares it), so the comparison is sound
// without any clock-skew allowance.
func readTorznabRequests(t0 time.Time) ([]torznabstub.Entry, error) {
	path := torznabRequestLogPath()
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // the stub has not been asked anything yet
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []torznabstub.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e torznabstub.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			// A torn final line is expected: the stub appends while this
			// reads. Skipping it is right; failing on it would be flaky.
			continue
		}
		if !e.At.Before(t0) {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// torznabRequestsSince is readTorznabRequests for an assertion: a log this
// suite cannot read at all is a harness failure, not a verdict about indexarr.
func torznabRequestsSince(t *testing.T, t0 time.Time) []torznabstub.Entry {
	t.Helper()
	es, err := readTorznabRequests(t0)
	if err != nil {
		t.Fatalf("torznabRequestsSince: %v", err)
	}
	return es
}

// torznabRequestsMatching filters the log to the requests on one apiPath,
// optionally narrowed to one t= function ("" means every function).
func torznabRequestsMatching(t *testing.T, t0 time.Time, apiPath, function string) []torznabstub.Entry {
	t.Helper()
	var out []torznabstub.Entry
	for _, e := range torznabRequestsSince(t, t0) {
		if e.Path != apiPath {
			continue
		}
		if function != "" && e.T != function {
			continue
		}
		out = append(out, e)
	}
	return out
}

// describeTorznabRequests renders the fixture's request log for a failure
// message. hack/e2e.sh copies the whole file into test/e2e/artifacts, but that
// happens after `go test` returns; a wait that fails needs the evidence
// inline, in the message, on the machine where it failed.
func describeTorznabRequests(t0 time.Time) func() string {
	return func() string {
		es, err := readTorznabRequests(t0)
		if err != nil {
			return fmt.Sprintf("the fixture indexer's request log could not be read: %v", err)
		}
		if len(es) == 0 {
			return "the fixture indexer logged NO request since this scenario started " +
				"-- indexarr never contacted it (check the indexarr logs and the Indexer's conditions)"
		}
		out := fmt.Sprintf("fixture indexer saw %d request(s) since this scenario started:", len(es))
		for _, e := range es {
			out += fmt.Sprintf("\n    %s %s?%s -> %d", e.At.Format(time.RFC3339), e.Path, e.Query, e.Status)
		}
		return out
	}
}

// newIndexer creates a generic Torznab Indexer pointed at the in-cluster
// fixture and registers its cleanup. apiPath selects the fixture's
// personality: "" takes Indexer.spec.generic.apiPath's own default of "/api"
// (the healthy one), torznabstub.PathSearchDown one that answers caps and
// fails every search, torznabstub.PathDown one that fails everything.
//
// The object NAME matters beyond uniqueness here, and uniqueName supplies what
// is needed: it is the second token of the release subject, the second segment
// of the firehose envelope key, and the first half of
// events.MsgIDForRelease -- which CLUSTARR_RELEASES dedups on for two hours. A
// fixed name would mean a rerun inside that window silently publishing
// nothing, and the firehose scenario waiting out its whole timeout for a
// release the broker had already suppressed.
func newIndexer(ctx context.Context, t *testing.T, prefix, apiPath string, rssInterval time.Duration, enableRss bool) *indexv1alpha1.Indexer {
	t.Helper()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix), Namespace: Namespace},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "http://torznab-stub.clustarr-system.svc",
			Generic: &indexv1alpha1.GenericNewznab{
				Protocol: commonv1.ProtocolTorrent,
				APIPath:  apiPath,
			},
			SecretRef:   &corev1.LocalObjectReference{Name: "torznab-fixture-credentials"},
			EnableRss:   ptr.To(enableRss),
			RssInterval: metav1.Duration{Duration: rssInterval},
			// 2s is the CRD default and would pace three fan-out requests
			// across six seconds for no reason against a local fixture.
			RequestDelay: metav1.Duration{Duration: 100 * time.Millisecond},
			Priority:     25,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, idx))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), idx) })
	return idx
}

// waitForIndexerReady waits until the Indexer reconciled to Ready and Healthy
// with status.caps populated from a live capabilities fetch.
func waitForIndexerReady(ctx context.Context, t *testing.T, idx *indexv1alpha1.Indexer) indexv1alpha1.Indexer {
	t.Helper()
	t0 := time.Now().Add(-time.Minute) // the caps fetch may already have happened
	var live indexv1alpha1.Indexer
	waitFor(t, ctx, indexerReadyTimeout, "Indexer "+idx.Name+" Ready with caps",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(idx), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return live.Status.Caps != nil &&
				isConditionTrue(live.Status.Conditions, indexv1alpha1.IndexerConditionReady) &&
				isConditionTrue(live.Status.Conditions, indexv1alpha1.IndexerConditionHealthy), nil
		}, describeIndexer(client.ObjectKeyFromObject(idx)), describeTorznabRequests(t0))
	return live
}

// describeIndexer renders one Indexer's resolved fields, escalation and
// conditions for a failure message. An Indexer that never goes healthy has
// almost always stalled on one condition, and naming it is the difference
// between a diagnosis and a shrug.
func describeIndexer(key client.ObjectKey) func() string {
	return func() string {
		var live indexv1alpha1.Indexer
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("Indexer %s could not be read back: %v", key.Name, err)
		}
		modes := "<no status.caps>"
		if live.Status.Caps != nil {
			keys := make([]string, 0, len(live.Status.Caps.Modes))
			for k := range live.Status.Caps.Modes {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			modes = strings.Join(keys, ",")
		}
		apiPath := "<no spec.generic>"
		if live.Spec.Generic != nil {
			apiPath = live.Spec.Generic.APIPath
		}
		out := fmt.Sprintf("Indexer %s apiPath=%q protocol=%q privacy=%q capsModes=[%s] "+
			"escalationLevel=%d disabledUntil=%v initialFailureAt=%v lastRssAt=%v lastRssNewCount=%d "+
			"indexedReleases=%d lastFailure=%q",
			key.Name, apiPath, live.Status.Protocol, live.Status.Privacy, modes,
			live.Status.EscalationLevel, live.Status.DisabledUntil, live.Status.InitialFailureAt,
			live.Status.LastRssAt, live.Status.LastRssNewCount,
			live.Status.IndexedReleases, live.Status.LastFailure)
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}

// describeSearch renders one Search's phase, timings, per-indexer outcomes and
// result count for a failure message. status.indexerOutcomes is the field that
// diagnoses a failed search, and it is the first thing a reader needs.
func describeSearch(key client.ObjectKey) func() string {
	return func() string {
		var live catalogv1alpha1.Search
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("Search %s could not be read back: %v", key.Name, err)
		}
		out := fmt.Sprintf("Search %s phase=%q startedAt=%v finishedAt=%v results=%d",
			key.Name, live.Status.Phase, live.Status.StartedAt, live.Status.FinishedAt,
			len(live.Status.Results))
		for _, o := range live.Status.IndexerOutcomes {
			out += fmt.Sprintf("\n    indexerOutcome %s state=%s count=%d durationMs=%d error=%q",
				o.Name, o.State, o.Count, o.DurationMs, o.Error)
		}
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}

// tier is one entry of a scenario-owned QualityProfile.
type tier struct {
	name      string
	qualities []string
}

// newRankedQualityProfile creates a cluster-scoped, multi-tier QualityProfile
// and registers its cleanup. Scenarios that assert an ORDER need it:
// config/e2e's e2e-any has a single tier, so every quality in it compares
// equal and a ranking assertion would pass on size or seeders instead of on
// quality.
//
// Two fields are deliberately not left at their CRD defaults, and both are
// working around the SAME Phase C defect rather than expressing a preference.
// catalogarr stores Movie.status.metadata.originalLanguage as a BCP-47 tag
// ("en", from TMDB) and hands it straight to pkg/decision as
// Target.OriginalLanguage and to the custom-format catalogue as
// ItemContext.OriginalLanguage -- but both of those consume Radarr's English
// DISPLAY names ("English"), which is what release.ParsedRelease.Languages
// carries and what catalogue.ItemContext's own doc comment demands. The
// mismatch means, for an English release of an English movie:
//
//   - language "original" (the CRD DEFAULT) rejects every release with
//     ReasonWantedLanguage ("original language en is wanted, but found
//     [English]"); and
//   - the language-not-original custom format matches, scoring -10000, which
//     the default MinFormatScore of 0 then rejects as well.
//
// So a profile left at its defaults approves NOTHING, on any real movie, in
// either the search path or the RSS path. That is not this task's to fix --
// it is in catalogarr/worker/search/snapshot.go and
// catalogarr/worker/rssmatcher/resolve.go -- and scenario 17 is about the
// indexer path, so the profile opts out of both checks and says why. Remove
// these two lines once the vocabulary mismatch is fixed.
func newRankedQualityProfile(ctx context.Context, t *testing.T, prefix string, tiers []tier) *catalogv1alpha1.QualityProfile {
	t.Helper()
	spec := catalogv1alpha1.QualityProfileSpec{
		MediaKind:      catalogv1alpha1.ProfileMediaKindVideo,
		BuiltIn:        false,
		Cutoff:         tiers[len(tiers)-1].name,
		UpgradeAllowed: ptr.To(true),
		Language:       "any",
		MinFormatScore: -10000,
	}
	for _, tr := range tiers {
		spec.Tiers = append(spec.Tiers, catalogv1alpha1.Tier{Name: tr.name, Qualities: tr.qualities})
	}
	// QualityProfile is cluster-scoped (api/catalog/v1alpha1/qualityprofile_types.go
	// "+kubebuilder:resource:scope=Cluster"), so the object carries no namespace.
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix)},
		Spec:       spec,
	}
	require.NoError(t, k8sClient.Create(ctx, qp))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), qp) })

	waitFor(t, ctx, 2*time.Minute, "QualityProfile "+qp.Name+" Ready", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.QualityProfile
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: qp.Name}, &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, catalogv1alpha1.QualityProfileConditionReady), nil
	})
	return qp
}

// newDelayProfile creates a namespaced DelayProfile holding torrent grabs back
// by torrentDelayMinutes, with bypassIfHighestQuality explicitly OFF.
//
// That flag defaults to TRUE on the CRD, and grab.Bypasses skips the delay
// entirely for a top-tier release -- which would turn a scenario asserting
// status.pendingGrab into one that silently created a Download instead.
func newDelayProfile(ctx context.Context, t *testing.T, prefix string, torrentDelayMinutes int32) *catalogv1alpha1.DelayProfile {
	t.Helper()
	dp := &catalogv1alpha1.DelayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix), Namespace: Namespace},
		Spec: catalogv1alpha1.DelayProfileSpec{
			EnableTorrent:          ptr.To(true),
			EnableUsenet:           ptr.To(true),
			PreferredProtocol:      catalogv1alpha1.DelayPreferredProtocolTorrent,
			TorrentDelayMinutes:    torrentDelayMinutes,
			UsenetDelayMinutes:     torrentDelayMinutes,
			BypassIfHighestQuality: ptr.To(false),
			// Lowest wins, and a scenario's own profile must beat the
			// catch-all the chart installs at order 1000 if one is present.
			Order: 1,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, dp))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), dp) })
	return dp
}

// newMovie creates a monitored Movie for a fixture-owned TMDB id and
// registers its cleanup.
//
// minimumAvailability is a parameter rather than the CRD's "released" default
// because it decides whether the RSS matcher will even consider the item:
// catalogarr/controller/movie.Availability returns "always available" for
// announced without consulting metadata at all, so a scenario that depends on
// a release being accepted can hold that guarantee independently of whether
// the metadata refresh has landed.
func newMovie(
	ctx context.Context,
	t *testing.T,
	prefix string,
	tmdbID int64,
	qualityProfileRef, rootFolderRef string,
	minAvail catalogv1alpha1.MinimumAvailability,
) *catalogv1alpha1.Movie {
	t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName(prefix), Namespace: Namespace},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID:              tmdbID,
			Monitored:           ptr.To(true),
			MinimumAvailability: minAvail,
			QualityProfileRef:   qualityProfileRef,
			RootFolderRef:       rootFolderRef,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, m))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), m) })
	return m
}

// patchMovieDelayProfile pins a DelayProfile onto a Movie. It is a spec write,
// which is exactly what a user or the UI would do; nothing in this suite ever
// writes a status.
func patchMovieDelayProfile(ctx context.Context, t *testing.T, m *catalogv1alpha1.Movie, delayProfileRef string) {
	t.Helper()
	var live catalogv1alpha1.Movie
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(m), &live))
	patch := client.MergeFrom(live.DeepCopy())
	live.Spec.DelayProfileRef = ptr.To(delayProfileRef)
	require.NoError(t, k8sClient.Patch(ctx, &live, patch))
}

// waitForMovieSettled waits until the metadata gateway has resolved the Movie
// AND the Movie reconciler has published status.available.
//
// wantTitle can only have come from the in-cluster TMDB stub's JSON: it
// appears nowhere on disk, in any CR, or in any other fixture, so asserting it
// is what distinguishes "the gateway reached a verdict" from "the name was
// echoed back from the spec".
func waitForMovieSettled(ctx context.Context, t *testing.T, m *catalogv1alpha1.Movie, wantTitle string) catalogv1alpha1.Movie {
	t.Helper()
	var live catalogv1alpha1.Movie
	waitFor(t, ctx, 3*time.Minute, "Movie "+m.Name+" metadata and availability",
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(m), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			return live.Status.Metadata != nil &&
				isConditionTrue(live.Status.Conditions, catalogv1alpha1.MovieConditionMetadataReady) &&
				live.Status.Available, nil
		}, describeMovie(client.ObjectKeyFromObject(m)))
	require.Equal(t, wantTitle, live.Status.Metadata.Title,
		"status.metadata.title must come from the TMDB stub's fixture JSON, not from anywhere convenient")
	return live
}

// runSearch creates an interactive Search CR scoped to indexerRefs, waits for
// it to reach Completed and returns the finished object. A Search that reaches
// Failed fails the test immediately: the controller has already reached a
// verdict, and its indexerOutcomes carry the reason.
func runSearch(ctx context.Context, t *testing.T, m *catalogv1alpha1.Movie, indexerRefs []string) catalogv1alpha1.Search {
	t.Helper()
	t0 := time.Now()
	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-search"), Namespace: Namespace},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef:    &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name},
			IndexerRefs: indexerRefs,
			Limit:       50,
			TTL:         metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, srch))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), srch) })

	var done catalogv1alpha1.Search
	waitFor(t, ctx, searchCompletedTimeout, "Search "+srch.Name+" Completed",
		func(ctx context.Context) (bool, error) {
			var live catalogv1alpha1.Search
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(srch), &live); err != nil {
				//nolint:nilerr // keep polling
				return false, nil
			}
			if live.Status.Phase == catalogv1alpha1.SearchPhaseFailed {
				return false, fmt.Errorf("Search %s reached Failed; outcomes=%+v conditions=%+v",
					live.Name, live.Status.IndexerOutcomes, live.Status.Conditions)
			}
			done = live
			return live.Status.Phase == catalogv1alpha1.SearchPhaseCompleted, nil
		}, describeSearch(client.ObjectKeyFromObject(srch)), describeTorznabRequests(t0))
	return done
}
