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

package specwrite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/mediafilespec/specwrite"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A transcoded file is renamed in its own folder (renameTranscoded):
// catalog's canonical path may name another folder ("Season 03" where
// the library has "Season 3"), and moving only the transcoded episodes
// there would split a season. The whole-path rename still holds such a
// file, as it always has.
func TestRenameFileKeepingTheFolderRenamesInPlace(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	season := filepath.Join(lib, "Bluey (2018) {tvdb-353546}", "Season 3")
	require.NoError(t, os.MkdirAll(season, 0o755))
	from := filepath.Join(season, "Bluey (2018) - S03E09 - Curry Quest [WEBDL-1080p][EAC3 5.1][h264]-NTb.mkv")
	require.NoError(t, os.WriteFile(from, []byte("hevc now"), 0o644))
	info, err := os.Stat(from)
	require.NoError(t, err)
	expected := filepath.Join(lib, "Bluey (2018) {tvdb-353546}", "Season 03", "Bluey (2018) - S03E09 - Curry Quest [WEBDL-1080p]-NTb.mkv")

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "bluey-s03e09", Namespace: "media"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "bluey-s03e09"},
			Path:     from, SizeBytes: info.Size(), ModTime: metav1.NewTime(info.ModTime().Truncate(time.Second)),
			Quality: commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: 1080},
		},
		Status: catalogv1alpha1.MediaFileStatus{Naming: &catalogv1alpha1.NamingStatus{ExpectedPath: expected}},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&catalogv1alpha1.MediaFile{}).WithObjects(mf).Build()

	out, err := specwrite.RenameFile(ctx, c, c, mf, false, specwrite.RenameWhole, "")
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameHeld, out.Reason, "the whole-path rename holds a file whose canonical folder differs")
	require.FileExists(t, from)

	out, err = specwrite.RenameFile(ctx, c, c, mf, false, specwrite.RenameOrganize, filepath.Join(lib, "Bluey (2018) {tvdb-353546}"))
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameHeld, out.Reason, "a video file keeps D5 under RenameOrganize, inside its series folder too")
	require.FileExists(t, from)
	assert.NoDirExists(t, filepath.Dir(expected), "and no folder is made for it")

	out, err = specwrite.RenameFile(ctx, c, c, mf, false, specwrite.RenameInFolder, "")
	require.NoError(t, err)
	require.True(t, out.Moved, "reason %q", out.Reason)
	want := filepath.Join(season, filepath.Base(expected))
	assert.Equal(t, want, out.To)
	assert.FileExists(t, want)
	assert.NoFileExists(t, from)
	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(mf), &got))
	assert.Equal(t, want, got.Spec.Path)

	out, err = specwrite.RenameFile(ctx, c, c, &got, false, specwrite.RenameInFolder, "")
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameNotCurrent, out.Reason, "already named: nothing to do")
}

