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

package mediafile_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestTranscodedFileBytesChangedOnDisk is the catalogarr side of the
// "a transcoded file's bytes legitimately changed" policy. After a transcode
// swap catalogarr owns spec.sizeBytes/modTime/original and the rescan leaves
// the file alone, so this controller alone must notice a later change:
//
//   - a transcoded file is rechecked every TranscodedRecheckInterval even
//     when nothing wakes it (a file change bumps no generation);
//   - an unchanged file keeps its compliance verdict;
//   - a change no newer TranscodeJob explains is re-probed, its size and
//     mtime re-recorded under catalogarr (never reclaimed by importarr), and
//     the stale verdict -- Compliant, ProfileTag -- dropped, LastResult kept,
//     with a Warning Event.
func TestTranscodedFileBytesChangedOnDisk(t *testing.T) {
	ctx := context.Background()
	c, _ := startEnv(t)
	const ns = "transcoded-changed-ns"
	mustNamespace(t, ctx, c, ns)
	mustTranscodeProfile(t, ctx, c, "hevc-main10", "profile-hash-def")

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	dir := t.TempDir()
	path := writeFile(t, dir, "Heat (1995).mkv", []byte("as imported"))
	require.NoError(t, os.Chtimes(path, base, base))
	importarrCreatesMediaFile(t, ctx, c, ns, "heat-abc1234567", path, int64(len("as imported")), base,
		commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

	rec := events.NewFakeRecorder(32)
	r := &mediafile.Reconciler{Client: c, Recorder: rec, Probe: fakeProbe, Clock: time.Now}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "heat-abc1234567"}}
	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "an untranscoded file is the rescan's to watch, not this timer's")

	// squasharr's swap: new bytes at the same path, a Succeeded job that
	// finished after the first probe (metav1.Time has whole seconds, so wait
	// one out) and before the reconcile that incorporates it.
	time.Sleep(1100 * time.Millisecond)
	encoded := []byte("re-encoded, and longer than before")
	require.NoError(t, os.WriteFile(path, encoded, 0o644))
	require.NoError(t, os.Chtimes(path, base.Add(10*time.Minute), base.Add(10*time.Minute)))
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-abc1234567-enc01", Namespace: ns},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: mf.Name, ProfileRef: "hevc-main10", SourcePath: path, SourceProbeHash: mf.Status.ProbeHash},
	}
	require.NoError(t, c.Create(ctx, tj))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr, transcodeac.TranscodeJob(tj.Name, ns).WithStatus(
		transcodeac.TranscodeJobStatus().
			WithPhase(transcodev1alpha1.TranscodeJobPhaseSucceeded).
			WithFinishedAt(metav1.NewTime(time.Now())).
			WithResult(transcodeac.Result().WithOutputPath(path).WithOutputSizeBytes(int64(len(encoded))))))
	require.NoError(t, err)

	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, mediafile.TranscodedRecheckInterval, res.RequeueAfter, "a transcoded file is rechecked on a timer")
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	require.NotNil(t, mf.Spec.Original)
	require.False(t, *mf.Spec.Original, "setup: the swap was incorporated")
	require.NotNil(t, mf.Status.Transcode)
	require.True(t, mf.Status.Transcode.Compliant)
	require.Equal(t, "hevc-main10@profile-hash-def", mf.Status.Transcode.ProfileTag)
	swappedHash := mf.Status.ProbeHash

	// Nothing changed: the verdict stands and nothing is reported.
	drain(rec)
	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, mediafile.TranscodedRecheckInterval, res.RequeueAfter)
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	assert.True(t, mf.Status.Transcode.Compliant, "an unchanged transcoded file keeps its verdict")
	assert.Equal(t, swappedHash, mf.Status.ProbeHash)
	assert.Empty(t, drain(rec), "an unchanged file reports nothing")

	// The bytes change with no TranscodeJob behind it.
	remuxed := []byte("re-muxed by hand, a different size again")
	require.NoError(t, os.WriteFile(path, remuxed, 0o644))
	require.NoError(t, os.Chtimes(path, base.Add(30*time.Minute), base.Add(30*time.Minute)))
	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, mediafile.TranscodedRecheckInterval, res.RequeueAfter)

	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	assert.NotEqual(t, swappedHash, mf.Status.ProbeHash, "the changed file is re-probed")
	assert.EqualValues(t, len(remuxed), mf.Spec.SizeBytes, "catalogarr re-records the size it owns since the swap")
	assert.True(t, mf.Spec.ModTime.Time.Equal(base.Add(30*time.Minute)), "and the mtime: got %s", mf.Spec.ModTime)
	require.NotNil(t, mf.Spec.Original)
	assert.False(t, *mf.Spec.Original, "the file stays catalogarr's: original never flips back")
	require.NotNil(t, mf.Status.Transcode)
	assert.False(t, mf.Status.Transcode.Compliant, "the old verdict described other bytes")
	assert.Empty(t, mf.Status.Transcode.ProfileTag, "and so did the profile revision it was judged against")
	assert.Equal(t, catalogv1alpha1.TranscodeResultSucceeded, mf.Status.Transcode.LastResult, "the last transcode did happen")

	// The spec fields stayed with catalogarr: importarr's rescan manager
	// claims none of them.
	importarrMain := managedFieldPaths(mf.ManagedFields, rescan.FieldManager.String(), "")
	require.NotNil(t, importarrMain)
	names := specFieldNames(importarrMain)
	assert.False(t, names["sizeBytes"] || names["modTime"] || names["original"],
		"importarr must not own the post-transcode facts: %v", names)

	var warned bool
	for _, e := range drain(rec) {
		if strings.HasPrefix(e, "Warning TranscodedFileChanged") {
			warned = true
		}
	}
	assert.True(t, warned, "an unexplained change of a transcoded file is reported")
}

