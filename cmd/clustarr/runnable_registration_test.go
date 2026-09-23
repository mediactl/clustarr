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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runnableServices returns every service the binary can run, derived from
// cmd/clustarr's own entrypoint variables (services.go: `runCatalogarr =
// catalogarr.Run`, ...) rather than hand-listed: a new service is guarded
// the moment the binary can start it, with no list here to forget.
//
// The list was hand-maintained until task X14 -- the very anti-pattern the
// RBAC guard beside it was rewritten to avoid -- and grabarr, squasharr,
// indexarr and captionarr each joined it only after a task noticed. ui was
// never on it; its one registrable component, the projection loop, is
// started by cmd/clustarr, which is why every service's wiring source
// includes cmd/clustarr (see wiringFiles).
func runnableServices(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "services.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	imports := map[string]string{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		alias := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		imports[alias] = path
	}
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, v := range vs.Values {
				sel, ok := v.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Run" {
					continue
				}
				x, ok := sel.X.(*ast.Ident)
				if !ok {
					continue
				}
				dir, ok := strings.CutPrefix(imports[x.Name], "github.com/mediactl/clustarr/")
				require.True(t, ok, "entrypoint %s.Run is not a Clustarr package", x.Name)
				out = append(out, dir)
			}
		}
	}
	require.GreaterOrEqual(t, len(out), 7,
		"found %v as the binary's services; services.go's `run<Service> = <pkg>.Run` block moved or changed shape",
		out)
	sort.Strings(out)
	return out
}