// flatTrack plants an album track whose canonical path lies in another
// folder than the one it is in -- the shape of an artist folder scanned
// flat (2026-11-10 non-video design D16-D19) -- with a lyrics sidecar
// status.sidecars records, and returns the MediaFile and a fake client
// holding it.
func flatTrack(t *testing.T, from, expected string) (*catalogv1alpha1.MediaFile, client.Client) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(from), 0o755))
	require.NoError(t, os.WriteFile(from, []byte("flac frames"), 0o644))
	stem := strings.TrimSuffix(filepath.Base(from), filepath.Ext(from))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(from), stem+".lrc"), []byte("[00:01] lyrics"), 0o644))
	info, err := os.Stat(from)
	require.NoError(t, err)
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "nevermind-01", Namespace: "media"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "nirvana-nevermind", Track: "rec-01"},
			Path:     from, SizeBytes: info.Size(), ModTime: metav1.NewTime(info.ModTime().Truncate(time.Second)),
			Quality: commonv1.Quality{Name: "FLAC"},
		},
		Status: catalogv1alpha1.MediaFileStatus{
			Naming:   &catalogv1alpha1.NamingStatus{ExpectedPath: expected},
			Sidecars: []catalogv1alpha1.Sidecar{{Name: stem + ".lrc"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&catalogv1alpha1.MediaFile{}).WithObjects(mf).Build()
	return mf, c
}

// Under RenameOrganize a non-video file moves into another folder inside
// its container's: a track flat in its artist's folder goes into the album
// folder the rename creates, its sidecar with it, and the artist folder --
// the container, with the album-level cover.jpg in it -- stays (design
// D19). A track already in a folder of its own under the artist's leaves
// that folder pruned once it is empty, the container never. A root without
// organizeFolders (RenameWhole) holds the same proposal and makes nothing.
func TestRenameFileOrganizingMovesATrackIntoItsAlbumFolder(t *testing.T) {
	for _, tc := range []struct {
		name, from string // relative to the artist folder
		pruned     string // a folder the move empties, removed after it
	}{
		{name: "flat in the artist folder", from: "01 Smells Like Teen Spirit.flac"},
		{name: "in an old album folder", from: "Nevermind/01 Smells Like Teen Spirit.flac", pruned: "Nevermind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			artist := filepath.Join(t.TempDir(), "Nirvana")
			from := filepath.Join(artist, tc.from)
			album := filepath.Join(artist, "Nevermind (1991)")
			expected := filepath.Join(album, "Nirvana - Nevermind - 01 - Smells Like Teen Spirit.flac")
			mf, c := flatTrack(t, from, expected)
			cover := filepath.Join(artist, "cover.jpg")
			require.NoError(t, os.WriteFile(cover, []byte("jpeg"), 0o644))

			out, err := specwrite.RenameFile(ctx, c, c, mf, false, specwrite.RenameWhole, "")
			require.NoError(t, err)
			assert.Equal(t, mediafilespec.RenameHeld, out.Reason, "a root without organizeFolders holds a folder move")
			assert.FileExists(t, from)
			assert.NoDirExists(t, album)

			out, err = specwrite.RenameFile(ctx, c, c, mf, true, specwrite.RenameOrganize, artist)
			require.NoError(t, err)
			assert.Equal(t, mediafilespec.RenameDryRun, out.Reason)
			assert.NoDirExists(t, album, "a dry run makes no folder")

			out, err = specwrite.RenameFile(ctx, c, c, mf, false, specwrite.RenameOrganize, artist)
			require.NoError(t, err)
			require.True(t, out.Moved, "reason %q", out.Reason)
			assert.Equal(t, expected, out.To)
			assert.FileExists(t, expected)
			assert.NoFileExists(t, from)
			lrc, err := os.ReadFile(filepath.Join(album, "Nirvana - Nevermind - 01 - Smells Like Teen Spirit.lrc"))
			require.NoError(t, err, "the sidecar moved with the track")
			assert.Equal(t, "[00:01] lyrics", string(lrc))
			assert.FileExists(t, cover, "an album-level file stays where it is")
			assert.DirExists(t, artist, "the container is never pruned")
			if tc.pruned != "" {
				assert.NoDirExists(t, filepath.Join(artist, tc.pruned), "the folder the move emptied is pruned")
			}
			var got catalogv1alpha1.MediaFile
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(mf), &got))
			assert.Equal(t, expected, got.Spec.Path)
		})
	}
}

// RenameOrganize keeps a move inside the container's folder: a proposal
// that would leave it -- another artist's folder, the root beside it -- or
// a file that is not in it is held, with nothing moved and no folder made
// (design D19: "strictly under its container's folder").
func TestRenameFileOrganizingHoldsAMoveOutsideTheContainer(t *testing.T) {
	ctx := context.Background()
	lib := t.TempDir()
	artist := filepath.Join(lib, "Nirvana")
	for _, tc := range []struct {
		name, from, expected string
	}{
		{
			name:     "into another artist's folder",
			from:     filepath.Join(artist, "01 Smells Like Teen Spirit.flac"),
			expected: filepath.Join(lib, "Foo Fighters", "Foo Fighters (1995)", "Foo Fighters - Foo Fighters - 01 - This Is a Call.flac"),
		},
		{
			name:     "onto the root beside the container",
			from:     filepath.Join(artist, "01 Smells Like Teen Spirit.flac"),
			expected: filepath.Join(lib, "Nirvana - Nevermind - 01 - Smells Like Teen Spirit.flac"),
		},
		{
			name:     "from outside the container",
			from:     filepath.Join(lib, "Misc", "01 Smells Like Teen Spirit.flac"),
			expected: filepath.Join(artist, "Nevermind (1991)", "Nirvana - Nevermind - 01 - Smells Like Teen Spirit.flac"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mf, c := flatTrack(t, tc.from, tc.expected)
			_, before := os.Stat(filepath.Dir(tc.expected))
			out, err := specwrite.RenameFile(ctx, c, c, mf, false, specwrite.RenameOrganize, artist)
			require.NoError(t, err)
			assert.Equal(t, mediafilespec.RenameHeld, out.Reason)
			assert.FileExists(t, tc.from)
			assert.NoFileExists(t, tc.expected)
			_, after := os.Stat(filepath.Dir(tc.expected))
			assert.Equal(t, os.IsNotExist(before), os.IsNotExist(after), "no folder is made for a held move")
			require.NoError(t, os.RemoveAll(filepath.Dir(tc.from)))
		})
	}
}
