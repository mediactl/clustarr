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

package fileimport_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// shortFilm is the release file every case below plants: a movie whose
// release parses cleanly and whose quality the fixture's profile allows, at
// a real short film's 45 MiB -- under fsops.DefaultSampleMaxBytes, so only
// the size floor suspects it.
const (
	shortFilm      = "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"
	shortFilmBytes = 45 << 20
)

// plantShortFilmRelease plants shortFilm beside the release's own
// name-marked sample and returns the content root.
func plantShortFilmRelease(t *testing.T) string {
	t.Helper()
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, shortFilm), shortFilmBytes)
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Sample", "sparks-matrix-1080p-sample.mkv"), 10<<20)
	return contentRoot
}

// A file only the size floor suspects is never passed over in silence: it
// is a rejection on status.import naming its size, the threshold, and the
// manual import that takes it, and with nothing imported the Download is
// Blocked where a person sees it. The name-marked sample beside it is the
// release's own packaging and is passed over without a rejection.
func TestHandleRejectsASuspectedSampleAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-suspect")
	require.Equal(t, fsops.DefaultSampleMaxBytes, f.worker.SampleMaxBytes, "NewWorker keeps the 50 MiB default")

	dl := f.createDownload(t, "suspect-dl", plantShortFilmRelease(t),
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	got := f.importState(t, dl).Status.Import
	assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State)
	assert.Empty(t, got.Imported)
	require.Len(t, got.Rejections, 1, "one rejection, for the film; the name-marked sample is not one: %v", got.Rejections)
	rejection := got.Rejections[0]
	assert.Contains(t, rejection, shortFilm+": suspected sample")
	assert.Contains(t, rejection, "45.0 MiB (47185920 bytes)", "the rejection names the file's size")
	assert.Contains(t, rejection, "50.0 MiB (52428800 bytes)", "and the threshold it fell under")
	assert.Contains(t, rejection, fileimport.AnnotationImportOverride+"=true", "and the manual import that takes it")

	var files catalogv1alpha1.MediaFileList
	require.NoError(t, f.api.List(ctx, &files, client.InNamespace(f.ns)))
	assert.Empty(t, files.Items, "a size-suspected file is never imported speculatively")
}

// status.import.rejections carries MaxItems=200, and an apply over it is
// rejected WHOLE: before G4-0 a pack with more than 200 rejected files
// recorded its outcome nowhere, and Handle failed on every redelivery. The
// list must land capped, its last entry counting what did not fit.
func TestHandleCapsRejectionsAtTheCRDsMaxItems(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-many-rejections")

	const planted = 230
	contentRoot := dataDir(t, "scratch")
	for i := range planted {
		mustWriteSparseFile(t, filepath.Join(contentRoot, fmt.Sprintf("The.Matrix.1999.1080p.BluRay.x264-SPARKS.part%03d.mkv", i)), shortFilmBytes)
	}
	dl := f.createDownload(t, "many-rejections-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")),
		"a status.import over its MaxItems is rejected by the apiserver, and Handle fails")

	got := f.importState(t, dl).Status.Import
	assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State)
	require.Len(t, got.Rejections, 200)
	assert.Contains(t, got.Rejections[0], "suspected sample")
	assert.Equal(t, fmt.Sprintf("... and %d more rejections not listed", planted-199), got.Rejections[199])
}

// The same release imports under a manual import -- a person's instruction
// to import this download's files, as it accepts an undeterminable non-video
// quality -- and under a zero threshold, which turns the size floor off. In
// both, the name-marked sample stays behind without a rejection.
func TestHandleImportsASuspectedSampleWhenManualOrTheRuleIsOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, f *fixture, dl *downloadv1alpha1.Download)
		manual bool
	}{
		{
			name: "fi-suspect-override",
			setup: func(t *testing.T, f *fixture, dl *downloadv1alpha1.Download) {
				f.setAnnotation(t, dl, fileimport.AnnotationImportOverride, "true")
			},
			manual: true,
		},
		{
			name:  "fi-suspect-off",
			setup: func(_ *testing.T, f *fixture, _ *downloadv1alpha1.Download) { f.worker.SampleMaxBytes = 0 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, tc.name)
			dl := f.createDownload(t, "suspect-dl", plantShortFilmRelease(t),
				commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
			tc.setup(t, f, dl)
			require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

			got := f.importState(t, dl).Status.Import
			require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
			assert.Empty(t, got.Rejections, "the name-marked sample is not a rejection")
			require.Len(t, got.Imported, 1)
			assert.Equal(t, shortFilm, got.Imported[0].SourcePath)

			var mf catalogv1alpha1.MediaFile
			require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: got.Imported[0].MediaFileRef}, &mf))
			assert.Equal(t, int64(shortFilmBytes), mf.Spec.SizeBytes)
			require.NotNil(t, mf.Spec.ImportedFrom)
			assert.Equal(t, tc.manual, mf.Spec.ImportedFrom.Manual)
		})
	}
}