// pendingWiring is every component [TestEveryServiceComponentIsRegistered]
// finds unregistered today, keyed "<dir>.<Type>", with the plan task that
// owns registering it. An entry is a debt with a name on it, not an
// exemption: the test FAILS when a listed component becomes registered, so
// the owning task has to delete its line, and it fails for any unregistered
// component not listed here.
//
// It is empty: G1-5, F-6 and G2-5 each wired their components and deleted
// their lines, the last of them G2-5's seven non-video reconcilers and
// fileimport's Retrigger. A task that builds a component ahead of its wiring
// task adds a line here, naming that task, in the same commit.
var pendingWiring = map[string]string{}

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
// The shape this test keys on is exact rather than heuristic: a type with
// `Start(context.Context) error` is, by controller-runtime's own interface, a
// Runnable. Every such exported type under a service directory must be named
// in that service's wiring source, and must ALSO declare
// NeedLeaderElection: until X14 the guard keyed on both methods, so a
// bare-Start runnable -- the exact shape that deadlocked every catalogarr and
// importarr rollout (C12a critical C2), because controller-runtime puts it
// behind the lease -- was invisible to it.
//
// It is source-level because that is where the evidence is: the alternative is
// to enumerate every side effect every runnable has and assert each one, which
// is what the envtests do for the ones we know about -- and it was precisely
// the runnable nobody thought about that went missing.
func TestEveryManagerRunnableIsRegistered(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	var total int
	for _, service := range runnableServices(t) {
		t.Run(service, func(t *testing.T) {
			wiring := wiringSource(t, filepath.Join(root, service))
			found := runnableTypes(t, filepath.Join(root, service))
			total += len(found)

			for _, r := range found {
				require.Contains(t, wiring, r.name,
					"%s is a manager.Runnable (it has Start(context.Context) error) declared in %s, "+
						"but %s's run.go/wiring.go never names it. A Runnable nobody adds to the "+
						"manager never runs, and neither the compiler nor the manager says a word.",
					r.name, r.file, service)
				require.True(t, r.lease,
					"%s (%s) has Start but no NeedLeaderElection method: controller-runtime puts such a "+
						"bare-Start runnable behind the leader lease, so on a replica that elects and loses it "+
						"never starts -- the shape that deadlocked every catalogarr and importarr rollout "+
						"(C12a critical C2). Declare NeedLeaderElection and decide.",
					r.name, r.file)
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
	for _, service := range runnableServices(t) {
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

// wiringFiles are the non-test .go files that make up a service's wiring:
// its own top-level package -- run.go and anything beside it, where every
// registration call lives -- plus cmd/clustarr's, which starts ui's
// projection loop and builds ui's cluster seams itself.
func wiringFiles(t *testing.T, serviceDir string) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{serviceDir, "."} {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
}

// wiringRefs parses wiringFiles: every import path under the alias it is
// imported by, and every "alias.Name" selector they contain.
func wiringRefs(t *testing.T, serviceDir string) refs {
	t.Helper()
	r := refs{aliases: map[string]string{}, selectors: map[string]bool{}}
	for _, path := range wiringFiles(t, serviceDir) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, imp := range file.Imports {
			ipath := strings.Trim(imp.Path.Value, `"`)
			alias := ipath[strings.LastIndex(ipath, "/")+1:]
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			r.aliases[ipath] = alias
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

// wiringSource concatenates wiringFiles.
func wiringSource(t *testing.T, serviceDir string) string {
	t.Helper()
	var b strings.Builder
	for _, path := range wiringFiles(t, serviceDir) {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		b.Write(raw)
	}
	require.NotEmpty(t, b.String(), "no wiring source found in %s", serviceDir)
	return b.String()
}

type runnableDecl struct {
	name  string // "qualityprofile.Bootstrap"
	file  string
	lease bool // it declares NeedLeaderElection
}

// runnableTypes finds every exported type under serviceDir with a
// Start(context.Context) error method -- controller-runtime's Runnable --
// and records whether it also declares NeedLeaderElection.
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
			isStart := fn.Name.Name == "Start" && isRunCtxError(fn.Type)
			if !isStart && fn.Name.Name != "NeedLeaderElection" {
				continue
			}
			key := dir + "." + recv
			m := seen[key]
			if m == nil {
				rel, _ := filepath.Rel(serviceDir, path)
				m = &methods{pkg: file.Name.Name, file: rel}
				seen[key] = m
			}
			if isStart {
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
		if !m.start {
			continue
		}
		typeName := key[strings.LastIndex(key, ".")+1:]
		out = append(out, runnableDecl{name: m.pkg + "." + typeName, file: m.file, lease: m.lease})
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

// productionFiles parses every non-test .go file under the binary's service
// directories and pkg/ -- everything that can register with a manager.
func productionFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	out := map[string]*ast.File{}
	for _, dir := range append(runnableServices(t), "pkg") {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
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
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = file
			return nil
		})
		require.NoError(t, err)
	}
	require.NotEmpty(t, out)
	return out
}

// TestNoBareRunnableFunc: manager.RunnableFunc is a bare func type with no
// NeedLeaderElection method, so controller-runtime runs it only on the
// leader -- the shape that deadlocked every catalogarr and importarr rollout
// (C12a critical C2: k8s.CacheSyncChecker's readiness runnable sat behind the
// lease, so no surge pod ever went Ready). k8s.EveryReplica, or a type that
// declares NeedLeaderElection, states the decision instead. Since X14 the
// registration guard catches a bare-Start type; this catches the bare func.
func TestNoBareRunnableFunc(t *testing.T) {
	var checked int
	for path, file := range productionFiles(t) {
		alias := importAlias(file, "sigs.k8s.io/controller-runtime/pkg/manager", "manager")
		if alias == "" {
			continue
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok && isSelector(e, alias, "RunnableFunc") {
				t.Errorf("%s uses %s.RunnableFunc, which controller-runtime runs only on the leader; "+
					"use k8s.EveryReplica, or a type that declares NeedLeaderElection", path, alias)
			}
			return true
		})
	}
	require.Positive(t, checked, "no file imports controller-runtime's manager package; this guard is looking in the wrong place")
}

// TestEveryEveryReplicaIsAdded closes the registration guard's third blind
// spot: an inline k8s.EveryReplica closure -- the dominant registration
// shape -- has no type, so TestEveryManagerRunnableIsRegistered cannot see
// it at all. What it CAN get wrong is being built and never added. So every
// EveryReplica(...) in the tree must be the direct argument of an Add call
// (mgr.Add): a runnable that is only constructed, or returned to a caller
// that may drop it, never runs, and nothing says so.
func TestEveryEveryReplicaIsAdded(t *testing.T) {
	var found int
	for path, file := range productionFiles(t) {
		alias := importAlias(file, "github.com/mediactl/clustarr/pkg/k8s", "k8s")
		added := map[*ast.CallExpr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Add" {
				for _, arg := range call.Args {
					if inner, ok := arg.(*ast.CallExpr); ok {
						added[inner] = true
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			isEveryReplica := isSelector(call.Fun, alias, "EveryReplica")
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "EveryReplica" && file.Name.Name == "k8s" {
				isEveryReplica = true // pkg/k8s's own use
			}
			if !isEveryReplica {
				return true
			}
			found++
			if !added[call] {
				t.Errorf("%s: an EveryReplica runnable is built but not passed straight to Add: a runnable no "+
					"manager adds never runs, and nothing logs it", path)
			}
			return true
		})
	}
	require.Positive(t, found, "no EveryReplica call was found; this guard is looking in the wrong place")
}

// TestControllerNamesAreUniqueAcrossTheBinary: controller-runtime's
// controller names are process-global -- they label per-controller metrics
// in one registry -- so two controllers of the same name cannot both start
// in one process, and `clustarr all` runs every service in one. It works
// today only because every name is distinct; this keeps it so, statically,
// without a cluster (the start envtest, which runs every service's
// controllers in one test binary, is the runtime backstop).
//
// Every ctrl.NewControllerManagedBy chain must call Named with a string
// literal, or a literal prefix concatenated with something computed -- the
// replay handler's "replay-" + kind -- which reserves that whole prefix.
func TestControllerNamesAreUniqueAcrossTheBinary(t *testing.T) {
	names := map[string]string{}    // literal name -> where
	prefixes := map[string]string{} // reserved prefix -> where
	var chains int
	for path, file := range productionFiles(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Named" || !chainStartsWith(sel.X, "NewControllerManagedBy") {
				return true
			}
			chains++
			require.Len(t, call.Args, 1, "%s: Named takes one argument", path)
			switch arg := call.Args[0].(type) {
			case *ast.BasicLit:
				name, err := strconv.Unquote(arg.Value)
				require.NoError(t, err)
				if other, dup := names[name]; dup {
					t.Errorf("controller name %q is registered by both %s and %s; the second cannot start "+
						"in a process that runs the first (`clustarr all`)", name, other, path)
				}
				names[name] = path
			case *ast.BinaryExpr:
				lit, ok := arg.X.(*ast.BasicLit)
				require.True(t, ok && arg.Op == token.ADD,
					"%s: a computed controller name must be a string literal prefix + something", path)
				prefix, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				require.NotEmpty(t, prefix, "%s: a computed controller name needs a literal prefix", path)
				prefixes[prefix] = path
			default:
				t.Errorf("%s: controller name %T is neither a literal nor a literal prefix; this guard "+
					"cannot prove it unique", path, arg)
			}
			return true
		})
	}
	for name, where := range names {
		for prefix, owner := range prefixes {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("controller name %q (%s) falls in the %q* family %s reserves", name, where, prefix, owner)
			}
		}
	}
	require.GreaterOrEqual(t, chains, 30,
		"found only %d controller builders; this guard is not looking where it thinks it is", chains)

	// Every builder must be Named: a chain without Named takes its For
	// kind's lowercase name, which this guard cannot see and which is
	// exactly how two services' watches of one kind would collide.
	var unnamed int
	for path, file := range productionFiles(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isCallTo(call, "NewControllerManagedBy") {
				return true
			}
			if !namedInChain(file, call) {
				unnamed++
				t.Errorf("%s: a controller built without Named takes its For kind's name implicitly; "+
					"name it, so the uniqueness guard can see it", path)
			}
			return true
		})
	}
	require.Zero(t, unnamed)
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
