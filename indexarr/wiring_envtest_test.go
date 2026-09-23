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

package indexarr

import (
	"context"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/controller/indexer"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// testCfg is the shared control plane. It is nil when KUBEBUILDER_ASSETS is
// unset, in which case every envtest here SKIPS -- and a suite that finishes
// in milliseconds skipped rather than passed. The source-level guards below
// do not need it and run either way.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Step 1: a guard that DISCOVERS what must be registered.
// ---------------------------------------------------------------------------

// TestEveryIndexarrRunnableIsRegistered walks indexarr/** and asserts that
// run.go names every component it finds.
//
// A hand-maintained list is the anti-pattern, and cmd/clustarr's equivalent
// guard shipped as one: its `runnableServices` names catalogarr and importarr
// only, so indexarr -- every controller, worker and RPC verb of it -- was
// invisible to it while all seven component tasks landed. This walks instead.
//
// Three shapes are discovered, each keyed on something structural rather than
// on a name we chose:
//
//   - a Runnable TYPE: an exported type with BOTH Start(ctx) error and
//     NeedLeaderElection() bool. By controller-runtime's own interfaces that
//     is a type written to be handed to mgr.Add. Nothing has that pair by
//     accident.
//   - a SetupWithManager METHOD: the registration call itself. run.go must
//     make a SetupWithManager call rooted at that package.
//   - an exported Serve FUNCTION: the shape indexarr/search uses to register
//     all three RPC verbs in one call. run.go must call it.
//
// # What it still cannot see, stated rather than hoped
//
//  1. A plain struct wired only by ASSIGNMENT into another component's field.
//     indexarr/download.Service and indexarr/query.Service reach production
//     as search.Service.Download and .Query, and nothing structural tells
//     them apart from a helper type. TestTheRPCVerbsAnswer below covers both
//     by driving them over a real bus, which is the only honest substitute.
//  2. pkg/relindex, which is under pkg/ and outside this walk. Its Open and
//     its sweep are covered by TestTheReleaseIndexIsOpenedAndSwept.
//  3. That a name in run.go is REACHED at run time. A registration behind a
//     role that is never selected, or inside an `if false`, still satisfies
//     this. TestIndexarrWiringRegistersEveryComponent starts a real manager
//     for that reason.
//  4. A field assignment that makes a component live. download.Service's
//     Definitions (the Cardigann grab path, G1-1) and the ClientCache's
//     Sessions are plain fields; left unset, every definition-backed grab
//     is refused with "not configured" and nothing structural notices. The
//     subtest "a definition-backed grab reaches the Cardigann download path"
//     below drives that path over the bus. The Torznab facade has no
//     SetupWithManager or Serve either -- it is facade.New plus Server.Run --
//     and cmd/clustarr's TestServiceStartsServesProbesAndStopsOnSignal is its
//     proof: it dials the port `clustarr all` binds.
//  5. Which VALUE a resolved local actually holds. Pass 1 below maps a local
//     variable to the package its initialiser came from by name, so two
//     variables from one package are indistinguishable -- registering the
//     same reconciler twice and never the other would satisfy this. The
//     envtest is again the backstop.
func TestEveryIndexarrRunnableIsRegistered(t *testing.T) {
	root, err := filepath.Abs(".")
	require.NoError(t, err)

	wiring := parseWiringSource(t, root)
	found := discoverComponents(t, root)

	var total int
	for _, r := range found.runnableTypes {
		total++
		require.Contains(t, wiring.text, r.name,
			"%s is a manager.Runnable (it has Start and NeedLeaderElection) declared in %s, "+
				"but indexarr/run.go never names it. A Runnable nobody adds to the manager "+
				"never runs, and neither the compiler nor the manager says a word.",
			r.name, r.file)
	}
	for _, s := range found.setups {
		total++
		require.Contains(t, wiring.setupRoots, s.pkg,
			"%s declares SetupWithManager in %s, but indexarr/run.go makes no "+
				"SetupWithManager call on anything from package %q. It is therefore never "+
				"registered: on a fresh cluster that component is simply inert, with no "+
				"compiler error and nothing in the logs. run.go's SetupWithManager calls are "+
				"rooted at %v.",
			s.name, s.file, s.pkg, sortedKeys(wiring.setupRoots))
	}
	for _, s := range found.serves {
		total++
		require.Contains(t, wiring.serveRoots, s.pkg,
			"%s is an exported Serve function in %s -- the shape that registers RPC "+
				"responders -- but indexarr/run.go never calls %s.Serve. Its verbs would "+
				"answer nothing, and every caller would get events.ErrNoResponders forever. "+
				"run.go's Serve calls are rooted at %v.",
			s.name, s.file, s.pkg, sortedKeys(wiring.serveRoots))
	}

	// Not per shape: indexarr has no Runnable TYPE today -- every runnable it
	// adds is an inline k8s.EveryReplica closure, which has no type to look
	// up. Asserting on the total keeps the guard honest about looking in the
	// right place without demanding a shape indexarr does not need.
	require.Positive(t, total,
		"no components were discovered under indexarr/; this guard is not looking where it "+
			"thinks it is")
	t.Logf("discovered %d components: %d runnable types, %d SetupWithManager, %d Serve",
		total, len(found.runnableTypes), len(found.setups), len(found.serves))
}

type componentDecl struct {
	name string // "rss.Worker" or "search.Serve"
	pkg  string // "rss"
	file string // path relative to indexarr/
}

type components struct {
	runnableTypes []componentDecl
	setups        []componentDecl
	serves        []componentDecl
}

// discoverComponents walks serviceDir for the three registrable shapes.
//
// It walks files and groups by directory rather than using
// go/parser.ParseDir, which is deprecated and which would in any case
// associate files with packages without regard for build tags.
func discoverComponents(t *testing.T, serviceDir string) components {
	t.Helper()

	type methods struct {
		start bool
		lease bool
		setup bool
		pkg   string
		file  string
	}
	seen := map[string]*methods{}
	var out components

	err := filepath.WalkDir(serviceDir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(serviceDir, path)
		dir := filepath.Dir(path)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			// A package-level exported Serve function.
			if fn.Recv == nil {
				if fn.Name.Name == "Serve" && ast.IsExported(fn.Name.Name) {
					out.serves = append(out.serves, componentDecl{
						name: file.Name.Name + ".Serve", pkg: file.Name.Name, file: rel,
					})
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
			if fn.Name.Name != "Start" && fn.Name.Name != "NeedLeaderElection" &&
				fn.Name.Name != "SetupWithManager" {
				continue
			}
			key := dir + "." + recv
			m := seen[key]
			if m == nil {
				m = &methods{pkg: file.Name.Name, file: rel}
				seen[key] = m
			}
			switch fn.Name.Name {
			case "Start":
				m.start = true
			case "NeedLeaderElection":
				m.lease = true
			case "SetupWithManager":
				m.setup = true
			}
		}
		return nil
	})
	require.NoError(t, err)

	for key, m := range seen {
		typeName := key[strings.LastIndex(key, ".")+1:]
		decl := componentDecl{name: m.pkg + "." + typeName, pkg: m.pkg, file: m.file}
		if m.start && m.lease {
			out.runnableTypes = append(out.runnableTypes, decl)
		}
		if m.setup {
			out.setups = append(out.setups, decl)
		}
	}
	sortDecls(out.runnableTypes)
	sortDecls(out.setups)
	sortDecls(out.serves)
	return out
}

func sortDecls(d []componentDecl) {
	sort.Slice(d, func(i, j int) bool { return d[i].name < d[j].name })
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
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

type wiring struct {
	text string
	// setupRoots and serveRoots are the package identifiers a
	// SetupWithManager / Serve call in the wiring source is rooted at:
	// `indexer.NewReconciler(...).SetupWithManager(mgr)` contributes
	// "indexer", `search.Serve(ctx, bus, svc)` contributes "search".
	setupRoots map[string]bool
	serveRoots map[string]bool
}

// parseWiringSource reads the service's top-level .go files -- run.go and
// anything beside it, which is where every registration call lives.
func parseWiringSource(t *testing.T, serviceDir string) wiring {
	t.Helper()
	entries, err := os.ReadDir(serviceDir)
	require.NoError(t, err)

	w := wiring{setupRoots: map[string]bool{}, serveRoots: map[string]bool{}}
	var b strings.Builder
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(serviceDir, name)
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		b.Write(raw)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		files = append(files, file)
	}
	require.NotEmpty(t, b.String(), "no wiring source found in %s", serviceDir)
	w.text = b.String()

	// Pass 1: local variables and what package they came from, so a
	// registration held in a variable still counts.
	//
	// `r := indexer.NewReconciler(...); r.Clients = c; r.SetupWithManager(mgr)`
	// registers exactly as much as the one-line chain does, and a guard that
	// rejected it would be dictating a coding style rather than checking a
	// property -- which is how a guard gets worked around instead of fixed.
	// It did reject it, once, which is why this exists.
	locals := map[string]string{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			lhs, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if root := rootIdent(assign.Rhs[0]); root != "" && root != lhs.Name {
				locals[lhs.Name] = root
			}
			return true
		})
	}

	// Pass 2: the registration calls themselves.
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			root := resolveRoot(rootIdent(sel.X), locals)
			if root == "" {
				return true
			}
			switch sel.Sel.Name {
			case "SetupWithManager":
				w.setupRoots[root] = true
			case "Serve":
				w.serveRoots[root] = true
			}
			return true
		})
	}
	return w
}

