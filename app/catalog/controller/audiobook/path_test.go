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

package audiobook_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/pkg/naming"
)

func audiobookRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "audiobooks", Namespace: "default"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindAudiobook},
	}
}

// TestPath's expected folder literal is pkg/naming/book_test.go's own
// TestAudiobookFile golden ("Terry Pratchett/Discworld/8 - 1989 - Guards!
// Guards! Nigel Planer"), confirmed rather than guessed: preset.go's
// audiobookFolderTemplate and book.go's audiobookFileTemplate are the same
// literal string, so BuildFolder(MediaKindAudiobook, c) -- what
// catalogctx.ItemFolder renders for audiobook.Path -- renders identically to
// the AudiobookFile golden for the same Context.
func TestPath(t *testing.T) {
	root := audiobookRoot("/data/media/audiobooks")
	ctx := naming.Context{
		Kind:       commonv1.MediaKindAudiobook,
		AuthorName: "Terry Pratchett", BookSeries: "Discworld", BookSeriesPosition: "8",
		Year: 1989, BookTitle: "Guards! Guards!", Narrator: "Nigel Planer",
	}

	got, err := audiobook.Path(root, nil, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer", got)

	override := "Guards! Guards! (Unabridged)"
	got, err = audiobook.Path(root, &override, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/Guards! Guards! (Unabridged)", got)

	empty := ""
	got, err = audiobook.Path(root, &empty, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer", got, "an empty override falls back to the engine")

	got, err = audiobook.Path(root, nil, naming.Context{Kind: commonv1.MediaKindAudiobook})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks", got, "an audiobook without a title yet lives at the root")

	_, err = audiobook.Path(root, nil, naming.Context{Kind: commonv1.MediaKindAudiobook, BookTitle: "..."})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a title of only dots is an error, never a path")
	_, err = audiobook.Path(root, nil, naming.Context{Kind: commonv1.MediaKindAudiobook, AuthorName: "Terry Pratchett", BookTitle: "???"})
	require.Error(t, err, "a title that sanitizes to nothing is an error, never a path")
}
