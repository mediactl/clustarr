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
	"go/ast"
	"go/token"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/guards/depguard"
)

// home is one app/ tree a binary reaches and where it is registered: its
// registration packages (app/<svc>/manager, app/<svc>/agent[/...],
// app/autoscale, app/segments/worker, app/transcode/worker) and the binary
// directories that import any of its packages. Homes are discovered from
// what the binaries import, never listed (split design §10.3.3, W5.15).
type home struct {
	service string   // "app/catalog"
	regs    []string // "app/catalog/manager", "app/catalog/agent/catalog", ...
	bins    []string // "internal/cli/manager", "internal/cli/agent", ...
}

// registrationBins are the directories each process's registration lives
// in: the manager's, the agent's and the ui's command packages, and the
// two credential-less mains.
var registrationBins = []string{"internal/cli/manager", "internal/cli/agent", "internal/cli/ui", "cmd/markers", "cmd/transcode"}

// regPkg matches a registration package: what a binary imports to register
// a domain's components (the domain's Register/Registration), as distinct
// from a component it wires itself.
var regPkg = regexp.MustCompile(`^app/[a-z]+/(manager|agent(/[a-z]+)?)$|^app/autoscale$|^app/segments/worker$|^app/transcode/worker$`)

// homes discovers every app/ tree the binaries import and its registration
// packages.
func homes(t *testing.T) []home {
	t.Helper()
	by := map[string]*home{}
	var managers, agents int
	for _, bin := range registrationBins {
		for _, f := range productionFiles(t, bin) {
			for _, imp := range f.Imports {
				ip, _ := strconv.Unquote(imp.Path.Value)
				rel, ok := strings.CutPrefix(ip, mod)
				if !ok || !strings.HasPrefix(rel, "app/") {
					continue
				}
				service := strings.Join(strings.Split(rel, "/")[:2], "/")
				h := by[service]
				if h == nil {
					h = &home{service: service}
					by[service] = h
				}
				if regPkg.MatchString(rel) && !slices.Contains(h.regs, rel) {
					h.regs = append(h.regs, rel)
					if strings.HasSuffix(rel, "/manager") {
						managers++
					}
					if strings.Contains(rel, "/agent") {
						agents++
					}
				}
				if !slices.Contains(h.bins, bin) {
					h.bins = append(h.bins, bin)
				}
			}
		}
	}
	require.GreaterOrEqual(t, managers, 6, "found %d manager registrations; internal/cli/manager moved or changed shape", managers)
	require.GreaterOrEqual(t, agents, 6, "found %d agent domain registrations; internal/cli/agent moved or changed shape", agents)
	require.Contains(t, by, "app/segments", "markers' worker is a home too")
	require.Contains(t, by, "app/transcode", "transcode's worker is a home too")
	out := make([]home, 0, len(by))
	for _, h := range by {
		out = append(out, *h)
	}
	slices.SortFunc(out, func(a, b home) int { return strings.Compare(a.service, b.service) })
	return out
}

// wiringRefs is every exported name the wiring reaches, as "<import
// path>.<Name>": each selector in the non-test files of every registration
// package and every binary directory, resolved through that file's own
// imports (an alias such as searchctl counts; a same-named package
// elsewhere does not), plus a registration package's unqualified uses of
// its own declarations. The wiring is global: a component one domain's
// registration wires for another (the remediation loop's stages) counts.
func wiringRefs(t *testing.T, hs []home) map[string]bool {
	t.Helper()
	dirs := slices.Clone(registrationBins)
	for _, h := range hs {
		dirs = append(dirs, h.regs...)
	}
	out := map[string]bool{}
	for _, dir := range dirs {
		for p, f := range productionFiles(t, dir) {
			self := mod + filepath.ToSlash(filepath.Dir(p))
			ast.Inspect(f, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.SelectorExpr:
					if x, ok := v.X.(*ast.Ident); ok {
						if ip := importPathOf(f, x.Name); ip != "" {
							out[ip+"."+v.Sel.Name] = true
						}
					}
				case *ast.Ident:
					out[self+"."+v.Name] = true
				}
				return true
			})
		}
	}
	require.NotEmpty(t, out)
	return out
}

