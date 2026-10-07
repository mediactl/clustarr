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

package pipeline_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPipelineLinksNoPkgK8s keeps pkg/pipeline out of pkg/k8s (spec §4.3 step
// 1.2). cmd/ui links pkg/pipeline, and pkg/k8s is where PatchStatus and Apply
// live, which the ui must never link (§4.5.2).
func TestPipelineLinksNoPkgK8s(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/mediactl/clustarr/pkg/k8s" {
			t.Errorf("pkg/pipeline links %s; read conditions through pkg/k8s/conditions", dep)
		}
	}
}
