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
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Item is one item kind on the remediation loop's queue (loop spec §3.12):
// how to reconcile one item, and every watch that wakes it except its
// MediaFiles', which the loop's file signature (S1') wakes it for. The six
// item packages implement it and never import app/remediation, which adapts
// each Watch to its Key.
type Item interface {
	ReconcileItem(ctx context.Context, nn types.NamespacedName) (reconcile.Result, error)
	Watches() []Watch
}

// Watch is one watch of an item kind on a kind other than MediaFile: the
// watched object, the map from an event's object to the items it wakes, and
// the predicates that pass the event.
type Watch struct {
	Object     client.Object
	Map        handler.MapFunc
	Predicates []predicate.Predicate
}

// Self is the Map of an item's watch on its own kind: the object is the item.
func Self(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
}
