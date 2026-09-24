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

package schema

import "reflect"

// Diff is the JSON merge patch of spec that takes old to current: a
// changed leaf sent, a key old has and current lacks sent as null (the
// merge-patch way to clear it), objects recursed so an unchanged sibling
// stays out, an array replaced whole when it differs. Empty when nothing
// changed. (Named Diff, not Patch: ui/guard_test.go bans a call named
// Patch outside ui/actions, by selector.)
func Diff(old, current map[string]any) map[string]any {
	out := map[string]any{}
	for k, ov := range old {
		nv, ok := current[k]
		if !ok {
			out[k] = nil
			continue
		}
		om, oIsObj := ov.(map[string]any)
		nm, nIsObj := nv.(map[string]any)
		if oIsObj && nIsObj {
			if sub := Diff(om, nm); len(sub) > 0 {
				out[k] = sub
			}
			continue
		}
		if !reflect.DeepEqual(ov, nv) {
			out[k] = nv
		}
	}
	for k, nv := range current {
		if _, ok := old[k]; !ok {
			out[k] = nv
		}
	}
	return out
}
