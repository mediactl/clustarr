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

package probe_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// steadyFile probes f (the loop's first pass, an answer, the incorporating
// pass) and returns the pass result that incorporated it.
func steadyFile(t *testing.T, h *probeHarness, r *remediation.Reconciler, f queuedFile) catalogv1alpha1.MediaFile {
	t.Helper()
	reconcileOnce(t, r, f.key)
	h.Answer(t, f.uid(t), fakeMediaInfo())
	reconcileOnce(t, r, f.key)
	steady := f.get(t)
	require.NotEmpty(t, steady.Status.ProbeHash, "setup: the steady state is probed")
	return steady
}

// incorporateSwap is a transcode's swap through the loop: the transcode
// planner's Swapping block over output, the probe the probe planner asks for
// it answered with the encode's tag, the incorporating pass -- whose result
// it returns -- and the transcode planner ending the block Succeeded (its
// planSwap row: compliant, the tag, the result cleared).
func incorporateSwap(t *testing.T, h *probeHarness, r *remediation.Reconciler, f queuedFile, clk *clock, output string, size int64) (time.Duration, catalogv1alpha1.MediaFile) {
	t.Helper()
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Transcode = swapping(output, size, clk.Now().Add(time.Second))
	})
	clk.Add(2 * time.Second)
	reconcileOnce(t, r, f.key)
	h.Answer(t, f.uid(t), encodeOf(output))
	res := reconcileOnce(t, r, f.key)
	got := f.get(t)
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Transcode.Phase, s.Transcode.Compliant, s.Transcode.ProfileTag = catalogv1alpha1.TranscodePhaseSucceeded, true, transcodeTag
		s.Transcode.Result, s.Transcode.ProbeHash = nil, got.Status.ProbeHash
	})
	return res.RequeueAfter, got
}

// TestTranscodedFileBytesChangedOnDisk is catalog's side of "a transcoded
// file's bytes legitimately changed": after a swap catalog owns
// spec.sizeBytes, modTime and original and the rescan leaves the file alone,
// so the loop alone must notice a later change: a transcoded file is
// rechecked every TranscodedRecheckInterval even when nothing wakes it, an
// unchanged file reports nothing, and a change no transcode explains is
// re-probed, its size and mtime re-recorded under catalog (never reclaimed by
// the import manager), with a Warning Event. (The verdict the old bytes
// earned is the transcode planner's to drop: transcodeplan's
// TestNewBytesOnASucceededFileDropItsVerdict.)
func TestTranscodedFileBytesChangedOnDisk(t *testing.T) {
	f := newQueuedFileAt(t, "transcoded-changed", "Heat (1995).mkv", []byte("as imported"), bluray1080p)
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	reconcileOnce(t, r, f.key)
	h.Answer(t, f.uid(t), fakeMediaInfo())
	res := reconcileOnce(t, r, f.key)
	assert.Zero(t, res.RequeueAfter, "an untranscoded file is the rescan's to watch, not this timer's")

	// The transcode's swap: new bytes at the same path.
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	encoded := []byte("re-encoded, and longer than before")
	require.NoError(t, os.WriteFile(f.path, encoded, 0o644))
	require.NoError(t, os.Chtimes(f.path, base.Add(10*time.Minute), base.Add(10*time.Minute)))
	after, mf := incorporateSwap(t, h, r, f, clk, f.path, int64(len(encoded)))
	assert.Equal(t, mediafile.TranscodedRecheckInterval, after, "a transcoded file is rechecked on a timer")
	require.NotNil(t, mf.Spec.Original)
	require.False(t, *mf.Spec.Original, "setup: the swap was incorporated")
	swappedHash := mf.Status.ProbeHash

	// Nothing changed: nothing is asked and nothing reported.
	remediationtest.Drain(f.e.Recorder)
	sent := len(h.Sent())
	clk.Add(time.Minute)
	res = reconcileOnce(t, r, f.key)
	assert.Equal(t, mediafile.TranscodedRecheckInterval, res.RequeueAfter)
	assert.Equal(t, swappedHash, f.get(t).Status.ProbeHash)
	assert.Len(t, h.Sent(), sent)
	assert.Empty(t, remediationtest.Drain(f.e.Recorder), "an unchanged file reports nothing")

	// The bytes change with no transcode behind them.
	remuxed := []byte("re-muxed by hand, a different size again")
	require.NoError(t, os.WriteFile(f.path, remuxed, 0o644))
	require.NoError(t, os.Chtimes(f.path, base.Add(30*time.Minute), base.Add(30*time.Minute)))
	clk.Add(time.Minute)
	reconcileOnce(t, r, f.key)
	assert.Len(t, h.Sent(), sent+1, "the changed file is probed again")
	h.Answer(t, f.uid(t), encodeOf(f.path))
	res = reconcileOnce(t, r, f.key)
	assert.Equal(t, mediafile.TranscodedRecheckInterval, res.RequeueAfter)

	mf = f.get(t)
	assert.NotEqual(t, swappedHash, mf.Status.ProbeHash, "the changed file is re-probed")
	assert.EqualValues(t, len(remuxed), mf.Spec.SizeBytes, "catalog re-records the size it owns since the swap")
	assert.True(t, mf.Spec.ModTime.Time.Equal(base.Add(30*time.Minute)), "and the mtime: got %s", mf.Spec.ModTime)
	require.NotNil(t, mf.Spec.Original)
	assert.False(t, *mf.Spec.Original, "the file stays catalog's: original never flips back")

	// The spec fields stayed with catalog: the import manager claims none of them.
	names := specFields(mf, importManager)
	assert.False(t, names["sizeBytes"] || names["modTime"] || names["original"],
		"the import manager must not own the post-transcode facts: %v", names)
	assert.True(t, specFields(mf, catalogManager)["sizeBytes"])

	var warned bool
	for _, e := range remediationtest.Drain(f.e.Recorder) {
		if strings.HasPrefix(e, "Warning TranscodedFileChanged") {
			warned = true
		}
	}
	assert.True(t, warned, "an unexplained change of a transcoded file is reported")
}