// pendingWiring is every component [TestEveryServiceComponentIsRegistered]
// finds unregistered today, keyed "<dir>.<Type>", with the plan task that
// owns registering it. An entry is a debt with a name on it, not an
// exemption: the test FAILS when a listed component becomes registered, so
// the owning task has to delete its line, and it fails for any unregistered
// component not listed here. It is empty.
var pendingWiring = map[string]string{}

// registrable is one exported type with a registrable shape.
type registrable struct {
	name       string // "Reconciler"
	dir        string // "app/catalog/controller/movie"
	importPath string
	file       string
	shape      string
	lease      bool // it declares NeedLeaderElection (runnables only)
	// ctors are the package's exported functions whose first result is the
	// type; pkgEntrypoints are its package-level Setup/Serve functions;
	// builders are its other exported functions whose body names the type
	// (a Runtime constructor that builds the Handler and Reporter it runs).
	ctors          []string
	pkgEntrypoints []string
	builders       []string
}

// entrypoints are the selectors that reach c from outside its package.
func (c registrable) entrypoints() []string {
	out := append([]string{c.name}, c.ctors...)
	out = append(out, c.pkgEntrypoints...)
	return append(out, c.builders...)
}

// wired reports whether refs reach c.
func (c registrable) wired(refs map[string]bool) bool {
	for _, sym := range c.entrypoints() {
		if refs[c.importPath+"."+sym] {
			return true
		}
	}
	return false
}

// Shapes serviceDecls finds.
const (
	shapeSetup    = "SetupWithManager registration"
	shapeHandler  = "events.Handler (bus consumer)"
	shapeServer   = "server (Run(ctx) error)"
	shapeRunnable = "manager.Runnable (Start(ctx) error)"
)

// serviceDecls walks h.service's packages, skipping its registration
// packages (the wiring), for the four registrable shapes: SetupWithManager,
// Handle(context.Context, events.Message) error, Run(context.Context) error
// and Start(context.Context) error.
func serviceDecls(t *testing.T, h home) []registrable {
	t.Helper()
	type pkgInfo struct {
		shapes         map[string]string // type -> shape (first found)
		runnable       map[string]bool
		lease          map[string]bool
		files          map[string]string
		ctors          map[string][]string
		pkgEntrypoints []string
		bodies         map[string]map[string]bool // exported func -> identifiers its body names
	}
	pkgs := map[string]*pkgInfo{}
	for p, file := range productionFiles(t, h.service) {
		dir := filepath.ToSlash(filepath.Dir(p))
		if slices.Contains(h.regs, dir) {
			continue
		}
		info := pkgs[dir]
		if info == nil {
			info = &pkgInfo{
				shapes: map[string]string{}, runnable: map[string]bool{}, lease: map[string]bool{},
				files: map[string]string{}, ctors: map[string][]string{}, bodies: map[string]map[string]bool{},
			}
			pkgs[dir] = info
		}
		eventsAlias := importAlias(file, mod+"pkg/events", "events")
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv == nil {
				if !ast.IsExported(fn.Name.Name) {
					continue
				}
				if fn.Name.Name == "Setup" || fn.Name.Name == "Serve" {
					info.pkgEntrypoints = append(info.pkgEntrypoints, fn.Name.Name)
				}
				if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
					if typ := receiverTypeName(fn.Type.Results.List[0].Type); typ != "" {
						info.ctors[typ] = append(info.ctors[typ], fn.Name.Name)
					}
				}
				if fn.Body != nil {
					idents := map[string]bool{}
					ast.Inspect(fn.Body, func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok {
							idents[id.Name] = true
						}
						return true
					})
					info.bodies[fn.Name.Name] = idents
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
				shape = shapeSetup
			case fn.Name.Name == "Handle" && isEventsHandler(fn.Type, eventsAlias):
				shape = shapeHandler
			case fn.Name.Name == "Run" && isRunCtxError(fn.Type):
				shape = shapeServer
			case fn.Name.Name == "Start" && isRunCtxError(fn.Type):
				info.runnable[recv] = true
				info.files[recv] = p
				continue
			case fn.Name.Name == "NeedLeaderElection":
				info.lease[recv] = true
				continue
			default:
				continue
			}
			if _, seen := info.shapes[recv]; !seen {
				info.shapes[recv] = shape
				info.files[recv] = p
			}
		}
	}
	var out []registrable
	for dir, info := range pkgs {
		add := func(typ, shape string) {
			var builders []string
			for fn, idents := range info.bodies {
				if idents[typ] && !slices.Contains(info.ctors[typ], fn) {
					builders = append(builders, fn)
				}
			}
			sort.Strings(builders)
			out = append(out, registrable{
				name: typ, dir: dir, importPath: mod + dir, file: info.files[typ], shape: shape,
				lease: info.lease[typ], ctors: info.ctors[typ], pkgEntrypoints: info.pkgEntrypoints, builders: builders,
			})
		}
		for typ, shape := range info.shapes {
			add(typ, shape)
		}
		for typ := range info.runnable {
			add(typ, shapeRunnable)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].dir+out[i].name+out[i].shape < out[j].dir+out[j].name+out[j].shape
	})
	return out
}

