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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// grabbedAs is a createDownloadWith spec edit: what caused the grab, and
// whether it is a manual one.
func grabbedAs(by downloadv1alpha1.GrabSource, manual bool) func(*downloadv1alpha1.DownloadSpec) {
	return func(s *downloadv1alpha1.DownloadSpec) {
		s.GrabbedBy = by
		s.Manual = manual
	}
}

// importGrab creates a completed Download of contentRoot for target, grabbed
// as edit says, runs the import, and returns its state.
func (f *fixture) importGrab(
	t *testing.T, name, contentRoot string, target commonv1.MediaRef, annotations map[string]string,
	edit func(*downloadv1alpha1.DownloadSpec),
) *downloadv1alpha1.ImportState {
	t.Helper()
	dl := f.createDownloadWith(t, name, contentRoot, target, "", annotations, edit)
	require.NoError(t, f.worker.Handle(context.Background(), newImportTaskMessage(t, f.ns, dl.Name, "")))
	return f.importState(t, dl).Status.Import
}

// markTranscoded makes the MediaFile name transcoded the two ways catalogarr
// does, each under catalogarr's own manager: by swap, it incorporates a
// transcode swap and takes spec.original over as false; otherwise its probe
// reads the file's CLUSTARR_PROFILE tag into status.mediaInfo. It waits for
// the cache the worker reads existing files through to see the verdict.
func (f *fixture) markTranscoded(t *testing.T, name string, swap bool) {
	t.Helper()
	ctx := context.Background()
	var err error
	if swap {
		_, err = k8s.Apply(ctx, f.c, k8s.ManagerCatalogarr,
			catalogac.MediaFile(name, f.ns).WithSpec(catalogac.MediaFileSpec().WithOriginal(false)))
	} else {
		_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr,
			catalogac.MediaFile(name, f.ns).WithStatus(catalogac.MediaFileStatus().
				WithProbeHash("probe-1").
				WithMediaInfo(commonv1.MediaInfo{
					Container: "matroska", VideoCodec: "hevc",
					TranscodeProfile: "hevc-main10@0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				})))
	}
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var mf catalogv1alpha1.MediaFile
		return f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &mf) == nil && mf.Transcoded()
	})
}

// withRecycleBin gives the fixture's movie root folder its own recycle bin,
// so a replaced movie file lands in the test's directory rather than the
// CRD default under /data.
func (f *fixture) withRecycleBin(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	bin := dataDir(t, "recycle")
	var rf catalogv1alpha1.RootFolder
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(f.rootFolder), &rf))
	patch := client.MergeFrom(rf.DeepCopy())
	rf.Spec.RecycleBin.Path = bin
	require.NoError(t, f.c.Patch(ctx, &rf, patch))
	waitFor(t, 5*time.Second, func() bool {
		var got catalogv1alpha1.RootFolder
		return f.c.Get(ctx, client.ObjectKeyFromObject(f.rootFolder), &got) == nil && got.Spec.RecycleBin.Path == bin
	})
	return bin
}

// A transcoded file is final (CLAUDE.md, "Transcoding"): an automatic grab
// that was already in flight when the transcode landed must not recycle it
// and put the source-quality release back. Its file is a rejection on
// status.import naming why, and the transcoded file and its MediaFile stay.
// A person's choice -- an interactive grab, spec.manual, or the
// import-override annotation -- replaces it like any other file.
//
// The candidate is a PROPER of the imported release, which the upgrade
// comparison approves (the control row: the same automatic grab over an
// untranscoded file replaces it), so a rejection can only be the gate's.
func TestHandleNeverLetsAnAutomaticGrabReplaceATranscodedMovie(t *testing.T) {
	cases := []struct {
		name        string
		transcoded  bool
		swap        bool
		edit        func(*downloadv1alpha1.DownloadSpec)
		annotations map[string]string
		// replaced: the import brings the PROPER in and the old file goes.
		replaced bool
		source   string
	}{
		{name: "control: a search grab over an untranscoded file", edit: grabbedAs(downloadv1alpha1.GrabSourceSearch, false), replaced: true},
		{name: "a search grab over a tagged file", transcoded: true, edit: grabbedAs(downloadv1alpha1.GrabSourceSearch, false), source: "search"},
		{name: "an RSS grab over a swapped file", transcoded: true, swap: true, edit: grabbedAs(downloadv1alpha1.GrabSourceRSS, false), source: "rss"},
		{name: "a redownload over a tagged file", transcoded: true, edit: grabbedAs(downloadv1alpha1.GrabSourceRedownload, false), source: "redownload"},
		{name: "a grab that recorded no source", transcoded: true, swap: true, edit: grabbedAs("", false), source: "unrecorded"},
		{name: "an interactive grab", transcoded: true, edit: grabbedAs(downloadv1alpha1.GrabSourceInteractive, false), replaced: true},
		{name: "spec.manual", transcoded: true, swap: true, edit: grabbedAs(downloadv1alpha1.GrabSourceSearch, true), replaced: true},
		{
			name: "the import-override annotation", transcoded: true, edit: grabbedAs(downloadv1alpha1.GrabSourceRSS, false),
			annotations: map[string]string{fileimport.AnnotationImportOverride: "true"}, replaced: true,
		},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, fmt.Sprintf("fi-transcoded-movie-%d", i))
			bin := f.withRecycleBin(t)
			movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}

			first := dataDir(t, "scratch")
			mustWriteSparseFile(t, filepath.Join(first, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
			got := f.importGrab(t, "first-dl", first, movie, nil, grabbedAs(downloadv1alpha1.GrabSourceSearch, false))
			require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
			require.Len(t, got.Imported, 1)
			existing := got.Imported[0].MediaFileRef
			existingPath := got.Imported[0].DestPath
			waitFor(t, 5*time.Second, func() bool {
				return f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: existing}, &catalogv1alpha1.MediaFile{}) == nil
			})
			if c.transcoded {
				f.markTranscoded(t, existing, c.swap)
			}

			proper := dataDir(t, "scratch")
			mustWriteSparseFile(t, filepath.Join(proper, "The.Matrix.1999.PROPER.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
			got = f.importGrab(t, "proper-dl", proper, movie, c.annotations, c.edit)

			var old catalogv1alpha1.MediaFile
			oldErr := f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: existing}, &old)
			_, statErr := os.Stat(existingPath)
			binned, err := os.ReadDir(bin)
			require.NoError(t, err)

			if c.replaced {
				require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
				assert.Empty(t, got.Rejections)
				require.Len(t, got.Imported, 1)
				assert.NotEqual(t, existing, got.Imported[0].MediaFileRef)
				assert.True(t, apierrors.IsNotFound(oldErr), "the replaced file's MediaFile is deleted: %v", oldErr)
				if existingPath != got.Imported[0].DestPath {
					assert.ErrorIs(t, statErr, os.ErrNotExist, "the replaced file left the library")
				}
				assert.NotEmpty(t, binned, "the replaced file went to the recycle bin")
				return
			}

			require.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State, "message %q, rejections %v", got.Message, got.Rejections)
			assert.Equal(t, downloadv1alpha1.ImportMessageEveryFileRejected, got.Message)
			assert.Empty(t, got.Imported)
			require.Len(t, got.Rejections, 1, "one reason for the one file, never a silent skip")
			r := got.Rejections[0]
			assert.Contains(t, r, "The.Matrix.1999.PROPER.1080p.BluRay.x264-SPARKS.mkv: ")
			assert.Contains(t, r, "movie the-matrix's existing file (MediaFile "+existing+") is transcoded")
			assert.Contains(t, r, "grabbedBy "+c.source)
			assert.Contains(t, r, "only an interactive grab or a manual import")
			assert.Contains(t, r, fileimport.AnnotationImportOverride+"=true")

			require.NoError(t, oldErr, "the transcoded file's MediaFile stays")
			assert.Equal(t, existingPath, old.Spec.Path)
			require.NoError(t, statErr, "the transcoded file stays in the library")
			assert.Empty(t, binned, "nothing was recycled")
			assert.Len(t, f.mediaFilesOf(t), 1, "no MediaFile for the rejected file")
		})
	}
}

