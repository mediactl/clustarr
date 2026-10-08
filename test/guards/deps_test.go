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

	"github.com/mediactl/clustarr/test/installtest"
)

// clustarr ships manager and ui in a distroless static image
// (images/Dockerfile.clustarr) with no dynamic loader, so both binaries must
// link statically: purego (ffgo's way of loading FFmpeg) makes the Go linker
// emit a dynamically linked executable, and the image's index and ui failed
// to start with "exec /clustarr: no such file or directory" (2026-10-01).
// The in-process engine belongs to the native image's binaries alone. This
// held cmd/clustarr until the manager and agent split; it holds its two
// successors in the static image now.
func TestClustarrNeverLinksADynamicLoader(t *testing.T) {
	for _, bin := range []string{"./cmd/manager", "./cmd/ui"} {
		cmd := exec.Command("go", "list", "-deps", bin)
		cmd.Dir = installtest.RepoRoot(t)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", bin, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, banned := range []string{"github.com/ebitengine/purego", "github.com/obinnaokechukwu/ffgo"} {
				if dep == banned || strings.HasPrefix(dep, banned+"/") {
					t.Errorf("%s depends on %s, which needs a dynamic loader", bin, dep)
				}
			}
		}
	}
}
