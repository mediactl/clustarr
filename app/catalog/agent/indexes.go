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

package agent

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// AssertIndexes adds a runnable that, once the caches have synced, issues
// one cached List per declared index and fails the manager if any is not
// registered (generalised from catalogarr's assertWorkerIndexes). The cache
// answers a MatchingFields on an unregistered index with an error that the
// readers swallow by design, so this is the one moment the difference
// between "live" and "silently inert" is observable. It costs one empty,
// cache-served List per index, once per process. No indexes, no runnable.
func AssertIndexes(mgr ctrl.Manager, ix []k8s.FieldIndex) error {
	if len(ix) == 0 {
		return nil
	}
	lists := make([]client.ObjectList, len(ix))
	for i, fi := range ix {
		l, err := listFor(mgr.GetScheme(), fi.Object)
		if err != nil {
			return fmt.Errorf("catalog agent: field index %q: %w", fi.Name, err)
		}
		lists[i] = l
	}
	// k8s.EveryReplica: a bare RunnableFunc would sit behind the lease and
	// never assert on a replica that loses it.
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return nil // shutting down
		}
		c := mgr.GetClient()
		for i, fi := range ix {
			list, _ := lists[i].DeepCopyObject().(client.ObjectList)
			if err := c.List(ctx, list, client.MatchingFields{fi.Name: "startup-probe"}); err != nil {
				return fmt.Errorf(
					"catalog agent: field index %q is not registered on this manager, so its readers would run "+
						"degraded -- an unregistered index reads as empty: %w", fi.Name, err)
			}
		}
		logging.FromContext(ctx).Info("catalog agent: declared field indexes are live", "indexes", len(ix))
		<-ctx.Done()
		return nil
	}))
}

// listFor is an empty list of obj's kind, through the scheme.
func listFor(scheme *runtime.Scheme, obj client.Object) (client.ObjectList, error) {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	if err != nil {
		return nil, err
	}
	o, err := scheme.New(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err != nil {
		return nil, err
	}
	l, ok := o.(client.ObjectList)
	if !ok {
		return nil, fmt.Errorf("%sList is not a list", gvk.Kind)
	}
	return l, nil
}
