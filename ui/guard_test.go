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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// actionsDir is the one directory under ui/ whose files may write: ui/actions
// itself, not a subdirectory of it and not any other directory that happens
// to be called "actions". Paths are relative to ui/, as `go test` runs this
// package there.
const actionsDir = "actions"

// statusWriteSelectors are banned in every non-test file under ui/,
// ui/actions included. The UI never writes status (amendment §A3.2,
// CLAUDE.md), and ruling R2 narrowed D3-4's guard for writes to spec, not for
// writes to status. Each name is a distinct route to a status write:
//
//   - Status: controller-runtime's accessor behind .Status().Update(),
//     .Status().Patch() and .Status().Apply().
//   - SubResource: the same writer by another name --
//     .SubResource("status").Patch() -- and client-go rest's
//     .SubResource("status") on a raw request. D3-4's guard did not list
//     it, so that route was open.
//   - PatchStatus: pkg/k8s's status write (its import is banned below too).
//   - UpdateStatus, ApplyStatus: client-go's typed and dynamic clients' status
//     writes.
var statusWriteSelectors = map[string]bool{
	"Status":       true,
	"SubResource":  true,
	"PatchStatus":  true,
	"UpdateStatus": true,
	"ApplyStatus":  true,
}

// neverWriteSelectors are banned in every non-test file under ui/,
// ui/actions included: controller-runtime's client.Writer verbs that no UI
// action is allowed to make. config/rbac/ui_role.yaml grants no update and
// no delete, so an Update, Delete or DeleteAllOf could only ever fail in a
// real cluster -- and pass every envtest, which does not enforce RBAC.
// Apply is the sixth client.Writer method, which D3-4's guard also did not
// list; ui/actions' package doc says why the UI's spec edits are merge
// patches rather than applies (an apply releases whatever the same manager
// set before and omits now, and applies to a missing object create it).
var neverWriteSelectors = map[string]bool{
	"Update":      true,
	"Delete":      true,
	"DeleteAllOf": true,
	"Apply":       true,
}

// actionWriteSelectors are the two writes ui/actions exists to make --
// actions.Writer's two methods, and the two write verbs ui_role.yaml grants
// (create on searches and libraryscans, patch on the catalog kinds) -- and
// are banned in every other file under ui/.
var actionWriteSelectors = map[string]bool{
	"Create": true,
	"Patch":  true,
}

const modulePrefix = "github.com/mediactl/clustarr/"

// bannedK8sImport is pkg/k8s: PatchStatus, Apply, ConnectBus and every
// controller-writing helper live there (CLAUDE.md: "All status writes go
// through pkg/k8s.PatchStatus"). It stays banned in every file under ui/,
// ui/actions included: ui/actions restates pkg/k8s.ManagerUI's value rather
// than importing it, the same way ui/reader.go restates its scheme list, and
// its tests pin the two together.
const bannedK8sImport = modulePrefix + "pkg/k8s"

// actionsModuleImports are the only packages from this module that
// ui/actions may import: API types, and logging/tracing. The per-service
// status packages (app/grab/status, app/indexer/status, ...) write status with a
// call named Patch -- which the selector checks above allow inside
// ui/actions -- so an allowlist on imports, not a denylist on names, is what
// keeps a status writer from being reached that way.
var actionsModuleImports = []string{
	modulePrefix + "api/",
	modulePrefix + "pkg/obs/",
}