// TestObservedFingerprintIsImportsKey holds the hand-over annotation's
// spellings equal: the import domain applies mediafilespec's (the rescan
// names the same constant), and the loop wakes on mediafile's (S1).
func TestObservedFingerprintIsImportsKey(t *testing.T) {
	assert.Equal(t, mediafilespec.AnnotationObservedFingerprint, mediafile.AnnotationObservedFingerprint)
	assert.Equal(t, rescan.AnnotationObservedFingerprint, mediafile.AnnotationObservedFingerprint)
}

// TestObservedFingerprintWakesTheController is the hand-over through a real
// manager running the loop: a transcoded file changes on disk, nothing about
// the MediaFile's spec changes, and the import domain's observed-fingerprint
// annotation is what wakes the loop to re-probe it -- without S1's annotation
// arm the change would wait for the recheck timer.
func TestObservedFingerprintWakesTheController(t *testing.T) {
	h := newProbeHarness(t).WithAgent(t, fakeProber)
	e := remediationtest.StartControllerOn(t, h.Pub, h.Probe())
	ctx := context.Background()
	const ns, name = "fingerprint", "heat-abc1234567"
	mustNamespace(t, ctx, e.Admin, ns)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	path := writeFile(t, t.TempDir(), "Heat (1995).mkv", []byte("transcoded bytes"))
	require.NoError(t, os.Chtimes(path, base, base))
	f := queuedFile{e: e, c: e.Admin, key: types.NamespacedName{Namespace: ns, Name: name}, path: path}
	importCreates(t, e.Admin, ns, name, movieRef("heat"), path, int64(len("transcoded bytes")), base, bluray1080p)

	require.Eventually(t, func() bool { got := f.get(t); return got.ProbeCurrent() }, 20*time.Second, 50*time.Millisecond,
		"the initial pass never probed the file")
	// Stand in for an incorporated swap: catalog owns original=false.
	_, err := k8s.Apply(ctx, e.Admin, k8s.ManagerCatalog, catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().WithPath(path).WithSizeBytes(int64(len("transcoded bytes"))).WithModTime(metav1.NewTime(base)).WithOriginal(false)))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got := f.get(t)
		return got.Spec.Original != nil && !*got.Spec.Original && got.Status.ObservedGeneration == got.Generation
	}, 20*time.Second, 50*time.Millisecond)
	hash := f.get(t).Status.ProbeHash

	changed := []byte("changed on disk, and longer too")
	require.NoError(t, os.WriteFile(path, changed, 0o644))
	later := base.Add(20 * time.Minute)
	require.NoError(t, os.Chtimes(path, later, later))
	require.Never(t, func() bool { return f.get(t).Status.ProbeHash != hash }, 2*time.Second, 100*time.Millisecond,
		"setup: nothing should wake the loop for a bare disk change")

	// The import domain's hand-over, under its own manager.
	_, err = k8s.Apply(ctx, e.Admin, k8s.ManagerImport, catalogac.MediaFile(name, ns).WithAnnotations(map[string]string{
		mediafilespec.AnnotationObservedFingerprint: mediafilespec.ObservedFingerprint(int64(len(changed)), later),
	}))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got := f.get(t)
		return got.Status.ProbeHash != hash && got.Spec.SizeBytes == int64(len(changed))
	}, 20*time.Second, 50*time.Millisecond, "the observed-fingerprint annotation must wake the re-probe")
}

