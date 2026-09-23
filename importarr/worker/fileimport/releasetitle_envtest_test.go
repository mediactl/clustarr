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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// createTitled creates a completed Download of release title title, as the
// grab path records it, and imports it; it returns the import state.
func (f *fixture) importTitled(t *testing.T, name, title, contentRoot string, target commonv1.MediaRef) *downloadv1alpha1.ImportState {
	t.Helper()
	ctx := context.Background()
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("c", 40)
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1.ReleaseInfo{
				GUID: "g-" + name, IndexerRef: "idx", IndexerName: "Example", Title: title,
				Protocol: commonv1.ProtocolTorrent, InfoHash: strings.Repeat("c", 40),
			},
			Target: target,
		},
	}
	require.NoError(t, f.c.Create(ctx, dl))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerGrabarr, downloadac.Download(dl.Name, f.ns).WithStatus(
		downloadac.DownloadStatus().WithPhase(downloadv1alpha1.DownloadPhaseCompleted).WithContentRoot(contentRoot)))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var got downloadv1alpha1.Download
		return f.c.Get(ctx, client.ObjectKeyFromObject(dl), &got) == nil && got.Status.Phase == downloadv1alpha1.DownloadPhaseCompleted
	})
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))
	return f.importState(t, dl).Status.Import
}

// importedFiles maps each imported file's source path to its MediaFile.
func (f *fixture) importedFiles(t *testing.T, got *downloadv1alpha1.ImportState) map[string]catalogv1alpha1.MediaFile {
	t.Helper()
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	out := map[string]catalogv1alpha1.MediaFile{}
	for _, i := range got.Imported {
		var mf catalogv1alpha1.MediaFile
		require.NoError(t, f.api.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: i.MediaFileRef}, &mf))
		out[i.SourcePath] = mf
	}
	return out
}

// releaseTitleFormats are the two ungrouped ReleaseTitle formats every
// video profile scores, which a name carrying REPACK and HDR matches.
var releaseTitleFormats = []string{"repack-proper", "hdr"}

func assertScored(t *testing.T, mf catalogv1alpha1.MediaFile, want bool, msg string) {
	t.Helper()
	for _, f := range releaseTitleFormats {
		if want {
			assert.Contains(t, mf.Spec.MatchedFormats, f, msg)
		} else {
			assert.NotContains(t, mf.Spec.MatchedFormats, f, msg)
		}
	}
	if want {
		assert.GreaterOrEqual(t, mf.Spec.FormatScore, int32(505), msg)
	}
}

// ReleaseTitle custom formats (repack/proper, HDR, codecs, streaming
// services) read the release's full name and the file's, as Radarr's
// LocalMovie input does: the Download's release title, and the file name.
// Before X3's catalogue fix they read the parsed movie title and never
// matched; after it, an import that passed neither name still matched none.
func TestMovieImportScoresReleaseTitleFormats(t *testing.T) {
	f := newFixture(t, "fi-rt-movie")
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}

	byRelease := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(byRelease, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
	got := f.importTitled(t, "rt-release", "The.Matrix.1999.REPACK.1080p.BluRay.HDR.x264-SPARKS", byRelease, target)
	assertScored(t, f.importedFiles(t, got)["The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"], true, "from the release title")

	byFile := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(byFile, "The.Matrix.1999.REPACK.1080p.BluRay.HDR.x264-SPARKS.mkv"), sampleFloor)
	got = f.importTitled(t, "rt-file", "The Matrix 1999 1080p BluRay x264-SPARKS", byFile, target)
	assertScored(t, f.importedFiles(t, got)["The.Matrix.1999.REPACK.1080p.BluRay.HDR.x264-SPARKS.mkv"], true, "from the file name")
}

// For an episode the release title is the file's scene name only when the
// download is not a full season's and holds no other video file (Sonarr's
// SceneNameCalculator): a single episode's REPACK HDR release scores those
// formats on a plainly named file; a pack's REPACK HDR name says nothing of
// which file it describes, so only a file whose own name carries the tokens
// scores them.
func TestEpisodeImportScoresReleaseTitleFormats(t *testing.T) {
	s := newSeriesFixture(t, "fi-rt-episode")

	single := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(single, "Breaking.Bad.S01E03.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	got := s.importTitled(t, "rt-single", "Breaking.Bad.S01E03.REPACK.1080p.BluRay.HDR.x264-GRP", single,
		commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e03"})
	assertScored(t, s.importedFiles(t, got)["Breaking.Bad.S01E03.1080p.BluRay.x264-GRP.mkv"], true, "a single episode's scene name")

	pack := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(pack, "Breaking.Bad.S01E01.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(pack, "Breaking.Bad.S01E02.REPACK.1080p.BluRay.HDR.x264-GRP.mkv"), sampleFloor)
	got = s.importTitled(t, "rt-pack", "Breaking.Bad.S01.REPACK.1080p.BluRay.HDR.x264-GRP", pack,
		commonv1.MediaRef{
			Kind: commonv1.MediaKindSeries, Name: s.series,
			Keys: []string{"breaking-bad-s01e01", "breaking-bad-s01e02"},
		})
	files := s.importedFiles(t, got)
	assertScored(t, files["Breaking.Bad.S01E01.1080p.BluRay.x264-GRP.mkv"], false, "a pack's name is not a file's scene name")
	assertScored(t, files["Breaking.Bad.S01E02.REPACK.1080p.BluRay.HDR.x264-GRP.mkv"], true, "the file's own name")

	// A full season's name is not a scene name even when the download
	// holds only one of its episodes (a fresh series: episode 3 above is
	// already at cutoff).
	s2 := newSeriesFixture(t, "fi-rt-lone")
	lone := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(lone, "Breaking.Bad.S01E03.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	got = s2.importTitled(t, "rt-lone", "Breaking.Bad.S01.REPACK.1080p.BluRay.HDR.x264-GRP", lone,
		commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: s2.series, Keys: []string{"breaking-bad-s01e03"}})
	assertScored(t, s2.importedFiles(t, got)["Breaking.Bad.S01E03.1080p.BluRay.x264-GRP.mkv"], false,
		"a full season's name, one file or not")

	// Nor is a release name that is not a season's, when the download holds
	// more than one video file: it cannot say which file it describes.
	two := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(two, "Breaking.Bad.S01E01.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(two, "Breaking.Bad.S01E02.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	got = s2.importTitled(t, "rt-two", "Breaking.Bad.S01E01E02.REPACK.1080p.BluRay.HDR.x264-GRP", two,
		commonv1.MediaRef{
			Kind: commonv1.MediaKindSeries, Name: s2.series,
			Keys: []string{"breaking-bad-s01e01", "breaking-bad-s01e02"},
		})
	for name, mf := range s2.importedFiles(t, got) {
		assertScored(t, mf, false, name+": one release name, two video files")
	}
}
