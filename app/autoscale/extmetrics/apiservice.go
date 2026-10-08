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
	"encoding/base64"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// newAPIServiceCABundle is the one object the manager applies to
// v1beta1.external.metrics.k8s.io: apiVersion, kind, name and
// spec.caBundle (base64, as the API's []byte field encodes). It is
// unstructured, so k8s.io/kube-aggregator is not linked.
func newAPIServiceCABundle(ca []byte) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"caBundle": base64.StdEncoding.EncodeToString(ca)},
	}}
	u.SetGroupVersionKind(APIServiceGVK)
	u.SetName(APIServiceName)
	return u
}

// applyCABundle server-side applies newAPIServiceCABundle under
// clustarr-autoscale with force-ownership, as k8s.Apply does for a typed
// configuration. It goes through the client's unstructured path, never
// k8s.Apply's typed one: the typed client encodes PatchOptions through the
// scheme's parameter codec, and the manager's scheme has no
// apiregistration.k8s.io/v1, so every injection was refused ("v1.PatchOptions
// is not suitable for converting to apiregistration.k8s.io/v1"; found by
// TestCAInjectorOwnsOnlyCABundle). The unstructured client sends the
// options unconverted.
func applyCABundle(ctx context.Context, c client.Client, ca []byte) error {
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(newAPIServiceCABundle(ca)),
		client.FieldOwner(k8s.ManagerAutoscale.String()), client.ForceOwnership)
}
