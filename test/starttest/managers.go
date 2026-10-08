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

package starttest

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// StatusManagers is the set of field managers with an entry on the status
// subresource. Values alone cannot show an over-claim (every apply forces
// ownership, CLAUDE.md), so a test that means to catch one reads this.
func StatusManagers(entries []metav1.ManagedFieldsEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		if e.Subresource == "status" {
			out[e.Manager] = true
		}
	}
	return out
}
