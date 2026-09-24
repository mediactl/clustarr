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

package worker

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenForWorker is what the NATS-only worker must never link: spec §9.
var forbiddenForWorker = []string{
	"k8s.io/client-go/",
	"sigs.k8s.io/controller-runtime/pkg/client",
	"sigs.k8s.io/controller-runtime/pkg/manager",
	"github.com/mediactl/clustarr/pkg/k8s",
}

func TestWorkerPackageImportsNoKubernetesClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range forbiddenForWorker {
			if dep == strings.TrimSuffix(bad, "/") || strings.HasPrefix(dep, bad) {
				t.Errorf("app/squash/worker depends on %s", dep)
			}
		}
		if dep == "github.com/mediactl/clustarr/pkg/obs" {
			t.Errorf("app/squash/worker depends on pkg/obs, which links controller-runtime; use pkg/obs/logging or tracing")
		}
	}
}
