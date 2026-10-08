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

package cli_test

import (
	"bytes"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/pkg/version"
)

func execute(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestVersionCommandPrintsTheBinaryAndToolchain(t *testing.T) {
	root, _, _ := cli.NewRoot("agent", "short", "long")
	out, err := execute(t, root, "version")
	require.NoError(t, err)
	assert.Equal(t, "agent "+version.String()+"\n"+
		"go "+runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH+"\n", out)
}

func TestRootBindsTheObservabilityFlagsOnce(t *testing.T) {
	root, lo, to := cli.NewRoot("manager", "short", "long")
	require.NotNil(t, lo)
	require.NotNil(t, to)
	for _, name := range []string{"log-level", "log-format", "log-add-source",
		"tracing-enabled", "tracing-endpoint", "tracing-insecure", "tracing-sample-ratio"} {
		assert.NotNil(t, root.PersistentFlags().Lookup(name), name)
		assert.Nil(t, root.LocalNonPersistentFlags().Lookup(name), "%s is bound once, on PersistentFlags", name)
	}
	// A subcommand inherits them rather than binding its own copy.
	sub := &cobra.Command{Use: "sub", RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(sub)
	_, err := execute(t, root, "sub", "--log-level", "debug", "--tracing-enabled")
	require.NoError(t, err)
	assert.Equal(t, "DEBUG", strings.ToUpper(root.PersistentFlags().Lookup("log-level").Value.String()))
	assert.Equal(t, "true", root.PersistentFlags().Lookup("tracing-enabled").Value.String())
}

func TestEnvOr(t *testing.T) {
	t.Setenv("CLUSTARR_TEST_ENV_OR", "")
	assert.Equal(t, "fallback", cli.EnvOr("CLUSTARR_TEST_ENV_OR", "fallback"), "empty is unset")
	t.Setenv("CLUSTARR_TEST_ENV_OR", "set")
	assert.Equal(t, "set", cli.EnvOr("CLUSTARR_TEST_ENV_OR", "fallback"))
}

func TestEnvBoolOr(t *testing.T) {
	for _, tc := range []struct {
		value    string
		fallback bool
		want     bool
	}{
		{"", true, true}, {"", false, false},
		{"false", true, false}, {"0", true, false},
		{"true", false, true}, {"1", false, true},
		{"nonsense", true, true}, {"nonsense", false, false},
	} {
		t.Setenv("CLUSTARR_TEST_ENV_BOOL", tc.value)
		assert.Equal(t, tc.want, cli.EnvBoolOr("CLUSTARR_TEST_ENV_BOOL", tc.fallback), "%q fallback %v", tc.value, tc.fallback)
	}
}

// cmd/ui imports this package; it must not drag a manager, pkg/k8s or an
// app package in (spec §3.2).
func TestCLIStaysLight(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err)
	deps := strings.Fields(string(out))
	require.Contains(t, deps, "github.com/mediactl/clustarr/internal/cli", "go list ran in the package")
	for _, dep := range deps {
		// controller-runtime's pkg/manager/signals is stdlib-only and allowed;
		// its root package and pkg/manager link the whole manager.
		assert.False(t, dep == "sigs.k8s.io/controller-runtime" ||
			dep == "sigs.k8s.io/controller-runtime/pkg/manager" ||
			strings.HasPrefix(dep, "sigs.k8s.io/controller-runtime/pkg/client"),
			"internal/cli links %s", dep)
		assert.NotEqual(t, "github.com/mediactl/clustarr/pkg/k8s", dep)
		assert.False(t, strings.HasPrefix(dep, "github.com/mediactl/clustarr/app/"), dep)
	}
}
