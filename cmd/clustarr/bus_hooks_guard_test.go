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

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// busServices are the service packages whose Run connects to the bus. ui is
// absent: it has no bus of its own.
var busServices = []string{
	"app/catalog",
	"app/import",
	"app/indexer",
	"app/grab",
	"app/squash",
	"app/caption",
}

// TestEveryServicePassesBusHooks is the guard behind Task C12a's step 2b
// decision.
//
// pkg/k8s deliberately does not import pkg/obs (see k8s.WithBusHooks for the
// two reasons), so the trace hooks are something each service's Run passes
// in -- and therefore something a new service can forget. That is not a
// hypothetical: it is exactly how Clustarr shipped a complete W3C
// propagation implementation in pkg/events, a matching pair of hooks in
// pkg/obs, and not one production call site that joined them. The
// propagation existed and never ran, and every unit test passed, because
// every unit test built its own bus.
//
// This test reads each service's run.go and insists that every
// k8s.ConnectBus call site passes obs.BusHooks() through k8s.WithBusHooks.
// It is source-level rather than behavioural because there is nothing to
// observe at runtime: a missing hook is silence, not an error.
func TestEveryServicePassesBusHooks(t *testing.T) {
	for _, service := range busServices {
		t.Run(service, func(t *testing.T) {
			path := filepath.Join("..", "..", service, "run.go")
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			require.NoError(t, err, "parse %s", path)

			var calls int
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSelector(call.Fun, "k8s", "ConnectBus") {
					return true
				}
				calls++

				var hooked bool
				for _, arg := range call.Args {
					inner, ok := arg.(*ast.CallExpr)
					if !ok || !isSelector(inner.Fun, "k8s", "WithBusHooks") {
						continue
					}
					for _, hookArg := range inner.Args {
						if hookCall, ok := hookArg.(*ast.CallExpr); ok &&
							isSelector(hookCall.Fun, "obs", "BusHooks") {
							hooked = true
						}
					}
				}
				require.True(t, hooked,
					"%s:%d: k8s.ConnectBus is called without k8s.WithBusHooks(obs.BusHooks()); "+
						"this service publishes and consumes with no trace propagation, silently",
					path, fset.Position(call.Pos()).Line)
				return true
			})

			require.Positive(t, calls,
				"%s has no k8s.ConnectBus call; either the service stopped using the bus "+
					"(remove it from busServices) or the call moved out of run.go "+
					"(this guard must move with it)", path)
		})
	}
}

// isSelector reports whether e is the expression `pkg.name`.
func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}
