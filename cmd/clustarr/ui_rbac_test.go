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
	"sigs.k8s.io/yaml"
)

// uiAllowedVerbs is the entire verb set config/rbac/ui_role.yaml may ever
// grant. ui/ holds a client.Reader, never a client.Client (ui/server.go),
// and this is the ONE place that promise is checked against the actual
// RBAC document a real cluster enforces -- envtest does not enforce RBAC
// (cmd/clustarr/rbac_markers_test.go's comments say as much for the
// controllers' own Role), so nothing else in the tree would notice this
// role quietly growing a write verb.
var uiAllowedVerbs = map[string]bool{"get": true, "list": true, "watch": true}

// uiRoleRule mirrors just enough of a ClusterRole's rules entry to check
// verbs and resources; it deliberately does not model apiGroups, which
// neither guard below needs to look at.
type uiRoleRule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

// uiRoleDocument is the shape of config/rbac/ui_role.yaml: a single
// ClusterRole, hand-written rather than generated (design plan ruling R3,
// docs/superpowers/plans/2026-09-22-phase-d3-ui.md, and the note at the top
// of config/rbac/ui_role.yaml itself).
type uiRoleDocument struct {
	Rules []uiRoleRule `json:"rules"`
}

// TestUIRoleGrantsOnlyReadVerbs is Task D3-4's second guard.
//
// A +kubebuilder:rbac marker can only ADD verbs, and merging into
// clustarr-manager-role would hand every controller the UI's grants besides
// -- so ui's ClusterRole is hand-written instead (R3), which means nothing
// generates it and nothing but this test checks it. It asserts the one
// property that document exists to hold: every rule's verbs are a subset of
// {get, list, watch}, and no resource -- not even to read it -- is a
// "/status" subresource, matching CLAUDE.md's "The UI never writes status
// and owns no CRD" down to the read side of that invariant.
func TestUIRoleGrantsOnlyReadVerbs(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(root, "config", "rbac", "ui_role.yaml"))
	require.NoError(t, err)

	var role uiRoleDocument
	require.NoError(t, yaml.Unmarshal(raw, &role))
	require.NotEmpty(t, role.Rules, "config/rbac/ui_role.yaml has no rules; this guard has nothing to check")

	for _, rule := range role.Rules {
		for _, verb := range rule.Verbs {
			require.True(t, uiAllowedVerbs[verb],
				"config/rbac/ui_role.yaml grants verb %q on %v/%v, which is not in {get, list, watch}. "+
					"ui/ holds only a client.Reader (ui/server.go's Options.Reader); a write verb here "+
					"grants a permission the code can never legitimately use and contradicts CLAUDE.md's "+
					`"The UI never writes status and owns no CRD"`,
				verb, rule.APIGroups, rule.Resources)
		}
		for _, resource := range rule.Resources {
			require.False(t, strings.HasSuffix(resource, "/status"),
				"config/rbac/ui_role.yaml grants access to %q in %v, a /status subresource. ui/ must "+
					"never read or write status through the RBAC-granted path, even get/list/watch -- "+
					"see config/rbac/ui_role.yaml's header comment",
				resource, rule.APIGroups)
		}
	}
}

// The anchors that bracket ui's ClusterRole rules inside the Helm chart.
// They are not comments added for this test -- they are the exact,
// already-unique text of the chart template around that block (verified:
// charts/clustarr/templates/rbac.yaml has exactly one "-ui" ClusterRole and
// it is immediately followed by its ClusterRoleBinding) -- so this test
// reads the chart as it stands rather than asking the chart to carry new
// sentinels on its behalf.
const (
	chartUIRoleBegin = "kind: ClusterRole\nmetadata:\n  name: {{ include \"clustarr.fullname\" . }}-ui\n" +
		"  labels:\n    {{- include \"clustarr.labels\" . | nindent 4 }}\nrules:\n"
	chartUIRoleEnd = "\n---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding"
)

// TestUIRoleChartMatchesConfig holds charts/clustarr's copy of ui's
// ClusterRole to config/rbac/ui_role.yaml, the way
// TestChartRBACMatchesTheGeneratedRole holds the chart's manager ClusterRole
// to the generated config/rbac/role.yaml (chart_rbac_test.go). Helm cannot
// read a file outside its own chart directory, so a second copy of the
// rules is unavoidable (the chart's ui ClusterRole is under
// charts/clustarr/templates/rbac.yaml, guarded by `{{- if .Values.ui.enabled
// }}`); this test is what stops that copy drifting from the source of
// truth in config/rbac/ui_role.yaml the way TestUIRoleGrantsOnlyReadVerbs
// checks it in the first place.
func TestUIRoleChartMatchesConfig(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	generated, err := os.ReadFile(filepath.Join(root, "config", "rbac", "ui_role.yaml"))
	require.NoError(t, err)
	_, want, found := strings.Cut(string(generated), "\nrules:\n")
	require.True(t, found, "config/rbac/ui_role.yaml has no rules: block")

	chart, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "templates", "rbac.yaml"))
	require.NoError(t, err)

	_, after, found := strings.Cut(string(chart), chartUIRoleBegin)
	require.True(t, found,
		"charts/clustarr/templates/rbac.yaml no longer has ui's ClusterRole in the expected shape "+
			"(looked for a ClusterRole named {{ include \"clustarr.fullname\" . }}-ui immediately "+
			"followed by \"rules:\"); either it was removed or reshaped without updating this test")
	got, _, found := strings.Cut(after, chartUIRoleEnd)
	require.True(t, found,
		"charts/clustarr/templates/rbac.yaml's ui ClusterRole is not immediately followed by its "+
			"ClusterRoleBinding in the expected shape; this test can no longer find where the rules "+
			"block ends")

	require.Equal(t, strings.TrimRight(want, "\n"), strings.TrimRight(got, "\n"),
		"charts/clustarr/templates/rbac.yaml's ui ClusterRole has drifted from config/rbac/ui_role.yaml.\n"+
			"Copy the rules: list from config/rbac/ui_role.yaml into the ui ClusterRole block in "+
			"charts/clustarr/templates/rbac.yaml (both are hand-written -- see the header comment in "+
			"config/rbac/ui_role.yaml for why).")
}
