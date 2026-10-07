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

// Package iptvprovider reconciles IPTVProvider (Live TV spec 2026-10-07
// §6): it gives each provider a Service in the manager's namespace, the
// address Plex is registered with; reads the provider's Secrets and hands
// the tuner a resolved configuration; and writes the provider's status, the
// sole writer under k8s.ManagerLiveTV, from the tuner's snapshot. It does no
// fetching: that is the tuner's.
//
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvproviders,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups=clustarr.io,resources=iptvchannels,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package iptvprovider
