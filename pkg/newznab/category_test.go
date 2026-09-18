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

package newznab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/newznab"
)

func TestTreeCoversTheFiveFamiliesClustarrUses(t *testing.T) {
	tree := newznab.Tree()
	byID := map[newznab.CategoryID]newznab.Category{}
	for _, c := range tree {
		byID[c.ID] = c
	}
	for _, id := range []newznab.CategoryID{newznab.CatMovies, newznab.CatAudio, newznab.CatTV, newznab.CatBooks, newznab.CatOther} {
		require.Contains(t, byID, id)
	}
	require.Contains(t, byID[newznab.CatTV].Sub, newznab.SubCategory{ID: newznab.CatTVAnime, Name: "TV/Anime"})
}

func TestParent(t *testing.T) {
	cases := []struct {
		name string
		id   newznab.CategoryID
		want newznab.CategoryID
	}{
		{"leaf", newznab.CatMoviesHD, newznab.CatMovies},
		{"already a parent", newznab.CatTV, newznab.CatTV},
		{"custom id is its own parent", newznab.CustomCategoryOffset + 42, newznab.CustomCategoryOffset + 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.id.Parent())
		})
	}
}

func TestExpandAddsSubcatsAndLeavesCustomIdsAlone(t *testing.T) {
	got := newznab.Expand([]newznab.CategoryID{newznab.CatMovies, newznab.CustomCategoryOffset + 7})
	assert.Contains(t, got, newznab.CatMovies)
	assert.Contains(t, got, newznab.CatMoviesHD)
	assert.Contains(t, got, newznab.CatMoviesUHD)
	assert.Contains(t, got, newznab.CustomCategoryOffset+7)
	assert.Len(t, got, len(newznab.Tree()[0].Sub)+1+1, "one parent's leaves, the parent itself, and the untouched custom id")
}

func TestCustomIsStableAndWithinTheOffsetRange(t *testing.T) {
	a := newznab.Custom("28")
	b := newznab.Custom("28")
	c := newznab.Custom("29")
	assert.Equal(t, a, b, "same tracker id must hash to the same category id")
	assert.NotEqual(t, a, c)
	assert.GreaterOrEqual(t, int32(a), int32(newznab.CustomCategoryOffset))
	assert.LessOrEqual(t, int32(a), int32(newznab.CustomCategoryOffset)+0xFFFF)
}

func TestByKind(t *testing.T) {
	cases := []struct {
		kind commonv1.MediaKind
		want []newznab.CategoryID
	}{
		{commonv1.MediaKindMovie, []newznab.CategoryID{newznab.CatMovies}},
		{commonv1.MediaKindSeries, []newznab.CategoryID{newznab.CatTV}},
		{commonv1.MediaKindEpisode, []newznab.CategoryID{newznab.CatTV}},
		{commonv1.MediaKindAlbum, []newznab.CategoryID{newznab.CatAudio}},
		{commonv1.MediaKindAudiobook, []newznab.CategoryID{newznab.CatAudioAudiobook}},
		{commonv1.MediaKindBook, []newznab.CategoryID{newznab.CatBooksEBook}},
		{commonv1.MediaKindComic, []newznab.CategoryID{newznab.CatBooksComics}},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			assert.Equal(t, tc.want, newznab.ByKind(tc.kind))
		})
	}
}
