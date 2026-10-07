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

// apiServiceCABundle is the one apply configuration the manager sends to
// v1beta1.external.metrics.k8s.io: apiVersion, kind, name and
// spec.caBundle. It implements k8s.ApplyConfiguration by hand, since
// controller-runtime's typed Apply needs only the four getters and a REST
// mapping (client/typed_client.go:151-186), so k8s.io/kube-aggregator is
// not linked.
type apiServiceCABundle struct {
	Kind       *string            `json:"kind,omitempty"`
	APIVersion *string            `json:"apiVersion,omitempty"`
	Metadata   apiServiceMetadata `json:"metadata"`
	Spec       apiServiceSpec     `json:"spec"`
}

type apiServiceMetadata struct {
	Name *string `json:"name,omitempty"`
}

type apiServiceSpec struct {
	CABundle []byte `json:"caBundle,omitempty"`
}

func newAPIServiceCABundle(ca []byte) *apiServiceCABundle {
	kind, apiVersion, name := APIServiceGVK.Kind, APIServiceGVK.GroupVersion().String(), APIServiceName
	return &apiServiceCABundle{
		Kind: &kind, APIVersion: &apiVersion,
		Metadata: apiServiceMetadata{Name: &name},
		Spec:     apiServiceSpec{CABundle: ca},
	}
}

func (*apiServiceCABundle) IsApplyConfiguration()    {}
func (a *apiServiceCABundle) GetName() *string       { return a.Metadata.Name }
func (*apiServiceCABundle) GetNamespace() *string    { return nil }
func (a *apiServiceCABundle) GetKind() *string       { return a.Kind }
func (a *apiServiceCABundle) GetAPIVersion() *string { return a.APIVersion }
