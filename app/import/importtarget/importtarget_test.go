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

package importtarget

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParseImportTarget(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    ImportTarget
		fileRef commonv1.MediaRef
	}{
		{"movie/the-matrix", ImportTarget{Kind: "movie", Name: "the-matrix"}, commonv1.MediaRef{Kind: "movie", Name: "the-matrix"}},
		{"album/ok-computer", ImportTarget{Kind: "album", Name: "ok-computer"}, commonv1.MediaRef{Kind: "album", Name: "ok-computer"}},
		{
			"comic/saga/saga-00001.0",
			ImportTarget{Kind: "comic", Name: "saga", Key: "saga-00001.0"},
			commonv1.MediaRef{Kind: "issue", Name: "saga-00001.0"},
		},
		{
			"series/bb/bb-s01e01",
			ImportTarget{Kind: "series", Name: "bb", Key: "bb-s01e01"},
			commonv1.MediaRef{Kind: "episode", Name: "bb-s01e01"},
		},
		{"issue/saga-00001.0", ImportTarget{Kind: "issue", Name: "saga-00001.0"}, commonv1.MediaRef{Kind: "issue", Name: "saga-00001.0"}},
	} {
		got, err := ParseImportTarget(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
		assert.Equal(t, tc.fileRef, got.FileRef(), tc.in)
		assert.Equal(t, tc.in, got.String(), "the grammar round-trips")
	}

	// Strict: nothing is repaired, because acting on a guessed reading of a
	// malformed instruction is the guess the never-guess rule forbids.
	for _, bad := range []string{
		"",
		"movie",
		"movie/",
		"/the-matrix",
		" movie/the-matrix",
		"movie/the-matrix ",
		"Movie/the-matrix",
		"film/the-matrix",
		"movie/The_Matrix",
		"movie/the-matrix/extra",  // a key on a kind with no keyed children
		"album/ok-computer/track", // likewise
		"comic/saga/Not Valid",
		"comic/saga/a/b",
	} {
		_, err := ParseImportTarget(bad)
		assert.Error(t, err, "%q must be rejected", bad)
	}
}

func TestParseImportOverride(t *testing.T) {
	v, err := ParseImportOverride("true")
	require.NoError(t, err)
	assert.True(t, v)
	v, err = ParseImportOverride("false")
	require.NoError(t, err)
	assert.False(t, v)
	for _, bad := range []string{"", "1", "t", "TRUE", "True", "yes", " true"} {
		_, err := ParseImportOverride(bad)
		assert.Error(t, err, "%q must be rejected, not read as a bool", bad)
	}
}

func TestReadDirectives(t *testing.T) {
	d, err := ReadDirectives(nil)
	require.NoError(t, err)
	assert.Nil(t, d.Target)
	assert.False(t, d.Override)

	d, err = ReadDirectives(map[string]string{AnnotationImportTarget: "book/dune", AnnotationImportOverride: "true"})
	require.NoError(t, err)
	require.NotNil(t, d.Target)
	assert.Equal(t, commonv1.MediaKindBook, d.Target.Kind)
	assert.True(t, d.Override)

	_, err = ReadDirectives(map[string]string{AnnotationImportTarget: "book/dune", AnnotationImportOverride: "yes"})
	assert.Error(t, err, "a malformed override fails the pair, even with a good target")
}

func TestTargetFromSpec(t *testing.T) {
	assert.Equal(t, commonv1.MediaRef{Kind: "issue", Name: "saga-1"},
		TargetFromSpec(commonv1.MediaRef{Kind: "comic", Name: "saga", Keys: []string{"saga-1"}}).FileRef())
	assert.Equal(t, commonv1.MediaRef{Kind: "comic", Name: "saga"},
		TargetFromSpec(commonv1.MediaRef{Kind: "comic", Name: "saga", Keys: []string{"a", "b"}}).FileRef(),
		"a pack stays the container: choosing one of its issues would be a guess")
	assert.Equal(t, commonv1.MediaRef{Kind: "movie", Name: "m"},
		TargetFromSpec(commonv1.MediaRef{Kind: "movie", Name: "m"}).FileRef())
}

func TestFileRefFitsRoot(t *testing.T) {
	assert.True(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "album"}, catalogv1alpha1.RootFolderKindMusic))
	assert.True(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "issue"}, catalogv1alpha1.RootFolderKindComic))
	assert.False(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "book"}, catalogv1alpha1.RootFolderKindAudiobook))
	assert.False(t, FileRefFitsRoot(commonv1.MediaRef{Kind: "comic"}, catalogv1alpha1.RootFolderKindComic),
		"a comic holds no file of its own; its issues do")
}
