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

package author_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/author"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func bookRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "books", Namespace: "default"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindBook},
	}
}

func TestPath(t *testing.T) {
	root := bookRoot("/data/books")

	t.Run("folder override wins", func(t *testing.T) {
		override := "Custom Folder"
		got, err := author.Path(root, &override, naming.Context{AuthorName: "J.R.R. Tolkien"})
		require.NoError(t, err)
		assert.Equal(t, "/data/books/Custom Folder", got)
	})

	t.Run("no override renders the author name", func(t *testing.T) {
		got, err := author.Path(root, nil, naming.Context{AuthorName: "J.R.R. Tolkien"})
		require.NoError(t, err)
		assert.Equal(t, "/data/books/J.R.R. Tolkien", got)
	})

	t.Run("empty author name collapses to the root path", func(t *testing.T) {
		got, err := author.Path(root, nil, naming.Context{Kind: commonv1.MediaKindAuthor})
		require.NoError(t, err)
		assert.Equal(t, "/data/books", got)
	})

	t.Run("a name the rule cannot render is an error, never a path", func(t *testing.T) {
		_, err := author.Path(root, nil, naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "..."})
		require.ErrorIs(t, err, naming.ErrUnsafeComponent, "only dots")
		_, err = author.Path(root, nil, naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "???"})
		require.Error(t, err, "nothing once sanitized")
		parent := ".."
		_, err = author.Path(root, &parent, naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "J.R.R. Tolkien"})
		require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a spec.folder naming the root's parent")
	})
}

// TestPathAgreesWithTheImport: the folder the controller writes is the one
// the import places the author's books' files under
// (catalogctx.BookFilePath, the book's own segment between), byte for byte,
// both rendered from one Author through the import's own context -- over a
// plain name, "What If...?" (a trailing dot behind a question mark), a name
// of every character the sanitizer rewrites, a trailing dot and a 390-byte
// name (task F1) -- and with status.path then written, the file path stays
// what it was. Until 2026-11-10 status.path carried the name as written
// while BookFilePath's whole-path sanitize placed the files under the
// sanitized folder.
func TestPathAgreesWithTheImport(t *testing.T) {
	root := bookRoot("/data/books")
	released := metav1.NewTime(metav1.Now().AddDate(-1, 0, 0))
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "guards", Namespace: "default"},
		Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Guards! Guards!", ReleaseDate: &released}},
	}
	for _, name := range []string{
		"Terry Pratchett",
		"What If...?",
		`A:B\C/D>E<F?G*H|I"J`,
		"Trailing dot.",
		strings.Repeat("Long ", 78),
	} {
		a := &catalogv1alpha1.Author{
			ObjectMeta: metav1.ObjectMeta{Name: "pratchett", Namespace: "default"},
			Spec:       catalogv1alpha1.AuthorSpec{RootFolderRef: "books"},
			Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: name}},
		}
		c, ok := catalogctx.Book(a, book)
		require.True(t, ok, name)
		file, err := catalogctx.BookFilePath(root, a, c, ".epub")
		require.NoError(t, err, name)
		// The reconciler's own folder context: the author's name alone.
		got, err := author.Path(root, a.Spec.Folder, naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: a.Status.Metadata.Name})
		require.NoError(t, err, name)
		assert.Equal(t, filepath.Dir(filepath.Dir(file)), got, "an author named %q: the book's folder lies in it", name)
		assert.LessOrEqual(t, len(filepath.Base(got)), naming.DefaultMaxComponentBytes, "a folder component fits the filesystem: %q", name)

		a.Status.Path = got
		again, err := catalogctx.BookFilePath(root, a, c, ".epub")
		require.NoError(t, err, name)
		assert.Equal(t, file, again, "the written status.path renders the same file path: %q", name)
	}
	got, err := author.Path(root, nil, naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: "What If...?"})
	require.NoError(t, err)
	assert.Equal(t, "/data/books/What If", got, "the sanitized name, never the name as written")
}
