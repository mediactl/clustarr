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

// The AST and import-graph helpers every source and deps guard in this
// package shares (W5.15's astutil_test.go). A guard that needs another
// helper adds it here, once, rather than a private copy beside it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/guards/depguard"
	"github.com/mediactl/clustarr/test/installtest"
)

// mod is the module path every import path under the repository starts
// with.
const mod = "github.com/mediactl/clustarr/"

// The build settings each binary's image builds it with: the manager, the
// ui, markers and transcode are static; the agent builds with cgo (R13).
var (
	linuxStatic = []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}
	linuxCgo    = []string{"CGO_ENABLED=1", "GOOS=linux", "GOARCH=amd64"}
)

// moduleDirs is the module-relative directory of every module package in g.
func moduleDirs(g depguard.Graph) map[string]bool {
	out := map[string]bool{}
	for _, p := range g.Packages() {
		if rel, ok := strings.CutPrefix(p, mod); ok {
			out[rel] = true
		}
	}
	return out
}

// importPathOf is the import path file binds the local name to, or "".
func importPathOf(file *ast.File, name string) string {
	for _, imp := range file.Imports {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := path.Base(ip)
		if imp.Name != nil {
			local = imp.Name.Name
		} else if strings.HasPrefix(local, "v") && strings.Count(ip, "/") > 0 {
			if _, err := strconv.Atoi(local[1:]); err == nil {
				local = path.Base(path.Dir(ip)) // a /vN major-version suffix
			}
		}
		if local == name {
			return ip
		}
	}
	return ""
}

// hasCall reports whether some argument in args is a call pkg.name(...) for
// which inner holds (nil: always).
func hasCall(args []ast.Expr, pkg, name string, inner func(*ast.CallExpr) bool) bool {
	for _, a := range args {
		c, ok := a.(*ast.CallExpr)
		if ok && isSelector(c.Fun, pkg, name) && (inner == nil || inner(c)) {
			return true
		}
	}
	return false
}

// controllerBuilders are the calls that start a controller-runtime builder
// chain: ctrl's NewControllerManagedBy alias, builder.ControllerManagedBy,
// and the generic builder.TypedControllerManagedBy[request] (the remediation
// loop's, F3.1). controller-runtime has no other.
var controllerBuilders = map[string]bool{"NewControllerManagedBy": true, "ControllerManagedBy": true, "TypedControllerManagedBy": true}

// unindex strips a generic instantiation from a callee: F[T](...) and
// pkg.F[T1, T2](...) call F.
func unindex(e ast.Expr) ast.Expr {
	switch v := e.(type) {
	case *ast.IndexExpr:
		return v.X
	case *ast.IndexListExpr:
		return v.X
	}
	return e
}

// isBuilderStart reports whether call starts a controller builder chain.
func isBuilderStart(call *ast.CallExpr) bool {
	switch f := unindex(call.Fun).(type) {
	case *ast.SelectorExpr:
		return controllerBuilders[f.Sel.Name] && isPackageIdent(f.X)
	case *ast.Ident:
		return controllerBuilders[f.Name]
	}
	return false
}

// builderChain reports whether the method chain e begins with a controller
// builder call (ctrl.NewControllerManagedBy(mgr).For(...)...,
// builder.TypedControllerManagedBy[Key](mgr).Named(...)...).
func builderChain(e ast.Expr) bool {
	for {
		switch v := e.(type) {
		case *ast.CallExpr:
			if isBuilderStart(v) {
				return true
			}
			e = v.Fun
		case *ast.SelectorExpr:
			e = v.X
		case *ast.IndexExpr, *ast.IndexListExpr:
			e = unindex(v)
		default:
			return false
		}
	}
}

// importAlias is the identifier file refers to importPath by, or "" when it
// does not import it.
func importAlias(file *ast.File, importPath, pkgName string) string {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return pkgName
	}
	return ""
}

// isEventsHandler reports whether ft is func(context.Context, events.Message) error.
func isEventsHandler(ft *ast.FuncType, eventsAlias string) bool {
	if eventsAlias == "" || ft.Params == nil || ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	var params []ast.Expr
	for _, f := range ft.Params.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			params = append(params, f.Type)
		}
	}
	if len(params) != 2 {
		return false
	}
	sel, ok := params[1].(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == eventsAlias && sel.Sel.Name == "Message" && isIdent(ft.Results.List[0].Type, "error")
}

// isRunCtxError reports whether ft is func(context.Context) error.
func isRunCtxError(ft *ast.FuncType) bool {
	if ft.Params == nil || len(ft.Params.List) != 1 || len(ft.Params.List[0].Names) > 1 {
		return false
	}
	if ft.Results == nil || len(ft.Results.List) != 1 || !isIdent(ft.Results.List[0].Type, "error") {
		return false
	}
	sel, ok := ft.Params.List[0].Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "context" && sel.Sel.Name == "Context"
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// receiverTypeName unwraps `T` or `*T` to "T".
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

// productionFiles parses every non-test .go file under dirs (module-relative;
// default the module's code trees, app, pkg, internal, cmd and ui), keyed by
// module-relative slash path. It was the retired clustarr binary's service
// directories and pkg/ until the manager and agent split (spec 2026-10-06
// §10.3.3, W5.15).
func productionFiles(t *testing.T, dirs ...string) map[string]*ast.File {
	t.Helper()
	root := installtest.RepoRoot(t)
	if len(dirs) == 0 {
		dirs = []string{"app", "pkg", "internal", "cmd", "ui"}
	}
	out := map[string]*ast.File{}
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution|parser.ParseComments)
			if perr != nil {
				return perr
			}
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = file
			return nil
		})
		require.NoError(t, err)
	}
	require.NotEmpty(t, out, "no production file under %v", dirs)
	return out
}

// chainStartsWith reports whether the method chain e begins with a call to
// fn (ctrl.NewControllerManagedBy(mgr).For(...)...).
func chainStartsWith(e ast.Expr, fn string) bool {
	for {
		switch v := e.(type) {
		case *ast.CallExpr:
			if isCallTo(v, fn) {
				return true
			}
			e = v.Fun
		case *ast.SelectorExpr:
			e = v.X
		default:
			return false
		}
	}
}

// isCallTo reports whether call calls pkg.fn or fn.
func isCallTo(call *ast.CallExpr, fn string) bool {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == fn && isPackageIdent(f.X)
	case *ast.Ident:
		return f.Name == fn
	}
	return false
}

// isPackageIdent reports whether e is a bare identifier (a package alias).
func isPackageIdent(e ast.Expr) bool {
	_, ok := e.(*ast.Ident)
	return ok
}

// namedInChain reports whether some .Named(...) call in file has start at
// the root of its method chain.
func namedInChain(file *ast.File, start *ast.CallExpr) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Named" {
			return true
		}
		for e := sel.X; ; {
			switch v := e.(type) {
			case *ast.CallExpr:
				if v == start {
					found = true
					return false
				}
				e = v.Fun
				continue
			case *ast.SelectorExpr:
				e = v.X
				continue
			}
			break
		}
		return true
	})
	return found
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
