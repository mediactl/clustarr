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

package movie_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func jellyfinRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: path, Kind: catalogv1alpha1.RootFolderKindMovie,
			Naming: catalogv1alpha1.NamingSpec{Dialect: catalogv1alpha1.NamingDialectJellyfin},
		},
	}
}

// TestPath's expected folder literal is the real Jellyfin preset output,
// confirmed against test/data/naming/dialects.json's golden
// ("The Matrix (1999) [tmdbid-603]") rather than guessed: [tmdbid-<id>], not
// Plex's {tmdb-<id>}.
func TestPath(t *testing.T) {
	root := jellyfinRoot("/data/media/movies")
	ctx := naming.Context{Kind: commonv1.MediaKindMovie, Title: "Inception", Year: 2010, TmdbID: "27205"}

	got, err := movie.Path(root, nil, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (2010) [tmdbid-27205]", got)

	override := "Inception (Director's Cut)"
	got, err = movie.Path(root, &override, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (Director's Cut)", got)

	empty := ""
	got, err = movie.Path(root, &empty, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (2010) [tmdbid-27205]", got, "an empty override falls back to the engine")

	parent := ".."
	_, err = movie.Path(root, &parent, ctx)
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a spec.folder naming the root's parent is refused, never the parent (path.Join had read it as one)")
}

// TestPathAgreesWithTheImport: the folder the controller writes is the one
// the import places the movie's file in (catalogctx.MovieFilePath), byte
// for byte, both rendered from one Movie through the import's own context
// -- over a plain title, TMDB's "What If...?" (a trailing dot behind a
// question mark), a title of every character the sanitizer rewrites, a
// trailing dot and a 390-byte title (task F1). "What If...?" pins the
// shape: the controller's folder reads the sanitized title, where until
// 2026-11-10 status.path carried the "?" and the ui's Refresh & Scan,
// Rename and delete named a folder that was not on disk.
func TestPathAgreesWithTheImport(t *testing.T) {
	root := jellyfinRoot("/data/media/movies")
	movieTitled := func(title string) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "what-if", Namespace: "default"},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 754, RootFolderRef: "movies"},
			Status:     catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{Title: title, Year: 2024}},
		}
	}
	for _, title := range []string{
		"Plain Title",
		"What If...?",
		`A:B\C/D>E<F?G*H|I"J`,
		"Trailing dot.",
		strings.Repeat("Long ", 78),
	} {
		m := movieTitled(title)
		c, ok := catalogctx.Movie(m)
		require.True(t, ok, title)
		file, err := catalogctx.MovieFilePath(root, m, c, ".mkv")
		require.NoError(t, err, title)
		got, err := movie.Path(root, m.Spec.Folder, c)
		require.NoError(t, err, title)
		assert.Equal(t, filepath.Dir(file), got, "a movie titled %q", title)
		assert.LessOrEqual(t, len(filepath.Base(got)), naming.DefaultMaxComponentBytes, "a folder component fits the filesystem: %q", title)
	}
	m := movieTitled("What If...?")
	c, ok := catalogctx.Movie(m)
	require.True(t, ok)
	got, err := movie.Path(root, nil, c)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/What If. (2024) [tmdbid-754]", got, "the sanitized title, never the title as written")

	m.Spec.Folder = new("Marvel/What If...?")
	file, err := catalogctx.MovieFilePath(root, m, c, ".mkv")
	require.NoError(t, err)
	got, err = movie.Path(root, m.Spec.Folder, c)
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(file), got, "a spec.folder is sanitized by component on both sides")
	assert.Equal(t, "/data/media/movies/Marvel/What If", got)
}