// The episode import applies the same gate per file: in a season pack, the
// episode whose file is transcoded keeps it and says why, while the pack's
// other episode imports; an interactive grab then replaces the transcoded
// file, as a person's choice does.
func TestHandleNeverLetsAnAutomaticGrabReplaceATranscodedEpisode(t *testing.T) {
	ctx := context.Background()
	s := newSeriesFixture(t, "fi-transcoded-episode")

	first := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(first, "Breaking.Bad.S01E01.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	e01 := commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e01"}
	got := s.importGrab(t, "e01-dl", first, e01, nil, grabbedAs(downloadv1alpha1.GrabSourceSearch, false))
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)
	existing := got.Imported[0].MediaFileRef
	existingPath := got.Imported[0].DestPath
	waitFor(t, 5*time.Second, func() bool {
		return s.c.Get(ctx, client.ObjectKey{Namespace: s.ns, Name: existing}, &catalogv1alpha1.MediaFile{}) == nil
	})
	s.markTranscoded(t, existing, true)

	pack := dataDir(t, "scratch")
	dir := filepath.Join(pack, "Breaking.Bad.S01.1080p.BluRay.x264-GRP")
	mustWriteSparseFile(t, filepath.Join(dir, "Breaking.Bad.S01E01.PROPER.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(dir, "Breaking.Bad.S01E02.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	season := commonv1.MediaRef{
		Kind: commonv1.MediaKindSeries, Name: s.series,
		Keys: []string{"breaking-bad-s01e01", "breaking-bad-s01e02"},
	}
	got = s.importGrab(t, "pack-dl", pack, season, nil, grabbedAs(downloadv1alpha1.GrabSourceRSS, false))
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1, "episode 2 has no file, so the pack's file for it imports")
	assert.Equal(t, filepath.Join("Breaking.Bad.S01.1080p.BluRay.x264-GRP", "Breaking.Bad.S01E02.1080p.BluRay.x264-GRP.mkv"),
		got.Imported[0].SourcePath)
	require.Len(t, got.Rejections, 1)
	assert.Contains(t, got.Rejections[0], "Breaking.Bad.S01E01.PROPER.1080p.BluRay.x264-GRP.mkv: episode breaking-bad-s01e01's "+
		"existing file (MediaFile "+existing+") is transcoded")
	assert.Contains(t, got.Rejections[0], "grabbedBy rss")
	kept := s.mediaFile(t, existing)
	assert.Equal(t, existingPath, kept.Spec.Path)
	_, err := os.Stat(existingPath)
	require.NoError(t, err, "the transcoded file stays in the library")

	// A person picks the PROPER from an interactive search: their choice
	// replaces the transcoded file.
	chosen := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(chosen, "Breaking.Bad.S01E01.PROPER.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	got = s.importGrab(t, "chosen-dl", chosen, e01, nil, grabbedAs(downloadv1alpha1.GrabSourceInteractive, false))
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	assert.Empty(t, got.Rejections)
	var gone catalogv1alpha1.MediaFile
	err = s.api.Get(ctx, client.ObjectKey{Namespace: s.ns, Name: existing}, &gone)
	assert.True(t, apierrors.IsNotFound(err), "the transcoded file's MediaFile is replaced: %v", err)
}
