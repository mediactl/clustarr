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

package audiobook

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// This file is an internal (package audiobook, not audiobook_test) test so
// it can exercise namingContext and joinNames directly -- both unexported,
// since nothing outside this package needs to build a naming.Context from
// an AudiobookMetadata.

func TestNamingContextNilMetadataIsTheZeroContext(t *testing.T) {
	ctx := namingContext(catalogv1alpha1.AudiobookSpec{}, nil)
	assert.Equal(t, "", ctx.BookTitle)
	assert.Equal(t, "", ctx.AuthorName)
	assert.Equal(t, "", ctx.Narrator)
	assert.Zero(t, ctx.Year)
}

func TestNamingContextMapsMetadataFields(t *testing.T) {
	release := metav1.NewTime(time.Date(1989, time.January, 1, 0, 0, 0, 0, time.UTC))
	meta := &catalogv1alpha1.AudiobookMetadata{
		Title:       "Guards! Guards!",
		Authors:     []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}},
		Narrators:   []string{"Nigel Planer"},
		ReleaseDate: &release,
		Series:      &catalogv1alpha1.SeriesLink{Series: "Discworld", Position: "8"},
	}

	ctx := namingContext(catalogv1alpha1.AudiobookSpec{}, meta)
	assert.Equal(t, "Guards! Guards!", ctx.BookTitle)
	assert.Equal(t, "Terry Pratchett", ctx.AuthorName)
	assert.Equal(t, "Nigel Planer", ctx.Narrator)
	assert.Equal(t, 1989, ctx.Year)
	assert.Equal(t, "Discworld", ctx.BookSeries)
	assert.Equal(t, "8", ctx.BookSeriesPosition)
}

func TestNamingContextJoinsMultipleAuthorsAndNarrators(t *testing.T) {
	meta := &catalogv1alpha1.AudiobookMetadata{
		Authors:   []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}, {Name: "Neil Gaiman"}},
		Narrators: []string{"Nigel Planer", "Some Narrator"},
	}
	ctx := namingContext(catalogv1alpha1.AudiobookSpec{}, meta)
	assert.Equal(t, "Terry Pratchett, Neil Gaiman", ctx.AuthorName)
	assert.Equal(t, "Nigel Planer, Some Narrator", ctx.Narrator)
}

func TestJoinNames(t *testing.T) {
	assert.Equal(t, "", joinNames(nil))
	assert.Equal(t, "A", joinNames([]catalogv1alpha1.NamedRef{{Name: "A"}}))
	assert.Equal(t, "A, B", joinNames([]catalogv1alpha1.NamedRef{{Name: "A"}, {Name: "B"}}))
}

// TestPathAgreesWithTheImport: the folder the controller writes is the one
// the import places the audiobook's parts in (catalogctx.AudiobookFilePath),
// byte for byte, both rendered from one Audiobook -- the controller through
// its own namingContext, the import through catalogctx.Audiobook -- over a
// plain title, "What If...?" (a trailing dot behind a question mark), a
// title of every character the sanitizer rewrites, a trailing dot and a
// 390-byte title, and the same shapes as the author's name, whose segment
// lies above the audiobook's (task F1); with status.path then written, the
// file path stays what it was. Until 2026-11-10 status.path carried the
// title as written while AudiobookFilePath's whole-path sanitize placed the
// parts under the sanitized folders.
func TestPathAgreesWithTheImport(t *testing.T) {
	root := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "audiobooks", Namespace: "default"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/audiobooks", Kind: catalogv1alpha1.RootFolderKindAudiobook},
	}
	release := metav1.NewTime(time.Date(1989, time.November, 1, 0, 0, 0, 0, time.UTC))
	shapes := []string{
		"Plain Title",
		"What If...?",
		`A:B\C/D>E<F?G*H|I"J`,
		"Trailing dot.",
		strings.Repeat("Long ", 78),
	}
	check := func(t *testing.T, ab *catalogv1alpha1.Audiobook, what string) {
		t.Helper()
		c, ok := catalogctx.Audiobook(ab, 0)
		require.True(t, ok, what)
		file, err := catalogctx.AudiobookFilePath(root, ab, c, ".m4b")
		require.NoError(t, err, what)
		got, err := Path(root, ab.Spec.Folder, namingContext(ab.Spec, ab.Status.Metadata))
		require.NoError(t, err, what)
		assert.Equal(t, filepath.Dir(file), got, what)
		for _, comp := range strings.Split(got, "/") {
			assert.LessOrEqual(t, len(comp), naming.DefaultMaxComponentBytes, "a folder component fits the filesystem: %s", what)
		}

		ab.Status.Path = got
		again, err := catalogctx.AudiobookFilePath(root, ab, c, ".m4b")
		require.NoError(t, err, what)
		assert.Equal(t, file, again, "the written status.path renders the same file path: %s", what)
	}
	for _, shape := range shapes {
		check(t, &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "guards", Namespace: "default"},
			Spec:       catalogv1alpha1.AudiobookSpec{RootFolderRef: "audiobooks"},
			Status: catalogv1alpha1.AudiobookStatus{Metadata: &catalogv1alpha1.AudiobookMetadata{
				Title: shape, Authors: []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}}, Narrators: []string{"Nigel Planer"},
				ReleaseDate: &release, Series: &catalogv1alpha1.SeriesLink{Series: "Discworld", Position: "8"},
			}},
		}, "an audiobook titled "+strconv.Quote(shape))
		check(t, &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "guards", Namespace: "default"},
			Spec:       catalogv1alpha1.AudiobookSpec{RootFolderRef: "audiobooks"},
			Status: catalogv1alpha1.AudiobookStatus{Metadata: &catalogv1alpha1.AudiobookMetadata{
				Title: "Guards! Guards!", Authors: []catalogv1alpha1.NamedRef{{Name: shape}}, Narrators: []string{"Nigel Planer"},
			}},
		}, "an audiobook by "+strconv.Quote(shape))
	}
	got, err := Path(root, nil, naming.Context{Kind: commonv1.MediaKindAudiobook, AuthorName: "What If...?", BookTitle: "What If...?"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/What If/What If", got, "the sanitized title, never the title as written")
}