// TestEveryManagerRunnableIsRegistered catches a whole class of wiring
// omission, of which Task C12a shipped one: `qualityprofile.Bootstrap`
// exists solely to be handed to mgr.Add, and setupControllers did not hand
// it over -- the manager came up healthy and the 13 built-in
// QualityProfiles were never created. There is no compiler error and no
// runtime error for a Runnable nobody runs.
//
// A type with `Start(context.Context) error` is, by controller-runtime's own
// interface, a Runnable. Every such exported type under a home must be
// reached from the wiring (the type or a constructor), and must ALSO
// declare NeedLeaderElection: controller-runtime puts a bare-Start runnable
// behind the lease, the shape that deadlocked every catalog and import
// rollout (C12a critical C2).
func TestEveryManagerRunnableIsRegistered(t *testing.T) {
	hs := homes(t)
	refs := wiringRefs(t, hs)
	var total int
	for _, h := range hs {
		t.Run(h.service, func(t *testing.T) {
			for _, r := range serviceDecls(t, h) {
				if r.shape != shapeRunnable {
					continue
				}
				total++
				assert.True(t, r.wired(refs),
					"%s.%s is a manager.Runnable (it has Start(context.Context) error) declared in %s, but no "+
						"registration package or binary reaches %v. A Runnable nobody adds to the manager never "+
						"runs, and neither the compiler nor the manager says a word.", r.dir, r.name, r.file, r.entrypoints())
				assert.True(t, r.lease,
					"%s.%s (%s) has Start but no NeedLeaderElection method: controller-runtime puts such a "+
						"bare-Start runnable behind the leader lease (C12a critical C2). Declare NeedLeaderElection and decide.",
					r.dir, r.name, r.file)
			}
		})
	}
	require.Positive(t, total, "no manager.Runnable types were found under any home; this guard is not looking where it thinks it is")
	t.Logf("checked %d runnable types", total)
}

