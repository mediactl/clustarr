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

package remediation

// The loop is the only writer of MediaFile status and applies its labels and
// spec takeover (loop spec §3.14); Events go through events.k8s.io/v1, the
// API mgr.GetEventRecorder's recorder writes. The blank line below keeps the
// markers package-level, where controller-gen collects them.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles/status,verbs=get;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
