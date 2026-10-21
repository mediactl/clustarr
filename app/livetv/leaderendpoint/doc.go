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

// Package leaderendpoint publishes the leading manager as the livetv
// Service's only endpoint (control-plane placement design §5.4). The tuner
// runs on the leader alone and a non-leader is not listening on its port, so
// with three manager replicas the Service has no selector: the leader writes
// its EndpointSlice once its tuner listens, and a new leader overwrites it.
//
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;create;update;patch
package leaderendpoint
