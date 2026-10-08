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

// TestOnlyTheManagerEnsuresTopology: only the manager creates or updates
// the JetStream topology (split design §5.9, §5.15; A3.7), so an older
// agent can never revert a newer one. Three call shapes write it:
//
//   - busconn.EnsureTopology, busconn.KeepTopology and
//     busconn.WatchDeadLetters, package-qualified, only from
//     internal/cli/manager (pkg/busconn's own KeepTopology calls
//     EnsureTopology unqualified, the one other caller §5.15 allows);
//   - events.EnsureTopology, only from pkg/events/natsbus (the JetStream
//     bus's Ensure);
//   - a Bus's Ensure(ctx, topology) method, only inside pkg/busconn and
//     pkg/events (the buses and the contract suite).
//
// Every other process waits for the topology with busconn.AwaitTopology
// and binds.
func TestOnlyTheManagerEnsuresTopology(t *testing.T) {
	var managerCalls, scanned int
	for p, f := range productionFiles(t) {
		scanned++
		busconnAlias := importAlias(f, mod+"pkg/busconn", "busconn")
		eventsAlias := importAlias(f, mod+"pkg/events", "events")
		inBusLayer := strings.HasPrefix(p, "pkg/busconn/") || strings.HasPrefix(p, "pkg/events/")
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, name := range []string{"EnsureTopology", "KeepTopology", "WatchDeadLetters"} {
				if busconnAlias == "" || !isSelector(call.Fun, busconnAlias, name) {
					continue
				}
				if strings.HasPrefix(p, "internal/cli/manager/") {
					managerCalls++
					continue
				}
				t.Errorf("%s calls busconn.%s: only the manager writes topology, so an older agent can never revert a newer one (§5.9)", p, name)
			}
			if eventsAlias != "" && isSelector(call.Fun, eventsAlias, "EnsureTopology") && !strings.HasPrefix(p, "pkg/events/natsbus/") {
				t.Errorf("%s calls events.EnsureTopology: only the JetStream bus's Ensure does (§5.9)", p)
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Ensure" && len(call.Args) == 2 && !inBusLayer {
				t.Errorf("%s calls %s.Ensure(ctx, topology): only the manager writes topology, through busconn.EnsureTopology (§5.9)",
					p, exprString(sel.X))
			}
			return true
		})
	}
	require.Greater(t, scanned, 500, "scanned only %d files; this guard is not looking where it thinks it is", scanned)
	require.GreaterOrEqual(t, managerCalls, 3, "found %d manager calls; internal/cli/manager ensures, keeps and watches", managerCalls)
}

// exprString renders a short selector receiver for a message.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprString(v.Fun) + "(...)"
	}
	return "<expr>"
}
