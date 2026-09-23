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

package ui_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeSelectors are the sigs.k8s.io/controller-runtime/pkg/client methods
// that turn a read into a write: the five client.Writer verbs, plus Status,
// the sub-resource accessor that unlocks .Status().Update()/.Patch() and is
// how CLAUDE.md's other banned calls (".Status().Update()", ".Status().Patch()")
// get reached in the first place.
var writeSelectors = map[string]bool{
	"Create":      true,
	"Update":      true,
	"Patch":       true,
	"Delete":      true,
	"DeleteAllOf": true,
	"Status":      true,
}

// bannedK8sImport is the other half of the invariant: pkg/k8s is where
// PatchStatus, ConnectBus and every controller-writing helper live
// (CLAUDE.md: "All status writes go through pkg/k8s.PatchStatus"). None of
// it belongs in ui/, so the import itself is a signal, independent of
// whether a write call has been added yet.
const bannedK8sImport = `"github.com/mediactl/clustarr/pkg/k8s"`

// TestUINeverWrites is Task D3-4's first guard, modelled on
// cmd/clustarr/bus_hooks_guard_test.go's TestEveryServicePassesBusHooks: a
// source-level AST check for an invariant that leaves nothing to observe at
// runtime. ui/server.go's Options.Reader is typed client.Reader, never
// client.Client, specifically so ui/ cannot reach for Create, Update, Patch,
// Delete or Status -- but a type choice is not self-enforcing. Nothing
// stops a later change from widening Options.Reader to client.Client (or
// adding a second, writing field) and nothing would fail until this guard
// exists: CLAUDE.md says "The UI never writes status and owns no CRD", and
// until D3-0 that held only because ui/ had no client at all. D3-0 gave it
// one; this test is what keeps the invariant real rather than incidental.
//
// It walks every non-test .go file under ui/, recursively (so ui/views and
// ui/projection are covered too), and fails on:
//   - a call whose selector name is one of writeSelectors -- syntactic, not
//     type-checked, because a type-checked guard would need
//     golang.org/x/tools/go/packages, which is not a direct dependency of
//     this module and adding one is out of scope for this task (the design
//     plan's Global Constraints: "D3 adds no dependency; if you think it
//     needs one, stop and say so"). Grep across ui/'s non-test files today
//     turns up zero calls to any of these six names, so the syntactic
//     version has no false positives to weigh against that cost.
//   - an import of pkg/k8s, by exact import path
//
// _test.go files are excluded on purpose, matching
// cmd/clustarr/rbac_markers_test.go's markerDirExists. ui/reader_test.go
// seeds an envtest fixture with a plain client.Client.Create -- the same
// pattern pkg/k8s's own suites and every controller's envtest use to set up
// state before exercising the code under test. That is a test creating its
// own fixture, not ui writing to a live cluster, and the invariant this
// guard protects is about the latter. Walking test files and then carving
// out an allowlist for fixture writers would end up re-deriving exactly
// this exclusion, file by file, with more code and no more safety.
func TestUINeverWrites(t *testing.T) {
	files := nonTestGoFiles(t)
	require.NotEmpty(t, files, "no non-test .go files were found under ui/; this guard is not looking where it thinks it is")

	t.Run("no controller-runtime write calls", func(t *testing.T) {
		for _, path := range files {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			require.NoError(t, err, "parse %s", path)

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !writeSelectors[sel.Sel.Name] {
					return true
				}
				t.Errorf("%s:%d: calls .%s(...) -- a controller-runtime client write method, or the "+
					"Status() sub-resource accessor that unlocks one. ui/ holds only a client.Reader "+
					"(ui/server.go's Options.Reader) and CLAUDE.md says the UI never writes status and "+
					"owns no CRD; if this call is legitimate, it does not belong in ui/",
					path, fset.Position(call.Pos()).Line, sel.Sel.Name)
				return true
			})
		}
	})

	t.Run("no pkg/k8s import", func(t *testing.T) {
		for _, path := range files {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			require.NoError(t, err, "parse %s", path)

			for _, imp := range file.Imports {
				if imp.Path.Value != bannedK8sImport {
					continue
				}
				t.Errorf("%s:%d: imports pkg/k8s, which ui/ must never import. pkg/k8s.PatchStatus and "+
					"the rest of that package exist for the controllers that write status; ui builds its "+
					"own, narrower scheme in ui/reader.go precisely so it never needs pkg/k8s at all",
					path, fset.Position(imp.Pos()).Line)
			}
		}
	})
}

// nonTestGoFiles returns every .go file under the current package directory
// (ui/, when this test runs, and recursively its subpackages ui/views and
// ui/projection) whose name does not end in _test.go, relative to the
// working directory `go test` gives this package.
func nonTestGoFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	require.NoError(t, err)
	return files
}
