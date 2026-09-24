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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types reported on an IndexerProxy.
const (
	// IndexerProxyConditionReady is True when the proxy is reachable.
	IndexerProxyConditionReady = "Ready"
)

// IndexerProxyType is the kind of proxy.
//
// +kubebuilder:validation:Enum=flaresolverr;http;socks4;socks5
type IndexerProxyType string

// Indexer proxy types.
const (
	IndexerProxyTypeFlareSolverr IndexerProxyType = "flaresolverr"
	IndexerProxyTypeHTTP         IndexerProxyType = "http"
	IndexerProxyTypeSocks4       IndexerProxyType = "socks4"
	IndexerProxyTypeSocks5       IndexerProxyType = "socks5"
)

// IndexerProxySpec defines the desired state of IndexerProxy.
type IndexerProxySpec struct {
	// Type is the kind of proxy.
	// +required
	Type IndexerProxyType `json:"type"`

	// Host is the proxy hostname or IP address.
	// +required
	Host string `json:"host"`

	// Port is the proxy port. It is required and has no default: a proxy is
	// addressed as host:port, and the defensible values are per-type
	// (flaresolverr 8191, http 3128/8080/8888, socks 1080), so any one
	// default would probe an endpoint nobody configured and report it Ready.
	// Before this was +required the CRD accepted a portless proxy that the
	// controller then always refused as an invalid spec.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// SecretRef names a Secret in the same namespace holding proxy credentials.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// RequestTimeout is how long a request through the proxy may take. A Go
	// client always sends a Duration, so the prober floors a zero (or
	// negative) one to this default: a probe that may never return is never
	// meant.
	// +optional
	// +kubebuilder:default="60s"
	RequestTimeout metav1.Duration `json:"requestTimeout,omitempty"`

	// Selector matches the Indexers (by label) that use this proxy. An empty
	// selector matches no Indexer: a Go client always sends {}, so "{}
	// matches everything" would route a whole namespace. An Indexer's own
	// spec.proxyRef wins over any selector. At most one http, socks4 or
	// socks5 proxy and at most one FlareSolverr may apply to an Indexer, and
	// the FlareSolverr is applied last; anything more, or a FlareSolverr
	// beside a proxy with credentials, fails closed (app/indexer/proxy).
	// +optional
	Selector metav1.LabelSelector `json:"selector,omitempty"`
}

// IndexerProxyStatus defines the observed state of IndexerProxy.
type IndexerProxyStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the proxy's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// LastCheckedAt is when the proxy was last probed.
	// +optional
	LastCheckedAt *metav1.Time `json:"lastCheckedAt,omitempty"`

	// Version is the proxy software version reported on the last probe.
	// +optional
	Version string `json:"version,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:ac:generate=true
// +kubebuilder:resource:scope=Namespaced,shortName=idxproxy,categories=clustarr
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.host`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// IndexerProxy is an HTTP, SOCKS or FlareSolverr proxy that selected
// Indexers route their requests through.
type IndexerProxy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IndexerProxySpec   `json:"spec,omitempty"`
	Status IndexerProxyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:ac:generate=true

// IndexerProxyList contains a list of IndexerProxy.
type IndexerProxyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IndexerProxy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IndexerProxy{}, &IndexerProxyList{})
}
