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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/mediafile"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
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