func drain(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestObservedFingerprintIsImportarrsKey holds the two spellings of the
// hand-over annotation equal: importarr applies rescan's, this controller
// watches its own.
func TestObservedFingerprintIsImportarrsKey(t *testing.T) {
	assert.Equal(t, rescan.AnnotationObservedFingerprint, mediafile.AnnotationObservedFingerprint)
}

// TestObservedFingerprintWakesTheController is the hand-over through a real
// manager: a transcoded file changes on disk, nothing about the MediaFile's
// spec changes, and importarr's observed-fingerprint annotation is what
// makes the controller re-probe it -- without the predicate arm the change
// would wait for the recheck timer.
func TestObservedFingerprintWakesTheController(t *testing.T) {
	_, cfg := startEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// See TestTranscodeJobWatchTriggersReconcile.
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(), events.NewFakeRecorder(64))
	r.Probe = fakeProbe
	require.NoError(t, r.SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c := mgr.GetClient()

	const ns, name = "fingerprint-ns", "heat-abc1234567"
	mustNamespace(t, ctx, c, ns)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	path := writeFile(t, t.TempDir(), "Heat (1995).mkv", []byte("transcoded bytes"))
	require.NoError(t, os.Chtimes(path, base, base))
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, int64(len("transcoded bytes")), base,
		commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

	var got catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got) == nil && got.Status.ProbeHash != ""
	}, 5*time.Second, 50*time.Millisecond, "initial reconcile did not land")
	// Stand in for an incorporated swap: catalogarr owns original=false.
	_, err = k8s.Apply(ctx, c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().WithSizeBytes(int64(len("transcoded bytes"))).WithModTime(metav1.NewTime(base)).WithOriginal(false)))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got) == nil &&
			got.Spec.Original != nil && !*got.Spec.Original
	}, 5*time.Second, 50*time.Millisecond)
	hash := got.Status.ProbeHash

	changed := []byte("changed on disk, and longer too")
	require.NoError(t, os.WriteFile(path, changed, 0o644))
	later := base.Add(20 * time.Minute)
	require.NoError(t, os.Chtimes(path, later, later))
	require.Never(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got) == nil && got.Status.ProbeHash != hash
	}, time.Second, 100*time.Millisecond, "setup: nothing should wake the controller for a bare disk change")

	// importarr's hand-over, under its own manager.
	_, err = k8s.Apply(ctx, c, k8s.ManagerImportarr, catalogac.MediaFile(name, ns).WithAnnotations(map[string]string{
		rescan.AnnotationObservedFingerprint: fmt.Sprintf("%d@%s", len(changed), later.UTC().Format(time.RFC3339)),
	}))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got) == nil &&
			got.Status.ProbeHash != hash && got.Spec.SizeBytes == int64(len(changed))
	}, 5*time.Second, 50*time.Millisecond, "the observed-fingerprint annotation must wake the re-probe")
}

