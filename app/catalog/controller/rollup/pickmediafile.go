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
// reflect, out of every MediaFile whose spec.mediaRef points at it: the most
// recently created of those flagged Original, else the most recently created
// of all, else nil; a creation-time tie goes to the greater name. Movie and
// Episode both call this on the result of their own field-indexed List --
// per the C6 controller amendment, the selection logic lives once here
// rather than as two verbatim copies.
//
// The choice must not depend on list order. spec.original defaults to true,
// so two freshly imported files for one item -- an upgrade whose importer
// has not yet removed the file it replaces -- are both flagged, and this
// used to return whichever the cache listed first. The item's fileRef then
// flapped between the two from one reconcile to the next, and since the
// gap-fix wave each flap is a mediafile.replaced event in the item's history.
func PickMediaFile(items []catalogv1alpha1.MediaFile) *catalogv1alpha1.MediaFile {
	var best, bestOriginal *catalogv1alpha1.MediaFile
	for i := range items {
		mf := &items[i]
		if newer(mf, best) {
			best = mf
		}
		if ptr.Deref(mf.Spec.Original, false) && newer(mf, bestOriginal) {
			bestOriginal = mf
		}
	}
	if bestOriginal != nil {
		return bestOriginal
	}
	return best
}

func newer(a, b *catalogv1alpha1.MediaFile) bool {
	if b == nil {
		return true
	}
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.After(b.CreationTimestamp.Time)
	}
	return a.Name > b.Name
}
