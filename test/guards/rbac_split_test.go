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
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/mediactl/clustarr/test/installtest"
)

// TestEachServiceAccountHoldsExactlyItsOwnRole is the X14 split's guard,
// read from what each installer actually renders: every Clustarr
// ServiceAccount is bound to exactly one ClusterRole, and that role's grants
// are exactly its own identity's generated role (config/rbac/<role>_role.yaml,
// or ui's hand-written ui_role.yaml) -- never another service's, and never
// the union every service shared until X14, under which import's
// rootfolders update, grab's StatefulSet writes and index's Secret
// creates belonged to every process.
//
// A component runs under the role of the same name (manager, agent-<domain>,
// ui), or under the name before its last "-<suffix>" for a second
// Deployment of one identity, as the per-service topology had. The engine
// pods (grab-engine) have a role of their own;
// the transcode Jobs the pool renderer creates carry no ServiceAccount at
// all (X14: transcode is the only writer of TranscodeJob.status, over NATS,
// so the pool pods need no Kubernetes credentials). The chart is rendered
// under two release names, since its names carry the fullname.
func TestEachServiceAccountHoldsExactlyItsOwnRole(t *testing.T) {
	helm := installtest.FindTool(t, "helm")
	kustomize := installtest.FindTool(t, "kustomize")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	want := map[string][]string{"ui": installtest.SortedGrants(installtest.GrantsOf(installtest.ReadRole(t, root, "ui_role.yaml").Rules))}
	for _, r := range installtest.RBACRoles(t, root) {
		want[r.Name] = installtest.SortedGrants(installtest.GrantsOf(installtest.ReadRole(t, root, r.File()).Rules))
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
		"helm template clustarr":   {installtest.Run(t, root, helm, "template", "clustarr", "charts/clustarr"), "clustarr-"},
		"helm template media":      {installtest.Run(t, root, helm, "template", "media", "charts/clustarr"), "media-clustarr-"},
		"kustomize config/default": {installtest.Run(t, root, kustomize, "build", "config/default"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := installtest.DecodeRendered(t, tc.out)

			// Every component some pod runs as: the Deployments' own
			// ServiceAccounts, plus the one identity no Deployment of
			// the installer's runs -- the engine pods, which the
			// DownloadClient controller creates at runtime.
			accounts := map[string]string{
				tc.prefix + "grab-engine": "grab-engine",
			}
			for _, d := range r.Deployments {
				component := d.Spec.Template.Labels["app.kubernetes.io/component"]
				if identityOf(component) == "" {
					continue // a fixture or subchart workload, not a Clustarr identity
				}
				accounts[d.Spec.Template.Spec.ServiceAccountName] = component
			}
			// One account per identity: every generated role plus ui's
			// (the manager, six agent domains, the engines and ui since
			// the installers wave; eleven service accounts before it).
			require.GreaterOrEqual(t, len(accounts), len(want),
				"found only %d Clustarr identities for %d roles; this guard is not reading what it thinks it is",
				len(accounts), len(want))

			for account, component := range accounts {
				require.True(t, r.ServiceAccounts[account], "%s runs as ServiceAccount %q, which %s does not create",
					component, account, name)
				var boundTo []string
				for _, b := range r.Bindings {
					for _, s := range b.Subjects {
						if s.Kind == rbacv1.ServiceAccountKind && s.Name == account {
							boundTo = append(boundTo, b.RoleRef.Name)
						}
					}
				}
				require.Len(t, boundTo, 1, "ServiceAccount %q (%s) must be bound to exactly its own role, and is "+
					"bound to %v", account, component, boundTo)
				role, ok := r.Roles[boundTo[0]]
				require.True(t, ok, "ServiceAccount %q is bound to ClusterRole %q, which %s does not render",
					account, boundTo[0], name)
				identity := identityOf(component)
				require.Equal(t, want[identity], installtest.SortedGrants(installtest.GrantsOf(role.Rules)),
					"ServiceAccount %q (%s) holds ClusterRole %q, whose grants are not %s's own generated role",
					account, component, boundTo[0], identity)
			}
		})
	}
}

// TestNoInstallerShipsATranscodeWorkerIdentity is X14's guard: the
// per-task transcode Job used to run as its own transcode-worker
// ServiceAccount, bound to a ClusterRole generated from
// app/transcode/worker's RBAC markers, so it could patch TranscodeJob.status
// directly. The pool Jobs cmd/transcode-worker runs report over NATS
// instead (spec §18.1, §18.2) and hold no Kubernetes credentials at all, so
// neither installer should render a transcode-worker ServiceAccount,
// ClusterRole or ClusterRoleBinding any more -- read from what each
// installer actually renders, using the same run/decodeRendered helpers
// TestEachServiceAccountHoldsExactlyItsOwnRole uses.
func TestNoInstallerShipsATranscodeWorkerIdentity(t *testing.T) {
	helm := installtest.FindTool(t, "helm")
	kustomize := installtest.FindTool(t, "kustomize")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	cases := map[string][]byte{
		"helm template clustarr":   installtest.Run(t, root, helm, "template", "clustarr", "charts/clustarr"),
		"helm template media":      installtest.Run(t, root, helm, "template", "media", "charts/clustarr"),
		"kustomize config/default": installtest.Run(t, root, kustomize, "build", "config/default"),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			r := installtest.DecodeRendered(t, out)
			for sa := range r.ServiceAccounts {
				require.NotContains(t, sa, "transcode-worker",
					"%s still renders ServiceAccount %q: pool pods run with no ServiceAccount token", name, sa)
			}
			for roleName := range r.Roles {
				require.NotContains(t, roleName, "transcode-worker",
					"%s still renders ClusterRole %q: pool pods run with no ServiceAccount token", name, roleName)
			}
			for _, b := range r.Bindings {
				require.NotContains(t, b.Name, "transcode-worker",
					"%s still renders ClusterRoleBinding %q: pool pods run with no ServiceAccount token", name, b.Name)
			}
		})
	}
}
