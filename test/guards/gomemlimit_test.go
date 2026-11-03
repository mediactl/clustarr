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
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/installtest"
)

// TestEveryBinarySetsItsGoMemoryLimitFirst (owner, 2026-11-03): the
// installers set AUTOMEMLIMIT and no GOMEMLIMIT
// (TestTheGoMemoryLimitIsItsShareOfTheLimit), so a binary that does not
// call gomemlimit.Set runs with no Go memory limit at all. It is main's
// first statement, ahead of the agent's par2 child dispatch.
func TestEveryBinarySetsItsGoMemoryLimitFirst(t *testing.T) {
	root := installtest.RepoRoot(t)
	mains, err := filepath.Glob(filepath.Join(root, "cmd", "*", "main.go"))
	require.NoError(t, err)
	require.Len(t, mains, 5, "manager, agent, ui, markers, transcode")
	for _, path := range mains {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)
		var main *ast.FuncDecl
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
				main = fn
			}
		}
		require.NotNil(t, main, path)
		require.NotEmpty(t, main.Body.List, path)
		call, ok := main.Body.List[0].(*ast.ExprStmt)
		require.True(t, ok, "%s: main's first statement is gomemlimit.Set()", path)
		ce, ok := call.X.(*ast.CallExpr)
		require.True(t, ok, path)
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		require.True(t, ok, path)
		pkg, ok := sel.X.(*ast.Ident)
		require.True(t, ok, path)
		assert.Equal(t, "gomemlimit.Set", pkg.Name+"."+sel.Sel.Name, "%s: main's first statement", path)
	}
}

// TestNoModuleSetsGOMAXPROCS: since Go 1.25 the runtime sizes GOMAXPROCS
// from the cgroup's CPU limit, rounded up and at least 2, and follows a
// change. go.uber.org/automaxprocs, even through a dependency's blank
// import, would round it down to 1 on the 500m and 1-CPU pods and stop the
// runtime's re-check (owner, 2026-11-03: not used; pkg/gomemlimit).
func TestNoModuleSetsGOMAXPROCS(t *testing.T) {
	gomod, err := os.ReadFile(filepath.Join(installtest.RepoRoot(t), "go.mod"))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(gomod), "go.uber.org/automaxprocs"), "go.mod requires go.uber.org/automaxprocs")
}
