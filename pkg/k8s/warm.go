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

package k8s

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// InformerGetter is the one cache method a warmer needs.
type InformerGetter interface {
	GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error)
}

// InformerWarmer is an EveryReplica runnable that starts an informer for each
// of objs and holds them until ctx ends. controller-runtime starts an
// informer only when something first asks for it -- a controller's watch, on
// the leader alone, or a field index -- so a standby manager held caches for
// the indexed kinds only; warming the kinds whose initial list dominates a
// cold start is what makes a failover take seconds, not the library's
// minutes (control-plane placement design §5.4.1). It warms nothing else, so
// a standby's memory stays those caches.
func InformerWarmer(c InformerGetter, objs ...client.Object) manager.Runnable {
	return EveryReplica(func(ctx context.Context) error {
		for _, obj := range objs {
			if _, err := c.GetInformer(ctx, obj); err != nil {
				return fmt.Errorf("k8s: warm the %T informer: %w", obj, err)
			}
		}
		<-ctx.Done()
		return nil
	})
}

// WarmInformers adds an [InformerWarmer] over mgr's cache to mgr.
func WarmInformers(mgr ctrl.Manager, objs ...client.Object) error {
	warmer := InformerWarmer(mgr.GetCache(), objs...)
	if err := mgr.Add(warmer); err != nil {
		return fmt.Errorf("k8s: add the informer warmer: %w", err)
	}
	return nil
}

// LeaderGauge is an EveryReplica runnable that reports this replica's
// leadership through set: 0 at start, 1 once elected closes (the manager's
// clustarr_manager_leader). A replica that loses the lease exits, so the
// gauge never goes back to 0 in one process.
func LeaderGauge(elected <-chan struct{}, set func(float64)) EveryReplica {
	return func(ctx context.Context) error {
		set(0)
		select {
		case <-ctx.Done():
			return nil
		case <-elected:
			set(1)
		}
		<-ctx.Done()
		return nil
	}
}
