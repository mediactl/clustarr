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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/audiobook"
	"github.com/mediactl/clustarr/pkg/naming"
)

// TestPath's expected folder literal is pkg/naming/book_test.go's own
// TestAudiobookFile golden ("Terry Pratchett/Discworld/8 - 1989 - Guards!
// Guards! Nigel Planer"), confirmed rather than guessed: preset.go's
// audiobookFolderTemplate and book.go's audiobookFileTemplate are the same
// literal string, so BuildFolder(MediaKindAudiobook, c) -- what audiobook.Path
// calls -- renders identically to the AudiobookFile golden for the same
// Context.
func TestPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{})
	ctx := naming.Context{
		Kind:       commonv1.MediaKindAudiobook,
		AuthorName: "Terry Pratchett", BookSeries: "Discworld", BookSeriesPosition: "8",
		Year: 1989, BookTitle: "Guards! Guards!", Narrator: "Nigel Planer",
	}

	got, err := audiobook.Path("/data/media/audiobooks", nil, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer", got)

	override := "Guards! Guards! (Unabridged)"
	got, err = audiobook.Path("/data/media/audiobooks", &override, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/Guards! Guards! (Unabridged)", got)

	empty := ""
	got, err = audiobook.Path("/data/media/audiobooks", &empty, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/audiobooks/Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer", got, "an empty override falls back to the engine")
}
