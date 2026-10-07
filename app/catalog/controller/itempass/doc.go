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

// Package itempass is the contract between the remediation loop's item
// stages and the item kinds' own renderers (ADR-0019 §7.0, ruling R5).
//
// One item pass lands one apply per field manager. The catalogarr apply is
// the kind's own -- each item package renders its rollups there -- and the
// stages' decisions for it (the grab entries, the download phase, the
// one-shot intent nonces, the Episode and Issue views of their container's
// entries) ride the pass's context as a Contribution, which the kind folds in
// through Downloads, Phase and Nonces on every catalogarr apply site: the
// main path, every early return and the deletion path, whether or not a
// stage ran -- the complete-declaration rule (CLAUDE.md, "Server-side apply
// replaces a field manager's ownership set on every apply").
//
// Every other manager's set (catalogarr-grab, catalogarr-metadata,
// catalogarr-artwork) is a Set the loop applies itself, after the kind's
// apply landed, chained by resourceVersion.
//
// The item packages import this package; it imports API types and pkg/k8s
// only, never app/remediation (loop spec §3.12).
package itempass
