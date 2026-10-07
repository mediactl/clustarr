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

package crdcheck

// MediaFile size budgets (loop spec §2.11.3), held by
// TestMediaFileAtEveryCapFitsTheBudget against a MediaFile with every list
// at its MaxItems, every string at its MaxLength and 256 KiB of
// annotations.
const (
	// MediaFileStatusBudgetBytes bounds status: its sidecars carry a name
	// alone, the path being dir(spec.path) (§2.7, §2.15).
	MediaFileStatusBudgetBytes = 256 << 10
	// MediaFileObjectBudgetBytes bounds the stored object, managedFields
	// included: the atomic sidecars list adds no per-entry keys to catalog's
	// managedFields entry. etcd's request limit is 1.5 MiB.
	MediaFileObjectBudgetBytes = 768 << 10
)
