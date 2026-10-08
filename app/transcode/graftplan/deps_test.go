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

package graftplan

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGraftplanIsPure (§3.5: Plan is pure): no client, no bus, no
// filesystem helpers, no native media library, no loop -- the loop imports
// it, not the other way, and admission reads it, not the reverse.
func TestGraftplanIsPure(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err)
	deps := strings.Fields(string(out))
	require.Contains(t, deps, "github.com/mediactl/clustarr/app/transcode/graftplan", "go list read this package")
	for _, bad := range []string{
		"sigs.k8s.io/controller-runtime/pkg/client",
		"k8s.io/client-go/",
		"github.com/mediactl/clustarr/app/remediation",
		"github.com/mediactl/clustarr/app/transcode/admission",
		"github.com/mediactl/clustarr/pkg/events/natsbus",
		"github.com/mediactl/clustarr/pkg/events/membus",
		"github.com/mediactl/clustarr/pkg/fsops",
		"github.com/obinnaokechukwu/ffgo",
		"github.com/ebitengine/purego",
	} {
		for _, dep := range deps {
			require.False(t, strings.HasPrefix(dep, bad), "graftplan links %s (through %s)", bad, dep)
		}
	}
}