// TestEveryServiceComponentIsRegistered is the guard plan task G1-5 exists
// to be: it finds code that is complete, tested and unreachable. Three
// shapes, each structural rather than a name we chose: a SetupWithManager
// method, a Handle(context.Context, events.Message) error method (an
// events.Handler, which does nothing until someone subscribes it), and a
// Run(context.Context) error method (a server). Each such exported type
// under a home must be reached from the wiring: the type, one of its
// constructors, or a package-level Setup or Serve of its package.
//
// What it cannot see, stated rather than hoped: that the name is REACHED at
// run time (a registration behind a flag nothing sets), and a component made
// live by a field assignment. The start envtests are the backstop.
func TestEveryServiceComponentIsRegistered(t *testing.T) {
	hs := homes(t)
	refs := wiringRefs(t, hs)
	var total, pending int
	for _, h := range hs {
		t.Run(h.service, func(t *testing.T) {
			for _, c := range serviceDecls(t, h) {
				if c.shape == shapeRunnable {
					continue
				}
				total++
				key := c.dir + "." + c.name
				wired := c.wired(refs)
				owner, isPending := pendingWiring[key]
				switch {
				case isPending && wired:
					t.Errorf("%s is registered now; delete its pendingWiring entry (owner %s)", key, owner)
				case isPending:
					pending++
					t.Logf("pending: %s is registered nowhere yet; plan task %s owns it", key, owner)
				case !wired:
					t.Errorf("%s (%s) is a %s, but no registration package or binary reaches %v through an import of "+
						"%s. It is complete, possibly tested, and inert: no compiler error, nothing in the logs.",
						key, c.file, c.shape, c.entrypoints(), c.importPath)
				}
			}
		})
	}
	require.GreaterOrEqual(t, total, 20,
		"found only %d SetupWithManager, events.Handler or Run(ctx) types under the homes; this guard is not looking where it thinks it is", total)
	t.Logf("checked %d components, %d pending", total, pending)
}

// TestNoBareRunnableFunc: manager.RunnableFunc is a bare func type with no
// NeedLeaderElection method, so controller-runtime runs it only on the
// leader -- the shape that deadlocked every catalog and import rollout
// (C12a critical C2). k8s.EveryReplica, k8s.LeaderOnly, or a type that
// declares NeedLeaderElection, states the decision instead.
func TestNoBareRunnableFunc(t *testing.T) {
	var checked int
	for p, file := range productionFiles(t) {
		alias := importAlias(file, "sigs.k8s.io/controller-runtime/pkg/manager", "manager")
		if alias == "" {
			continue
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok && isSelector(e, alias, "RunnableFunc") {
				t.Errorf("%s uses %s.RunnableFunc, which controller-runtime runs only on the leader; "+
					"use k8s.EveryReplica or k8s.LeaderOnly, or a type that declares NeedLeaderElection", p, alias)
			}
			return true
		})
	}
	require.Positive(t, checked, "no file imports controller-runtime's manager package; this guard is looking in the wrong place")
}

// runnableWrappers are pkg/k8s's two func types that state a runnable's
// lease decision.
var runnableWrappers = []string{"EveryReplica", "LeaderOnly"}

