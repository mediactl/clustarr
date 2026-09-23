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

package artist_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/artist"
)

func TestRollup(t *testing.T) {
	albums := []catalogv1alpha1.Album{
		{Status: catalogv1alpha1.AlbumStatus{TrackFileCount: 3}},
		{Status: catalogv1alpha1.AlbumStatus{TrackFileCount: 0}},
		{Status: catalogv1alpha1.AlbumStatus{TrackFileCount: 1}},
	}
	count, fileCount := artist.Rollup(albums)
	assert.EqualValues(t, 3, count)
	assert.EqualValues(t, 2, fileCount, "only albums with at least one imported track count")
}

func TestRollupEmpty(t *testing.T) {
	count, fileCount := artist.Rollup(nil)
	assert.Zero(t, count)
	assert.Zero(t, fileCount)
}
