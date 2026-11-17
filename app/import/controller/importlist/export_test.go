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
package importlist

import (
	"context"
	"errors"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
)

// ApplySeries exposes applySeries to the envtest suite in importlist_test.
var ApplySeries = applySeries

// MarkListed runs one kind's marking pass as diffKind does: the list's
// spec.listed brought to the remembered items, taken off the ones existing
// remembered and remembered no longer does. It returns remembered with
// what each item now carries, and how many items were written and cleared.
func MarkListed(
	ctx context.Context, r *Reconciler, il *catalogv1alpha1.ImportList, kind commonv1.MediaKind,
	existing, remembered []importliststate.StoredItem,
) (out []importliststate.StoredItem, marked, cleared int, err error) {
	marks, err := marksOf(il.Spec.Listed)
	if err != nil {
		return nil, 0, 0, err
	}
	lib, err := loadLibrary(ctx, r.Client, il.Namespace, il.Name, kind)
	if err != nil {
		return nil, 0, 0, err
	}
	res := r.markListed(ctx, il, kind, marks, existing, remembered, lib)
	return remembered, res.marked, res.cleared, errors.Join(res.errs...)
}

// ClearListed exposes clearListed, a deleted list's cleanup.
func ClearListed(ctx context.Context, r *Reconciler, il *catalogv1alpha1.ImportList) error {
	return r.clearListed(ctx, il)
}

// MarksOf exposes marksOf's verdict on a spec.listed block.
func MarksOf(l *catalogv1alpha1.ListedMetadata) error {
	_, err := marksOf(l)
	return err
}
