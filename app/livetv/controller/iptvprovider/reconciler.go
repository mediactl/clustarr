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

package iptvprovider

import (
	"context"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/index"
	"github.com/mediactl/clustarr/app/livetv/tuner"
)

// Tuner is what the reconciler needs of app/livetv/tuner.
type Tuner interface {
	Apply(tuner.Config)
	Remove(types.NamespacedName)
	Snapshot(types.NamespacedName) (tuner.Snapshot, bool)
}

// Reconciler reconciles IPTVProvider.
type Reconciler struct {
	Client client.Client
	// SecretReader reads the provider's Secrets by name: the manager caches
	// no Secret, so this is its API reader.
	SecretReader client.Reader
	Tuner        Tuner
	// Address is the livetv Service, host:port, where the Plex pods'
	// proxies send each block's port (Live TV spec §5.0).
	Address string
	// Ports is the range blocks' ports come from.
	Ports PortRange
	// Events carries the tuner's "snapshot changed" notices.
	Events <-chan event.GenericEvent
	// Now is the clock for Lease expiry; time.Now when nil.
	Now func() time.Time

	ports ports
}

// Reconcile converges one provider.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	key := req.NamespacedName
	var p clustarrv1.IPTVProvider
	if err := r.Client.Get(ctx, key, &p); err != nil {
		if apierrors.IsNotFound(err) {
			r.Tuner.Remove(key)
			r.ports.set(key, nil)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !p.DeletionTimestamp.IsZero() {
		r.Tuner.Remove(key)
		r.ports.set(key, nil)
		return ctrl.Result{}, nil
	}

	urls, valid := r.resolveURLs(ctx, &p)
	channels, err := index.ChannelsOf(ctx, r.Client, p.Namespace, p.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	pl, err := r.planBlocks(ctx, &p, channels)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if err := r.owners(ctx, p.Namespace, pl.blocks, now()); err != nil {
		return ctrl.Result{}, err
	}
	held := make([]int32, 0, len(pl.blocks))
	for _, b := range pl.blocks {
		held = append(held, b.Port)
	}
	r.ports.set(key, held)

	switch {
	case valid.Status != "True":
		// The tuner keeps serving what it last had: a deleted Secret must
		// not take a device off Plex.
	case !p.Spec.EnabledOrDefault():
		r.Tuner.Remove(key)
		return ctrl.Result{}, r.writeStatus(ctx, &p, pl, valid, true)
	default:
		r.Tuner.Apply(buildConfig(&p, tunerBlocks(pl), urls, channels))
	}
	return ctrl.Result{}, r.writeStatus(ctx, &p, pl, valid, false)
}

// SetupWithManager registers the controller, its channel index, and its
// watches: the provider, its channels, every other provider (the rules
// across providers, spec §3.1), the blocks' Leases, and the tuner.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := index.Register(context.Background(), mgr); err != nil {
		return err
	}
	toProvider := func(_ context.Context, o client.Object) []reconcile.Request {
		ch, ok := o.(*clustarrv1.IPTVChannel)
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ch.Namespace, Name: ch.Spec.ProviderRef}}}
	}
	// Providers are few, so a change to one re-checks every other.
	toOthers := func(ctx context.Context, o client.Object) []reconcile.Request {
		var all clustarrv1.IPTVProviderList
		if err := mgr.GetClient().List(ctx, &all); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(all.Items))
		for _, p := range all.Items {
			if p.Namespace != o.GetNamespace() || p.Name != o.GetName() {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: p.Namespace, Name: p.Name}})
			}
		}
		return out
	}
	leaseOwner := func(_ context.Context, o client.Object) []reconcile.Request {
		name := o.GetLabels()[LabelLeaseProvider]
		if name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: name}}}
	}
	hasProviderLabel := predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.GetLabels()[LabelLeaseProvider]
		return ok
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("iptvprovider").
		For(&clustarrv1.IPTVProvider{}).
		Watches(&clustarrv1.IPTVChannel{}, handler.EnqueueRequestsFromMapFunc(toProvider)).
		Watches(&clustarrv1.IPTVProvider{}, handler.EnqueueRequestsFromMapFunc(toOthers)).
		Watches(&coordinationv1.Lease{}, handler.EnqueueRequestsFromMapFunc(leaseOwner), builder.WithPredicates(hasProviderLabel))
	if r.Events != nil {
		b = b.WatchesRawSource(source.Channel(r.Events, &handler.EnqueueRequestForObject{}))
	}
	return b.Complete(r)
}
