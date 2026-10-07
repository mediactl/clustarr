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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui"
)

// TestEveryProjectionStreamIsWiredIntoBothUICommands guards the gap that
// closed this file's own reason for existing.
//
// ui/projection.Projection exposes one Subscribe* method per SSE stream, and
// ui.Options has a matching field per stream. Both are optional: a nil field
// falls back to a per-connection poll of the Reader, which renders correctly
// and passes every test in the ui package. So a stream that is never wired in
// cmd/clustarr is not a failure anywhere -- it is simply slower in production
// and nowhere else, which is the definition of inert code.
//
// That is exactly what happened with SubscribeDownloads: D3-3 added the
// projection method, the Options field and the route, proved the whole path
// with tests, and left `clustarr ui` and `clustarr all` falling back to the
// per-connection poll that the shared projection exists to eliminate. Nothing
// was red. CLAUDE.md's own note on the D1 wiring task says it: registration is
// where inert code hides.
//
// This test is deliberately written against the METHOD SET rather than against
// a hard-coded list of names, so a third stream added later is covered the day
// it appears instead of needing someone to remember this file.
//
// It happened again with G3-3 and G3-4: SubscribeLibrary, SubscribeUnmatched
// and SubscribeImportLists landed wired nowhere, and -- the part a Subscribe*
// rule alone cannot see -- so did the page accessors Library, Unmatched and
// ImportLists, whose nil defaults render the Library, Unmatched and Import
// Lists pages with no rows at all. So beside every Subscribe* method, every
// *Projection method that shares its name with a ui.Options field is held to
// the same rule. ui_options_wiring_test.go is the behavioural half: it
// executes both commands and inspects the Options they build.
func TestEveryProjectionStreamIsWiredIntoBothUICommands(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	// Every ui.Options field name: a *Projection method sharing one is a page
	// accessor (Entries, Library, ...) that Options expects wired.
	uiOptionFields := map[string]bool{}
	ot := reflect.TypeOf(ui.Options{})
	for i := range ot.NumField() {
		uiOptionFields[ot.Field(i).Name] = true
	}

	// Every exported Subscribe* method on *projection.Projection, and every
	// one named after a ui.Options field.
	var streams []string
	projDir := filepath.Join(root, "ui", "projection")
	entries, err := os.ReadDir(projDir)
	require.NoError(t, err, "read ui/projection")
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(projDir, e.Name()), nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Subscribe") && !uiOptionFields[fn.Name.Name] {
				continue
			}
			if recvTypeName(fn) == "Projection" {
				streams = append(streams, fn.Name.Name)
			}
		}
	}
	require.NotEmpty(t, streams,
		"found no Subscribe* methods on *projection.Projection; this test's premise has changed")

	// Both production entry points must reference each of them.
	for _, file := range []string{"services.go", "all.go"} {
		src, err := os.ReadFile(filepath.Join(root, "cmd", "clustarr", file))
		require.NoError(t, err)
		for _, m := range streams {
			// strings.Contains rather than require.Contains, whose failure
			// message prints the whole source file.
			if !strings.Contains(string(src), "proj."+m) {
				t.Errorf("cmd/clustarr/%s never wires projection.%s into ui.Options.\n"+
					"A nil field is legal and silently falls back to a per-connection "+
					"poll, or (for a page accessor) to no rows at all, so this is inert "+
					"rather than broken -- no other test in the tree will fail. Wire it, "+
					"or delete the method.", file, m)
			}
		}
	}
}

// recvTypeName returns the receiver's bare type name, dereferencing a pointer
// receiver.
func recvTypeName(fn *ast.FuncDecl) string {
	if len(fn.Recv.List) == 0 {
		return ""
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}
