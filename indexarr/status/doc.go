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

// The RBAC markers live here, on the package that actually performs the
// write, rather than on whichever controller happens to call it. They are
// package-level comments on purpose: controller-gen collects markers only
// from package-level comments and silently ignores one attached to a
// function, which in Phase C meant four controllers' rules were never
// generated at all -- and every envtest still passed, because envtest does
// not enforce RBAC.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers/status,verbs=get;update;patch
package status