// resolveRoot follows a local variable back to the package its value came
// from, bounded so a cycle cannot hang the test.
func resolveRoot(root string, locals map[string]string) string {
	for range 8 {
		next, ok := locals[root]
		if !ok || next == root {
			return root
		}
		root = next
	}
	return root
}

// rootIdent walks a call chain back to the identifier it starts at, so that
// `indexer.NewReconciler(c, rec, lim, bus)` in
// `indexer.NewReconciler(...).SetupWithManager(mgr)` resolves to "indexer",
// and `(&download.Service{}).Handle` to "download".
func rootIdent(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e.Name
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.CallExpr:
			expr = e.Fun
		case *ast.ParenExpr:
			expr = e.X
		case *ast.UnaryExpr:
			expr = e.X
		case *ast.CompositeLit:
			expr = e.Type
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		default:
			return ""
		}
	}
}

// ---------------------------------------------------------------------------
// The limiter default, and the CRD it is derived from.
// ---------------------------------------------------------------------------

// TestIndexerSpecDefaultsMatchTheCRD pins run.go's mirror of
// spec.requestDelay's default against the generated schema, so the limiter's
// fallback for an unknown host cannot drift away from what an operator
// actually gets.
//
// It is the same shape as indexarr/controller/indexer's
// TestSpecDefaultsMatchTheGeneratedCRD, deliberately: both are anchored on
// the same generated file rather than on each other. (spec.timeout is pinned
// only there now -- the client construction this file used to duplicate moved
// into indexer.ClientCache.)
func TestIndexerSpecDefaultsMatchTheCRD(t *testing.T) {
	raw, err := os.ReadFile("../config/crd/bases/index.clustarr.io_indexers.yaml")
	require.NoError(t, err)

	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Default string `json:"default"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions, "the CRD was not parsed; run `make manifests`")

	props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties
	require.Equal(t, DefaultIndexerRequestDelay.String(), props["requestDelay"].Default,
		"run.go's DefaultIndexerRequestDelay no longer mirrors spec.requestDelay's "+
			"+kubebuilder:default, so the limiter paces an unknown host at a rate no Indexer asks for")
}

// TestTheDefaultLimiterPacesAnUnknownHost is the regression test for the
// silent construction.
//
// ratelimit.New(ratelimit.Config{}) is the obvious call -- every real per-host
// Config arrives later via SetConfig -- and it is wrong: pkg/ratelimit falls
// back to the Limiter's `defaults` for a key it has never seen, and
// Config.RPS <= 0 is rate.Inf. Any host without a Config yet is then entirely
// unpaced, which against a private tracker is a ban rather than a slowdown.
// The window is real: every host between process start and that Indexer's
// first reconcile, and any key spelled differently from the reconciler's.
func TestTheDefaultLimiterPacesAnUnknownHost(t *testing.T) {
	lim := ratelimit.New(defaultLimiterConfig())
	const unknown = "tracker.invalid:8443"

	require.True(t, lim.Allow(unknown), "the first request should be allowed by the burst")
	require.False(t, lim.Allow(unknown),
		"a host the Indexer reconciler has not configured yet is completely unpaced: "+
			"ratelimit falls back to the Limiter's defaults and RPS <= 0 is rate.Inf")
}

// TestAnExplicitZeroRequestDelayStaysUnpaced is the counter-example, and it
// matters as much as the test above.
//
// `requestDelay: 0s` is a SUPPORTED "do not pace this indexer": the CRD
// permits it and indexarr/controller/indexer's rpsFor maps it to RPS 0 on
// purpose, refusing to floor it so an operator's explicit choice is not
// silently overruled. A default that paces unknown hosts must not leak into
// that decision -- and it cannot, because SetConfig installs a config FOR THE
// KEY and pkg/ratelimit consults `defaults` only when the key has none.
func TestAnExplicitZeroRequestDelayStaysUnpaced(t *testing.T) {
	lim := ratelimit.New(defaultLimiterConfig())
	const key = "public.invalid"

	// What the Indexer reconciler does for spec.requestDelay: 0s.
	lim.SetConfig(key, ratelimit.Config{RPS: 0, Burst: 1})

	for i := range 50 {
		require.True(t, lim.Allow(key),
			"request %d was paced, but this indexer explicitly asked not to be", i)
	}
}

// ---------------------------------------------------------------------------
// Step 2: readiness must not be leader-election-gated.
// ---------------------------------------------------------------------------

// TestReadinessPassesOnANonLeaderReplica turns leader election ON, even though
// indexarr forbids it, and makes sure this manager can NEVER win the lease.
//
// Phase C's equivalent test set LeaderElect = false, which puts every runnable
// in one group and makes the assertion vacuous -- so a readiness check added
// as a bare manager.RunnableFunc shipped, never started on a non-leader
// replica, and deadlocked every rollout. indexarr is pinned to one replica
// with no lease, which makes that latent here rather than fatal; it is a
// deployment fact, not a property of this code, and this test is what keeps
// the two from being confused.
func TestReadinessPassesOnANonLeaderReplica(t *testing.T) {
	cfg := requireEnvtest(t)

	const (
		leaseNamespace = "default"
		leaseID        = "indexarr-wiring-test.clustarr.io"
	)

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	renew := metav1.NewMicroTime(time.Now().Add(24 * time.Hour))
	require.NoError(t, c.Create(t.Context(), &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: leaseID, Namespace: leaseNamespace},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To("someone-else"),
			LeaseDurationSeconds: ptr.To(int32(86400)),
			AcquireTime:          &renew,
			RenewTime:            &renew,
		},
	}))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  k8s.MustNewScheme(),
		Metrics:                 metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress:  k8s.DisabledBindAddress,
		LeaderElection:          true,
		LeaderElectionID:        leaseID,
		LeaderElectionNamespace: leaseNamespace,
	})
	require.NoError(t, err)

	store := openTestIndex(t)

	// The production call, not a restatement of it.
	ready, err := readinessChecks(mgr, store, healthz.Ping)
	require.NoError(t, err)
	require.NoError(t, k8s.AddProbes(mgr, ready))
	require.ElementsMatch(t, []string{"jetstream", "cache", "releaseindex"}, sortedCheckNames(ready),
		"§13 names the informer caches, the SQLite index and the bus; readiness gates on all three")

	// Something has to make the cache non-empty of informers, or
	// WaitForCacheSync is trivially true and the test proves less than it
	// looks like it does.
	_, err = mgr.GetCache().GetInformer(t.Context(), &indexv1alpha1.Indexer{})
	require.NoError(t, err)

	startManager(t, mgr)

	require.Never(t, func() bool {
		var lease coordinationv1.Lease
		if err := c.Get(t.Context(), client.ObjectKey{Namespace: leaseNamespace, Name: leaseID}, &lease); err != nil {
			return false
		}
		return lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "someone-else"
	}, 2*time.Second, 200*time.Millisecond,
		"the manager acquired a lease it was supposed to be locked out of")

	for name, check := range ready {
		require.Eventually(t, func() bool { return check(&http.Request{}) == nil },
			30*time.Second, 100*time.Millisecond,
			"readiness check %q never passed on a replica that does not hold the leader lease: "+
				"its runnable is leader-gated, so every rollout of this service deadlocks", name)
	}
}

// TestTheIndexReadinessCheckFailsOnAClosedStore proves the SQLite gate is a
// real check and not a constant nil. A checker that cannot fail is the same
// as no checker, and it is the shape that ships when nobody tries it.
func TestTheIndexReadinessCheckFailsOnAClosedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "releases.db")
	store, closer, err := relindex.Open(t.Context(), path)
	require.NoError(t, err)

	check := IndexReadyChecker(store)
	require.NoError(t, check(&http.Request{}))

	require.NoError(t, closer.Close())
	require.Error(t, check(&http.Request{}),
		"the readiness check passed against a closed SQLite handle, so it would never take an "+
			"indexarr pod with a broken index out of service")
}

// ---------------------------------------------------------------------------
// Step 3: the bus hooks, and the bus the reconciler needs.
// ---------------------------------------------------------------------------

// TestRunPassesTheBusHooks restates cmd/clustarr's AST guard locally, so a
// change to this file fails in this package rather than only in cmd's suite.
// Without the hooks, trace propagation exists and never runs -- which was true
// in production for an entire phase.
//
// It asserts on the parsed ConnectBus CALL rather than on the file's text.
// require.Contains over a 28 KB source file prints the whole escaped file on
// failure, which buries the one line that matters; this prints the arguments
// that were actually passed.
func TestRunPassesTheBusHooks(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var args []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ConnectBus" {
			return true
		}
		for _, arg := range call.Args {
			var b strings.Builder
			require.NoError(t, printer.Fprint(&b, fset, arg))
			args = append(args, b.String())
		}
		return false
	})
	require.NotEmpty(t, args,
		"indexarr/run.go no longer calls k8s.ConnectBus; this guard is looking for a call that "+
			"is not there")
	require.Contains(t, args, "k8s.WithBusHooks(obs.BusHooks())",
		"k8s.ConnectBus was called with %v. Without the hooks, trace propagation exists and "+
			"never runs: every span this service publishes is an orphaned root at the far end.",
		args)
}

// ---------------------------------------------------------------------------
// Step 1 again, at run time: start a real manager and watch each component work.
// ---------------------------------------------------------------------------

// TestIndexarrWiringRegistersEveryComponent is the behavioural half of the
// guard above, and the only test in this package that may call
// setupControllers: controller-runtime enforces unique controller names per
// PROCESS, not per manager, so a second call would fail with "controller with
// name indexer already exists".
//
// Each subtest is a separate component, driven through the manager
// setupControllers/setupWorkers actually built. Together they are the answer
// to the Phase C Critical this task exists for -- a controller that was never
// registered at all, with a tree-wide grep showing zero production call sites
// and every component test passing.
func TestIndexarrWiringRegistersEveryComponent(t *testing.T) {
	cfg := requireEnvtest(t)
	ctx := t.Context()

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	ns := newNamespace(t, c, "indexarr-wiring")

	nc, bus := newJetStreamBus(t)
	store := openTestIndex(t)
	clients := indexer.NewClientCache(c, ratelimit.New(defaultLimiterConfig()))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
	})
	require.NoError(t, err)

	require.NoError(t, setupControllers(mgr, bus, clients))
	_, err = setupWorkers(mgr, bus, store, clients)
	require.NoError(t, err)
	startManager(t, mgr)

	t.Run("the Indexer reconciler runs and seeds the RSS chain with a REAL bus", func(t *testing.T) {
		// indexer.NewReconciler tolerates a nil Bus by logging a warning and
		// skipping the seed. That nil path reproduces exactly the bug ruling
		// R36 fixed: nothing else starts an RSS chain, so the release
		// firehose never publishes in a real cluster and catalogarr's
		// rssmatcher -- subscribed since Phase C -- keeps receiving nothing.
		// A warning log is thin protection for that, so it is asserted here.
		srv := capsServer(t)
		idx := &indexv1alpha1.Indexer{
			ObjectMeta: metav1.ObjectMeta{Name: "seeded", Namespace: ns},
			Spec: indexv1alpha1.IndexerSpec{
				BaseURL: srv.URL,
				Generic: &indexv1alpha1.GenericNewznab{
					Protocol: commonv1alpha1.ProtocolTorrent,
					APIPath:  "/api",
				},
			},
		}
		require.NoError(t, c.Create(ctx, idx))

		var live indexv1alpha1.Indexer
		key := types.NamespacedName{Namespace: ns, Name: "seeded"}
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, key, &live); err != nil {
				return false
			}
			return live.Status.ObservedGeneration == live.Generation && live.Status.Caps != nil
		}, 30*time.Second, 200*time.Millisecond,
			"the Indexer controller never reconciled: run.go's setupControllers did not register it")

		require.Eventually(t, func() bool {
			_, ok := scheduledPoll(t, nc, string(live.UID))
			return ok
		}, 30*time.Second, 200*time.Millisecond,
			"no RssTask was seeded. Either the Indexer controller is not registered, or "+
				"run.go passed a nil events.Bus to indexer.NewReconciler -- which the "+
				"reconciler tolerates with a warning and which means this indexer is never "+
				"polled and the release firehose never starts.")
	})

	t.Run("the IndexerDefinition reconciler runs", func(t *testing.T) {
		def := &indexv1alpha1.IndexerDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "wiring-def"},
			Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: "not: a valid cardigann definition\n"},
		}
		require.NoError(t, c.Create(ctx, def))
		t.Cleanup(func() { _ = c.Delete(context.Background(), def) })

		require.Eventually(t, func() bool {
			var live indexv1alpha1.IndexerDefinition
			if err := c.Get(ctx, types.NamespacedName{Name: "wiring-def"}, &live); err != nil {
				return false
			}
			return live.Status.ObservedGeneration == live.Generation && len(live.Status.Conditions) > 0
		}, 30*time.Second, 200*time.Millisecond,
			"the IndexerDefinition controller never reconciled: run.go's setupControllers did not register it")
	})

	t.Run("the IndexerProxy reconciler runs", func(t *testing.T) {
		pxy := &indexv1alpha1.IndexerProxy{
			ObjectMeta: metav1.ObjectMeta{Name: "wiring-proxy", Namespace: ns},
			Spec: indexv1alpha1.IndexerProxySpec{
				Type: indexv1alpha1.IndexerProxyTypeHTTP,
				Host: "127.0.0.1",
				Port: 1,
				// Short, because an unreachable proxy is the point: the
				// probe must fail and status must still be written.
				RequestTimeout: metav1.Duration{Duration: 2 * time.Second},
			},
		}
		require.NoError(t, c.Create(ctx, pxy))

		require.Eventually(t, func() bool {
			var live indexv1alpha1.IndexerProxy
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "wiring-proxy"}, &live); err != nil {
				return false
			}
			return live.Status.ObservedGeneration == live.Generation && len(live.Status.Conditions) > 0
		}, 30*time.Second, 200*time.Millisecond,
			"the IndexerProxy controller never reconciled: run.go's setupControllers did not register it")
	})

	t.Run("the RPC verbs answer", func(t *testing.T) {
		// This is what covers indexarr/download and indexarr/query, which the
		// source-level guard cannot see: they reach production only as
		// search.Service.Download and .Query, and a run.go that left either
		// nil would still compile, still register the RPC group, and answer
		// every call with a populated Error field. Driving all three over the
		// bus is the only way to tell.
		require.Eventually(t, func() bool {
			var resp schema.QueryResponse
			return bus.Request(ctx, events.RPCIndexQuery, schema.QueryRequest{Limit: 1}, &resp) == nil
		}, 30*time.Second, 200*time.Millisecond,
			"nothing answered clustarr.rpc.indexarr.query: run.go never called search.Serve, "+
				"so every caller gets events.ErrNoResponders forever")

		var q schema.QueryResponse
		require.NoError(t, bus.Request(ctx, events.RPCIndexQuery, schema.QueryRequest{Limit: 1}, &q))
		require.Empty(t, q.Error,
			"the query verb answered with an error: query.Service was wired without a relindex.Store")

		// A download with no URL is a hard Error by design -- relindex cannot
		// resolve a GUID to a URL -- so a populated Error here is the verb
		// WORKING. What is being asserted is that something answered at all
		// and that it is the download body rather than search's nil-Download
		// fallback.
		var d schema.DownloadResponse
		require.NoError(t, bus.Request(ctx, events.RPCIndexDownload, schema.DownloadRequest{
			IndexerRef: schema.Ref{Namespace: ns, Name: "seeded"},
			GUID:       "wiring-probe",
		}, &d))
		require.NotContains(t, strings.ToLower(d.Error), "not wired",
			"search.Service.Download is nil: run.go registered the RPC group without the "+
				"download verb's body, so every grab from a private indexer fails")

		var s schema.SearchResponse
		require.NoError(t, bus.Request(ctx, events.RPCIndexSearch, schema.SearchRequest{
			Namespace: ns, Kind: commonv1alpha1.MediaKindMovie, Text: "wiring probe",
		}, &s))
	})

	t.Run("a definition-backed grab reaches the Cardigann download path", func(t *testing.T) {
		// download.Service REFUSES a grab from a spec.definition or
		// spec.definitionRef Indexer while its Definitions field is nil --
		// deliberately, rather than GETting what is usually a details page
		// -- so until plan task G1-5 set it, every Cardigann indexer was
		// searchable and ungrabbable, and every test in the tree was green.
		// The definition named here does not exist, so the grab still fails;
		// what is asserted is WHERE it fails: inside
		// ClientCache.DefinitionFetcherFor, building the engine, rather than
		// at the "not configured" gate in front of it.
		idx := &indexv1alpha1.Indexer{
			ObjectMeta: metav1.ObjectMeta{Name: "cardigann-grab", Namespace: ns},
			Spec: indexv1alpha1.IndexerSpec{
				DefinitionRef: ptr.To("no-such-definition"),
				BaseURL:       "http://127.0.0.1:1/",
			},
		}
		require.NoError(t, c.Create(ctx, idx))

		var d schema.DownloadResponse
		require.Eventually(t, func() bool {
			d = schema.DownloadResponse{}
			err := bus.Request(ctx, events.RPCIndexDownload, schema.DownloadRequest{
				IndexerRef: schema.Ref{Namespace: ns, Name: idx.Name},
				GUID:       "cardigann-probe",
				URL:        "http://127.0.0.1:1/download/1",
			}, &d)
			// "indexer <ns>/<name> not found" is the download verb's cache
			// catching up to the Create above; anything else is an answer.
			return err == nil && !strings.Contains(d.Error, "indexer "+ns+"/"+idx.Name+" not found")
		}, 30*time.Second, 200*time.Millisecond, "the download verb never saw the Indexer: %q", d.Error)
		require.NotContains(t, d.Error, "Cardigann download path is not configured",
			"download.Service.Definitions is nil: run.go never set it to "+
				"ClientCache.DefinitionFetcherFor, so every grab from a definition-backed indexer is refused")
		require.Contains(t, d.Error, "build client for",
			"the grab should have failed building the Cardigann engine for a missing IndexerDefinition; got %q", d.Error)
	})

	t.Run("the release index is opened and swept", func(t *testing.T) {
		// relindex lives under pkg/ and is invisible to the walk above, and
		// Open deliberately starts no goroutine: the 10-minute sweep §6.2
		// requires is run.go's to schedule. Prune is driven directly here
		// because waiting out IndexSweepInterval is not a test.
		require.Equal(t, 72*time.Hour, IndexRetention, "§6.2 fixes the window at 72h")
		require.Equal(t, 10*time.Minute, IndexSweepInterval, "§6.2 sweeps every 10 min")

		stale := time.Now().Add(-2 * IndexRetention)
		inserted, err := store.Upsert(ctx, []relindex.Release{{
			Indexer: "wiring", GUID: "stale-1", Title: "Stale Release 2019",
			TitleNorm: "stale release 2019", Group: "grp", Protocol: "torrent",
			Categories: []int{2000}, SizeBytes: 1, FetchedAt: stale,
			InfoJSON: []byte(`{}`),
		}})
		require.NoError(t, err)
		require.Equal(t, 1, inserted)

		pruneOnce(ctx, store)

		rows, err := store.Search(ctx, relindex.Query{Indexers: []string{"wiring"}})
		require.NoError(t, err)
		require.Empty(t, rows,
			"the retention sweep left a release older than IndexRetention in the index")
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func requireEnvtest(t *testing.T) *rest.Config {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return testCfg
}

func sortedCheckNames(m map[string]healthz.Checker) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newNamespace(t *testing.T, c client.Client, name string) string {
	t.Helper()
	require.NoError(t, c.Create(t.Context(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}))
	return name
}

// openTestIndex opens a real SQLite release index under the test's temporary
// directory and closes it on cleanup.
func openTestIndex(t *testing.T) relindex.Store {
	t.Helper()
	store, closer, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })
	return store
}

// newJetStreamBus boots an embedded JetStream server and provisions the real
// topology on it. membus would not do: the RSS seed's idempotence is a
// property of the broker (msg-id dedup plus Nats-Rollup on a scheduled
// publish), and membus has neither.
func newJetStreamBus(t *testing.T) (*nats.Conn, *natsbus.Bus) {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-indexarr-wiring",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   dir,
		NoLog:      true,
		NoSigs:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return nc, bus
}

// startManager runs mgr until the test finishes.
func startManager(t *testing.T, mgr ctrl.Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(60 * time.Second):
			t.Error("the manager did not stop within 60s")
		}
	})
}

// capsServer answers a t=caps probe with a minimal but valid document, so an
// Indexer reconciles all the way to Ready and the RSS seed fires. No test here
// reaches the network.
func capsServer(t *testing.T) *httptest.Server {
	t.Helper()
	const caps = `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server title="Wiring Fixture"/>
  <limits max="100" default="50"/>
  <searching>
    <search available="yes" supportedParams="q"/>
    <movie-search available="yes" supportedParams="q,imdbid"/>
  </searching>
  <categories>
    <category id="2000" name="Movies"/>
  </categories>
</caps>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, caps)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// scheduledPoll returns the RssTask queued for uid, and whether there was one
// at all. It looks on both subjects a scheduled poll can be on -- held on the
// schedule subject, or republished onto the work subject once the schedule
// fires -- because watching only one is a race.
func scheduledPoll(t *testing.T, nc *nats.Conn, uid string) (schema.RssTask, bool) {
	t.Helper()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	st, err := js.Stream(context.Background(), events.StreamWorkIndexarr)
	require.NoError(t, err)

	hold, err := events.ScheduleSubject(events.WorkRSSSubject(uid))
	require.NoError(t, err)
	for _, subj := range []string{hold, events.WorkRSSSubject(uid)} {
		msg, err := st.GetLastMsgForSubject(context.Background(), subj)
		if err != nil {
			continue
		}
		var task schema.RssTask
		if err := schema.Decode(msg.Header.Get(events.HeaderSchema), msg.Data, &task); err != nil {
			continue
		}
		return task, true
	}
	return schema.RssTask{}, false
}
