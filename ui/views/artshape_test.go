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

package views

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// Music's images are square (owner, 2026-11-16: "use square image
// containers" for the music library and albums): an artist's fanart.tv
// thumb, an album's or an audiobook's cover is 1:1, and a 2:3 poster frame
// cut its sides off. Every other kind keeps the 2:3 poster, a book's cover
// and an author's photo included.
func TestMusicCardsAreSquareAndTheRestPosters(t *testing.T) {
	for kind, want := range map[commonv1.MediaKind]string{
		commonv1.MediaKindArtist:    "--ratio: 1/1",
		commonv1.MediaKindAlbum:     "--ratio: 1/1",
		commonv1.MediaKindAudiobook: "--ratio: 1/1",
		commonv1.MediaKindMovie:     "--ratio: 2/3",
		commonv1.MediaKindSeries:    "--ratio: 2/3",
		commonv1.MediaKindAuthor:    "--ratio: 2/3",
		commonv1.MediaKindBook:      "--ratio: 2/3",
		commonv1.MediaKindComic:     "--ratio: 2/3",
	} {
		li := projection.LibraryItem{Kind: kind, Title: "X", Ref: types.NamespacedName{Namespace: "ns", Name: "x"}, Poster: "/art/x"}
		var buf bytes.Buffer
		require.NoError(t, libraryCard(li).Render(context.Background(), &buf))
		assert.Contains(t, buf.String(), want, "%s card", kind)
	}
}

// An artist's, an album's and an audiobook's page frames its image square,
// art or no art yet; the header search's and Add New's thumbnails of those
// kinds are square too.
func TestMusicPagesAndThumbnailsAreSquare(t *testing.T) {
	for _, url := range []string{"/art/x", ""} {
		var buf bytes.Buffer
		require.NoError(t, squarePoster(url, "X", "k").Render(context.Background(), &buf))
		assert.Contains(t, buf.String(), "aspect-square", "art %q", url)
		assert.NotContains(t, buf.String(), "aspect-[2/3]", "art %q", url)
	}
	assert.Contains(t, artThumbClass(commonv1.MediaKindArtist), "w-12")
	assert.Contains(t, artThumbClass(commonv1.MediaKindMovie), "w-8")
	assert.Contains(t, addThumbClass(commonv1.MediaKindAudiobook), "aspect-square")
	assert.Contains(t, addThumbClass(commonv1.MediaKindMovie), "aspect-[2/3]")
}
