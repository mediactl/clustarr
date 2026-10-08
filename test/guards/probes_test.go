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
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOnlyPkgK8sRegistersProbes: k8s.AddProbes is the only registrar of
// readiness and liveness checks (split design §3.3). controller-runtime's
// check map silently keeps the last of two same-named checks, so a second
// registrar can replace a domain's check with nothing said.
func TestOnlyPkgK8sRegistersProbes(t *testing.T) {
	var scanned int
	for p, f := range productionFiles(t) {
		if strings.HasPrefix(p, "pkg/k8s/") {
			continue
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "AddReadyzCheck" || sel.Sel.Name == "AddHealthzCheck") {
				t.Errorf("%s calls %s; register checks through k8s.AddProbes (§3.3)", p, sel.Sel.Name)
			}
			return true
		})
	}
	require.Greater(t, scanned, 500, "scanned only %d files; this guard is not looking where it thinks it is", scanned)
}
