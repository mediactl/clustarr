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
// the same placement app/grab/status and app/indexer/status use, for the same
// reason.
//
// They are package-level comments on purpose: controller-gen collects markers
// only from package-level comments and silently ignores one attached to a
// function, which in Phase C meant four controllers' rules were never
// generated at all -- and every envtest still passed, because envtest does
// not enforce RBAC.
//
// config/rbac/role.yaml and the chart's copy of it ARE regenerated for these
// markers, because cmd/clustarr's TestGeneratedRoleCoversEveryStatusWriter
// reads marker TEXT out of the tree and requires the generated Role to grant
// every <resource>/status it finds. A marker without a regeneration is a
// tree-wide test failure, not a note for later.
//
// transcode's OTHER rules -- the TranscodeProfile reconciler's and
// transcode-admission's own read access, batch/v1 Jobs, MediaFile reads --
// sit on those controllers. This package's markers cover exactly what it
// writes: TranscodeProfile's /status subresource, plus the read access its
// callers need to Get before applying. Nothing writes TranscodeJob status
// any more (ADR-0016).
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles/status,verbs=get;update;patch
package status
