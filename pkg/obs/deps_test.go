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

package obs_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestObsLinksNoControllerRuntimeManager holds pkg/obs to what cmd/ui may link
// (spec §4.5.2). Bootstrap bridges logging through controller-runtime's
// pkg/log. It must not use the root package: its alias.go imports pkg/manager,
// and with it the manager, webhook and leader-election tree.
func TestObsLinksNoControllerRuntimeManager(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch dep {
		case "sigs.k8s.io/controller-runtime", "sigs.k8s.io/controller-runtime/pkg/manager":
			t.Errorf("pkg/obs links %s; set the logger through sigs.k8s.io/controller-runtime/pkg/log", dep)
		}
	}
}
