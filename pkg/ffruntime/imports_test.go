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

package ffruntime

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFFRuntimeImportsOnlyFFgo holds the gate to the standard library, ffgo
// and purego (spec §7.4), so every binary can take it without dragging in
// clustarr code.
func TestFFRuntimeImportsOnlyFFgo(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").CombinedOutput()
	require.NoError(t, err, "go list: %s", out)
	for _, dep := range strings.Fields(string(out)) {
		switch {
		case dep == "github.com/mediactl/clustarr/pkg/ffruntime",
			dep == "github.com/obinnaokechukwu/ffgo", strings.HasPrefix(dep, "github.com/obinnaokechukwu/ffgo/"),
			dep == "github.com/ebitengine/purego", strings.HasPrefix(dep, "github.com/ebitengine/purego/"):
		default:
			t.Errorf("pkg/ffruntime links %s: it may link only the standard library, ffgo and purego", dep)
		}
	}
}