// transcodeSteadyState creates a probed, untranscoded MediaFile at
// dir/name (importarr's apply plus one reconcile), the steady state a
// transcode lands on.
func transcodeSteadyState(t *testing.T, ctx context.Context, c client.Client, r *mediafile.Reconciler, ns, mfName, path string) ctrl.Request {
	t.Helper()
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, base, base))
	info, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, mfName, path, info.Size(), base,
		commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: mfName}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	require.NotEmpty(t, mf.Status.ProbeHash, "setup: the steady state is probed")
	return req
}

// succeededJob records a Succeeded TranscodeJob for mfName whose output is
// outputPath, finished after the steady state's probe.
func succeededJob(t *testing.T, ctx context.Context, c client.Client, ns, mfName, source, outputPath string, size int64) {
	t.Helper()
	time.Sleep(1100 * time.Millisecond) // metav1.Time has whole seconds: finish after the probe
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: mfName + "-enc01", Namespace: ns},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: mfName, ProfileRef: "hevc-main10", SourcePath: source},
	}
	require.NoError(t, c.Create(ctx, tj))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr, transcodeac.TranscodeJob(tj.Name, ns).WithStatus(
		transcodeac.TranscodeJobStatus().
			WithPhase(transcodev1alpha1.TranscodeJobPhaseSucceeded).
			WithFinishedAt(metav1.NewTime(time.Now())).
			WithResult(transcodeac.Result().WithOutputPath(outputPath).WithOutputSizeBytes(size))))
	require.NoError(t, err)
}

// TestContainerChangeMovesSpecPath is gap-fix R-11's catalogarr half, the
// replaceSource=true case: the encode changes container, so squasharr's
// worker writes "<stem>.<container>" under a new name and retires the
// source. Incorporating that swap must move spec.path to the output --
// taking the field over from importarr, which managedFields must show --
// or the MediaFile names a file that no longer exists.
func TestContainerChangeMovesSpecPath(t *testing.T) {
	ctx := context.Background()
	c, _ := startEnv(t)
	const ns = "container-change-ns"
	mustNamespace(t, ctx, c, ns)
	mustTranscodeProfile(t, ctx, c, "hevc-main10", "profile-hash-def")
	dir := t.TempDir()
	source := writeFile(t, dir, "Heat (1995).avi", []byte("an avi as imported"))

	r := &mediafile.Reconciler{Client: c, Recorder: events.NewFakeRecorder(64), Probe: fakeProbe, Clock: time.Now}
	req := transcodeSteadyState(t, ctx, c, r, ns, "heat-abc1234567", source)

	// squasharr's worker: place the output under its new name, retire the
	// source, then report Succeeded.
	output := strings.TrimSuffix(source, ".avi") + ".mkv"
	encoded := []byte("a matroska encode, not the same size")
	require.NoError(t, os.WriteFile(output, encoded, 0o644))
	require.NoError(t, os.Remove(source))
	succeededJob(t, ctx, c, ns, "heat-abc1234567", source, output, int64(len(encoded)))

	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, mediafile.TranscodedRecheckInterval, res.RequeueAfter)

	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	assert.Equal(t, output, mf.Spec.Path, "the MediaFile must follow the file to its new name")
	assert.EqualValues(t, len(encoded), mf.Spec.SizeBytes)
	require.NotNil(t, mf.Spec.Original)
	assert.False(t, *mf.Spec.Original)
	require.NotNil(t, mf.Status.Transcode)
	assert.True(t, mf.Status.Transcode.Compliant)
	assert.Equal(t, "hevc-main10@profile-hash-def", mf.Status.Transcode.ProfileTag)
	assert.True(t, k8s.IsConditionTrue(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady), "the new path is present and probed")

	catalogarrSpec := specFieldNames(managedFieldPaths(mf.ManagedFields, "catalogarr", ""))
	assert.True(t, catalogarrSpec["path"], "catalogarr must own spec.path after the swap: %v", catalogarrSpec)
	importarrSpec := specFieldNames(managedFieldPaths(mf.ManagedFields, rescan.FieldManager.String(), ""))
	assert.False(t, importarrSpec["path"], "importarr must have released spec.path to catalogarr: %v", importarrSpec)
	assert.True(t, importarrSpec["quality"], "and kept everything it froze at import")

	// The next reconcile finds the file where the MediaFile now says it is.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	assert.Equal(t, output, mf.Spec.Path)
	assert.True(t, k8s.IsConditionTrue(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady))
}