// isRunnableWrap reports whether call converts a func to k8s.EveryReplica or
// k8s.LeaderOnly (or, inside pkg/k8s, the bare name), and which.
func isRunnableWrap(file *ast.File, call *ast.CallExpr) (string, bool) {
	alias := importAlias(file, mod+"pkg/k8s", "k8s")
	for _, w := range runnableWrappers {
		if isSelector(call.Fun, alias, w) {
			return w, true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == w && file.Name.Name == "k8s" {
			return w, true
		}
	}
	return "", false
}

// adderParams are the names of fn's parameters typed func(<pkg>.Runnable)
// error: an adder a caller injects (mgr.Add, passed as a method value).
func adderParams(fn *ast.FuncDecl) map[string]int {
	out := map[string]int{}
	if fn.Type.Params == nil {
		return out
	}
	i := 0
	for _, field := range fn.Type.Params.List {
		n := max(len(field.Names), 1)
		ft, ok := field.Type.(*ast.FuncType)
		isAdder := ok && ft.Params != nil && len(ft.Params.List) == 1 && ft.Results != nil &&
			len(ft.Results.List) == 1 && isIdent(ft.Results.List[0].Type, "error")
		if isAdder {
			sel, ok := ft.Params.List[0].Type.(*ast.SelectorExpr)
			isAdder = ok && sel.Sel.Name == "Runnable"
		}
		for j := range n {
			if isAdder && j < len(field.Names) {
				out[field.Names[j].Name] = i + j
			}
		}
		i += n
	}
	return out
}

// TestEveryEveryReplicaIsAdded closes the registration guard's third blind
// spot: an inline k8s.EveryReplica or k8s.LeaderOnly closure -- the
// dominant registration shape -- has no type, so the runnable guard cannot
// see it. What it CAN get wrong is being built and never added. So each must
// reach a manager in one of three shapes, each checked to its end:
//
//   - the direct argument of an Add call (mgr.Add(k8s.EveryReplica(...)));
//   - the direct argument of an injected adder: a parameter typed
//     func(manager.Runnable) error, where every call of the enclosing
//     function passes an Add method value for it (app/indexer/agent's
//     newClientCache, app/import/agent's registerProbeWorkers);
//   - the result of a function F, where every call of F assigns it to a
//     variable that the same function then passes to Add (app/indexer/
//     manager's bundleRunnable).
//
// A runnable that is only constructed, or returned to a caller that drops
// it, never runs, and nothing logs it.
func TestEveryEveryReplicaIsAdded(t *testing.T) {
	files := productionFiles(t)
	type site struct{ path, fn, pkg string }
	injected := map[string]map[int]bool{} // "<pkg dir>.<func>" -> adder param positions that must get an Add
	returned := map[string]bool{}         // "<pkg dir>.<func>" -> returns a wrapper
	var found int
	for p, file := range files {
		dir := filepath.ToSlash(filepath.Dir(p))
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			adders := adderParams(fn)
			ok2 := map[*ast.CallExpr]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.CallExpr:
					added := false
					if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Add" {
						added = true
					}
					adderPos, viaAdder := -1, false
					if id, ok := v.Fun.(*ast.Ident); ok {
						if pos, isAdder := adders[id.Name]; isAdder {
							adderPos, viaAdder = pos, true
						}
					}
					for _, arg := range v.Args {
						if inner, ok := arg.(*ast.CallExpr); ok && (added || viaAdder) {
							ok2[inner] = true
							if viaAdder {
								if injected[dir+"."+fn.Name.Name] == nil {
									injected[dir+"."+fn.Name.Name] = map[int]bool{}
								}
								injected[dir+"."+fn.Name.Name][adderPos] = true
							}
						}
					}
				case *ast.ReturnStmt:
					for _, r := range v.Results {
						if inner, ok := r.(*ast.CallExpr); ok {
							if _, wrap := isRunnableWrap(file, inner); wrap {
								ok2[inner] = true
								returned[dir+"."+fn.Name.Name] = true
							}
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				w, wrap := isRunnableWrap(file, call)
				if !wrap {
					return true
				}
				found++
				if !ok2[call] {
					t.Errorf("%s (%s): a k8s.%s runnable is built but neither passed straight to Add, to an injected "+
						"adder, nor returned: a runnable no manager adds never runs, and nothing logs it", p, fn.Name.Name, w)
				}
				return true
			})
		}
	}
	require.GreaterOrEqual(t, found, 10, "found only %d EveryReplica/LeaderOnly calls; this guard is looking in the wrong place", found)
	t.Logf("checked %d EveryReplica/LeaderOnly runnables, %d injected adders, %d returning functions", found, len(injected), len(returned))

	// Close the injected and returned shapes at their call sites.
	callsOf := func(key string, visit func(path string, enclosing *ast.FuncDecl, call *ast.CallExpr, assign *ast.AssignStmt)) int {
		dir, name := key[:strings.LastIndex(key, ".")], key[strings.LastIndex(key, ".")+1:]
		var n int
		for p, file := range files {
			fdir := filepath.ToSlash(filepath.Dir(p))
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var assign *ast.AssignStmt
				ast.Inspect(fn.Body, func(nd ast.Node) bool {
					if a, ok := nd.(*ast.AssignStmt); ok {
						assign = a
					}
					call, ok := nd.(*ast.CallExpr)
					if !ok {
						return true
					}
					match := false
					switch f := call.Fun.(type) {
					case *ast.Ident:
						match = fdir == dir && f.Name == name
					case *ast.SelectorExpr:
						if x, ok := f.X.(*ast.Ident); ok {
							match = importPathOf(file, x.Name) == mod+dir && f.Sel.Name == name
						}
					}
					if match {
						n++
						var a *ast.AssignStmt
						if assign != nil && slices.Contains(assign.Rhs, ast.Expr(call)) {
							a = assign
						}
						visit(p, fn, call, a)
					}
					return true
				})
			}
		}
		return n
	}
	for _, key := range slices.Sorted(maps.Keys(injected)) {
		n := callsOf(key, func(p string, _ *ast.FuncDecl, call *ast.CallExpr, _ *ast.AssignStmt) {
			for pos := range injected[key] {
				if pos >= len(call.Args) {
					t.Errorf("%s: a call of %s passes no argument for its injected adder", p, key)
					continue
				}
				if sel, ok := call.Args[pos].(*ast.SelectorExpr); !ok || sel.Sel.Name != "Add" {
					t.Errorf("%s: %s is called with %T for its injected adder, not a manager's Add: the runnables it "+
						"builds may never be added", p, key, call.Args[pos])
				}
			}
		})
		assert.Positive(t, n, "%s injects an adder but nothing calls it", key)
	}
	for _, key := range slices.Sorted(maps.Keys(returned)) {
		n := callsOf(key, func(p string, enclosing *ast.FuncDecl, call *ast.CallExpr, assign *ast.AssignStmt) {
			if assign == nil || len(assign.Lhs) == 0 {
				t.Errorf("%s: the runnable %s returns is not assigned, so nothing adds it", p, key)
				return
			}
			v, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				t.Errorf("%s: the runnable %s returns is assigned to %T; this guard cannot follow it", p, key, assign.Lhs[0])
				return
			}
			addedLater := false
			ast.Inspect(enclosing.Body, func(nd ast.Node) bool {
				c, ok := nd.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Add" && len(c.Args) == 1 && isIdent(c.Args[0], v.Name) {
					addedLater = true
				}
				return true
			})
			assert.True(t, addedLater, "%s: %s's runnable lands in %s, which %s never passes to Add", p, key, v.Name, enclosing.Name.Name)
		})
		assert.Positive(t, n, "%s returns a runnable but nothing calls it", key)
	}
}

