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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// chartRBACSentinels are the comments that bracket one generated role's
// rules in the chart template. They are comments, so Helm passes them
// through untouched and this test can read each block without rendering the
// chart -- which matters, because the blocks contain no Go template at all
// and a test that shelled out to helm would skip on any machine without it.
func chartRBACSentinels(file string) (begin, end string) {
	return "# BEGIN generated from config/rbac/" + file + " -- do not edit by hand.",
		"# END generated from config/rbac/" + file + "."
}

// TestChartRBACMatchesTheGeneratedRoles is the chart-drift gate, for every
// role `make manifests` generates (the Makefile's RBAC_ROLES).
//
// Clustarr ships two installers for one topology: config/ (kustomize) and
// charts/clustarr (Helm). Each config/rbac/<role>_role.yaml is generated
// from its identity's +kubebuilder:rbac markers; the chart's first copy was
// written by hand in M0 and had drifted into something unrelated -- a
// blanket `resources: ["*"]` READ on every Clustarr group and nothing else,
// so a Helm-installed Clustarr could not have written a single status.
//
// Helm cannot read a file outside its own chart directory, so the chart
// cannot template the generated rules at render time: a copy inside the
// chart is unavoidable. What makes it safe is this test, not the copy's
// location. It compares each block verbatim, so a stale copy fails with a
// diff rather than with a permission gap nobody sees until an operator's pod
// is denied -- and since the X14 split there are eight copies, not one.
func TestChartRBACMatchesTheGeneratedRoles(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	chart, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "templates", "rbac.yaml"))
	require.NoError(t, err)

	for _, r := range rbacRoles(t, root) {
		t.Run(r.name, func(t *testing.T) {
			generated, err := os.ReadFile(filepath.Join(root, "config", "rbac", r.file()))
			require.NoError(t, err)
			_, want, found := strings.Cut(string(generated), "\nrules:\n")
			require.True(t, found, "config/rbac/%s has no rules: block", r.file())

			begin, end := chartRBACSentinels(r.file())
			require.Equal(t, 1, strings.Count(string(chart), begin),
				"charts/clustarr/templates/rbac.yaml must carry the %q sentinel exactly once; "+
					"every generated role needs its copy in the chart", begin)
			_, after, _ := strings.Cut(string(chart), begin+"\n")
			got, _, found := strings.Cut(after, end)
			require.True(t, found, "charts/clustarr/templates/rbac.yaml has no %q sentinel", end)

			require.Equal(t, strings.TrimRight(want, "\n"), strings.TrimRight(got, "\n"),
				"the Helm chart's %s ClusterRole has drifted from config/rbac/%s.\n"+
					"Run `make manifests`, then copy the rules: list from config/rbac/%s into\n"+
					"charts/clustarr/templates/rbac.yaml between its BEGIN/END sentinels.", r.name, r.file(), r.file())
		})
	}
}
