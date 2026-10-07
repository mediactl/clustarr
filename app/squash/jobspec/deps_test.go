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

package jobspec

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManagerSideSquashLinksNoWorker holds spec §4.3 S1 (and the AudioGraft
// drift): the squash reconcilers, graft included, and jobspec itself link no
// part of the pool worker, ffgo, or an ffprobe exec. `test/guards` (W5.14)
// overlaps it.
func TestManagerSideSquashLinksNoWorker(t *testing.T) {
	denied := []string{
		"github.com/mediactl/clustarr/app/squash/worker", // worker, worker/inprocess, worker/graft
		"github.com/mediactl/clustarr/pkg/transcode/engine",
		"github.com/mediactl/clustarr/pkg/mediainfo/ffprobeexec",
		"github.com/obinnaokechukwu/ffgo",
		"github.com/ebitengine/purego",
	}
	for _, pkg := range []string{
		"github.com/mediactl/clustarr/app/squash/jobspec",
		"github.com/mediactl/clustarr/app/squash/controller/...",
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		require.NoError(t, err, "go list -deps %s:\n%s", pkg, out)
		for _, dep := range strings.Fields(string(out)) {
			for _, bad := range denied {
				if dep == bad || strings.HasPrefix(dep, bad+"/") {
					assert.Failf(t, "links worker code", "%s links %s", pkg, dep)
				}
			}
		}
	}
}
