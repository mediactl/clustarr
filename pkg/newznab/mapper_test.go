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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/newznab"
)

func TestCategoryMapperKindRoundTripsByKind(t *testing.T) {
	m := newznab.CategoryMapper{}
	for _, kind := range []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries,
		commonv1.MediaKindAlbum, commonv1.MediaKindAudiobook,
		commonv1.MediaKindBook, commonv1.MediaKindComic,
	} {
		ids := newznab.ByKind(kind)
		got, ok := m.Kind(ids)
		assert.True(t, ok, "ByKind(%s) must map back", kind)
		assert.Equal(t, kind, got)
	}
}

func TestCategoryMapperKindUnknownFamily(t *testing.T) {
	m := newznab.CategoryMapper{}
	_, ok := m.Kind([]newznab.CategoryID{4050}) // PC/Games -- no Clustarr kind
	assert.False(t, ok)
}
