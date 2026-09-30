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
package librarydelete

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func mediaFile(name string, ref commonv1.MediaRef, path string, sidecars ...string) catalogv1alpha1.MediaFile {
	mf := catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: path},
	}
	for _, s := range sidecars {
		mf.Status.Sidecars = append(mf.Status.Sidecars, catalogv1alpha1.Sidecar{Path: s})
	}
	return mf
}

func write(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

var heat = commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}

func heatTarget(root string) (Target, catalogv1alpha1.MediaFile) {
	folder := filepath.Join(root, "Heat (1995)")
	mf := mediaFile("heat-file", heat, filepath.Join(folder, "Heat.mkv"), filepath.Join(folder, "Heat.en.srt"))
	return Target{
		Root: root, Folder: folder,
		Keys:  map[string]bool{TargetKey(commonv1.MediaKindMovie, "heat"): true},
		Files: []catalogv1alpha1.MediaFile{mf},
	}, mf
}

// The whole folder goes, untracked files included, and its emptied parent
// with it; the RootFolder stays.
func TestRemoveFromDiskRemovesTheFolderAndPrunes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "movies")
	tgt, _ := heatTarget(filepath.Join(root, "H"))
	tgt.Root = root
	write(t, filepath.Join(tgt.Folder, "Heat.mkv"))
	write(t, filepath.Join(tgt.Folder, "Heat.en.srt"))
	write(t, filepath.Join(tgt.Folder, "movie.nfo"))
	write(t, filepath.Join(tgt.Folder, "Extras", "trailer.mkv"))

	require.NoError(t, Check(tgt, tgt.Files))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	assert.NoDirExists(t, tgt.Folder)
	assert.NoDirExists(t, filepath.Join(root, "H"), "emptied parent pruned")
	assert.DirExists(t, root, "the RootFolder is never removed")
}

func TestCheckRefusesAFolderHoldingAnotherItemsFile(t *testing.T) {
	root := t.TempDir()
	tgt, mf := heatTarget(root)
	other := mediaFile("ronin-file", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "ronin"},
		filepath.Join(tgt.Folder, "Ronin.mkv"))
	err := Check(tgt, []catalogv1alpha1.MediaFile{mf, other})
	require.ErrorIs(t, err, ErrRefused)
	assert.Contains(t, err.Error(), "ronin-file")
}

func TestCheckRefusesAPathOutsideTheRootFolder(t *testing.T) {
	root := t.TempDir()
	tgt, _ := heatTarget(root)
	tgt.Files[0].Status.Sidecars = append(tgt.Files[0].Status.Sidecars, catalogv1alpha1.Sidecar{Path: "/etc/passwd"})
	require.ErrorIs(t, Check(tgt, tgt.Files), ErrRefused)

	tgt, _ = heatTarget(root)
	tgt.Folder = root
	require.ErrorIs(t, Check(tgt, tgt.Files), ErrRefused, "the RootFolder itself")
}

func TestCheckRefusesAnUnknownRootFolder(t *testing.T) {
	tgt, _ := heatTarget(t.TempDir())
	tgt.Root = ""
	require.ErrorIs(t, Check(tgt, tgt.Files), ErrRefused)
}

// A multi-episode file names one episode and lists the others in keys: any
// of them belonging to the series makes it the series' file.
func TestCheckTreatsAPackFileAsTheSeries(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Andor")
	pack := mediaFile("pack", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e01",
		Keys: []string{"andor-s01e01", "andor-s01e02"}}, filepath.Join(folder, "S01E01-E02.mkv"))
	tgt := Target{Root: root, Folder: folder, Keys: map[string]bool{
		TargetKey(commonv1.MediaKindSeries, "andor"):         true,
		TargetKey(commonv1.MediaKindEpisode, "andor-s01e02"): true,
	}}
	assert.True(t, tgt.Owns(pack.Spec.MediaRef))
	require.NoError(t, Check(tgt, []catalogv1alpha1.MediaFile{pack}))
}

func TestCheckWithNoFolderRemovesOnlyTheFiles(t *testing.T) {
	root := t.TempDir()
	tgt, _ := heatTarget(root)
	tgt.Folder = ""
	write(t, tgt.Files[0].Spec.Path)
	write(t, filepath.Join(filepath.Dir(tgt.Files[0].Spec.Path), "movie.nfo"))
	require.NoError(t, Check(tgt, tgt.Files))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	assert.NoFileExists(t, tgt.Files[0].Spec.Path)
	assert.FileExists(t, filepath.Join(filepath.Dir(tgt.Files[0].Spec.Path), "movie.nfo"),
		"with no folder resolved nothing is removed but the recorded files")
}

func TestRemoveFromDiskSkipsWhatIsAlreadyGone(t *testing.T) {
	tgt, _ := heatTarget(t.TempDir())
	require.NoError(t, RemoveFromDisk(context.Background(), tgt), "a retry after a partial run")
}

func TestRemoveFromDiskDoesNotFollowASymlinkedFolder(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "precious.mkv"))
	tgt, _ := heatTarget(root)
	require.NoError(t, os.Symlink(elsewhere, tgt.Folder))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	_, err := os.Lstat(tgt.Folder)
	assert.True(t, errors.Is(err, os.ErrNotExist), "the link is gone")
	assert.FileExists(t, filepath.Join(elsewhere, "precious.mkv"), "its target is untouched")
}

// A file recorded outside the folder (a sidecar next to it, an old path)
// is removed on its own.
func TestRemoveFromDiskRemovesFilesOutsideTheFolder(t *testing.T) {
	root := t.TempDir()
	tgt, _ := heatTarget(root)
	stray := filepath.Join(root, "loose", "Heat.old.mkv")
	tgt.Files = append(tgt.Files, mediaFile("heat-old", heat, stray))
	write(t, stray)
	require.NoError(t, Check(tgt, tgt.Files))
	require.NoError(t, RemoveFromDisk(context.Background(), tgt))
	assert.NoFileExists(t, stray)
	assert.NoDirExists(t, filepath.Join(root, "loose"))
}
