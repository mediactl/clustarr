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

package guards

import (
	"os/exec"
	"strings"
	"testing"
)

// clustarr ships in a distroless static image with no dynamic loader, so
// the binary must link statically: purego (ffgo's way of loading FFmpeg)
// makes the Go linker emit a dynamically linked executable, and the image's
// indexarr and ui failed to start with "exec /clustarr: no such file or
// directory" (2026-10-01). The in-process engine belongs to
// cmd/squasharr-worker alone.
func TestClustarrNeverLinksADynamicLoader(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, banned := range []string{"github.com/ebitengine/purego", "github.com/obinnaokechukwu/ffgo"} {
			if dep == banned || strings.HasPrefix(dep, banned+"/") {
				t.Errorf("cmd/clustarr depends on %s, which needs a dynamic loader", dep)
			}
		}
	}
}
