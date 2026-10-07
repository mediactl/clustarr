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

package extmetrics

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// NamespaceGuard deletes v1beta1.external.metrics.k8s.io on the manager's
// way out when the manager's own namespace is terminating and the
// APIService names this manager's Service (§9.4). Namespace deletion
// stops pods first, so the manager is still running then; without this a
// `kubectl delete ns` leaves an APIService whose discovery fails and every
// namespace deletion in the cluster stalls. `helm uninstall` deletes the
// APIService itself; the guard is a no-op then.
type NamespaceGuard struct {
	Client      client.Client // deletes
	Reader      client.Reader // the manager's APIReader
	Namespace   string
	ServiceName string
	Timeout     time.Duration // 0 means 10 s
}

// NeedLeaderElection is false: every replica checks on its way out.
func (g *NamespaceGuard) NeedLeaderElection() bool { return false }

// Start waits for ctx to end, then checks once with its own deadline.
func (g *NamespaceGuard) Start(ctx context.Context) error {
	<-ctx.Done()
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	log := logging.FromContext(ctx)
	deleted, err := g.Check(cctx)
	switch {
	case err != nil:
		log.Error("extmetrics: could not remove the APIService of a terminating namespace; delete it by hand: kubectl delete apiservice "+APIServiceName, "error", err)
	case deleted:
		log.Info("extmetrics: the release namespace is terminating; deleted the APIService", "apiservice", APIServiceName)
	}
	return nil
}

// Check deletes the APIService when the namespace is terminating and the
// APIService names this namespace and Service.
func (g *NamespaceGuard) Check(ctx context.Context) (bool, error) {
	var ns corev1.Namespace
	if err := g.Reader.Get(ctx, types.NamespacedName{Name: g.Namespace}, &ns); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if ns.DeletionTimestamp.IsZero() {
		return false, nil
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(APIServiceGVK)
	if err := g.Reader.Get(ctx, types.NamespacedName{Name: APIServiceName}, u); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, err
	}
	svcNS, _, _ := unstructured.NestedString(u.Object, "spec", "service", "namespace")
	svcName, _, _ := unstructured.NestedString(u.Object, "spec", "service", "name")
	if svcNS != g.Namespace || svcName != g.ServiceName {
		return false, nil
	}
	uid := u.GetUID()
	if err := g.Client.Delete(ctx, u, client.Preconditions{UID: &uid}); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return true, nil
}
