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

// Package itemstatus is the remediation loop's one item status write (loop
// spec §3.12, applyItemStatus): a Movie's, Episode's, Album's, Book's,
// Audiobook's or Issue's status under catalogarr, preconditioned on the
// object the reconcile planned from. It lives apart from rollup, which the
// catalog agent links, so a catalogarr write is reachable from the manager
// alone (split spec §5.15, TestEveryFieldManagerHasItsHome).
package itemstatus

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// ConflictRequeue is how soon an item whose apply met a newer object is
// reconciled again (loop spec §3.12).
const ConflictRequeue = time.Second

// Apply applies ac -- the item's complete status, from its one renderer --
// under k8s.ManagerCatalogarr with item's resourceVersion as a precondition:
// k8s.PatchStatusCAS's single attempt over the planned view, without the
// read, since the view is item itself. conflicted reports that the
// apiserver holds a newer object (catalogarr-grab, catalogarr-metadata or
// catalogarr-series wrote since the cache read), so the caller requeues in
// ConflictRequeue and plans again from the newer cache instead of rolling
// that write back with an apply seeded from a stale read. An item apply is
// never skipped: the loop does not own the whole item status, and the cache
// strips managedFields.
func Apply[A k8s.CASApplyConfiguration[A]](ctx context.Context, c client.Client, item client.Object, ac A) (conflicted bool, err error) {
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, ac.WithResourceVersion(item.GetResourceVersion())); err != nil {
		if apierrors.IsConflict(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// Requeue is what a reconcile returns after an Apply that did not fail:
// ConflictRequeue after a conflict, nothing otherwise.
func Requeue(conflicted bool) reconcile.Result {
	if conflicted {
		return reconcile.Result{RequeueAfter: ConflictRequeue}
	}
	return reconcile.Result{}
}
