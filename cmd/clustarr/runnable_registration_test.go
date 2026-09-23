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
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runnableServices are the service packages whose run.go is the registration
// point for their own sub-packages.
//
// grabarr joined this list for plan task D2-8: D2-8b's torrent.Reaper and
// usenet.Reaper (commit d5c01d2) are exactly the failure shape this test was
// built for -- a manager.Runnable with NeedLeaderElection()==false so it
// runs on every replica, sitting in its own package with nothing under
// grabarr/run.go naming it. An unregistered reaper is not a failing test
// anywhere; it is simply a torrent that seeds, or a usenet fetch that keeps
// spending the provider's connection budget, forever.
//
// squasharr joined for plan task E-4. indexarr and captionarr joined for plan
// task G1-5, together with the component shapes below: indexarr had its own
// walk (indexarr/wiring_envtest_test.go) and so was left out here, and
// captionarr's controllers and fetch worker were -- and, pending F-6, are --
// registered nowhere.
var runnableServices = []string{"catalogarr", "importarr", "indexarr", "grabarr", "squasharr", "captionarr"}

// pendingWiring is every component [TestEveryServiceComponentIsRegistered]
// finds unregistered today, keyed "<dir>.<Type>", with the plan task that
// owns registering it. An entry is a debt with a name on it, not an
// exemption: the test FAILS when a listed component becomes registered, so
// the owning task has to delete its line, and it fails for any unregistered
// component not listed here.
//
// album, author and book, and fileimport's Retrigger, are listed before they
// are committed: plan tasks G2-2 and G2-4 build them in parallel with this,
// and G2-5 is the task that wires every G2 component, so the guard must not
// turn their own commits red. It will turn G2-5's red until G2-5 deletes
// the lines -- which is the point.
var pendingWiring = map[string]string{
	"catalogarr/controller/album.Reconciler":            "G2-5",
	"catalogarr/controller/artist.Reconciler":           "G2-5",
	"catalogarr/controller/audiobook.Reconciler":        "G2-5",
	"catalogarr/controller/author.Reconciler":           "G2-5",
	"catalogarr/controller/book.Reconciler":             "G2-5",
	"catalogarr/controller/comic.Reconciler":            "G2-5",
	"catalogarr/controller/issue.Reconciler":            "G2-5",
	"importarr/worker/fileimport.Retrigger":             "G2-5",
	"captionarr/controller/subtitleprofile.Reconciler":  "F-6",
	"captionarr/controller/subtitleprovider.Reconciler": "F-6",
	"captionarr/controller/subtitlerequest.Reconciler":  "F-6",
	"captionarr/worker/fetch.Worker":                    "F-6",
}

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

// TestEveryServiceComponentIsRegistered is the guard plan task G1-5 exists
// to be: it finds code that is complete, tested and unreachable.
//
// TestEveryManagerRunnableIsRegistered keys on Start+NeedLeaderElection, and
// almost nothing Phases D through G built has that shape. The history sink,
// the DLQ projector, the import-list worker, the Torznab facade and every
// reconciler are registered through SetupWithManager, a closure around
// bus.Subscribe(..., x.Handle), or mgr.Add(k8s.EveryReplica(srv.Run)) -- so
// every one of them was invisible to it, and G1-5 found all four G1
// components registered nowhere with every test green. D2's equivalent task
// found three more.
//
// Three shapes, each structural rather than a name we chose:
//
//   - a SetupWithManager method: the registration call itself;
//   - a Handle(context.Context, events.Message) error method: an
//     events.Handler, which does nothing until someone subscribes it;
//   - a Run(context.Context) error method: a server, which serves nothing
//     until someone adds it to the manager.
//
// Each such exported type must be reachable from its service's wiring
// source: that source must name the type, or one of its constructors (an
// exported function of the same package whose first result is the type), or
// a package-level Setup or Serve of that package -- catalogarr/metadata and
// indexarr/search register their own internals through one of those. The
// package qualifier is resolved through the wiring files' own imports, so an
// alias (searchctl, importlistctrl) counts and a same-named package
// elsewhere does not.
//
// What it cannot see, stated rather than hoped: that the name is REACHED at
// run time (a registration behind a role nothing selects), and a component
// made live by a field assignment (download.Service.Definitions). The start
// envtest is the backstop for the first; indexarr's wiring envtest drives
// the second.
func TestEveryServiceComponentIsRegistered(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	pending := map[string]bool{}
	var total int
	for _, service := range runnableServices {
		t.Run(service, func(t *testing.T) {
			serviceDir := filepath.Join(root, service)
			refs := wiringRefs(t, serviceDir)
			for _, c := range serviceComponents(t, root, serviceDir) {
				total++
				key := c.dir + "." + c.name
				alias, imported := refs.aliases[c.importPath]
				wired := false
				if imported {
					for _, sym := range c.entrypoints() {
						if refs.selectors[alias+"."+sym] {
							wired = true
							break
						}
					}
				}
				owner, isPending := pendingWiring[key]
				switch {
				case isPending && wired:
					t.Errorf("%s is registered now; delete its pendingWiring entry (owner %s) so the guard "+
						"watches it again", key, owner)
				case isPending:
					pending[key] = true
					t.Logf("pending: %s is registered nowhere yet; plan task %s owns it", key, owner)
				case !wired:
					t.Errorf("%s (%s) is a %s, but %s's wiring source (%s/*.go) never reaches it -- "+
						"it does not name %v through an import of %s. It is complete, possibly tested, "+
						"and inert: no compiler error, nothing in the logs.",
						key, c.file, c.shape, service, service, c.entrypoints(), c.importPath)
				}
			}
		})
	}
	require.Positive(t, total,
		"no SetupWithManager, events.Handler or Run(ctx) types were found under any service; "+
			"this guard is not looking where it thinks it is")
	t.Logf("checked %d components, %d pending", total, len(pending))
}

