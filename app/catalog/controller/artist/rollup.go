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
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// Rollup counts the Artist's albums and those with at least one imported
// track file (status.albumCount, status.albumFileCount).
func Rollup(albums []catalogv1alpha1.Album) (albumCount, albumFileCount int32) {
	for _, alb := range albums {
		albumCount++
		if alb.Status.TrackFileCount > 0 {
			albumFileCount++
		}
	}
	return albumCount, albumFileCount
}

// Children is what the Artist's container rollup reads from each Album
// (rollup.Container, 2026-11-10 design §4.1): its monitor flag (true when
// unset, the Album reconciler's reading), its phase, its file and the
// release group's earliest release date.
func Children(albums []catalogv1alpha1.Album) []rollup.ContainerChild {
	out := make([]rollup.ContainerChild, 0, len(albums))
	for i := range albums {
		alb := &albums[i]
		c := rollup.ContainerChild{
			Monitored: ptr.Deref(alb.Spec.Monitored, true),
			Phase:     alb.Status.Phase,
			HasFile:   alb.Status.HasFile,
		}
		if alb.Status.Metadata != nil {
			c.ReleaseDate = alb.Status.Metadata.ReleaseDate
		}
		out = append(out, c)
	}
	return out
}
