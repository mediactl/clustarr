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

// Package mediafilestatus is MediaFile status's render boundary (loop spec
// §2.11.1; ADR-0016). The remediation loop (F3.1) passes every status it
// is about to apply through Render, which replaces the runes a server-side
// apply cannot carry, bounds every string to its CRD MaxLength (dropping,
// never truncating, a path, file name or language), keeps a stored value as
// stored, drops duplicate map-list keys, sorts the lists the spec sorts and
// cuts times to whole seconds -- so the one apply is never refused for its
// content, and an unchanged render compares equal to the stored status.
// Bounds is the clamp table, held equal to the CRD by
// TestRenderClampsEveryBoundedString. It imports only api/ and pkg/k8s.
package mediafilestatus
