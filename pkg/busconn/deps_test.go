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

package busconn_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestBusconnLinksNeitherPkgK8sNorTheManager keeps the bus connector usable by
// cmd/ui (spec §4.5.2), which links no pkg/k8s and no controller-runtime
// manager. It also keeps it off pkg/obs: the trace hooks come in through
// WithHooks, so the bus layer never depends on the observability layer.
func TestBusconnLinksNeitherPkgK8sNorTheManager(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch dep {
		case "github.com/mediactl/clustarr/pkg/k8s",
			"github.com/mediactl/clustarr/pkg/obs",
			"sigs.k8s.io/controller-runtime",
			"sigs.k8s.io/controller-runtime/pkg/manager":
			t.Errorf("pkg/busconn links %s", dep)
		}
	}
}
