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

package artist_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func musicRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: path, Kind: catalogv1alpha1.RootFolderKindMusic,
			Naming: catalogv1alpha1.NamingSpec{Dialect: catalogv1alpha1.NamingDialectPlex},
		},
	}
}

func TestPathUsesFolderOverrideWhenSet(t *testing.T) {
	override := "Radiohead (custom)"
	got, err := artist.Path(musicRoot("/music"), &override, naming.Context{})
	require.NoError(t, err)
	assert.Equal(t, "/music/Radiohead (custom)", got)

	pinned := "Therapy?"
	got, err = artist.Path(musicRoot("/music"), &pinned, naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "Therapy?"})
	require.NoError(t, err)
	assert.Equal(t, "/music/Therapy?", got, "a pinned spec.folder is joined as written: it names the folder on disk")
}

func TestPathFallsBackToTheNamingEngine(t *testing.T) {
	ctx := naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "Radiohead"}
	got, err := artist.Path(musicRoot("/music"), nil, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/music/Radiohead", got)
}

func TestPathEmptyFolderOverrideFallsBackToo(t *testing.T) {
	ctx := naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "Radiohead"}
	for _, override := range []string{"", "   "} {
		got, err := artist.Path(musicRoot("/music"), &override, ctx)
		require.NoError(t, err)
		assert.Equal(t, "/music/Radiohead", got, "an empty or blank override is treated as unset, mirroring series.Path")
	}
}

func TestPathDegrades(t *testing.T) {
	root := musicRoot("/music")
	got, err := artist.Path(root, nil, naming.Context{Kind: commonv1.MediaKindArtist})
	require.NoError(t, err)
	assert.Equal(t, "/music", got, "an artist whose name has not arrived reads the root")

	_, err = artist.Path(root, nil, naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "..."})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "only dots")
	_, err = artist.Path(root, nil, naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "???"})
	require.Error(t, err, "nothing once sanitized")
	parent := ".."
	_, err = artist.Path(root, &parent, naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "Radiohead"})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a spec.folder naming the root's parent")
}

// TestPathAgreesWithTheImport: the folder the controller writes is the one
// the import places the artist's tracks under (catalogctx.TrackFilePath,
// the album's own folder between), byte for byte, both rendered from one
// Artist through the import's own context -- over a plain name, "What
// If...?" (a trailing dot behind a question mark), a name of every
// character the sanitizer rewrites, a trailing dot and a 390-byte name
// (task F1) -- and with status.path then written, the file path stays what
// it was. Until the F1 fix round (2026-11-10) status.path carried the name
// as written while TrackFilePath placed the tracks under the sanitized
// folder.
func TestPathAgreesWithTheImport(t *testing.T) {
	root := musicRoot("/data/music")
	for _, name := range []string{
		"Radiohead",
		"What If...?",
		`A:B\C/D>E<F?G*H|I"J`,
		"Trailing dot.",
		strings.Repeat("Long ", 78),
	} {
		a := &catalogv1alpha1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "default"},
			Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", RootFolderRef: "music"},
			Status:     catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: name}},
		}
		album := &catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: "default"},
			Spec:       catalogv1alpha1.AlbumSpec{ReleaseGroupID: "b1392450-e5a3-37d1-83d3-b8b08ca6c4d9", ArtistRef: "radiohead"},
			Status:     catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{Title: "OK Computer"}},
		}
		track := &catalogv1alpha1.Track{Number: 1, Medium: 1, Title: "Airbag", RecordingID: "r1"}
		c, ok := catalogctx.Album(a, album, track)
		require.True(t, ok, name)
		file, err := catalogctx.TrackFilePath(root, a, album, c, ".flac")
		require.NoError(t, err, name)
		// The reconciler's own folder context: the artist's name and id alone.
		got, err := artist.Path(root, a.Spec.Folder, naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: a.Status.Metadata.Name, ArtistMbID: a.Spec.MusicBrainzID})
		require.NoError(t, err, name)
		assert.Equal(t, filepath.Dir(filepath.Dir(file)), got, "an artist named %q: the album's folder lies in it", name)
		assert.LessOrEqual(t, len(filepath.Base(got)), naming.DefaultMaxComponentBytes, "a folder component fits the filesystem: %q", name)

		a.Status.Path = got
		again, err := catalogctx.TrackFilePath(root, a, album, c, ".flac")
		require.NoError(t, err, name)
		assert.Equal(t, file, again, "the written status.path renders the same file path: %q", name)
		albumFolder, err := catalogctx.AlbumFolder(root, a, c)
		require.NoError(t, err, name)
		assert.Equal(t, filepath.Dir(file), albumFolder, "the album's folder under the written status.path is the track's: %q", name)
	}
	got, err := artist.Path(root, nil, naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "What If...?"})
	require.NoError(t, err)
	assert.Equal(t, "/data/music/What If", got, "the sanitized name, never the name as written")
}
