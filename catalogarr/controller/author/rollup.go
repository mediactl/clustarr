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
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Rollup derives status.bookCount and status.bookFileCount from an Author's
// currently-owned Book list. Both status.hasFile and status.metadata on each
// Book are written by that Book's own reconciler and the metadata gateway
// respectively (this package's doc.go) -- this is a pure read of fields this
// reconciler never itself sets.
func Rollup(books []catalogv1alpha1.Book) (bookCount, bookFileCount int32) {
	bookCount = int32(len(books))
	for _, b := range books {
		if b.Status.HasFile {
			bookFileCount++
		}
	}
	return bookCount, bookFileCount
}
