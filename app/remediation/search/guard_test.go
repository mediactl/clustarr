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

package search_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoEpisodeSearchIsPublishedForASeason is series search design §16
// item 2 (no burst): the season planner and the stage's Series mode build
// every search subject they publish from the Series' media key, never an
// Episode's -- a season's episodes are searched by the one season query,
// and an episode left uncovered by its own planner on its own backoff.
func TestNoEpisodeSearchIsPublishedForASeason(t *testing.T) {
	for _, file := range []string{"season.go", "../../catalog/searchplan/season.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err, file)
		subjects := 0
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSel(call.Fun, "events", "WorkSearchSubject") {
				return true
			}
			subjects++
			require.Len(t, call.Args, 2)
			key := mediaKeyOf(f, call.Args[1])
			require.NotNil(t, key, "%s: the subject's media key is not an events.MediaKey call", fset.Position(call.Pos()))
			require.True(t, isSel(key.Args[0], "commonv1", "MediaKindSeries") || isSeriesString(key.Args[0]),
				"%s: a season's search subject names %s, not the Series", fset.Position(call.Pos()), exprString(key.Args[0]))
			return true
		})
		if file == "../../catalog/searchplan/season.go" {
			require.Equal(t, 1, subjects, "the season planner builds exactly one search subject: the season task's")
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "MediaKindEpisode" {
				require.Failf(t, "an Episode kind in the season path", "%s names MediaKindEpisode", fset.Position(sel.Pos()))
			}
			return true
		})
	}
}

func isSel(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// mediaKeyOf resolves e to an events.MediaKey call: e itself, or the call
// a local variable of the file was assigned.
func mediaKeyOf(f *ast.File, e ast.Expr) *ast.CallExpr {
	if call, ok := e.(*ast.CallExpr); ok && isSel(call.Fun, "events", "MediaKey") {
		return call
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return nil
	}
	var found *ast.CallExpr
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			if l, ok := lhs.(*ast.Ident); ok && l.Name == id.Name && i < len(as.Rhs) {
				if call, ok := as.Rhs[i].(*ast.CallExpr); ok && isSel(call.Fun, "events", "MediaKey") {
					found = call
				}
			}
		}
		return true
	})
	return found
}

// isSeriesString is string(commonv1.MediaKindSeries).
func isSeriesString(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "string" {
		return false
	}
	return isSel(call.Args[0], "commonv1", "MediaKindSeries")
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.Ident:
		return x.Name
	case *ast.CallExpr:
		return exprString(x.Fun) + "(...)"
	}
	return "?"
}
