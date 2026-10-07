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
	"maps"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
)

// A Service names its provider twice: AnnotationProvider ("ns/name") is
// what the Service's watch maps back through, since an owner reference
// cannot cross namespaces; the two labels are for kubectl, and carry a value
// only when it fits a label's 63 characters.
const (
	AnnotationProvider     = "clustarr.io/livetv-provider"
	LabelProviderNamespace = "clustarr.io/livetv-provider-namespace"
	LabelProviderName      = "clustarr.io/livetv-provider-name"
	// servicePort is the port Plex dials: HTTP's default, so the address
	// alone is enough.
	servicePort = 80
)

// ServiceName is the provider namespace/name's Service in the manager's
// namespace.
func ServiceName(namespace, name string) string {
	return "livetv-" + names.HashSuffix(namespace, name)
}

// ensureService creates the provider's Service when absent and returns its
// ClusterIP. A drifted selector or port is updated in place, never by
// deleting and recreating: that keeps the ClusterIP Plex was given.
func (r *Reconciler) ensureService(ctx context.Context, key types.NamespacedName) (string, error) {
	name := types.NamespacedName{Namespace: r.Namespace, Name: ServiceName(key.Namespace, key.Name)}
	labels := map[string]string{
		"app.kubernetes.io/part-of":    "clustarr",
		"app.kubernetes.io/managed-by": "clustarr-livetv",
	}
	for k, v := range map[string]string{LabelProviderNamespace: key.Namespace, LabelProviderName: key.Name} {
		if len(v) <= names.MaxLabelValueLength {
			labels[k] = v
		}
	}
	annotations := map[string]string{AnnotationProvider: key.String()}
	ports := []corev1.ServicePort{{Name: "hdhomerun", Protocol: corev1.ProtocolTCP, Port: servicePort, TargetPort: intstr.FromInt32(r.TargetPort)}}
	var svc corev1.Service
	err := r.Client.Get(ctx, name, &svc)
	switch {
	case apierrors.IsNotFound(err):
		svc = corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: name.Namespace, Labels: labels, Annotations: annotations},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: r.Selector, Ports: ports},
		}
		if err := r.Client.Create(ctx, &svc, client.FieldOwner(k8s.ManagerLiveTV.String())); err != nil {
			return "", fmt.Errorf("create Service %s: %w", name, err)
		}
	case err != nil:
		return "", fmt.Errorf("get Service %s: %w", name, err)
	default:
		drift := !maps.Equal(svc.Spec.Selector, r.Selector) || len(svc.Spec.Ports) != 1 ||
			svc.Spec.Ports[0].Port != servicePort || svc.Spec.Ports[0].TargetPort != ports[0].TargetPort
		for k, v := range labels {
			if svc.Labels[k] != v {
				drift = true
			}
		}
		if svc.Annotations[AnnotationProvider] != key.String() {
			drift = true
		}
		if drift {
			if svc.Labels == nil {
				svc.Labels = map[string]string{}
			}
			if svc.Annotations == nil {
				svc.Annotations = map[string]string{}
			}
			maps.Copy(svc.Labels, labels)
			maps.Copy(svc.Annotations, annotations)
			svc.Spec.Selector, svc.Spec.Ports = r.Selector, ports
			if err := r.Client.Update(ctx, &svc, client.FieldOwner(k8s.ManagerLiveTV.String())); err != nil {
				return "", fmt.Errorf("update Service %s: %w", name, err)
			}
		}
	}
	return svc.Spec.ClusterIP, nil
}

// deleteService removes the provider's Service; an absent one is done.
func (r *Reconciler) deleteService(ctx context.Context, key types.NamespacedName) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: r.Namespace, Name: ServiceName(key.Namespace, key.Name)}}
	if err := r.Client.Delete(ctx, svc); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete Service %s/%s: %w", svc.Namespace, svc.Name, err)
	}
	return nil
}
