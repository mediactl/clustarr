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

package audiobook

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// resolveBookRef checks whether m.Spec.BookRef, when set, names a Book that
// exists in the same namespace. It never fails the reconcile over a
// dangling reference -- spec.bookRef is an optional link, not a dependency
// the rest of §8.1's Want flow needs -- so the only outcomes are "resolved"
// (true, for both "unset" and "found") and "dangling" (false), plus a
// genuine API error for anything else. Callers turn the bool into
// AudiobookConditionBookRefResolved; this function has no opinion on
// conditions.
func (r *Reconciler) resolveBookRef(ctx context.Context, m *catalogv1alpha1.Audiobook) (resolved bool, err error) {
	if m.Spec.BookRef == nil || *m.Spec.BookRef == "" {
		return true, nil
	}
	var book catalogv1alpha1.Book
	getErr := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: *m.Spec.BookRef}, &book)
	if getErr == nil {
		return true, nil
	}
	if apierrors.IsNotFound(getErr) {
		return false, nil
	}
	return false, getErr
}

// mapBookRef is the reverse direction from a watched Book to every
// Audiobook whose spec.bookRef names it -- so a Book created after its
// Audiobook (the common order: bookRef is optional and often filled in
// later) clears AudiobookConditionBookRefResolved promptly instead of
// waiting on some unrelated event (a metadata refresh up to 30 days away, a
// spec edit, a QualityProfile change) to happen to reconcile the item
// again. Mirrors movie.Reconciler.mapQualityProfile's shape: List against
// the reverse index, in the Book's own namespace since Book and Audiobook
// are both namespaced and a bookRef is never cross-namespace.
func (r *Reconciler) mapBookRef(ctx context.Context, o client.Object) []reconcile.Request {
	book, ok := o.(*catalogv1alpha1.Book)
	if !ok {
		return nil
	}
	var audiobooks catalogv1alpha1.AudiobookList
	if err := r.List(ctx, &audiobooks, client.InNamespace(book.Namespace), client.MatchingFields{audiobookByBookRefIndexKey: book.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(audiobooks.Items))
	for _, a := range audiobooks.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
	}
	return reqs
}
