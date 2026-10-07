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

package conditions_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestConditionsLinksNoControllerRuntime keeps the condition readers light
// enough for cmd/ui (spec §4.5.2): apimachinery only, with no controller-runtime
// and no pkg/k8s.
func TestConditionsLinksNoControllerRuntime(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/mediactl/clustarr/pkg/k8s" ||
			dep == "sigs.k8s.io/controller-runtime" || strings.HasPrefix(dep, "sigs.k8s.io/controller-runtime/") {
			t.Errorf("pkg/k8s/conditions links %s", dep)
		}
	}
}
