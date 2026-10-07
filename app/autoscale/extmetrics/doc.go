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

// Package extmetrics is the External Metrics API inside cmd/manager (spec
// 2026-10-06 §9.4): external.metrics.k8s.io/v1beta1 serving one metric,
// clustarr_consumer_lag{stream,consumer}, hand-rolled on net/http and
// crypto/tls -- no k8s.io/apiserver, k8s.io/metrics or
// k8s.io/kube-aggregator -- with front-proxy client-certificate
// authentication, a self-generated CA kept in a Secret and injected as the
// APIService's caBundle, the work-queue gauge, and the guard that removes
// the APIService when the release namespace is deleted.
package extmetrics

// The External Metrics API's RBAC (spec 2026-10-06 §9.5). secrets: the
// Server reads the TLS Secret, CertManager creates it create-only and
// renews it with a resourceVersion-carrying update. configmaps: the Server
// reads kube-system/extension-apiserver-authentication; the installers
// also bind the extension-apiserver-authentication-reader Role there
// (§10.2.2), and cmd/clustarr.TestEveryBuiltinKindAControllerTouchesHasAnRBACMarker
// needs the read declared here. namespaces: NamespaceGuard reads its own.
// apiservices: CertManager reads and patches spec.caBundle, NamespaceGuard
// deletes, only v1beta1.external.metrics.k8s.io.
//
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get
// +kubebuilder:rbac:groups=apiregistration.k8s.io,resources=apiservices,resourceNames=v1beta1.external.metrics.k8s.io,verbs=get;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