// component is one exported type with a registrable shape.
type component struct {
	name       string // "Reconciler"
	dir        string // "catalogarr/controller/movie", slash-separated, relative to the repo root
	importPath string
	file       string
	shape      string
	// ctors are the package's exported functions whose first result is the
	// type; pkgEntrypoints are its package-level Setup/Serve functions.
	ctors          []string
	pkgEntrypoints []string
}

// entrypoints are the selectors that reach c from outside its package.
func (c component) entrypoints() []string {
	out := append([]string{c.name}, c.ctors...)
	return append(out, c.pkgEntrypoints...)
}

// serviceComponents walks serviceDir's sub-packages for the three shapes
// TestEveryServiceComponentIsRegistered describes. The service's own
// top-level package is the wiring, not a component, and is skipped.
func serviceComponents(t *testing.T, root, serviceDir string) []component {
	t.Helper()

	type pkgInfo struct {
		shapes         map[string]string   // type -> shape
		files          map[string]string   // type -> file
		ctors          map[string][]string // type -> constructors
		pkgEntrypoints []string
	}
	pkgs := map[string]*pkgInfo{}

	err := filepath.WalkDir(serviceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		if dir == serviceDir {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		info := pkgs[dir]
		if info == nil {
			info = &pkgInfo{shapes: map[string]string{}, files: map[string]string{}, ctors: map[string][]string{}}
			pkgs[dir] = info
		}
		rel, _ := filepath.Rel(root, path)
		eventsAlias := importAlias(file, "github.com/mediactl/clustarr/pkg/events", "events")

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !ast.IsExported(fn.Name.Name) {
				continue
			}
			if fn.Recv == nil {
				if fn.Name.Name == "Setup" || fn.Name.Name == "Serve" {
					info.pkgEntrypoints = append(info.pkgEntrypoints, fn.Name.Name)
				}
				if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
					if typ := receiverTypeName(fn.Type.Results.List[0].Type); typ != "" {
						info.ctors[typ] = append(info.ctors[typ], fn.Name.Name)
					}
				}
				continue
			}
			if len(fn.Recv.List) != 1 {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			if recv == "" || !ast.IsExported(recv) {
				continue
			}
			shape := ""
			switch {
			case fn.Name.Name == "SetupWithManager":
				shape = "SetupWithManager registration"
			case fn.Name.Name == "Handle" && isEventsHandler(fn.Type, eventsAlias):
				shape = "events.Handler (bus consumer)"
			case fn.Name.Name == "Run" && isRunCtxError(fn.Type):
				shape = "server (Run(ctx) error)"
			default:
				continue
			}
			if _, seen := info.shapes[recv]; !seen {
				info.shapes[recv] = shape
				info.files[recv] = rel
			}
		}
		return nil
	})
	require.NoError(t, err)

	var out []component
	for dir, info := range pkgs {
		relDir, _ := filepath.Rel(root, dir)
		relDir = filepath.ToSlash(relDir)
		for typ, shape := range info.shapes {
			out = append(out, component{
				name: typ, dir: relDir, importPath: "github.com/mediactl/clustarr/" + relDir,
				file: info.files[typ], shape: shape,
				ctors: info.ctors[typ], pkgEntrypoints: info.pkgEntrypoints,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir+out[i].name < out[j].dir+out[j].name })
	return out
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

// refs is what a service's wiring files reach: every import path under the
// alias it is imported by, and every "alias.Name" selector they contain.
type refs struct {
	aliases   map[string]string
	selectors map[string]bool
}

// wiringRefs parses the service's top-level .go files -- run.go and anything
// beside it, the same set wiringSource reads.
func wiringRefs(t *testing.T, serviceDir string) refs {
	t.Helper()
	entries, err := os.ReadDir(serviceDir)
	require.NoError(t, err)

	r := refs{aliases: map[string]string{}, selectors: map[string]bool{}}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(serviceDir, name), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			alias := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			r.aliases[path] = alias
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok {
					r.selectors[x.Name+"."+sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	return r
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
