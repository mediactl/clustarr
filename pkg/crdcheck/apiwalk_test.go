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

package crdcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is a source-level model of every type under api/, shared by the
// two walkers in defaults_guard_test.go and statuslists_guard_test.go. It
// reads the Go source with go/parser rather than the generated CRDs because
// both defect classes live in the Go shape of a field -- whether it is a
// pointer, whether its json tag carries omitempty -- which the OpenAPI
// schema no longer records.

const (
	apiModule = "github.com/mediactl/clustarr"
	apiRoot   = "../../api"
)

// apiModel is every named type declared under api/, keyed by import path and
// then by type name.
type apiModel struct {
	fset *token.FileSet
	pkgs map[string]map[string]*apiType
}

// apiType is one named type declaration and the file it came from, which is
// what resolving its selector expressions (commonv1alpha1.Foo) needs.
type apiType struct {
	pkgPath string
	name    string
	spec    *ast.TypeSpec
	file    *ast.File
	markers []string
}

// qualified names a type by its API group directory, "transcode.PolicySpec",
// which every version here shares (all are v1alpha1).
func (t *apiType) qualified() string {
	return filepath.Base(filepath.Dir(t.pkgPath)) + "." + t.name
}

func newAPIModel() *apiModel {
	return &apiModel{fset: token.NewFileSet(), pkgs: map[string]map[string]*apiType{}}
}

// loadAPIModel parses every hand-written Go file under api/. The generated
// apply configurations and deepcopy files are skipped: they declare no
// schema.
func loadAPIModel(t *testing.T) *apiModel {
	t.Helper()
	m := newAPIModel()

	err := filepath.WalkDir(apiRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "applyconfiguration" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "zz_generated") {
			return nil
		}
		f, err := parser.ParseFile(m.fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(apiRoot), filepath.Dir(path))
		if err != nil {
			return err
		}
		m.addFile(f, apiModule+"/"+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("parse api/: %v", err)
	}
	if len(m.pkgs) == 0 {
		t.Fatal("parsed no packages under api/; the guard would pass vacuously")
	}
	return m
}

// modelFromSource builds a model from one synthetic file, declared as the
// package api/synthetic/v1alpha1, so each walker's classification can be
// pinned case by case rather than only against whatever api/ holds today.
func modelFromSource(t *testing.T, src string) *apiModel {
	t.Helper()
	m := newAPIModel()
	f, err := parser.ParseFile(m.fset, "synthetic.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	m.addFile(f, apiModule+"/api/synthetic/v1alpha1")
	return m
}

// addFile registers every type declaration in f under pkgPath.
func (m *apiModel) addFile(f *ast.File, pkgPath string) {
	if m.pkgs[pkgPath] == nil {
		m.pkgs[pkgPath] = map[string]*apiType{}
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			ts := s.(*ast.TypeSpec)
			doc := ts.Doc
			if doc == nil && len(gd.Specs) == 1 {
				doc = gd.Doc
			}
			m.pkgs[pkgPath][ts.Name.Name] = &apiType{
				pkgPath: pkgPath,
				name:    ts.Name.Name,
				spec:    ts,
				file:    f,
				markers: markersOf(doc),
			}
		}
	}
}

// markersOf returns the +marker lines of a comment group, without the
// leading "// +".
func markersOf(cg *ast.CommentGroup) []string {
	if cg == nil {
		return nil
	}
	var out []string
	for _, c := range cg.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if strings.HasPrefix(line, "+") {
			out = append(out, strings.TrimPrefix(line, "+"))
		}
	}
	return out
}

// docText is a comment group's prose, markers excluded.
func docText(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range cg.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if strings.HasPrefix(line, "+") {
			continue
		}
		b.WriteString(line)
		b.WriteByte(' ')
	}
	return b.String()
}

