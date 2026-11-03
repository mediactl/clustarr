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
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/installtest"
)

// TestEveryBinaryImportsAutomemlimit (owner, 2026-11-03): no installer
// sets GOMEMLIMIT (TestTheGoMemoryLimitIsItsShareOfTheLimit), so a binary
// without automemlimit's blank import, which sets the Go memory limit from
// the cgroup's memory limit before main, runs with no Go memory limit at
// all.
func TestEveryBinaryImportsAutomemlimit(t *testing.T) {
	mains, err := filepath.Glob(filepath.Join(installtest.RepoRoot(t), "cmd", "*", "main.go"))
	require.NoError(t, err)
	require.Len(t, mains, 5, "manager, agent, ui, markers, transcode")
	for _, path := range mains {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		require.NoError(t, err)
		blank := false
		for _, imp := range f.Imports {
			if imp.Path.Value == `"github.com/KimMachineGun/automemlimit"` && imp.Name != nil && imp.Name.Name == "_" {
				blank = true
			}
		}
		assert.True(t, blank, `%s: import _ "github.com/KimMachineGun/automemlimit"`, path)
	}
}

// TestNoModuleSetsGOMAXPROCS: since Go 1.25 the runtime sizes GOMAXPROCS
// from the cgroup's CPU limit, rounded up and at least 2, and follows a
// change. go.uber.org/automaxprocs, even through a dependency's blank
// import, would round it down to 1 on the 500m and 1-CPU pods and stop the
// runtime's re-check (owner, 2026-11-03: not used).
func TestNoModuleSetsGOMAXPROCS(t *testing.T) {
	gomod, err := os.ReadFile(filepath.Join(installtest.RepoRoot(t), "go.mod"))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(gomod), "go.uber.org/automaxprocs"), "go.mod requires go.uber.org/automaxprocs")
}
