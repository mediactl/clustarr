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
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestStatusLabelReadsAnUnmonitoredContainerAsUnmonitored: the Status fact
// of an artist, an author or a comic with nothing on disk and nothing
// wanted reads Unmonitored, as a series' does, not Radarr's Missing, which
// stays the word for a movie or a book without a file (2026-11-10 design
// §9.1); a container with missing children reads Missing.
func TestStatusLabelReadsAnUnmonitoredContainerAsUnmonitored(t *testing.T) {
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAuthor, commonv1.MediaKindComic, commonv1.MediaKindSeries} {
		require.Equal(t, "Unmonitored", statusLabel(projection.LibraryItem{Kind: kind, Monitored: false, Phase: "Unmonitored"}), kind)
		require.Equal(t, "Unmonitored", statusLabel(projection.LibraryItem{Kind: kind, Monitored: true, Phase: "Ready"}), "%s with nothing wanted", kind)
	}
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAuthor, commonv1.MediaKindComic} {
		require.Equal(t, "Missing", statusLabel(projection.LibraryItem{Kind: kind, Monitored: true, MissingChildren: 2}), kind)
		require.Equal(t, "Downloaded", statusLabel(projection.LibraryItem{Kind: kind, Monitored: true, HasFile: true}), kind)
	}
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindBook, commonv1.MediaKindAudiobook} {
		require.Equal(t, "Missing", statusLabel(projection.LibraryItem{Kind: kind, Monitored: false, Phase: "Unmonitored"}), kind)
	}
}

// TestCardLabelAndTabWordsForTheSixTabs: a card without a year is its title
// alone (an artist's, an author's; 2026-11-10 design §9.1), and each tab
// names what it holds and what it adds (design D1).
func TestCardLabelAndTabWordsForTheSixTabs(t *testing.T) {
	require.Equal(t, "Björk", cardLabel(projection.LibraryItem{Title: "Björk", Kind: commonv1.MediaKindArtist}))
	require.Equal(t, "Saga (2012)", cardLabel(projection.LibraryItem{Title: "Saga", Year: 2012, Kind: commonv1.MediaKindComic}))
	for tab, want := range map[projection.Tab][3]string{
		projection.TabMovies:     {"Movies", "movie", "movies"},
		projection.TabTV:         {"TV", "series", "series"},
		projection.TabMusic:      {"Music", "artist", "artists"},
		projection.TabBooks:      {"Books", "author", "authors"},
		projection.TabComics:     {"Comics", "comic", "comics"},
		projection.TabAudiobooks: {"Audiobooks", "audiobook", "audiobooks"},
	} {
		require.Equal(t, want[0], tabLabel(tab), tab)
		require.Equal(t, want[1], addLabel(tab), tab)
		require.Equal(t, want[2], findNoun(tab), tab)
	}
}
