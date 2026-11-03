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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/mediactl/clustarr/pkg/gomemlimit"
	"github.com/mediactl/clustarr/test/installtest"
)

func managerContainer(t *testing.T, out []byte) corev1.Container {
	t.Helper()
	m := installtest.Workloads(installtest.DecodeRendered(t, out))["manager"]
	require.Len(t, m.Spec.Template.Spec.Containers, 1)
	return m.Spec.Template.Spec.Containers[0]
}

func envValue(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// TestManagerMemoryKnobs is the control-plane placement design's §5.4.1
// items 1, 2 and 4: pprof to profile the manager, a tighter GC (GOGC and a
// GOMEMLIMIT of its own, in place of the share of its memory limit), and the
// cache scoped to the release's namespace.
func TestManagerMemoryKnobs(t *testing.T) {
	out := installtest.RenderChart(t, "--namespace", "clustarr-system",
		"--set", "manager.pprofBindAddress=127.0.0.1:6060", "--set", "manager.gogc=50",
		"--set", "manager.goMemLimit=1GiB", "--set", "manager.watchReleaseNamespace=true")
	c := managerContainer(t, out)

	assert.Contains(t, c.Args, "--pprof-bind-address=127.0.0.1:6060")
	assert.Contains(t, c.Args, "--watch-namespace=clustarr-system")
	gogc, ok := envValue(c, "GOGC")
	require.True(t, ok)
	assert.Equal(t, "50", gogc)
	limit, ok := envValue(c, "GOMEMLIMIT")
	require.True(t, ok)
	assert.Equal(t, "1GiB", limit, "Go reads a GOMEMLIMIT with a unit suffix")
	_, ok = envValue(c, gomemlimit.Env)
	assert.False(t, ok, "an explicit GOMEMLIMIT takes the share's place")
}

func TestManagerMemoryKnobsOffByDefault(t *testing.T) {
	c := managerContainer(t, installtest.RenderChart(t))
	for _, a := range c.Args {
		assert.False(t, strings.HasPrefix(a, "--pprof-bind-address") || strings.HasPrefix(a, "--watch-namespace"), a)
	}
	_, ok := envValue(c, "GOGC")
	assert.False(t, ok)
	_, ok = envValue(c, "GOMEMLIMIT")
	assert.False(t, ok, "the binary reads its memory limit from its cgroup")
	share, ok := envValue(c, gomemlimit.Env)
	require.True(t, ok)
	assert.Equal(t, "0.80", share, "80% of the memory limit")
}
