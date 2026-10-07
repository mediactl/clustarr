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

package k8s

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/pkg/legacynames"
)

// OwnedBy reports whether the managedFields entry e is field manager m's:
// its manager is m, or m's chart 0.4.x name (legacynames.LegacyManager) on
// an object release N's Migrator has not renamed yet
// (docs/superpowers/specs/2026-10-07-unbranded-names-design.md §4.1.5).
// Every non-test reader of managedFields by manager name goes through it,
// so an unrenamed object reads as the renamed one would. F9.4 removes the
// legacy half.
func OwnedBy(e metav1.ManagedFieldsEntry, m FieldManager) bool {
	if e.Manager == string(m) {
		return true
	}
	old, ok := legacynames.LegacyManager(string(m))
	return ok && e.Manager == old
}