// TestContainerChangeMovesSpecPath is R-11's catalog half: the encode
// changes container, so the worker writes "<stem>.<container>" under a new
// name and retires the source. Incorporating that swap moves spec.path to
// the output -- taking the field over from the import manager, which
// managedFields must show -- or the MediaFile names a file that no longer
// exists.
func TestContainerChangeMovesSpecPath(t *testing.T) {
	f := newQueuedFileAt(t, "container-change", "Heat (1995).avi", []byte("an avi as imported"), bluray1080p)
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	steadyFile(t, h, r, f)

	output := strings.TrimSuffix(f.path, ".avi") + ".mkv"
	encoded := []byte("a matroska encode, not the same size")
	require.NoError(t, os.WriteFile(output, encoded, 0o644))
	require.NoError(t, os.Remove(f.path))
	after, mf := incorporateSwap(t, h, r, f, clk, output, int64(len(encoded)))
	assert.Equal(t, mediafile.TranscodedRecheckInterval, after)

	assert.Equal(t, output, mf.Spec.Path, "the MediaFile must follow the file to its new name")
	assert.EqualValues(t, len(encoded), mf.Spec.SizeBytes)
	require.NotNil(t, mf.Spec.Original)
	assert.False(t, *mf.Spec.Original)
	assert.Equal(t, "mkv", mf.Status.MediaInfo.Container)
	assert.True(t, k8s.IsConditionTrue(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady), "the new path is present and probed")

	catalogSpec := specFields(mf, catalogManager)
	assert.True(t, catalogSpec["path"], "catalog must own spec.path after the swap: %v", catalogSpec)
	importSpec := specFields(mf, importManager)
	assert.False(t, importSpec["path"], "the import manager must have released spec.path to catalog: %v", importSpec)
	assert.True(t, importSpec["quality"], "and kept everything it froze at import")

	// The next pass finds the file where the MediaFile now says it is.
	clk.Add(time.Minute)
	reconcileOnce(t, r, f.key)
	mf = f.get(t)
	assert.Equal(t, output, mf.Spec.Path)
	assert.True(t, k8s.IsConditionTrue(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady))
	assert.True(t, mf.ProbeCurrent())
}

// TestReplaceSourceFalseIsNotASwap is R-11's other case: a profile that keeps
// the source writes "<stem> - <profile>.<container>" beside it, and the
// transcode planner records the copy -- Succeeded, the tag, never Swapping
// (transcodeplan's kept rule). The MediaFile is the source, untouched: not
// original=false, spec.path unchanged and never claimed by catalog. Later
// deleting the source reads as FileMissing, never as adopting the copy.
func TestReplaceSourceFalseIsNotASwap(t *testing.T) {
	f := newQueuedFileAt(t, "replace-false", "Heat (1995).mkv", []byte("the source, kept"), bluray1080p)
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	before := steadyFile(t, h, r, f)

	derived := strings.TrimSuffix(f.path, ".mkv") + " - hevc-main10.mp4"
	require.NoError(t, os.WriteFile(derived, []byte("a derived encode"), 0o644))
	kept := swapping(derived, int64(len("a derived encode")), clk.Now().Add(time.Second))
	kept.Phase, kept.ProfileTag = catalogv1alpha1.TranscodePhaseSucceeded, transcodeTag
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) { s.Transcode = kept.DeepCopy() })
	// Past the loop's applied-status memo (30 s): this test writes the
	// transcode planner's block from outside the loop.
	clk.Add(31 * time.Second)
	sent := len(h.Sent())

	res := reconcileOnce(t, r, f.key)
	assert.Zero(t, res.RequeueAfter, "the MediaFile is still an untranscoded original")
	mf := f.get(t)
	assert.Equal(t, f.path, mf.Spec.Path, "the kept source stays the MediaFile's file")
	assert.Equal(t, before.Spec.SizeBytes, mf.Spec.SizeBytes)
	assert.True(t, mf.Spec.Original == nil || *mf.Spec.Original, "the source was not transcoded")
	assert.Equal(t, before.Status.ProbeHash, mf.Status.ProbeHash)
	assert.Len(t, h.Sent(), sent, "a kept copy is not a swap: nothing is probed")
	assert.True(t, equality.Semantic.DeepEqual(kept, mf.Status.Transcode), "the probe planner never writes status.transcode")
	assert.Empty(t, specFields(mf, catalogManager), "catalog claims no spec field for a kept source")

	// Deleting the kept source later is a missing file, not an adoption.
	require.NoError(t, os.Remove(f.path))
	clk.Add(time.Minute)
	reconcileOnce(t, r, f.key)
	mf = f.get(t)
	assert.Equal(t, f.path, mf.Spec.Path, "the derived copy is never adopted")
	assert.Equal(t, "FileMissing", reasonOf(mf, catalogv1alpha1.MediaFileConditionReady))
}
