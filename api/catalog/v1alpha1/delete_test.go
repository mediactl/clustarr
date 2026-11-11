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
package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A Book has had an exclusion kind of its own since 2026-11-10 (the
// import-list sync reads the book's Open Library work id, and a standalone
// book has no container to re-create it), and an Artist and an Author
// since the root scan creates both from their folders (the 2026-11-10
// design's D16, wave G): the scan applier refuses an excluded create. An
// Album's exists (ExclusionKindAlbum, which import's librarydelete writes
// and the sync reads) but is withheld from the dialog until the Artist's
// fan-out honours it, since the fan-out re-creates a deleted album of a
// listed release group whatever the exclusion says; every child kind has
// none.
func TestExclusionKindFor(t *testing.T) {
	for kind, want := range map[commonv1.MediaKind]catalogv1alpha1.ExclusionKind{
		commonv1.MediaKindMovie:     catalogv1alpha1.ExclusionKindMovie,
		commonv1.MediaKindSeries:    catalogv1alpha1.ExclusionKindSeries,
		commonv1.MediaKindArtist:    catalogv1alpha1.ExclusionKindArtist,
		commonv1.MediaKindAuthor:    catalogv1alpha1.ExclusionKindAuthor,
		commonv1.MediaKindBook:      catalogv1alpha1.ExclusionKindBook,
		commonv1.MediaKindAudiobook: catalogv1alpha1.ExclusionKindAudiobook,
		commonv1.MediaKindComic:     catalogv1alpha1.ExclusionKindComic,
	} {
		got, ok := catalogv1alpha1.ExclusionKindFor(kind)
		assert.True(t, ok, kind)
		assert.Equal(t, want, got, kind)
	}
	for _, kind := range []commonv1.MediaKind{
		commonv1.MediaKindEpisode, commonv1.MediaKindIssue, commonv1.MediaKindAlbum,
	} {
		_, ok := catalogv1alpha1.ExclusionKindFor(kind)
		assert.False(t, ok, kind)
	}
}

// An Album and a Book are deletable since the 2026-11-10 non-video
// libraries design (§9.2, §9.3); Episode and Issue still go with their
// parent.
func TestDeletableKinds(t *testing.T) {
	assert.ElementsMatch(t, []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist,
		commonv1.MediaKindAuthor, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
		commonv1.MediaKindAlbum, commonv1.MediaKindBook,
	}, catalogv1alpha1.DeletableKinds())
	assert.NotContains(t, catalogv1alpha1.DeletableKinds(), commonv1.MediaKindEpisode)
	assert.NotContains(t, catalogv1alpha1.DeletableKinds(), commonv1.MediaKindIssue)
}
