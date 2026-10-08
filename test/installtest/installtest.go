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

// Package installtest holds what the installer guards and the binaries'
// manifest tests share (spec 2026-10-06 §10.3.1): finding and running helm
// and kustomize, decoding what they render, and reading the RBAC roles
// `make manifests` generates. It is imported only by tests.
package installtest

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// module is the first line of the repository's go.mod; RepoRoot walks up
// to the directory holding it.
const module = "module github.com/mediactl/clustarr"

// RenderTimeout bounds each helm/kustomize invocation. Both are local,
// offline renders (the chart's subcharts are vendored under
// charts/clustarr/charts), so anything slower than this is a hang, not a
// slow machine.
const RenderTimeout = 2 * time.Minute

// UnreachableKubeconfig is a well-formed kubeconfig naming a server nothing
// listens on: enough for ctrl.GetConfig and a lazy client, never contacted.
const UnreachableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
users:
- name: u
  user: {token: x}
`

// RequireTools reports whether a missing tool is a failure rather than a
// skip: `make test` and CI set CLUSTARR_REQUIRE_TOOLS=1.
func RequireTools() bool { return os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" }

// RepoRoot walks up from the working directory to the directory holding
// clustarr's go.mod.
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if f, err := os.Open(filepath.Join(dir, "go.mod")); err == nil {
			s := bufio.NewScanner(f)
			first := ""
			if s.Scan() {
				first = strings.TrimSpace(s.Text())
			}
			_ = f.Close()
			if first == module {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod declaring %q above the working directory", module)
		}
		dir = parent
	}
}

// FindTool resolves name on PATH, then in GOPATH/bin (where the Makefile
// installs kustomize), and skips the test with a usable message when it is
// absent -- an installer guard cannot say anything at all without the
// binaries that render the installers. Under RequireTools it fails instead.
func FindTool(t testing.TB, name string) string {
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
	if RequireTools() {
		t.Fatalf("%s is not installed (PATH or $(go env GOPATH)/bin) and CLUSTARR_REQUIRE_TOOLS=1", name)
	}
	t.Skipf("%s is not installed (PATH or $(go env GOPATH)/bin); the installers cannot be checked without it", name)
	return ""
}

// Run executes bin in dir and returns its stdout, failing the test on a
// non-zero exit or after RenderTimeout.
func Run(t testing.TB, dir, bin string, args ...string) []byte {
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
	case <-time.After(RenderTimeout):
		_ = cmd.Process.Kill()
		t.Fatalf("%s %s: still running after %s", bin, strings.Join(args, " "), RenderTimeout)
	}
	return stdout.Bytes()
}

// RenderChart is `helm template clustarr charts/clustarr <args...>` from the
// repository root.
func RenderChart(t testing.TB, args ...string) []byte {
	t.Helper()
	return Run(t, RepoRoot(t), FindTool(t, "helm"), append([]string{"template", "clustarr", "charts/clustarr"}, args...)...)
}

// BuildKustomize is `kustomize build <dir>` from the repository root.
func BuildKustomize(t testing.TB, dir string) []byte {
	t.Helper()
	return Run(t, RepoRoot(t), FindTool(t, "kustomize"), "build", dir)
}