// controllerName resolves a Named argument: a string literal, a literal
// prefix concatenated with something computed (which reserves the prefix),
// or a string constant declared at package level in dir (resolved in the
// package's own files, or through an import for pkg.Const).
func controllerName(t *testing.T, files map[string]*ast.File, file *ast.File, dir string, arg ast.Expr) (name string, prefix bool, ok bool) {
	t.Helper()
	switch a := arg.(type) {
	case *ast.BasicLit:
		s, err := strconv.Unquote(a.Value)
		return s, false, err == nil
	case *ast.BinaryExpr:
		lit, isLit := a.X.(*ast.BasicLit)
		if !isLit || a.Op != token.ADD {
			return "", false, false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, true, err == nil && s != ""
	case *ast.Ident:
		return packageConst(files, dir, a.Name)
	case *ast.SelectorExpr:
		if x, isIdent := a.X.(*ast.Ident); isIdent {
			if ip := importPathOf(file, x.Name); strings.HasPrefix(ip, mod) {
				return packageConst(files, strings.TrimPrefix(ip, mod), a.Sel.Name)
			}
		}
	}
	return "", false, false
}

// packageConst is the string value of the package-level constant name
// declared in dir's files.
func packageConst(files map[string]*ast.File, dir, name string) (string, bool, bool) {
	for p, f := range files {
		if path.Dir(p) != dir {
			continue
		}
		for _, d := range f.Decls {
			gen, ok := d.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if n.Name != name || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, err := strconv.Unquote(lit.Value)
						return s, false, err == nil
					}
				}
			}
		}
	}
	return "", false, false
}

