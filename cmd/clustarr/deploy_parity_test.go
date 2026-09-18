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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Clustarr ships two installers for the same topology: config/ (kustomize)
// and charts/clustarr (Helm). They are written by hand, independently, and
// nothing until now compared them.
//
// That is how `--leader-elect` shipped on config/manager/importarr.yaml with
// no leader-election RoleBinding behind it: the chart drives every per-service
// object from one $components list, so it was right; kustomize lists each
// object by hand, so importarr's was simply forgotten. The pod stays Ready
// and loops on "leases.coordination.k8s.io is forbidden" -- a silent failure
// no other test in this repo can see.
//
// TestChartAndKustomizeAgreePerComponent renders both and asserts they name
// the same components for each of the four per-service objects.

// renderTimeout bounds each helm/kustomize invocation. Both are local,
// offline renders (the chart's subcharts are vendored under
// charts/clustarr/charts), so anything slower than this is a hang, not a slow
// machine.
const renderTimeout = 2 * time.Minute

// parityKinds are the per-service objects both installers must declare for
// exactly the same set of components. Each is a silent failure when it goes
// missing for one service: no ServiceAccount is a scheduling error, no
// ClusterRoleBinding is a forbidden-on-every-watch crash loop, no
// leader-election RoleBinding is a Ready pod that never reconciles, and no
// ServiceMonitor is a service that simply never appears in Prometheus.
var parityKinds = []string{
	"ServiceAccount",
	"ClusterRoleBinding",
	"RoleBinding",
	"ServiceMonitor",
}

// renderer is one installer's rendering, reduced to "which components does
// this declare a <kind> for".
type renderer struct {
	// name is what a failure message calls this installer.
	name string

	// prefix is stripped from object and subject names to get the component.
	// kustomize names its ServiceAccounts after the component directly;
	// Helm prefixes everything with the release fullname.
	prefix string

	// managerRole and leaderElectionRole are the roleRef names that mark a
	// binding as one of ours, so the NATS subchart's own RBAC (and any
	// future binding to a different role) is not counted.
	managerRole        string
	leaderElectionRole string

	// docs is every YAML document the installer emitted.
	docs []manifestDoc
}

// manifestDoc is the slice of a rendered object this test reads. Decoding
// into a fixed shape rather than unstructured keeps the failure mode a
// compile error instead of a nil map index.
type manifestDoc struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	RoleRef struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"roleRef"`
	Subjects []struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"subjects"`
}

// componentsFor returns the components this installer declares kind for.
func (r renderer) componentsFor(kind string) map[string]bool {
	out := map[string]bool{}
	for _, doc := range r.docs {
		if doc.Kind != kind {
			continue
		}
		switch kind {
		case "ServiceAccount":
			// Both installers label their own ServiceAccounts
			// app.kubernetes.io/name=clustarr; the vendored NATS, NACK and
			// KEDA subcharts label theirs with their own names.
			if doc.Metadata.Labels["app.kubernetes.io/name"] != "clustarr" {
				continue
			}
			out[r.component(doc.Metadata.Name)] = true
		case "ClusterRoleBinding":
			if doc.RoleRef.Kind != "ClusterRole" || doc.RoleRef.Name != r.managerRole {
				continue
			}
			out[r.component(subjectAccount(doc))] = true
		case "RoleBinding":
			if doc.RoleRef.Kind != "Role" || doc.RoleRef.Name != r.leaderElectionRole {
				continue
			}
			out[r.component(subjectAccount(doc))] = true
		case "ServiceMonitor":
			// The one object both installers label identically, because
			// the ServiceMonitor's own selector is built from that label.
			if c := doc.Metadata.Labels["app.kubernetes.io/component"]; c != "" {
				out[c] = true
			}
		}
	}
	delete(out, "")
	return out
}

// component strips the installer's name prefix.
func (r renderer) component(name string) string {
	return strings.TrimPrefix(name, r.prefix)
}

// subjectAccount returns the ServiceAccount a binding names, or "" when it
// names something else.
func subjectAccount(doc manifestDoc) string {
	for _, s := range doc.Subjects {
		if s.Kind == "ServiceAccount" {
			return s.Name
		}
	}
	return ""
}

