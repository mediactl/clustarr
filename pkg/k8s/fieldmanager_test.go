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

import "testing"

func TestFieldManagersAreTheOnesTheSpecLists(t *testing.T) {
	want := []string{
		"catalogarr", "catalogarr-series", "catalogarr-worker",
		"catalogarr-metadata", "catalogarr-grab", "catalogarr-fanout",
		"importarr", "importarr-worker",
		"indexarr",
		"indexarr-worker", "grabarr", "grabarr-engine",
		"squasharr", "squasharr-worker", "squasharr-pool", "captionarr", "captionarr-worker",
		"clustarr-dlq-projector", "clustarr-ui",
	}
	got := FieldManagers()
	if len(got) != len(want) {
		t.Fatalf("got %d field managers, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("manager %d = %q, want %q", i, got[i], w)
		}
		if !got[i].Valid() {
			t.Errorf("%q reported invalid", got[i])
		}
	}
	if FieldManager("").Validate() == nil {
		t.Error("the empty field manager was accepted")
	}
	if FieldManager("kubectl").Validate() == nil {
		t.Error("an unlisted field manager was accepted")
	}
}
