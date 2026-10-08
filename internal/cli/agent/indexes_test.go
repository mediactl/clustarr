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

package agent_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/installtest"
)

const module = "github.com/mediactl/clustarr"

// moduleDirs is every package of this module root links, as a directory
// relative to the repository, for the platform the binary is built for.
func moduleDirs(t *testing.T, root, pkg string, env ...string) map[string]bool {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if .Module}}{{if .Module.Main}}{{.ImportPath}}{{end}}{{end}}", module+"/"+pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	require.NoError(t, err, "go list -deps %s", pkg)
	dirs := map[string]bool{}
	for _, ip := range strings.Fields(string(out)) {
		if rel, ok := strings.CutPrefix(ip, module+"/"); ok {
			dirs[rel] = true
		}
	}
	require.True(t, dirs[pkg], "go list -deps %s did not list it", pkg)
	return dirs
}

// goFiles is dir's non-test .go files: a test that builds a manager by hand
// registers through test/starttest, which no binary links.
func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	var out []string
	for _, m := range matches {
		if !strings.HasSuffix(m, "_test.go") {
			out = append(out, m)
		}
	}
	return out
}

// TestTheAgentIsTheOnlyIndexRegistrar: declared field indexes are registered
// once, by the agent (§3.5.2 step 9), never by a worker or a domain's
// registration: among the packages only cmd/agent links, IndexField appears
// only in internal/cli/agent. Two registrars of one (kind, name) are an
// "indexer conflict" at start, and a registrar the agent does not know of
// escapes its declared-index assertion. Packages the manager links too
// (app/caption/itemindex) register for the manager's controllers.
func TestTheAgentIsTheOnlyIndexRegistrar(t *testing.T) {
	root := installtest.RepoRoot(t)
	agentDirs := moduleDirs(t, root, "cmd/agent", "CGO_ENABLED=1", "GOOS=linux", "GOARCH=amd64")
	for dir := range moduleDirs(t, root, "cmd/manager", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64") {
		delete(agentDirs, dir)
	}
	require.True(t, agentDirs["internal/cli/agent"], "internal/cli/agent is the agent's own")
	var here int
	for dir := range agentDirs {
		for _, file := range goFiles(t, filepath.Join(root, dir)) {
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
			require.NoError(t, err)
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "IndexField" {
					if dir == "internal/cli/agent" {
						here++
					} else {
						t.Errorf("%s registers a field index itself; declare it in the domain's Registration.Indexes (§3.5.2)", file)
					}
				}
				return true
			})
		}
	}
	require.Positive(t, here, "internal/cli/agent registers the declared indexes")
}
