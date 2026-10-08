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

// Package depguard loads one package's import graph through `go list -deps`
// and checks it against rules (split design §4.5). It is imported only by
// tests: every binary's deps guard in test/guards is built on it.
package depguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Rule matches the import paths one deny-list entry forbids.
type Rule struct {
	Path   string
	Exact  bool           // false: Path or Path/...
	Except []string       // exact import paths the rule allows
	Regexp *regexp.Regexp // when set, matches instead of Path
	Why    string
}

// Matches reports whether r forbids pkg.
func (r Rule) Matches(pkg string) bool {
	if slices.Contains(r.Except, pkg) {
		return false
	}
	if r.Regexp != nil {
		return r.Regexp.MatchString(pkg)
	}
	return pkg == r.Path || (!r.Exact && strings.HasPrefix(pkg, r.Path+"/"))
}

// Graph is a package's transitive import closure.
type Graph struct {
	root    string
	imports map[string][]string
	std     map[string]bool
}

type listed struct {
	ImportPath string
	Imports    []string
	Standard   bool
	Dir        string
	Module     *struct{ Path string }
}

// mod is the module path; Load runs from its root and AllowApp classifies
// its app/ packages.
const mod = "github.com/mediactl/clustarr/"

// Load runs `go list -deps -json=ImportPath,Imports,Standard pkg` under env
// (appended to the process environment) from the repository root, so the
// graph is the one the binary is built with under those build settings.
func Load(t testing.TB, pkg string, env ...string) Graph {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-deps", "-json=ImportPath,Imports,Standard,Dir,Module", pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, stderr.String())
	}
	g := Graph{root: pkg, imports: map[string][]string{}, std: map[string]bool{}}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listed
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode go list output for %s: %v", pkg, err)
		}
		g.imports[p.ImportPath] = p.Imports
		g.std[p.ImportPath] = p.Standard
		if p.Module != nil && p.Module.Path == strings.TrimSuffix(mod, "/") {
			RecordInputs(t, p.Dir)
		}
	}
	RecordInputs(t, root, "go.mod", "go.sum")
	if !g.Has(pkg) {
		t.Fatalf("go list did not list %s", pkg)
	}
	return g
}

// RecordInputs makes `go test`'s result cache see what a subprocess read: it
// lists dir (a new or removed file changes the listing's hash) and stats
// each named file in it, or every .go file when none is named (an edit
// changes its size or mtime). `go list` reads the module's sources in a
// child process, which the test cache does not observe, so without this a
// guard built on it can report a cached pass after the code it guards
// changed.
func RecordInputs(t testing.TB, dir string, files ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("record the inputs in %s: %v", dir, err)
	}
	if len(files) == 0 {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				files = append(files, e.Name())
			}
		}
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("record the input %s: %v", filepath.Join(dir, f), err)
		}
	}
}

// repoRoot walks up from the working directory to the directory holding
// go.mod.
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// Root is the package the graph was loaded for.
func (g Graph) Root() string { return g.root }

// Has reports whether pkg is in the closure.
func (g Graph) Has(pkg string) bool {
	_, ok := g.imports[pkg]
	return ok
}

// Imports is pkg's direct imports.
func (g Graph) Imports(pkg string) []string { return slices.Clone(g.imports[pkg]) }

// Packages is every package in the closure, sorted.
func (g Graph) Packages() []string { return slices.Sorted(maps.Keys(g.imports)) }

// Chain is the shortest import path from the root to pkg (BFS over Imports),
// root first; nil when pkg is not in the closure.
func (g Graph) Chain(pkg string) []string {
	prev := map[string]string{g.root: ""}
	queue := []string{g.root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == pkg {
			var chain []string
			for p := pkg; p != ""; p = prev[p] {
				chain = append([]string{p}, chain...)
			}
			return chain
		}
		for _, next := range g.imports[cur] {
			if _, seen := prev[next]; !seen {
				prev[next] = cur
				queue = append(queue, next)
			}
		}
	}
	return nil
}

// Deny fails once per linked package a rule matches, naming why and the
// shortest chain that links it.
func (g Graph) Deny(t testing.TB, rules []Rule) {
	t.Helper()
	for _, pkg := range g.Packages() {
		for _, r := range rules {
			if r.Matches(pkg) {
				t.Errorf("%s links %s (%s):\n  %s", g.root, pkg, r.Why, strings.Join(g.Chain(pkg), "\n  <- "))
				break
			}
		}
	}
}

// Require fails when pkg is not in the closure: an anchor that keeps a deny
// list from passing vacuously after a rename.
func (g Graph) Require(t testing.TB, pkg, why string) {
	t.Helper()
	if !g.Has(pkg) {
		t.Errorf("%s no longer links %s (%s): a guard anchored on it would pass vacuously", g.root, pkg, why)
	}
}

// AllowApp fails for every module app/ package in the closure that matches
// none of allowed (patterns over the module-relative path).
func (g Graph) AllowApp(t testing.TB, allowed []*regexp.Regexp) {
	t.Helper()
	for _, pkg := range g.Packages() {
		rel, ok := strings.CutPrefix(pkg, mod)
		if !ok || !strings.HasPrefix(rel, "app/") {
			continue
		}
		if !slices.ContainsFunc(allowed, func(re *regexp.Regexp) bool { return re.MatchString(rel) }) {
			t.Errorf("%s links %s, which is not classified for this binary; add it to the allow-list or cut the edge:\n  %s",
				g.root, rel, strings.Join(g.Chain(pkg), "\n  <- "))
		}
	}
}

// OnlyImportedBy fails for every edge q -> p in the closure where target(p)
// and !importers(q).
func (g Graph) OnlyImportedBy(t testing.TB, target, importers func(string) bool, why string) {
	t.Helper()
	for _, q := range g.Packages() {
		for _, p := range g.imports[q] {
			if target(p) && !importers(q) {
				t.Errorf("%s imports %s (%s)", q, p, why)
			}
		}
	}
}
