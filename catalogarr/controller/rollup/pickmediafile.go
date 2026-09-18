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

package rollup

import (
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// PickMediaFile chooses the MediaFile a catalog item's status should
// reflect, out of every MediaFile whose spec.mediaRef points at it: the one
// flagged Original, else the most recently created, else nil. Movie and
// Episode both call this on the result of their own field-indexed List --
// per the C6 controller amendment, the selection logic lives once here
// rather than as two verbatim copies.
func PickMediaFile(items []catalogv1alpha1.MediaFile) *catalogv1alpha1.MediaFile {
	if len(items) == 0 {
		return nil
	}
	for i := range items {
		if ptr.Deref(items[i].Spec.Original, false) {
			return &items[i]
		}
	}
	best := &items[0]
	for i := 1; i < len(items); i++ {
		if items[i].CreationTimestamp.After(best.CreationTimestamp.Time) {
			best = &items[i]
		}
	}
	return best
}
