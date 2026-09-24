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

package captionarr

import "testing"

// TestRolesSelectTheirHalves pins what each --role starts. RoleAll is what
// `clustarr all` runs (X14): the controller role alone plans requests and
// publishes fetch tasks that nothing in that one process would consume.
func TestRolesSelectTheirHalves(t *testing.T) {
	for _, tc := range []struct {
		role                 Role
		valid                bool
		controllers, workers bool
	}{
		{RoleController, true, true, false},
		{RoleWorker, true, false, true},
		{RoleAll, true, true, true},
		{"controller,worker", false, false, false},
		{"", false, false, false},
	} {
		if got := tc.role.Valid(); got != tc.valid {
			t.Errorf("Role(%q).Valid() = %v, want %v", tc.role, got, tc.valid)
		}
		if got := tc.role.RunsControllers(); got != tc.controllers {
			t.Errorf("Role(%q).RunsControllers() = %v, want %v", tc.role, got, tc.controllers)
		}
		if got := tc.role.RunsWorkers(); got != tc.workers {
			t.Errorf("Role(%q).RunsWorkers() = %v, want %v", tc.role, got, tc.workers)
		}
	}
}
