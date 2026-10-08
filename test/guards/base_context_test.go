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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryManagerGetsTheBaseContext: every ctrl.NewManager / manager.New in
// production code wraps its options in k8s.WithBaseContext, discovered
// rather than listed (split design §10.3.3). controller-runtime derives each
// runnable's context from Options.BaseContext -- context.Background by
// default -- not from the context mgr.Start is given, so a manager built
// without it runs every reconciler, worker and engine job under
// logging.FromContext's discard logger: the whole process logs nothing of
// its own, and nothing fails (2026-09-24, the owner's cluster). It is
// source-level because silence is not observable.
func TestEveryManagerGetsTheBaseContext(t *testing.T) {
	var calls int
	for p, f := range productionFiles(t) {
		ctrlAlias := importAlias(f, "sigs.k8s.io/controller-runtime", "controllerruntime")
		mgrAlias := importAlias(f, "sigs.k8s.io/controller-runtime/pkg/manager", "manager")
		k8sAlias := importAlias(f, mod+"pkg/k8s", "k8s")
		if f.Name.Name == "k8s" {
			k8sAlias = "" // pkg/k8s's own WithBaseContext is unqualified
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !(isSelector(call.Fun, ctrlAlias, "NewManager") || isSelector(call.Fun, mgrAlias, "New")) {
				return true
			}
			calls++
			wrapped := slices.ContainsFunc(call.Args, func(a ast.Expr) bool {
				inner, ok := a.(*ast.CallExpr)
				if !ok {
					return false
				}
				if k8sAlias == "" {
					return isIdent(inner.Fun, "WithBaseContext")
				}
				return isSelector(inner.Fun, k8sAlias, "WithBaseContext")
			})
			assert.True(t, wrapped, "%s: a manager built without k8s.WithBaseContext runs every runnable under the discard logger", p)
			return true
		})
	}
	require.GreaterOrEqual(t, calls, 2, "found %d manager constructions; the manager and the agent each build one", calls)
}