// TestControllerNamesAreUniqueAcrossTheBinary: controller-runtime's
// controller names are process-global -- they label per-controller metrics
// in one registry -- so two controllers of the same name cannot both start
// in one process. Only the manager registers controllers in bulk, so
// uniqueness is held across what cmd/manager links (split design §10.3.2).
//
// Every builder chain (NewControllerManagedBy, ControllerManagedBy, the
// generic TypedControllerManagedBy[K] the remediation loop uses) must call
// Named with a string literal, a package-level string constant
// (admission.ControllerName), or a literal prefix concatenated with
// something computed -- the replay handler's "replay-" + kind -- which
// reserves that whole prefix.
//
// The floor is 20: the manager linked 30-odd builders before the fold, and
// the remediation loop replaced the MediaFile, rename, SubtitleRequest,
// TranscodeJob, AudioGraft and Download reconcilers (F3.4, F5.3, F6.6,
// F7.3, A9.1) with one loop controller per key kind; 25 were counted when
// the batch rewrote this guard (2026-10-07).
func TestControllerNamesAreUniqueAcrossTheBinary(t *testing.T) {
	linked := moduleDirs(depguard.Load(t, mod+"cmd/manager", linuxStatic...))
	all := productionFiles(t)
	files := map[string]*ast.File{}
	for p, f := range all {
		if linked[filepath.ToSlash(filepath.Dir(p))] {
			files[p] = f
		}
	}
	names := map[string]string{}    // literal name -> where
	prefixes := map[string]string{} // reserved prefix -> where
	var chains int
	for p, file := range files {
		dir := filepath.ToSlash(filepath.Dir(p))
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Named" || !builderChain(sel.X) {
				return true
			}
			chains++
			require.Len(t, call.Args, 1, "%s: Named takes one argument", p)
			name, isPrefix, ok := controllerName(t, all, file, dir, call.Args[0])
			switch {
			case !ok:
				t.Errorf("%s: controller name %T is neither a literal, a package string constant nor a literal "+
					"prefix; this guard cannot prove it unique", p, call.Args[0])
			case isPrefix:
				prefixes[name] = p
			default:
				if other, dup := names[name]; dup {
					t.Errorf("controller name %q is registered by both %s and %s; the second cannot start "+
						"in the manager that runs the first", name, other, p)
				}
				names[name] = p
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
	require.GreaterOrEqual(t, chains, 20,
		"found only %d controller builders the manager links; this guard is not looking where it thinks it is", chains)
	t.Logf("checked %d named controller builders, %d reserved prefixes", chains, len(prefixes))

	// Every builder must be Named: a chain without Named takes its For
	// kind's lowercase name, which this guard cannot see and which is
	// exactly how two watches of one kind would collide.
	for p, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isBuilderStart(call) {
				return true
			}
			if !namedInChain(file, call) {
				t.Errorf("%s: a controller built without Named takes its For kind's name implicitly; "+
					"name it, so the uniqueness guard can see it", p)
			}
			return true
		})
	}
}

