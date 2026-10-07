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
	// MediaFileStatusBudgetBytes is release N's: its sidecars carry the
	// absolute path beside the name (§2.16), about 131 KB at 32 entries.
	// F9.2 lowers it to 256 << 10 when Sidecar.path goes.
	MediaFileStatusBudgetBytes = 320 << 10
	// MediaFileObjectBudgetBytes bounds the stored object, managedFields
	// included. Release N's is 832 KiB, not §2.11.3's 768: N's sidecars list
	// is a map keyed by that 4,096-byte path, so catalogarr's managedFields
	// entry repeats every key (32 × about 4.2 KB), which the spec's estimate
	// leaves out; the modelled worst case is about 781 KiB. F9.2 restores
	// 768 << 10 with the atomic list. etcd's request limit is 1.5 MiB.
	MediaFileObjectBudgetBytes = 832 << 10
)
