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

package book_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
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
	tolkien := &catalogv1alpha1.Author{ObjectMeta: metav1.ObjectMeta{Name: "tolkien", Namespace: "default"}}
	hobbit := naming.Context{Kind: commonv1.MediaKindBook, BookTitle: "The Hobbit", Year: 1937}

	t.Run("an author's book renders under its author's folder -- no separate book folder", func(t *testing.T) {
		c := hobbit
		c.AuthorName = "J.R.R. Tolkien"
		got, err := book.Path(root, tolkien, c)
		require.NoError(t, err)
		assert.Equal(t, "/data/books/J.R.R. Tolkien", got)
	})

	t.Run("an author's book lives under the author's spec.folder when one is set", func(t *testing.T) {
		c := hobbit
		c.AuthorName = "J.R.R. Tolkien"
		custom := tolkien.DeepCopy()
		custom.Spec.Folder = new("Tolkien, J.R.R.")
		got, err := book.Path(root, custom, c)
		require.NoError(t, err)
		assert.Equal(t, "/data/books/Tolkien, J.R.R.", got, "a pinned folder is taken as written, its trailing dot kept: it names the folder on disk (the F1 fix round)")
	})

	t.Run("an author's book whose author's name is not known yet reads the root, never its own title", func(t *testing.T) {
		got, err := book.Path(root, tolkien, hobbit)
		require.NoError(t, err)
		assert.Equal(t, "/data/books", got)
	})

	t.Run("a standalone book has a folder of its own under the root, its title", func(t *testing.T) {
		got, err := book.Path(root, nil, hobbit)
		require.NoError(t, err)
		assert.Equal(t, "/data/books/The Hobbit", got)
	})

	t.Run("a standalone book with no title yet reads the root", func(t *testing.T) {
		got, err := book.Path(root, nil, naming.Context{Kind: commonv1.MediaKindBook})
		require.NoError(t, err)
		assert.Equal(t, "/data/books", got)
	})

	t.Run("a title the rule cannot render is an error, never a path", func(t *testing.T) {
		_, err := book.Path(root, nil, naming.Context{Kind: commonv1.MediaKindBook, BookTitle: "???"})
		require.Error(t, err, "a standalone title that sanitizes to nothing")
		_, err = book.Path(root, nil, naming.Context{Kind: commonv1.MediaKindBook, BookTitle: "..."})
		require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a standalone title of only dots")
		_, err = book.Path(root, tolkien, naming.Context{Kind: commonv1.MediaKindBook, BookTitle: "The Hobbit", AuthorName: "..."})
		require.ErrorIs(t, err, naming.ErrUnsafeComponent, "an author's name of only dots")
	})
}

// TestPathAgreesWithTheImport: the folder the controller writes is the one
// the import places the book's file in (catalogctx.BookFilePath), byte for
// byte, both rendered from one Book through the import's own context and
// rule -- the titles the import's sanitizer rewrites included: a colon
// (which the book-title token leaves in place, unlike a movie's), a question
// mark, trailing dots, a slash pkg/naming makes a "+", every character the
// sanitizer rewrites or drops, a trailing dot alone and a 390-byte title
// over the component limit (task F1). An author's book is its author's
// folder, the book's file two segments under it, over the same shapes as
// the author's name and under an author's spec.folder.
func TestPathAgreesWithTheImport(t *testing.T) {
	root := bookRoot("/data/books")
	released := metav1.NewTime(time.Date(1989, time.November, 1, 0, 0, 0, 0, time.UTC))
	bookTitled := func(title string) *catalogv1alpha1.Book {
		return &catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: "guards", Namespace: "default"},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: title, ReleaseDate: &released}},
		}
	}
	shapes := []string{
		"Guards! Guards!",
		"What If?: Serious Scientific Answers to Absurd Hypothetical Questions",
		"What If...?",
		"Etc...",
		"AC/DC: Maximum Rock & Roll",
		"Mr. Men",
		`A:B\C/D>E<F?G*H|I"J`,
		"Trailing dot.",
		strings.Repeat("A long title ", 30),
	}

	for _, title := range shapes {
		c, ok := catalogctx.Book(nil, bookTitled(title))
		require.True(t, ok, title)
		file, err := catalogctx.BookFilePath(root, nil, c, ".epub")
		require.NoError(t, err, title)
		got, err := book.Path(root, nil, c)
		require.NoError(t, err, title)
		assert.Equal(t, filepath.Dir(file), got, "a standalone book titled %q", title)
		assert.NotEqual(t, root.Spec.Path, got, "a standalone book's folder is never the root: %q", title)
		assert.LessOrEqual(t, len(filepath.Base(got)), naming.DefaultMaxComponentBytes, "a folder component fits the filesystem: %q", title)
	}
	c, ok := catalogctx.Book(nil, bookTitled("Guards! Guards!"))
	require.True(t, ok)
	got, err := book.Path(root, nil, c)
	require.NoError(t, err)
	assert.Equal(t, "/data/books/Guards! Guards!", got, "the title alone, no year (the import's standalone rule)")
	c, ok = catalogctx.Book(nil, bookTitled("What If...?"))
	require.True(t, ok)
	got, err = book.Path(root, nil, c)
	require.NoError(t, err)
	assert.Equal(t, "/data/books/What If", got, "the sanitized title, never the title as written")

	for _, name := range shapes {
		author := &catalogv1alpha1.Author{
			ObjectMeta: metav1.ObjectMeta{Name: "pratchett", Namespace: "default"},
			Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: name}},
		}
		c, ok := catalogctx.Book(author, bookTitled("Guards! Guards!"))
		require.True(t, ok, name)
		file, err := catalogctx.BookFilePath(root, author, c, ".epub")
		require.NoError(t, err, name)
		got, err := book.Path(root, author, c)
		require.NoError(t, err, name)
		assert.Equal(t, filepath.Dir(filepath.Dir(file)), got, "an author named %q: the import places the book two segments under its folder", name)

		author.Status.Path = got
		again, err := catalogctx.BookFilePath(root, author, c, ".epub")
		require.NoError(t, err, name)
		assert.Equal(t, file, again, "the author's written status.path renders the same file path: %q", name)
	}
	author := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "pratchett", Namespace: "default"},
		Spec:       catalogv1alpha1.AuthorSpec{Folder: new("Pratchett, Terry.")},
		Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: "Terry Pratchett"}},
	}
	c, ok = catalogctx.Book(author, bookTitled("Guards! Guards!"))
	require.True(t, ok)
	file, err := catalogctx.BookFilePath(root, author, c, ".epub")
	require.NoError(t, err)
	got, err = book.Path(root, author, c)
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(filepath.Dir(file)), got, "an author's spec.folder decides the book's folder on both sides")
	assert.Equal(t, "/data/books/Pratchett, Terry.", got, "until 2026-11-10 the book's path ignored the author's spec.folder and read the name; the pin is joined as written, trailing dot and all")
}
