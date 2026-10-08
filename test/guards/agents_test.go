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
	"go/types"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/guards/depguard"
)

// k8sWriters are pkg/k8s's functions that write the Kubernetes API.
var k8sWriters = []string{"PatchStatus", "PatchStatusCAS", "Apply", "EnsureFinalizer", "RemoveFinalizer", "ScaleTo", "PatchStatusUnstructured"}

// clientWriteMethods are the write methods of controller-runtime's
// client.Writer and client.SubResourceWriter.
var clientWriteMethods = []string{"Create", "Update", "Patch", "Apply", "Delete", "DeleteAllOf"}

// clientGoWriteMethods are the write methods of client-go's typed and
// dynamic clients.
var clientGoWriteMethods = []string{"Create", "Update", "UpdateStatus", "Patch", "Apply", "ApplyStatus", "Delete", "DeleteCollection"}

// agentModulePackages type-checks the module packages cmd/agent links (it
// links every agent domain root), minus pkg/k8s, the writers' declaring
// home, and its readonly wrapper.
func agentModulePackages(t *testing.T) []typedPackage {
	t.Helper()
	pkgs := typeCheckClosure(t, mod+"cmd/agent", linuxCgo, func(p string) bool {
		return strings.HasPrefix(p, mod) && p != mod+"pkg/k8s"
	})
	require.GreaterOrEqual(t, len(pkgs), 150, "type-checked only %d module packages cmd/agent links; this guard is not looking where it thinks it is", len(pkgs))
	return pkgs
}

// TestAgentsCallNoKubernetesWriter (ADR-0019 §9.2, A7.1): no module package
// an agent links -- every agent domain root, the grab engines and what they
// reach -- calls a Kubernetes writer, type-checked: pkg/k8s's writers
// (PatchStatus, PatchStatusCAS, Apply, EnsureFinalizer, RemoveFinalizer,
// ScaleTo, PatchStatusUnstructured), Create/Update/Patch/Apply/Delete/
// DeleteAllOf on anything implementing controller-runtime's client.Writer or
// client.SubResourceWriter, a client-go typed or dynamic client's writers,
// and an event recorder (GetEventRecorderFor, or Event/Eventf/
// AnnotatedEventf on client-go's record or events recorder). Agents report
// over NATS records buckets; the manager is the one Kubernetes writer.
// pkg/k8s.ReadOnly is the runtime half (an agent's write fails with
// ErrAgentWrite and counts); this is the static half, so a write is found
// before it ships rather than refused in production.
func TestAgentsCallNoKubernetesWriter(t *testing.T) {
	pkgs := agentModulePackages(t)
	crclient := findImported(pkgs, "sigs.k8s.io/controller-runtime/pkg/client")
	require.NotNil(t, crclient, "the agent links controller-runtime's client; the guard needs its Writer interface")
	writer := interfaceIn(t, crclient, "Writer")
	subWriter := interfaceIn(t, crclient, "SubResourceWriter")
	implements := func(recv types.Type) bool {
		for _, iface := range []*types.Interface{writer, subWriter} {
			if types.Implements(recv, iface) || types.Implements(types.NewPointer(recv), iface) {
				return true
			}
		}
		return false
	}
	var calls int
	for _, tp := range pkgs {
		for file, f := range tp.files {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				calls++
				fn := calleeOf(tp.info, call)
				if fn == nil || fn.Pkg() == nil {
					return true
				}
				pos := tp.fset.Position(call.Pos())
				where := file + ":" + itoa(pos.Line)
				name, pkgPath := fn.Name(), fn.Pkg().Path()
				sig, _ := fn.Type().(*types.Signature)
				switch {
				case pkgPath == mod+"pkg/k8s" && slices.Contains(k8sWriters, name):
					t.Errorf("%s calls k8s.%s: an agent never writes the Kubernetes API (ADR-0019 §9.2)", where, name)
				case sig != nil && sig.Recv() != nil && slices.Contains(clientWriteMethods, name) && implements(sig.Recv().Type()):
					t.Errorf("%s calls %s on a %s: an agent never writes the Kubernetes API (ADR-0019 §9.2)", where, name, sig.Recv().Type())
				case slices.Contains(clientGoWriteMethods, name) &&
					(strings.HasPrefix(pkgPath, "k8s.io/client-go/kubernetes/typed/") || strings.HasPrefix(pkgPath, "k8s.io/client-go/dynamic")):
					t.Errorf("%s calls client-go's %s.%s: an agent never writes the Kubernetes API (ADR-0019 §9.2)", where, pkgPath, name)
				case name == "GetEventRecorderFor" || name == "GetEventRecorder":
					t.Errorf("%s calls %s: an agent records no Event (ADR-0019 §9.2; its recorder discards)", where, name)
				case (name == "Event" || name == "Eventf" || name == "AnnotatedEventf") &&
					(pkgPath == "k8s.io/client-go/tools/record" || pkgPath == "k8s.io/client-go/tools/events"):
					t.Errorf("%s calls %s.%s: an agent records no Event (ADR-0019 §9.2)", where, pkgPath, name)
				}
				return true
			})
		}
	}
	require.Greater(t, calls, 10000, "inspected only %d calls; this guard is not looking where it thinks it is", calls)
	t.Logf("inspected %d calls in %d module packages", calls, len(pkgs))
}

