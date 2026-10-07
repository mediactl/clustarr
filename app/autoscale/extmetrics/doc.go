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
