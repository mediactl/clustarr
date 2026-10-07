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

package admission

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// LoopSource hands the ledger the loop's queue (S22, §5.5): grants,
// directives and changed wait reasons are added at GrantPriority, so they
// jump the LowPriority initial list. Generic, so admission never imports
// app/remediation (which imports admission for FileView).
func LoopSource[K comparable](l *Ledger, toKey func(types.NamespacedName) K) source.TypedSource[K] {
	return source.TypedFunc[K](func(_ context.Context, q workqueue.TypedRateLimitingInterface[K]) error {
		pq, isPQ := q.(priorityqueue.PriorityQueue[K])
		l.setWaker(func(nn types.NamespacedName, prio int) {
			if isPQ {
				pq.AddWithOpts(priorityqueue.AddOpts{Priority: new(prio)}, toKey(nn))
				return
			}
			q.Add(toKey(nn))
		})
		return nil
	})
}
