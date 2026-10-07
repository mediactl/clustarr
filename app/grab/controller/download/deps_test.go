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

package download

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDownloadControllerLinksNoEngineRuntime holds spec §4.3 G1: the
// Download controller names the engine finalizer through app/grab/status and
// links no engine package (`test/guards` (W5.14) overlaps it).
func TestDownloadControllerLinksNoEngineRuntime(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	require.NoError(t, err, "go list -deps:\n%s", out)
	for _, dep := range strings.Fields(string(out)) {
		assert.False(t, dep == "github.com/mediactl/clustarr/app/grab/engine" ||
			strings.HasPrefix(dep, "github.com/mediactl/clustarr/app/grab/engine/"),
			"the Download controller links %s", dep)
	}
}