// itoa is strconv.Itoa without another import in a message-only path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// taskSubjectBuilder reports whether call builds a work subject: a
// pkg/events Work*Subject or ScheduleSubject, or a module function whose
// first result is one (taskFuncs, "<import path>.<Func>").
func taskSubjectBuilder(f *ast.File, call *ast.CallExpr, taskFuncs map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	ip := importPathOf(f, x.Name)
	if ip == mod+"pkg/events" && (strings.HasPrefix(sel.Sel.Name, "Work") && strings.HasSuffix(sel.Sel.Name, "Subject") || sel.Sel.Name == "ScheduleSubject") {
		return true
	}
	return taskFuncs[ip+"."+sel.Sel.Name]
}

// TestAgentsPublishNoTask (ADR-0019 §9.2, A7.1): an agent publishes only
// intake, release and event subjects (clustarr.intake.>, clustarr.rel.>,
// clustarr.evt.>); every task -- a work subject, or one scheduled -- is the
// manager's to dispatch, so the dispatch ledger sees every one. In every
// module package cmd/agent, cmd/markers or cmd/transcode links, no Publish
// takes its subject from a pkg/events Work*Subject or ScheduleSubject, from
// a module function returning one (overlayplan.Task), or from a variable
// the same function assigned from either.
func TestAgentsPublishNoTask(t *testing.T) {
	agentDirs := map[string]bool{}
	for _, bin := range []binaryBuild{{"cmd/agent", linuxCgo}, {"cmd/markers", linuxStatic}, {"cmd/transcode", linuxStatic}} {
		for dir := range moduleDirs(depguard.Load(t, mod+bin.pkg, bin.env...)) {
			agentDirs[dir] = true
		}
	}
	files := productionFiles(t)
	// Module functions whose first result is a work subject.
	taskFuncs := map[string]bool{}
	for p, f := range files {
		dir := filepath.ToSlash(filepath.Dir(p))
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ret, ok := n.(*ast.ReturnStmt)
				if !ok || len(ret.Results) == 0 {
					return true
				}
				if call, ok := ret.Results[0].(*ast.CallExpr); ok && taskSubjectBuilder(f, call, nil) {
					taskFuncs[mod+dir+"."+fn.Name.Name] = true
				}
				return true
			})
		}
	}
	require.Contains(t, taskFuncs, mod+"app/catalog/overlayplan.Task", "the overlay plan's render task builder; this guard no longer sees module task builders")
	var publishes int
	for p, f := range files {
		if !agentDirs[filepath.ToSlash(filepath.Dir(p))] {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			taskVars := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if a, ok := n.(*ast.AssignStmt); ok && len(a.Rhs) == 1 && len(a.Lhs) > 0 {
					if call, ok := a.Rhs[0].(*ast.CallExpr); ok && taskSubjectBuilder(f, call, taskFuncs) {
						if id, ok := a.Lhs[0].(*ast.Ident); ok {
							taskVars[id.Name] = true
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
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Publish" || len(call.Args) < 3 {
					return true
				}
				publishes++
				subject := call.Args[1]
				isTask := false
				switch s := subject.(type) {
				case *ast.CallExpr:
					isTask = taskSubjectBuilder(f, s, taskFuncs)
				case *ast.Ident:
					isTask = taskVars[s.Name]
				}
				if isTask {
					t.Errorf("%s (%s) publishes a task: an agent publishes only intake, release and event subjects; "+
						"the manager dispatches every task (ADR-0019 §9.2)", p, fn.Name.Name)
				}
				return true
			})
		}
	}
	require.GreaterOrEqual(t, publishes, 5, "found only %d Publish calls in agent code; this guard is not looking where it thinks it is", publishes)
	t.Logf("checked %d Publish calls in agent-linked code, %d module task builders", publishes, len(taskFuncs))
}
