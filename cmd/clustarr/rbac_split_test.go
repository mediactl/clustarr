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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
)

// TestEachServiceAccountHoldsExactlyItsOwnRole is the X14 split's guard,
// read from what each installer actually renders: every Clustarr
// ServiceAccount is bound to exactly one ClusterRole, and that role's grants
// are exactly its own identity's generated role (config/rbac/<role>_role.yaml,
// or ui's hand-written ui_role.yaml) -- never another service's, and never
// the union every service shared until X14, under which importarr's
// rootfolders update, grabarr's StatefulSet writes and indexarr's Secret
// creates belonged to every process.
//
// A component runs under the role of the same name, or -- for a service's
// second Deployment (catalogarr-metadata, importarr-worker,
// captionarr-worker) -- under its service's: the name before its last
// "-<suffix>". The engine pods (grabarr-engine) and the transcode Jobs
// (squasharr-worker) have roles of their own. The chart is rendered under
// two release names, since its names carry the fullname.
func TestEachServiceAccountHoldsExactlyItsOwnRole(t *testing.T) {
	helm := findTool(t, "helm")
	kustomize := findTool(t, "kustomize")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	want := map[string][]string{"ui": sortedGrants(grantsOf(readRole(t, root, "ui_role.yaml").Rules))}
	for _, r := range rbacRoles(t, root) {
		want[r.name] = sortedGrants(grantsOf(readRole(t, root, r.file()).Rules))
	}
	identityOf := func(component string) string {
		if _, ok := want[component]; ok {
			return component
		}
		if i := strings.LastIndex(component, "-"); i > 0 {
			if _, ok := want[component[:i]]; ok {
				return component[:i]
			}
		}
		return ""
	}

	for name, tc := range map[string]struct {
		out    []byte
		prefix string
	}{
		"helm template clustarr":   {run(t, root, helm, "template", "clustarr", "charts/clustarr"), "clustarr-"},
		"helm template media":      {run(t, root, helm, "template", "media", "charts/clustarr"), "media-clustarr-"},
		"kustomize config/default": {run(t, root, kustomize, "build", "config/default"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := decodeRendered(t, tc.out)

			// Every component some pod runs as: the Deployments' own
			// ServiceAccounts, plus the two identities no Deployment of
			// the installer's runs -- the engine pods and the transcode
			// Jobs, which the controllers create at runtime.
			accounts := map[string]string{
				tc.prefix + "grabarr-engine":   "grabarr-engine",
				tc.prefix + "squasharr-worker": "squasharr-worker",
			}
			for _, d := range r.deployments {
				component := d.Spec.Template.Labels["app.kubernetes.io/component"]
				if identityOf(component) == "" {
					continue // a fixture or subchart workload, not a Clustarr identity
				}
				accounts[d.Spec.Template.Spec.ServiceAccountName] = component
			}
			require.GreaterOrEqual(t, len(accounts), 12,
				"found only %d Clustarr identities; this guard is not reading what it thinks it is", len(accounts))

			for account, component := range accounts {
				require.True(t, r.serviceAccounts[account], "%s runs as ServiceAccount %q, which %s does not create",
					component, account, name)
				var boundTo []string
				for _, b := range r.bindings {
					for _, s := range b.Subjects {
						if s.Kind == rbacv1.ServiceAccountKind && s.Name == account {
							boundTo = append(boundTo, b.RoleRef.Name)
						}
					}
				}
				require.Len(t, boundTo, 1, "ServiceAccount %q (%s) must be bound to exactly its own role, and is "+
					"bound to %v", account, component, boundTo)
				role, ok := r.roles[boundTo[0]]
				require.True(t, ok, "ServiceAccount %q is bound to ClusterRole %q, which %s does not render",
					account, boundTo[0], name)
				identity := identityOf(component)
				require.Equal(t, want[identity], sortedGrants(grantsOf(role.Rules)),
					"ServiceAccount %q (%s) holds ClusterRole %q, whose grants are not %s's own generated role",
					account, component, boundTo[0], identity)
			}
		})
	}
}
