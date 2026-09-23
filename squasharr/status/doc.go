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
// write, rather than on whichever controller or worker happens to call it --
// the same placement grabarr/status and indexarr/status use, for the same
// reason.
//
// They are package-level comments on purpose: controller-gen collects markers
// only from package-level comments and silently ignores one attached to a
// function, which in Phase C meant four controllers' rules were never
// generated at all -- and every envtest still passed, because envtest does
// not enforce RBAC.
//
// config/rbac/role.yaml and the chart's copy of it ARE regenerated for these
// four markers, because cmd/clustarr's TestGeneratedRoleCoversEveryStatusWriter
// reads marker TEXT out of the tree and requires the generated Role to grant
// every <resource>/status it finds. A marker without a regeneration is a
// tree-wide test failure, not a note for later.
//
// squasharr's OTHER rules -- the TranscodeProfile and TranscodeJob
// reconcilers' own read/write access, batch/v1 Jobs, MediaFile reads -- are
// not here, because the controllers and worker that need them do not exist
// yet (tasks E-1 through E-3). This package's markers cover exactly what it
// writes: both kinds' /status subresources, plus the read access this
// package's own seed functions need to Get before applying.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles/status,verbs=get;update;patch
package status
