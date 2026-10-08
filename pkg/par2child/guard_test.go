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

package par2child

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheOnlyExecIsTheRepairChild: exec.Command and exec.CommandContext
// appear only in exec.go, inside Exec.run; exec.LookPath nowhere (§8.9
// guard 4). The child is always this binary, never a program found on PATH.
func TestTheOnlyExecIsTheRepairChild(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	sawRun := false
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || x.Name != "exec" {
				return true
			}
			fn := enclosingFunc(f, sel.Pos())
			switch {
			case sel.Sel.Name == "LookPath":
				t.Errorf("%s: %s uses exec.LookPath: the child is always os.Executable()", p, fn)
			case strings.HasPrefix(sel.Sel.Name, "Command") && (p != "exec.go" || fn != "run"):
				t.Errorf("%s: %s uses exec.%s: the only exec is Exec.run's child", p, fn, sel.Sel.Name)
			case strings.HasPrefix(sel.Sel.Name, "Command"):
				sawRun = true
			}
			return true
		})
	}
	require.True(t, sawRun, "Exec.run's exec.CommandContext was not found: the guard is looking at the wrong files")
}

// enclosingFunc names the function declaration around pos, or "package
// scope" for a selector outside every function (a var initialiser).
func enclosingFunc(f *ast.File, pos token.Pos) string {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Pos() <= pos && pos < fn.End() {
			return fn.Name.Name
		}
	}
	return "package scope"
}
