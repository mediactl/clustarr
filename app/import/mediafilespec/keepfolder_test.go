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

package mediafilespec_test

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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A transcoded file is renamed in its own folder (renameTranscoded):
// catalogarr's canonical path may name another folder ("Season 03" where
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

	out, err := mediafilespec.RenameFile(ctx, c, c, mf, false, false)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameHeld, out.Reason, "the whole-path rename holds a file whose canonical folder differs")
	require.FileExists(t, from)

	out, err = mediafilespec.RenameFile(ctx, c, c, mf, false, true)
	require.NoError(t, err)
	require.True(t, out.Moved, "reason %q", out.Reason)
	want := filepath.Join(season, filepath.Base(expected))
	assert.Equal(t, want, out.To)
	assert.FileExists(t, want)
	assert.NoFileExists(t, from)
	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(mf), &got))
	assert.Equal(t, want, got.Spec.Path)

	out, err = mediafilespec.RenameFile(ctx, c, c, &got, false, true)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameNotCurrent, out.Reason, "already named: nothing to do")
}
