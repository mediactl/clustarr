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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// importOne imports a single file named fileName through a fresh Download
// and returns the MediaFile it produced.
func (f *fixture) importOne(t *testing.T, dlName, fileName string) catalogv1alpha1.MediaFile {
	t.Helper()
	ctx := context.Background()
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, fileName), sampleFloor)

	dl := f.createDownload(t, dlName, contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	var got downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: dl.Name}, &got))
	require.NotNil(t, got.Status.Import)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.Status.Import.State, "message: %s", got.Status.Import.Message)
	require.Len(t, got.Status.Import.Imported, 1)

	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: got.Status.Import.Imported[0].MediaFileRef}, &mf))
	return mf
}

// TestImportFreezesTheParsedGroupEvenWhenItIsAQualityWord pins the removal
// of the second copy of the old release-group guard. pkg/release now ports
// Radarr's ReleaseGroupParser, and parses "-UHD" at the end of this scene
// name as the group; the guard dropped any group that equalled a quality
// word, so a real group was frozen as "".
func TestImportFreezesTheParsedGroupEvenWhenItIsAQualityWord(t *testing.T) {
	f := newFixture(t, "fi-group")
	mf := f.importOne(t, "group-dl", "The.Matrix.1999.1080p.BluRay.x264-UHD.mkv")
	require.Equal(t, "UHD", mf.Spec.ReleaseGroup, "the parser's group is frozen verbatim")
}

// TestAnUntaggedImportTakesTheMoviesOriginalLanguage is Radarr's
// AggregateLanguages rule on the import path: a file name that names no
// language takes the movie's original language, both in the languages
// frozen into the spec and in the custom-format score (an untagged release
// of a Japanese film must not score as a non-original English one).
func TestAnUntaggedImportTakesTheMoviesOriginalLanguage(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-lang")
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr,
		catalogac.Movie(f.movieName, f.ns).WithStatus(catalogac.MovieStatus().WithMetadata(
			catalogac.MovieMetadata().WithTitle("The Matrix").WithYear(1999).WithOriginalLanguage("ja"))))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var m catalogv1alpha1.Movie
		return f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.movieName}, &m) == nil &&
			m.Status.Metadata != nil && m.Status.Metadata.OriginalLanguage == "ja"
	})

	untagged := f.importOne(t, "lang-dl", "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv")
	require.Equal(t, []string{"Japanese"}, untagged.Spec.Languages,
		"an untagged file takes the item's original language, not the parser's English default")
}

// TestAnUnreadableFolderInADownloadIsARejectionNotAnAbort: a folder of the
// download the walk cannot list is reported on status.import and the rest
// of the download is still imported. It sorts before the movie file, so a
// walk that aborted on it would import nothing.
func TestAnUnreadableFolderInADownloadIsARejectionNotAnAbort(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 directory anyway")
	}
	ctx := context.Background()
	f := newFixture(t, "fi-unreadable")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Movie", "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
	locked := filepath.Join(contentRoot, "Extras-locked")
	mustWriteSparseFile(t, filepath.Join(locked, "bonus.mkv"), sampleFloor)
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	dl := f.createDownload(t, "unreadable-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	var got downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: dl.Name}, &got))
	require.NotNil(t, got.Status.Import)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.Status.Import.State, "message: %s", got.Status.Import.Message)
	require.Len(t, got.Status.Import.Imported, 1)
	require.Len(t, got.Status.Import.Rejections, 1)
	require.Contains(t, got.Status.Import.Rejections[0], "Extras-locked: could not be read")
}

// An obfuscated post names its one video "2ef6f194995e4a11b055d0f2354ef0ba.mp4"
// (the first grab on the owner's cluster, 2026-09-24): the file name parses
// to nothing, so the import rejected the only file and the controller
// blocklisted a whole, repaired release for it. Radarr's ImportDecisionMaker
// falls back from the file's name to the download client item's title; the
// Download carries that title, and it names the quality and group as well
// as any file name would.
func TestAnObfuscatedFileNameTakesTheDownloadsReleaseTitle(t *testing.T) {
	f := newFixture(t, "fi-obfuscated")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "2ef6f194995e4a11b055d0f2354ef0ba.mkv"), sampleFloor)

	got := f.importTitled(t, "obfuscated-dl", "The.Matrix.1999.1080p.BluRay.x264-SPARKS", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName})
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)
	for _, mf := range f.importedFiles(t, got) {
		require.Equal(t, "Bluray-1080p", mf.Spec.Quality.Name, "the quality is the release title's")
		require.Equal(t, "SPARKS", mf.Spec.ReleaseGroup, "so is the group")
	}
}
