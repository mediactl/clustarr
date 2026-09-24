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

package artist

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Rollup folds an Artist's owned Albums into the two counts ArtistStatus
// keeps: the total number of Album objects, and how many of them have at
// least one imported track (status.trackFileCount > 0), per
// ArtistStatus.AlbumFileCount's own doc comment ("albums with at least one
// imported file"). Mirrors series.Rollup's shape; there is no per-album
// breakdown analogous to SeasonStatus because Album carries no further
// sub-structure ArtistStatus rolls up.
func Rollup(albums []catalogv1alpha1.Album) (albumCount, albumFileCount int32) {
	for _, alb := range albums {
		albumCount++
		if alb.Status.TrackFileCount > 0 {
			albumFileCount++
		}
	}
	return albumCount, albumFileCount
}
