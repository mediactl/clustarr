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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/test/fixtures/seed"
)

// fixtureDirName is the directory under $CLUSTARR_DATA_DIR that hack/e2e.sh
// seeds the probe clip into. It is deliberately dot-prefixed so it sits
// beside, not inside, the media roots a scenario walks.
const fixtureDirName = ".e2e-fixtures"

// pollInterval is how often every scenario-level waitFor re-checks. It is
// slower than a controller's own requeue on purpose: the suite is measuring
// convergence, not latency.
const pollInterval = 2 * time.Second

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
	t.Cleanup(func() { _ = os.Remove(hostAbsPath) })
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
	t.Cleanup(func() { _ = os.Remove(hostAbsPath) })
}

// waitFor polls check every pollInterval until it returns true, or fails the
// test with desc after timeout. Controllers here use RequeueAfter, never
// sleep (CLAUDE.md); this is the test-side mirror of that patience.
func waitFor(t *testing.T, ctx context.Context, timeout time.Duration, desc string, check func(context.Context) (bool, error)) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, check); err != nil {
		t.Fatalf("timed out waiting for %s: %v", desc, err)
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
	if err := json.Unmarshal(entry.FieldsV1.Raw, &raw); err != nil {
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
	t.Cleanup(func() {
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
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), scan) })
	return waitForScanCompleted(ctx, t, client.ObjectKeyFromObject(scan))
}

// waitForScanCompleted polls one LibraryScan until it reports Completed and
// returns it.
func waitForScanCompleted(ctx context.Context, t *testing.T, key client.ObjectKey) catalogv1alpha1.LibraryScan {
	t.Helper()
	var done catalogv1alpha1.LibraryScan
	waitFor(t, ctx, 5*time.Minute, "LibraryScan "+key.Name+" Completed", func(ctx context.Context) (bool, error) {
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
	})
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