// TestUINeverWrites is D3-4's AST guard, narrowed by ruling R2
// (docs/superpowers/plans/2026-09-23-phase-g-parity.md) rather than lifted.
// Until Phase G the UI could not write at all; amendment §A3.2 gives it three
// actions -- create a Search, create a LibraryScan, patch spec.monitored --
// and ui/actions is where all three live. So this test holds that:
//
//   - status is never written from anywhere under ui/, ui/actions included;
//   - Update, Delete, DeleteAllOf and Apply are never called from anywhere
//     under ui/, ui/actions included;
//   - Create and Patch are called only from ui/actions;
//   - pkg/k8s is imported from nowhere under ui/, ui/actions included;
//   - ui/actions imports nothing from this module but api/ and pkg/obs/;
//   - and the ui/actions exemption points at real code that really writes,
//     so it cannot quietly become an exemption for nothing.
//
// It is syntactic: it matches a call's selector name, whatever the receiver,
// because a type-checked guard would need golang.org/x/tools/go/packages,
// which this module does not depend on. So it over-matches (any method named
// Update, not only a client's) and cannot see a method value called through
// a variable (f := c.Create; f(...)). ui/actions' own shape covers the
// second: the rest of ui/ holds an *actions.Actions, whose writer is
// unexported. The envtest in ui/actions is what checks the runtime result on
// a real apiserver.
//
// _test.go files are excluded, as before: ui/reader_test.go and ui/actions'
// envtest seed fixtures with a plain client.Client, which is a test building
// its own state, not the UI writing to a cluster.
func TestUINeverWrites(t *testing.T) {
	files := parseNonTestGoFiles(t)
	require.NotEmpty(t, files, "no non-test .go files were found under ui/; this guard is not looking where it thinks it is")

	t.Run("status is never written, ui/actions included", func(t *testing.T) {
		for _, f := range files {
			for _, c := range selectorCalls(f) {
				if statusWriteSelectors[c.name] {
					t.Errorf("%s:%d: calls .%s(...), a route to a status write. The UI never writes status "+
						"(amendment §A3.2, CLAUDE.md) -- not from ui/actions either, which may create a "+
						"Search or LibraryScan and patch spec, and nothing else",
						f.path, c.line, c.name)
				}
			}
		}
	})

	t.Run("update, delete and apply are never called, ui/actions included", func(t *testing.T) {
		for _, f := range files {
			for _, c := range selectorCalls(f) {
				if neverWriteSelectors[c.name] {
					t.Errorf("%s:%d: calls .%s(...). No UI action updates, deletes or applies: "+
						"config/rbac/ui_role.yaml grants create and patch only, so this call could only "+
						"fail in a cluster (and pass envtest, which does not enforce RBAC); ui/actions' "+
						"package doc says why spec edits are merge patches, not applies",
						f.path, c.line, c.name)
				}
			}
		}
	})

	t.Run("create and patch only from ui/actions", func(t *testing.T) {
		for _, f := range files {
			if f.inActions {
				continue
			}
			for _, c := range selectorCalls(f) {
				if actionWriteSelectors[c.name] {
					t.Errorf("%s:%d: calls .%s(...) outside ui/actions. Every write the UI makes lives in "+
						"ui/actions (ruling R2); the rest of ui/ reads through Options.Reader, a "+
						"client.Reader, and writes only by calling an *actions.Actions method",
						f.path, c.line, c.name)
				}
			}
		}
	})

	t.Run("no pkg/k8s import, ui/actions included", func(t *testing.T) {
		for _, f := range files {
			for _, imp := range f.imports() {
				if imp.path == bannedK8sImport || strings.HasPrefix(imp.path, bannedK8sImport+"/") {
					t.Errorf("%s:%d: imports %s, which ui/ must never import -- ui/actions included. "+
						"pkg/k8s is where PatchStatus lives; ui/actions restates ManagerUI's value and "+
						"pins it in a test instead, as ui/reader.go restates its scheme list",
						f.path, imp.line, imp.path)
				}
			}
		}
	})

	t.Run("ui/actions imports nothing from this module but api/ and pkg/obs/", func(t *testing.T) {
		for _, f := range files {
			if !f.inActions {
				continue
			}
			for _, imp := range f.imports() {
				if !strings.HasPrefix(imp.path, modulePrefix) {
					continue
				}
				allowed := false
				for _, prefix := range actionsModuleImports {
					if strings.HasPrefix(imp.path, prefix) {
						allowed = true
						break
					}
				}
				if !allowed {
					t.Errorf("%s:%d: ui/actions imports %s. Inside ui/actions a call named Patch or "+
						"Create is allowed, so a module package whose Patch writes status (every "+
						"<service>/status package does) would slip past the selector checks; ui/actions "+
						"may import only %v from this module",
						f.path, imp.line, imp.path, actionsModuleImports)
				}
			}
		}
	})

	t.Run("the ui/actions carve-out is real", func(t *testing.T) {
		seen := map[string]bool{}
		actionFiles := 0
		for _, f := range files {
			if !f.inActions {
				continue
			}
			actionFiles++
			for _, c := range selectorCalls(f) {
				if actionWriteSelectors[c.name] {
					seen[c.name] = true
				}
			}
		}
		require.Positive(t, actionFiles,
			"no non-test .go file found in ui/%s; the write carve-out above exempts a directory that "+
				"does not exist, so either ui/actions moved (move actionsDir with it) or the walk is broken",
			actionsDir)
		for name := range actionWriteSelectors {
			require.True(t, seen[name],
				"ui/%s makes no .%s(...) call; the carve-out exempts it from a ban it no longer needs "+
					"exempting from -- narrow actionWriteSelectors rather than keep an unused allowance",
				actionsDir, name)
		}
	})
}

// goFile is one parsed non-test .go file under ui/.
type goFile struct {
	path      string
	fset      *token.FileSet
	file      *ast.File
	inActions bool
}

type selectorCall struct {
	name string
	line int
}

// selectorCalls returns every call in f whose callee is a selector
// expression (x.Name(...)), by selector name and line.
func selectorCalls(f goFile) []selectorCall {
	var calls []selectorCall
	ast.Inspect(f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			calls = append(calls, selectorCall{name: sel.Sel.Name, line: f.fset.Position(call.Pos()).Line})
		}
		return true
	})
	return calls
}

type goImport struct {
	path string
	line int
}

func (f goFile) imports() []goImport {
	out := make([]goImport, 0, len(f.file.Imports))
	for _, imp := range f.file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			path = imp.Path.Value
		}
		out = append(out, goImport{path: path, line: f.fset.Position(imp.Pos()).Line})
	}
	return out
}

// parseNonTestGoFiles parses every .go file under the current package
// directory (ui/, when this test runs, and recursively ui/actions, ui/views
// and ui/projection) whose name does not end in _test.go.
func parseNonTestGoFiles(t *testing.T) []goFile {
	t.Helper()
	var files []goFile
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files = append(files, goFile{
			path:      path,
			fset:      fset,
			file:      file,
			inActions: filepath.Dir(path) == actionsDir,
		})
		return nil
	})
	require.NoError(t, err)
	return files
}
