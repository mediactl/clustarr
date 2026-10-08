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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mediactl/clustarr/test/guards/depguard"
	"github.com/mediactl/clustarr/test/installtest"
)

// typedPackage is one module package parsed and type-checked against the
// export data of its dependencies, as the build sees it.
type typedPackage struct {
	path  string // import path
	rel   string // module-relative directory
	fset  *token.FileSet
	files map[string]*ast.File // module-relative file path -> syntax
	info  *types.Info
	pkg   *types.Package
}

// listedExport is the slice of `go list -export -compiled -json` this loader
// reads.
type listedExport struct {
	ImportPath      string
	Dir             string
	Export          string
	CompiledGoFiles []string
	ImportMap       map[string]string
	Standard        bool
}

// typeCheckClosure type-checks every module package in pkg's closure under
// env (the binary's build settings) for which want holds. Dependencies come
// from the compiler's export data (`go list -export`), so a guard sees the
// resolved type of every call -- a method on a client.Writer, whatever the
// variable is called -- without golang.org/x/tools, which is not a module
// dependency.
func typeCheckClosure(t *testing.T, pkg string, env []string, want func(importPath string) bool) []typedPackage {
	t.Helper()
	root := installtest.RepoRoot(t)
	cmd := exec.Command("go", "list", "-deps", "-export", "-compiled",
		"-json=ImportPath,Dir,Export,CompiledGoFiles,ImportMap,Standard", pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export %s: %v\n%s", pkg, err, stderr.String())
	}
	var listed []listedExport
	exports := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedExport
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		listed = append(listed, p)
		exports[p.ImportPath] = p.Export
	}
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
	var result []typedPackage
	for _, p := range listed {
		if p.Standard || !want(p.ImportPath) {
			continue
		}
		rel, err := filepath.Rel(root, p.Dir)
		if err != nil {
			t.Fatalf("%s: %v", p.Dir, err)
		}
		depguard.RecordInputs(t, p.Dir)
		tp := typedPackage{path: p.ImportPath, rel: filepath.ToSlash(rel), fset: fset, files: map[string]*ast.File{}}
		var files []*ast.File
		for _, name := range p.CompiledGoFiles {
			full := name
			if !filepath.IsAbs(full) {
				full = filepath.Join(p.Dir, name)
			}
			f, err := parser.ParseFile(fset, full, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", full, err)
			}
			files = append(files, f)
			key := full
			if r, err := filepath.Rel(root, full); err == nil && !filepath.IsAbs(r) && r[:2] != ".." {
				key = filepath.ToSlash(r)
			}
			tp.files[key] = f
		}
		tp.info = &types.Info{
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
			Types:      map[ast.Expr]types.TypeAndValue{},
		}
		importMap := p.ImportMap
		conf := types.Config{
			Importer: importerFunc(func(path string) (*types.Package, error) {
				if mapped, ok := importMap[path]; ok {
					path = mapped
				}
				return imp.Import(path)
			}),
			Error: func(error) {},
		}
		checked, err := conf.Check(p.ImportPath, fset, files, tp.info)
		if err != nil {
			t.Fatalf("type-check %s: %v", p.ImportPath, err)
		}
		tp.pkg = checked
		result = append(result, tp)
	}
	return result
}

// importerFunc adapts a function to types.Importer.
type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// calleeOf is the function or method a call resolves to, or nil (a
// conversion, a builtin, a call of a func value).
func calleeOf(info *types.Info, call *ast.CallExpr) *types.Func {
	switch fun := unindex(call.Fun).(type) {
	case *ast.Ident:
		f, _ := info.Uses[fun].(*types.Func)
		return f
	case *ast.SelectorExpr:
		if sel, ok := info.Selections[fun]; ok {
			f, _ := sel.Obj().(*types.Func)
			return f
		}
		f, _ := info.Uses[fun.Sel].(*types.Func)
		return f
	}
	return nil
}

// findImported is the package path among pkgs' transitive imports, or nil.
func findImported(pkgs []typedPackage, path string) *types.Package {
	seen := map[*types.Package]bool{}
	var walk func(p *types.Package) *types.Package
	walk = func(p *types.Package) *types.Package {
		if seen[p] {
			return nil
		}
		seen[p] = true
		if p.Path() == path {
			return p
		}
		for _, imp := range p.Imports() {
			if found := walk(imp); found != nil {
				return found
			}
		}
		return nil
	}
	for _, tp := range pkgs {
		if found := walk(tp.pkg); found != nil {
			return found
		}
	}
	return nil
}

// interfaceIn is the interface type name declared in pkg.
func interfaceIn(t *testing.T, pkg *types.Package, name string) *types.Interface {
	t.Helper()
	obj := pkg.Scope().Lookup(name)
	if obj == nil {
		t.Fatalf("%s declares no %s", pkg.Path(), name)
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok {
		t.Fatalf("%s.%s is not an interface", pkg.Path(), name)
	}
	return iface
}
