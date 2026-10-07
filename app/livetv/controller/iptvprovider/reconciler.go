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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/index"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Finalizer holds a provider until its Service, in another namespace and so
// beyond an owner reference, is deleted.
const Finalizer = "clustarr.io/livetv-service"

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
	// Namespace is the manager's own namespace, where every provider's
	// Service lives: a selector reaches only its own namespace's pods.
	Namespace string
	// Selector is the manager pod's labels, which each Service selects.
	Selector map[string]string
	// TargetPort is the tuner's port on the manager pod.
	TargetPort int32
	// Events carries the tuner's "snapshot changed" notices.
	Events <-chan event.GenericEvent
}

// Reconcile converges one provider.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p clustarrv1.IPTVProvider
	if err := r.Client.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			r.Tuner.Remove(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	key := req.NamespacedName
	if !p.DeletionTimestamp.IsZero() {
		r.Tuner.Remove(key)
		if err := r.deleteService(ctx, key); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(&p, Finalizer) {
			orig := p.DeepCopy()
			controllerutil.RemoveFinalizer(&p, Finalizer)
			if err := r.Client.Patch(ctx, &p, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}),
				client.FieldOwner(k8s.ManagerLiveTV.String())); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&p, Finalizer) {
		orig := p.DeepCopy()
		controllerutil.AddFinalizer(&p, Finalizer)
		if err := r.Client.Patch(ctx, &p, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}),
			client.FieldOwner(k8s.ManagerLiveTV.String())); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
	}

	address, err := r.ensureService(ctx, key)
	if err != nil {
		return ctrl.Result{}, err
	}
	urls, valid := r.resolveURLs(ctx, &p)
	if valid.Status != "True" {
		// The tuner keeps serving what it last had: a deleted Secret must
		// not take the device off Plex.
		return ctrl.Result{}, r.writeStatus(ctx, &p, address, valid, false)
	}
	if !p.Spec.EnabledOrDefault() {
		r.Tuner.Remove(key)
		return ctrl.Result{}, r.writeStatus(ctx, &p, address, valid, true)
	}
	channels, err := index.ChannelsOf(ctx, r.Client, p.Namespace, p.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	r.Tuner.Apply(buildConfig(&p, address, urls, channels))
	return ctrl.Result{}, r.writeStatus(ctx, &p, address, valid, false)
}

// SetupWithManager registers the controller, its channel index, and its
// watches: the provider, its channels, its Service, and the tuner.
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
	serviceOwner := func(_ context.Context, o client.Object) []reconcile.Request {
		ns, name, ok := strings.Cut(o.GetAnnotations()[AnnotationProvider], "/")
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
	}
	isOurs := predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.GetAnnotations()[AnnotationProvider]
		return ok && o.GetNamespace() == r.Namespace
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("iptvprovider").
		For(&clustarrv1.IPTVProvider{}).
		Watches(&clustarrv1.IPTVChannel{}, handler.EnqueueRequestsFromMapFunc(toProvider)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(serviceOwner), builder.WithPredicates(isOurs))
	if r.Events != nil {
		b = b.WatchesRawSource(source.Channel(r.Events, &handler.EnqueueRequestForObject{}))
	}
	return b.Complete(r)
}
