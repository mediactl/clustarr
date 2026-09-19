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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runnableServices are the service packages whose run.go is the registration
// point for their own sub-packages.
var runnableServices = []string{"catalogarr", "importarr"}

// TestEveryManagerRunnableIsRegistered catches a whole class of wiring
// omission, of which Task C12a shipped one.
//
// `qualityprofile.Bootstrap` exists solely to be handed to mgr.Add -- its doc
// comment says so -- and setupControllers did not hand it over. Nothing broke:
// the manager came up, every controller reconciled, and the 13 built-in
// QualityProfiles were simply never created, so every qualityProfileRef
// resolved to "not found" and the whole release-decision path was inert while
// reporting healthy. There is no compiler error and no runtime error for a
// Runnable nobody runs.
//
// The shape this test keys on is exact rather than heuristic: a type with BOTH
// `Start(context.Context) error` and `NeedLeaderElection() bool` is, by
// controller-runtime's own interfaces, a Runnable written to be added to a
// manager. Nothing else in this tree has that pair by accident. Every such
// exported type under a service directory must be named in that service's
// wiring source.
//
// It is source-level because that is where the evidence is: the alternative is
// to enumerate every side effect every runnable has and assert each one, which
// is what the envtests do for the ones we know about -- and it was precisely
// the runnable nobody thought about that went missing.
func TestEveryManagerRunnableIsRegistered(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	var total int
	for _, service := range runnableServices {
		t.Run(service, func(t *testing.T) {
			wiring := wiringSource(t, filepath.Join(root, service))
			found := runnableTypes(t, filepath.Join(root, service))
			total += len(found)

			for _, r := range found {
				require.Contains(t, wiring, r.name,
					"%s is a manager.Runnable (it has Start and NeedLeaderElection) declared in %s, "+
						"but %s's run.go/wiring.go never names it. A Runnable nobody adds to the "+
						"manager never runs, and neither the compiler nor the manager says a word.",
					r.name, r.file, service)
			}
		})
	}

	// Not per service: importarr legitimately has none today -- every runnable
	// it adds is an inline k8s.EveryReplica closure, which has no type to
	// look up. Asserting globally keeps the guard honest about looking in the
	// right place without demanding a shape a service does not need.
	require.Positive(t, total,
		"no manager.Runnable types were found under any service; this guard is not looking "+
			"where it thinks it is")
}

// wiringSource concatenates the service's top-level .go files -- run.go and
// anything beside it, which is where every registration call lives.
func wiringSource(t *testing.T, serviceDir string) string {
	t.Helper()
	entries, err := os.ReadDir(serviceDir)
	require.NoError(t, err)

	var b strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(serviceDir, name))
		require.NoError(t, err)
		b.Write(raw)
	}
	require.NotEmpty(t, b.String(), "no wiring source found in %s", serviceDir)
	return b.String()
}

type runnableDecl struct {
	name string // "qualityprofile.Bootstrap"
	file string
}

// runnableTypes finds every exported type under serviceDir with both a
// Start(context.Context) error method and a NeedLeaderElection() bool method.
//
// It walks files and groups by the package clause rather than using
// go/parser.ParseDir, which is deprecated -- and which would in any case
// associate files with packages without regard for build tags.
func runnableTypes(t *testing.T, serviceDir string) []runnableDecl {
	t.Helper()

	// (package path + type name) -> what we know about it.
	type methods struct {
		start bool
		lease bool
		pkg   string
		file  string
	}
	seen := map[string]*methods{}

	err := filepath.WalkDir(serviceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		dir := filepath.Dir(path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			if recv == "" || !ast.IsExported(recv) {
				continue
			}
			if fn.Name.Name != "Start" && fn.Name.Name != "NeedLeaderElection" {
				continue
			}
			key := dir + "." + recv
			m := seen[key]
			if m == nil {
				rel, _ := filepath.Rel(serviceDir, path)
				m = &methods{pkg: file.Name.Name, file: rel}
				seen[key] = m
			}
			if fn.Name.Name == "Start" {
				m.start = true
			} else {
				m.lease = true
			}
		}
		return nil
	})
	require.NoError(t, err)

	var out []runnableDecl
	for key, m := range seen {
		if !m.start || !m.lease {
			continue
		}
		typeName := key[strings.LastIndex(key, ".")+1:]
		out = append(out, runnableDecl{name: m.pkg + "." + typeName, file: m.file})
	}
	return out
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
