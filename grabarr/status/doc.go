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
// config/rbac/role.yaml and the chart's copy of it ARE regenerated for these
// two markers, because cmd/clustarr's TestGeneratedRoleCoversEveryStatusWriter
// reads marker TEXT out of the tree and requires the generated Role to grant
// every <resource>/status it finds. A marker without a regeneration is a
// tree-wide test failure, not a note for later -- which is the point of that
// guard: it refuses to let a permission exist only as a comment.
//
// grabarr's OTHER rules -- downloadclients, the engine StatefulSet, the
// blocklist sweep -- are not here, because the controllers that need them do
// not exist yet. Task D2-8 regenerates again once they do.
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/status,verbs=get;update;patch
package status
