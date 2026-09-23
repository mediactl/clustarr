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
// write, rather than on whichever controller or engine happens to call it --
// the same placement indexarr/status uses, for the same reason.
//
// They are package-level comments on purpose: controller-gen collects markers
// only from package-level comments and silently ignores one attached to a
// function, which in Phase C meant four controllers' rules were never
// generated at all -- and every envtest still passed, because envtest does
// not enforce RBAC.
//
// config/rbac/role.yaml is NOT regenerated in D2-0: the download controllers
// that need the rest of grabarr's rules do not exist yet, and role.yaml and
// the chart's copy of it were being rewritten by another task while this
// landed. Task D2-8 owns the regeneration and the chart sync; the markers are
// here so that regeneration picks them up without anyone having to remember.
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/status,verbs=get;update;patch
package status
