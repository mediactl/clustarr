/*
Copyright (C) 2026 The Clustarr Authors.

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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryServiceGivesTheManagerItsBaseContext holds every service's
// ctrl.NewManager call to k8s.WithBaseContext. controller-runtime derives
// each runnable's context from Options.BaseContext -- context.Background by
// default -- not from the context mgr.Start is given, so a manager built
// without it runs every reconciler, worker and engine job under
// logging.FromContext's discard logger: the whole service logs nothing of
// its own, and nothing fails (2026-09-24, the owner's cluster). Like the
// bus-hooks guard, it is source-level because silence is not observable.
func TestEveryServiceGivesTheManagerItsBaseContext(t *testing.T) {
	for _, src := range busServiceSources {
		if src.name == "cmd/clustarr/services.go (ui)" {
			continue // ui builds a cache, not a manager
		}
		t.Run(src.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, src.path, nil, parser.SkipObjectResolution)
			require.NoError(t, err, "parse %s", src.path)

			var calls int
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isSelector(call.Fun, "ctrl", "NewManager") {
					return true
				}
				calls++
				wrapped := false
				for _, arg := range call.Args {
					if inner, ok := arg.(*ast.CallExpr); ok && isSelector(inner.Fun, "k8s", "WithBaseContext") {
						wrapped = true
					}
				}
				require.True(t, wrapped,
					"%s:%d: ctrl.NewManager is called without k8s.WithBaseContext(opts, ctx); "+
						"every runnable of this service would run under the discard logger",
					src.path, fset.Position(call.Pos()).Line)
				return true
			})
			require.Positive(t, calls, "%s has no ctrl.NewManager call; this guard must move with it", src.path)
		})
	}
}
