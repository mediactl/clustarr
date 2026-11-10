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

package author

import (
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// Rollup counts the Author's books and those with an imported file
// (status.bookCount, status.bookFileCount).
func Rollup(books []catalogv1alpha1.Book) (bookCount, bookFileCount int32) {
	bookCount = int32(len(books))
	for _, b := range books {
		if b.Status.HasFile {
			bookFileCount++
		}
	}
	return bookCount, bookFileCount
}

// Children is what the Author's container rollup reads from each Book
// (rollup.Container, 2026-11-10 design §4.1): its monitor flag (true when
// unset, the Book reconciler's reading), its phase, its file and the work's
// first publication date.
func Children(books []catalogv1alpha1.Book) []rollup.ContainerChild {
	out := make([]rollup.ContainerChild, 0, len(books))
	for i := range books {
		bk := &books[i]
		c := rollup.ContainerChild{
			Monitored: ptr.Deref(bk.Spec.Monitored, true),
			Phase:     bk.Status.Phase,
			HasFile:   bk.Status.HasFile,
		}
		if bk.Status.Metadata != nil {
			c.ReleaseDate = bk.Status.Metadata.ReleaseDate
		}
		out = append(out, c)
	}
	return out
}
