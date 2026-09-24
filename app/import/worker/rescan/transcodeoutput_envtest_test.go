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

package rescan_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// finishedJob creates a TranscodeJob for mediaFile that ended in phase,
// having written output, as squasharr records it.
func finishedJob(t *testing.T, ctx context.Context, f *fixture, name, mediaFile, source, output string,
	phase transcodev1alpha1.TranscodeJobPhase,
) {
	t.Helper()
	require.NoError(t, f.c.Create(ctx, &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: mediaFile, ProfileRef: "hevc-1080p", SourcePath: source},
	}))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerSquasharr, transcodeac.TranscodeJob(name, f.ns).WithStatus(
		transcodeac.TranscodeJobStatus().WithPhase(phase).WithResult(transcodeac.Result().WithOutputPath(output))))
	require.NoError(t, err)
	waitFor(t, 10*time.Second, func() bool {
		var j transcodev1alpha1.TranscodeJob
		return f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &j) == nil && j.Status.Phase == phase
	})
}

// The rename-to-path-update race gap-fix X10 found. A container change
// writes a NEW file, <stem>.<container>, and retires the source;
// replaceSource=false writes "<stem> - <profile>.<container>" beside the
// kept source. Until catalogarr moves the MediaFile's spec.path to the new
// file -- and for good, for a kept source's derived file -- a scan used to
// adopt the output as an unrecorded file: here, a second MediaFile for the
// movie. A file a Succeeded TranscodeJob names is left alone until a
// MediaFile records it; then it is the catalog's like any other. A job that
// did not succeed protects nothing, and squasharr's half-written
// <stem>.part.<ext> is a part.
func TestHandleLeavesTranscodeOutputsToCatalogarr(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-transcode-out", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeIncremental)
	name, source, _ := f.importedFile(t, ctx) // Heat, steady state: recorded, fingerprint current
	f.waitOriginal(t, ctx, name, true)

	dir := filepath.Dir(source)
	stem := filepath.Join(dir, "Heat (1995) [tmdbid-949] - Bluray-1080p")
	containerChange := stem + ".mp4"
	keptBeside := stem + " - hevc-1080p.mkv"
	partial := stem + ".part.mp4"
	failed := filepath.Join(f.root, "Alien (1979) [tmdbid-348]", "Alien (1979) [tmdbid-348].mkv")
	for _, p := range []string{containerChange, keptBeside, partial, failed} {
		mustWriteFile(t, p, sampleFloor)
	}
	finishedJob(t, ctx, f, "heat-to-mp4", name, source, containerChange, transcodev1alpha1.TranscodeJobPhaseSucceeded)
	finishedJob(t, ctx, f, "heat-kept", name, source, keptBeside, transcodev1alpha1.TranscodeJobPhaseSucceeded)
	finishedJob(t, ctx, f, "alien-failed", "alien", failed, failed, transcodev1alpha1.TranscodeJobPhaseFailed)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(2), got.TranscodeOutputs, "both outputs are left to catalogarr")
	assert.Equal(t, int64(1), got.Unchanged, "the recorded source")
	assert.Equal(t, int64(3), got.FilesSkipped)
	assert.Equal(t, int64(1), got.Parts, "the half-written output is a part")
	assert.Equal(t, int64(1), got.FilesMatched, "a failed job's file is scanned like any other")
	assert.Empty(t, got.Unmatched)

	files := mediaFilesIn(t, ctx, f.c, f.ns, 2)
	paths := []string{files[0].Spec.Path, files[1].Spec.Path}
	assert.ElementsMatch(t, []string{source, failed}, paths, "no MediaFile for either transcode output")

	// catalogarr incorporates the container change: spec.path moves to the
	// new file, and the source is retired. The next scan finds the output
	// recorded, and handles it as the transcoded file it now is; the kept
	// source's derived file is still left alone.
	info, err := os.Stat(containerChange)
	require.NoError(t, err)
	_, err = k8s.Apply(ctx, f.c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, f.ns).WithSpec(
		catalogac.MediaFileSpec().WithPath(containerChange).WithSizeBytes(info.Size()).
			WithModTime(metav1.NewTime(info.ModTime())).WithOriginal(false)))
	require.NoError(t, err)
	require.NoError(t, os.Remove(source))
	waitFor(t, 10*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return f.c.List(ctx, &list, client.InNamespace(f.ns),
			client.MatchingFields{rescan.MediaFilePathIndexKey: containerChange}) == nil && len(list.Items) == 1
	})

	next, msg := f.nextScan(t, ctx, "tick-2")
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))
	again := readProgress(t, ctx, f.bus, string(next.UID))
	assert.Equal(t, int64(1), again.Transcoded, "the recorded output is the transcoded file")
	assert.Equal(t, int64(1), again.TranscodeOutputs, "the kept source's derived file")
	mediaFilesIn(t, ctx, f.c, f.ns, 2)
}