func TestChartAndKustomizeAgreePerComponent(t *testing.T) {
	helm := findTool(t, "helm")
	kustomize := findTool(t, "kustomize")

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}

	// Release name "clustarr" makes the chart's fullname "clustarr", which
	// is the same prefix kustomize's namePrefix uses for its RBAC objects
	// and keeps the failure messages readable. serviceMonitor.enabled is off
	// by default (the Prometheus Operator CRDs may not be installed), and
	// config/prometheus is an optional overlay for the same reason, so both
	// sides are asked for them explicitly.
	chart := renderer{
		name:               "helm template charts/clustarr",
		prefix:             "clustarr-",
		managerRole:        "clustarr-manager",
		leaderElectionRole: "clustarr-leader-election",
		docs: decodeManifests(t, run(t, root, helm,
			"template", "clustarr", "charts/clustarr",
			"--set", "metrics.serviceMonitor.enabled=true")),
	}

	// kustomize names its ServiceAccounts after the component with no
	// prefix, and its ServiceMonitors likewise; only the RBAC objects carry
	// "clustarr-", and those are reached through subjects/labels above.
	kz := run(t, root, kustomize, "build", "config/default")
	kz = append(kz, run(t, root, kustomize, "build", "config/prometheus")...)
	kustom := renderer{
		name:               "kustomize build config/default + config/prometheus",
		prefix:             "",
		managerRole:        "clustarr-manager-role",
		leaderElectionRole: "clustarr-leader-election-role",
		docs:               decodeManifests(t, kz),
	}

	for _, kind := range parityKinds {
		want := chart.componentsFor(kind)
		got := kustom.componentsFor(kind)
		if len(want) == 0 {
			t.Errorf("%s declared no %s at all -- the filter in componentsFor is wrong, "+
				"not the manifests", chart.name, kind)
			continue
		}
		for c := range want {
			if !got[c] {
				t.Errorf("%s declares a %s for %q and %s does not.\n"+
					"  chart:     %v\n  kustomize: %v",
					chart.name, kind, c, kustom.name, sortedKeys(want), sortedKeys(got))
			}
		}
		for c := range got {
			if !want[c] {
				t.Errorf("%s declares a %s for %q and %s does not.\n"+
					"  chart:     %v\n  kustomize: %v",
					kustom.name, kind, c, chart.name, sortedKeys(want), sortedKeys(got))
			}
		}
	}
}

// findTool resolves name on PATH, then in GOPATH/bin (where the Makefile
// installs kustomize), and skips the test with a usable message when it is
// absent -- this test is about two installers agreeing, and it cannot say
// anything at all without the binaries that render them.
func findTool(t *testing.T, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	out, err := exec.Command("go", "env", "GOPATH").Output()
	if err == nil {
		p := filepath.Join(strings.TrimSpace(string(out)), "bin", name)
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	t.Skipf("%s is not installed (PATH or $(go env GOPATH)/bin); "+
		"chart/kustomize parity cannot be checked without it", name)
	return ""
}

// run executes bin in the repo root and returns its stdout.
func run(t *testing.T, dir, bin string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s %s: %v", bin, strings.Join(args, " "), err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", bin, strings.Join(args, " "), err, stderr.String())
		}
	case <-time.After(renderTimeout):
		_ = cmd.Process.Kill()
		t.Fatalf("%s %s: still running after %s", bin, strings.Join(args, " "), renderTimeout)
	}
	return stdout.Bytes()
}

// decodeManifests splits a multi-document YAML stream into manifestDocs,
// skipping the empty documents helm's conditional templates leave behind.
func decodeManifests(t *testing.T, in []byte) []manifestDoc {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(in), 4096)
	var out []manifestDoc
	for {
		var doc manifestDoc
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("decode rendered manifests: %v", err)
		}
		if doc.Kind == "" {
			continue
		}
		out = append(out, doc)
	}
}

// sortedKeys renders a set deterministically for a failure message.
func sortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("%v", keys)
}
