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

	"github.com/mediactl/clustarr/ui/actions"
)

// uiReadVerbs may be granted on any resource ui_role.yaml names, except a
// status subresource, which it may not name at all.
var uiReadVerbs = map[string]bool{"get": true, "list": true, "watch": true}

// uiNeverVerbs are refused on every resource, whatever else this file
// allows, so that widening uiActionGrants by mistake cannot let one through:
// the UI never updates (its spec edits are merge patches, see ui/actions'
// package doc), never deletes, and never holds a wildcard or an
// RBAC-escalation verb.
var uiNeverVerbs = map[string]bool{
	"update": true, "delete": true, "deletecollection": true,
	"*": true, "escalate": true, "bind": true, "impersonate": true,
}

// uiGrant is one (API group, resource, verb) permission.
type uiGrant struct {
	Group, Resource, Verb string
}

// uiActionGrants is the complete set of write permissions ui_role.yaml may
// grant -- and must grant, since each is one the UI's three actions
// (amendment §A3.2, ruling R2) make: create a Search, create a LibraryScan,
// patch spec.monitored on a catalog kind that has one. It is written out
// here rather than derived from ui/actions so a widening shows up in review
// as an edit to this list; the test then also holds ui/actions.Grants() to
// it, so the code's declared needs, this list and the role are one set.
var uiActionGrants = map[uiGrant]bool{
	{"catalog.clustarr.io", "searches", "create"}:     true,
	{"catalog.clustarr.io", "libraryscans", "create"}: true,
	{"catalog.clustarr.io", "albums", "patch"}:        true,
	{"catalog.clustarr.io", "artists", "patch"}:       true,
	{"catalog.clustarr.io", "audiobooks", "patch"}:    true,
	{"catalog.clustarr.io", "authors", "patch"}:       true,
	{"catalog.clustarr.io", "books", "patch"}:         true,
	{"catalog.clustarr.io", "comics", "patch"}:        true,
	{"catalog.clustarr.io", "episodes", "patch"}:      true,
	{"catalog.clustarr.io", "issues", "patch"}:        true,
	{"catalog.clustarr.io", "movies", "patch"}:        true,
	{"catalog.clustarr.io", "series", "patch"}:        true,
}

// uiRoleRule mirrors a ClusterRole rules entry, including the fields no rule
// here should ever use, so the test can refuse them rather than silently
// not see them.
type uiRoleRule struct {
	APIGroups       []string `json:"apiGroups"`
	Resources       []string `json:"resources"`
	ResourceNames   []string `json:"resourceNames"`
	NonResourceURLs []string `json:"nonResourceURLs"`
	Verbs           []string `json:"verbs"`
}

// uiRoleDocument is the shape of config/rbac/ui_role.yaml: a single
// ClusterRole, hand-written rather than generated (design plan ruling R3,
// docs/superpowers/plans/2026-09-22-phase-d3-ui.md, and the note at the top
// of config/rbac/ui_role.yaml itself).
type uiRoleDocument struct {
	Rules []uiRoleRule `json:"rules"`
}

// TestUIRoleGrantsOnlyReadsAndActionWrites is D3-4's role guard
// (TestUIRoleGrantsOnlyReadVerbs until Phase G), narrowed by ruling R2
// (docs/superpowers/plans/2026-09-23-phase-g-parity.md) rather than lifted.
//
// A +kubebuilder:rbac marker can only ADD verbs, and merging into
// clustarr-manager-role would hand every controller the UI's grants besides
// -- so ui's ClusterRole is hand-written, nothing generates it, and nothing
// but this test checks it. envtest does not enforce RBAC, so a grant missing
// here fails only in a real cluster and a grant too many fails nowhere.
//
// It expands every rule into (group, resource, verb) triples and asserts:
//   - no resource ends in "/status", for any verb -- the UI never writes
//     status and does not read it through RBAC either;
//   - no update, delete, deletecollection, wildcard or escalation verb, on
//     anything;
//   - no wildcard group or resource (a "*" resource matches every
//     subresource, status included), no resourceNames, no nonResourceURLs;
//   - every other verb is a read, or a write triple in uiActionGrants;
//   - every triple in uiActionGrants is granted -- the actions need all of
//     them;
//   - uiActionGrants is exactly ui/actions.Grants().
func TestUIRoleGrantsOnlyReadsAndActionWrites(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(root, "config", "rbac", "ui_role.yaml"))
	require.NoError(t, err)

	var role uiRoleDocument
	require.NoError(t, yaml.Unmarshal(raw, &role))
	require.NotEmpty(t, role.Rules, "config/rbac/ui_role.yaml has no rules; this guard has nothing to check")

	granted := map[uiGrant]bool{}
	for i, rule := range role.Rules {
		require.Empty(t, rule.NonResourceURLs,
			"config/rbac/ui_role.yaml rule %d grants nonResourceURLs %v; ui needs none", i, rule.NonResourceURLs)
		require.Empty(t, rule.ResourceNames,
			"config/rbac/ui_role.yaml rule %d is restricted by resourceNames %v; ui's grants are per "+
				"resource, and a create cannot be name-restricted at all", i, rule.ResourceNames)
		for _, group := range rule.APIGroups {
			require.NotEqual(t, "*", group,
				"config/rbac/ui_role.yaml rule %d grants on every API group (\"*\")", i)
		}
		for _, resource := range rule.Resources {
			require.NotEqual(t, "*", resource,
				"config/rbac/ui_role.yaml rule %d grants on every resource (\"*\") in %v -- which matches "+
					"every subresource, status included", i, rule.APIGroups)
			require.False(t, strings.HasSuffix(resource, "/status"),
				"config/rbac/ui_role.yaml grants access to %q in %v, a /status subresource. The UI never "+
					"writes status (amendment §A3.2, ruling R2) and does not read it through RBAC either, "+
					"even get/list/watch -- see config/rbac/ui_role.yaml's header comment",
				resource, rule.APIGroups)
		}

		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					g := uiGrant{Group: group, Resource: resource, Verb: verb}
					require.False(t, uiNeverVerbs[verb],
						"config/rbac/ui_role.yaml grants %q on %s/%s. The UI never updates, deletes or "+
							"escalates: its only writes are create on searches and libraryscans and a "+
							"merge patch of spec.monitored (ui/actions)", verb, group, resource)
					if uiReadVerbs[verb] {
						continue
					}
					require.True(t, uiActionGrants[g],
						"config/rbac/ui_role.yaml grants write verb %q on %s/%s, which no UI action "+
							"makes. The UI's writes are exactly uiActionGrants (ruling R2): create on "+
							"searches and libraryscans, patch on the catalog kinds with a spec.monitored",
						verb, group, resource)
					granted[g] = true
				}
			}
		}
	}

	for g := range uiActionGrants {
		require.True(t, granted[g],
			"config/rbac/ui_role.yaml does not grant %q on %s/%s, which ui/actions needs; the action "+
				"would pass every envtest (no RBAC there) and be Forbidden in a real cluster",
			g.Verb, g.Group, g.Resource)
	}

	declared := map[uiGrant]bool{}
	for _, g := range actions.Grants() {
		declared[uiGrant{Group: g.Group, Resource: g.Resource, Verb: g.Verb}] = true
	}
	require.Equal(t, uiActionGrants, declared,
		"ui/actions.Grants() and this test's uiActionGrants disagree: the actions' declared needs, this "+
			"reviewed list and config/rbac/ui_role.yaml must be one set")
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
// truth in config/rbac/ui_role.yaml the way
// TestUIRoleGrantsOnlyReadsAndActionWrites checks it in the first place.
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
