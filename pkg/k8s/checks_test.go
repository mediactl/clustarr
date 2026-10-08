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

package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

func TestChecksKeepTheirOrder(t *testing.T) {
	var c Checks // the zero value is usable: cmd/manager declares `var ready, live k8s.Checks`
	for _, name := range []string{"jetstream", "cache", "index.releaseindex"} {
		require.NoError(t, c.Add(name, healthz.Ping))
	}
	require.Equal(t, []string{"jetstream", "cache", "index.releaseindex"}, c.Names())
	require.NotNil(t, c.Get("cache"))
	require.Nil(t, c.Get("ffgo"))
}

// controller-runtime keeps healthz and readyz checks in a map and overwrites
// a duplicate name without a word; Checks refuses it (spec §3.3).
func TestChecksRejectWhatControllerRuntimeWouldSilentlyOverwrite(t *testing.T) {
	c := NewChecks()
	require.NoError(t, c.Add("cache", healthz.Ping))
	for _, tc := range []struct {
		name  string
		check healthz.Checker
		want  string
	}{
		{"cache", healthz.Ping, "twice"},
		{"", healthz.Ping, "not a check name"},
		{"Cache", healthz.Ping, "not a check name"},
		{"index.release.index", healthz.Ping, "not a check name"},
		{"import.", healthz.Ping, "not a check name"},
		{"9lives", healthz.Ping, "not a check name"},
		{"data", nil, "no checker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, c.Add(tc.name, tc.check), tc.want)
		})
	}
	require.Equal(t, []string{"cache"}, c.Names(), "a rejected check must not be half-added")
}

func TestChecksMergeKeepsOrderAndRejectsACollision(t *testing.T) {
	var process, domain Checks
	require.NoError(t, process.Add("cache", healthz.Ping))
	require.NoError(t, domain.Add("import.data", healthz.Ping))
	require.NoError(t, process.Merge(&domain))
	require.NoError(t, process.Merge(nil), "a domain with no checks merges as nothing")
	require.Equal(t, []string{"cache", "import.data"}, process.Names())
	require.ErrorContains(t, process.Merge(&domain), "import.data")
}

func TestANilChecksIsEmpty(t *testing.T) {
	var c *Checks
	require.Empty(t, c.Names())
	require.Nil(t, c.Get("cache"))
}
