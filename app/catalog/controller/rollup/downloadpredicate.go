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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// DownloadPredicate is S9 (loop spec §3.3, §3.12), the Download predicate
// the six item kinds share: a spec change (Create always passes), a
// status.phase transition and the deletion timestamp appearing, as each
// package's downloadPredicate had them, plus a change of
// DownloadNonTerminal, which reads status.import too -- an import importarr
// holds for a person leaves the phase Completed, but the Download is no
// longer the item's active download, and the old predicate missed that
// edge.
//
// GenerationChanged alone would never fire on the transitions this watch
// exists for: grabarr writes status.phase through k8s.PatchStatus, which
// never touches generation. The deletion arm is there because a Download
// being torn down stops counting as the item's active download the moment
// it is marked, not when its finalizers finally let it go.
func DownloadPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) downloadv1alpha1.DownloadPhase {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok {
				return ""
			}
			return dl.Status.Phase
		}),
		k8s.StatusFieldChanged(k8s.IsDeleting),
		k8s.StatusFieldChanged(func(o client.Object) bool {
			dl, ok := o.(*downloadv1alpha1.Download)
			return ok && DownloadNonTerminal(dl)
		}),
	)
}