// TestReplaceSourceFalseIsNotASwap is R-11's other case: replaceSource=false
// writes "<stem> - <profile>.<container>" beside a source that is kept.
// The MediaFile is the source, untouched: not original=false, not
// compliant, spec.path unchanged and never claimed by catalogarr. The
// profile revision is recorded so squasharr does not plan the same copy
// again. Later deleting the source reads as FileMissing, never as adopting
// the copy.
func TestReplaceSourceFalseIsNotASwap(t *testing.T) {
	ctx := context.Background()
	c, _ := startEnv(t)
	const ns = "replace-false-ns"
	mustNamespace(t, ctx, c, ns)
	mustTranscodeProfile(t, ctx, c, "hevc-main10", "profile-hash-def")
	dir := t.TempDir()
	source := writeFile(t, dir, "Heat (1995).mkv", []byte("the source, kept"))

	rec := events.NewFakeRecorder(64)
	r := &mediafile.Reconciler{Client: c, Recorder: rec, Probe: fakeProbe, Clock: time.Now}
	req := transcodeSteadyState(t, ctx, c, r, ns, "heat-abc1234567", source)
	var before catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

	derived := strings.TrimSuffix(source, ".mkv") + " - hevc-main10.mkv"
	require.NoError(t, os.WriteFile(derived, []byte("a derived encode"), 0o644))
	succeededJob(t, ctx, c, ns, "heat-abc1234567", source, derived, int64(len("a derived encode")))
	drain(rec)

	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "the MediaFile is still an untranscoded original")

	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	assert.Equal(t, source, mf.Spec.Path, "the kept source stays the MediaFile's file")
	assert.Equal(t, before.Spec.SizeBytes, mf.Spec.SizeBytes)
	assert.True(t, mf.Spec.Original == nil || *mf.Spec.Original, "the source was not transcoded")
	require.NotNil(t, mf.Status.Transcode)
	assert.False(t, mf.Status.Transcode.Compliant, "the file at spec.path is not the encode")
	assert.Equal(t, "hevc-main10@profile-hash-def", mf.Status.Transcode.ProfileTag, "recorded, so squasharr does not plan the copy again")
	assert.Equal(t, catalogv1alpha1.TranscodeResultSucceeded, mf.Status.Transcode.LastResult)
	assert.False(t, claimsSpecField(managedFieldPaths(mf.ManagedFields, "catalogarr", "")),
		"catalogarr claims no spec field for a kept source")
	var reported bool
	for _, e := range drain(rec) {
		if strings.HasPrefix(e, "Normal TranscodeKept") {
			reported = true
		}
	}
	assert.True(t, reported, "the kept copy is reported")

	// Deleting the kept source later is a missing file, not an adoption.
	require.NoError(t, os.Remove(source))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	assert.Equal(t, source, mf.Spec.Path, "the derived copy is never adopted")
	cond := k8s.FindCondition(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, "FileMissing", cond.Reason)
}
