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
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runMainEnv makes this test binary run cmd/agent's main (TestMain).
const runMainEnv = "AGENT_TEST_RUN_MAIN"

// TestThePar2ChildIsDispatchedBeforeCobra: `agent par2-repair` reaches
// par2child.ChildMain with no flag parsing, logging or tracing (split spec
// §8.9 guard 7). Cobra would refuse the positional argument ("unknown
// command"), so a child that got that far never repairs anything and the
// usenet engine blocklists every damaged download. PAR2GO_LIB names nothing
// and the dynamic loader's path is empty, so the child's self-check can only
// fail, naming PAR2GO_LIB; a system copy of the library found by its bare
// soname instead answers "par2go <library>" -- ChildMain either way.
func TestThePar2ChildIsDispatchedBeforeCobra(t *testing.T) {
	cmd := exec.Command(os.Args[0], "par2-repair", "--self-check")
	cmd.Env = append(os.Environ(), runMainEnv+"=1", "PAR2GO_LIB=/nonexistent/libpar2shim.so", "LD_LIBRARY_PATH="+t.TempDir())
	out, err := cmd.CombinedOutput()
	assert.NotContains(t, string(out), "unknown command", "par2-repair reached cobra")
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		assert.Equal(t, 1, ee.ExitCode(), "%s", out)
		assert.Contains(t, string(out), "PAR2GO_LIB")
	case err == nil:
		assert.True(t, strings.HasPrefix(string(out), "par2go "), "%s", out)
	default:
		require.NoError(t, err)
	}
}
