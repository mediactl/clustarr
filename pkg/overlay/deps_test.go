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

package overlay_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestOverlayLinksNoRasteriser keeps the badge model (templates, hashes,
// logos) free of golang.org/x/image (spec §4.5.1). The manager's OverlayProfile
// controller hashes templates; only the agent's renderer draws, through
// pkg/overlay/render.
func TestOverlayLinksNoRasteriser(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "golang.org/x/image") {
			t.Errorf("pkg/overlay links %s; drawing belongs in pkg/overlay/render", dep)
		}
	}
}