// markerValue returns the value of the first marker named name
// ("kubebuilder:default") and whether it was present.
func markerValue(markers []string, name string) (string, bool) {
	for _, mk := range markers {
		if mk == name {
			return "", true
		}
		if v, ok := strings.CutPrefix(mk, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// sortedTypes returns every type in the model in a stable order, so a
// failure lists its findings the same way on every run.
func (m *apiModel) sortedTypes() []*apiType {
	var out []*apiType
	for _, types := range m.pkgs {
		for _, at := range types {
			out = append(out, at)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].qualified() < out[j].qualified() })
	return out
}

// resolveLocal looks a type expression up in the model: an identifier in the
// declaring file's own package, or a selector naming another api/ package.
// Anything outside api/ (metav1, corev1, resource) returns nil, and the
// caller gets its qualified name from exprString.
func (m *apiModel) resolveLocal(expr ast.Expr, from *apiType) *apiType {
	switch e := expr.(type) {
	case *ast.Ident:
		return m.pkgs[from.pkgPath][e.Name]
	case *ast.SelectorExpr:
		pkgIdent, ok := e.X.(*ast.Ident)
		if !ok {
			return nil
		}
		path := importPathOf(from.file, pkgIdent.Name)
		if types, ok := m.pkgs[path]; ok {
			return types[e.Sel.Name]
		}
	}
	return nil
}

// importPathOf maps a file-local import name to its import path.
func importPathOf(f *ast.File, local string) string {
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		name := filepath.Base(path)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == local {
			return path
		}
	}
	return ""
}

// exprString renders a type expression as it is written in its file
// ("metav1.Duration", "*commonv1alpha1.SeedCriteria"), for messages.
func exprString(expr ast.Expr, from *apiType) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X, from)
	case *ast.ArrayType:
		return "[]" + exprString(e.Elt, from)
	case *ast.MapType:
		return "map[" + exprString(e.Key, from) + "]" + exprString(e.Value, from)
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name + "." + e.Sel.Name
		}
	case *ast.StructType:
		return "struct{...}"
	case *ast.InterfaceType:
		return "interface{}"
	}
	return "?"
}

// jsonTag is a field's parsed json struct tag.
type jsonTag struct {
	name      string
	omitempty bool
	omitzero  bool
	inline    bool
	skip      bool
}

func parseJSONTag(field *ast.Field) jsonTag {
	if field.Tag == nil {
		return jsonTag{}
	}
	raw, _ := strconv.Unquote(field.Tag.Value)
	v, ok := reflect.StructTag(raw).Lookup("json")
	if !ok {
		return jsonTag{}
	}
	if v == "-" {
		return jsonTag{skip: true}
	}
	parts := strings.Split(v, ",")
	out := jsonTag{name: parts[0]}
	for _, opt := range parts[1:] {
		switch opt {
		case "omitempty":
			out.omitempty = true
		case "omitzero":
			out.omitzero = true
		case "inline":
			out.inline = true
		}
	}
	return out
}

// structFields returns a named type's struct fields, or nil when it is not a
// struct.
func structFields(at *apiType) []*ast.Field {
	st, ok := at.spec.Type.(*ast.StructType)
	if !ok {
		return nil
	}
	return st.Fields.List
}

// fieldName is a field's Go name; an embedded field is named by its type.
func fieldName(f *ast.Field, owner *apiType) string {
	if len(f.Names) > 0 {
		return f.Names[0].Name
	}
	s := exprString(f.Type, owner)
	return s[strings.LastIndex(s, ".")+1:]
}

// position is "api/<group>/<version>/<file>.go:<line>", relative to the
// repository root so a failure message is clickable.
func (m *apiModel) position(pos token.Pos) string {
	p := m.fset.Position(pos)
	rel, err := filepath.Rel(filepath.Dir(apiRoot), p.Filename)
	if err != nil {
		rel = p.Filename
	}
	return filepath.ToSlash(rel) + ":" + strconv.Itoa(p.Line)
}

// externalName is the fully qualified name of a type declared outside api/
// ("k8s.io/apimachinery/pkg/apis/meta/v1.Duration"), or "" when expr is
// not a selector into another package.
func externalName(expr ast.Expr, from *apiType) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return importPathOf(from.file, id.Name) + "." + sel.Sel.Name
}