// controllerBuildsIn reports whether f builds a controller: a builder chain,
// or controller-runtime's controller.New/NewTyped/NewUnmanaged/
// NewTypedUnmanaged.
func controllerBuildsIn(f *ast.File) bool {
	ctrlAlias := importAlias(f, "sigs.k8s.io/controller-runtime/pkg/controller", "controller")
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		if isBuilderStart(call) {
			found = true
			return false
		}
		if sel, ok := unindex(call.Fun).(*ast.SelectorExpr); ok && ctrlAlias != "" {
			switch sel.Sel.Name {
			case "New", "NewTyped", "NewUnmanaged", "NewTypedUnmanaged":
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == ctrlAlias {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// binaryBuild is one binary and the build settings its image uses.
type binaryBuild struct {
	pkg string
	env []string
}

// nonManagerBinaries are every binary but the manager.
var nonManagerBinaries = []binaryBuild{
	{"cmd/agent", linuxCgo}, {"cmd/markers", linuxStatic}, {"cmd/transcode", linuxStatic}, {"cmd/ui", linuxStatic},
}

// TestOnlyTheManagerRegistersControllers: no module package another binary
// links builds a controller (split design §10.3.2). The split let the two
// grab engines keep an engine-local Download watch; the Download kind is
// gone (A9.1) and the engines take commands over NATS (A3.6), so the agent
// links none now, like markers, transcode and the ui. A controller anywhere
// else would reconcile from a process that never elects and, since
// ADR-0019, cannot write what it reconciles.
func TestOnlyTheManagerRegistersControllers(t *testing.T) {
	files := productionFiles(t)
	builders := map[string]bool{}
	for p, f := range files {
		if controllerBuildsIn(f) {
			builders[filepath.ToSlash(filepath.Dir(p))] = true
		}
	}
	require.GreaterOrEqual(t, len(builders), 15, "found only %d packages building a controller; this guard is not looking where it thinks it is", len(builders))
	allowed := map[string][]string{
		"cmd/agent":     nil,
		"cmd/markers":   nil,
		"cmd/transcode": nil,
		"cmd/ui":        nil,
	}
	for _, bin := range nonManagerBinaries {
		linked := moduleDirs(depguard.Load(t, mod+bin.pkg, bin.env...))
		var got []string
		for dir := range builders {
			if linked[dir] {
				got = append(got, dir)
			}
		}
		assert.ElementsMatch(t, allowed[bin.pkg], got, "%s links controller builders", bin.pkg)
	}
}

// leaderTypes are the "<import path>.<Name>" of every type whose
// NeedLeaderElection returns true, and of every exported function of the
// declaring package whose first result is that type (or a pointer to it).
func leaderTypes(files map[string]*ast.File) map[string]bool {
	types := map[string]bool{} // "<dir>.<Type>"
	for p, f := range files {
		dir := filepath.ToSlash(filepath.Dir(p))
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "NeedLeaderElection" || fn.Body == nil || len(fn.Body.List) != 1 {
				continue
			}
			if ret, ok := fn.Body.List[0].(*ast.ReturnStmt); ok && len(ret.Results) == 1 && isIdent(ret.Results[0], "true") {
				types[dir+"."+receiverTypeName(fn.Recv.List[0].Type)] = true
			}
		}
	}
	out := map[string]bool{}
	for k := range types {
		out[mod+k] = true
	}
	for p, f := range files {
		dir := filepath.ToSlash(filepath.Dir(p))
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !ast.IsExported(fn.Name.Name) || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
				continue
			}
			if typ := receiverTypeName(fn.Type.Results.List[0].Type); typ != "" && types[dir+"."+typ] {
				out[mod+dir+"."+fn.Name.Name] = true
			}
		}
	}
	return out
}

// TestLeaderOnlyRunnablesLiveInTheManager (R9): a leader-only runnable -- a
// type whose NeedLeaderElection returns true, a constructor of one, or a
// k8s.LeaderOnly wrapper -- is wired only in packages cmd/manager links and
// no other binary does. Every other binary never elects, so a leader-only
// runnable there would never start, or (were it to elect) contend for a
// lease the manager's identity owns.
func TestLeaderOnlyRunnablesLiveInTheManager(t *testing.T) {
	managerOnly := moduleDirs(depguard.Load(t, mod+"cmd/manager", linuxStatic...))
	for _, bin := range nonManagerBinaries {
		for dir := range moduleDirs(depguard.Load(t, mod+bin.pkg, bin.env...)) {
			delete(managerOnly, dir)
		}
	}
	files := productionFiles(t)
	leaders := leaderTypes(files)
	var sites int
	for p, f := range files {
		dir := filepath.ToSlash(filepath.Dir(p))
		if dir == "pkg/k8s" {
			continue // declares LeaderOnly
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			ip := importPathOf(f, x.Name)
			if (ip == mod+"pkg/k8s" && sel.Sel.Name == "LeaderOnly") || leaders[ip+"."+sel.Sel.Name] {
				sites++
				assert.True(t, managerOnly[dir], "%s wires the leader-only %s.%s, but %s is linked by a binary that never elects (R9)",
					p, x.Name, sel.Sel.Name, dir)
			}
			return true
		})
	}
	require.GreaterOrEqual(t, sites, 4, "found %d leader-only wiring sites; this guard is not looking where it thinks it is", sites)
	t.Logf("checked %d leader-only wiring sites", sites)
}
