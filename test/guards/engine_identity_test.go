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
	"cmp"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/test/installtest"
)

// engineRenderer is the Deployment that renders the download engines --
// the one carrying the native image the engines run (the manager's
// CLUSTARR_NATIVE_IMAGE, spec 2026-10-06 §3.11) -- and its literal env.
func engineRenderer(t *testing.T, r installtest.Rendered) (appsv1.Deployment, map[string]string) {
	t.Helper()
	for _, d := range r.Deployments {
		env := map[string]string{}
		for _, e := range d.Spec.Template.Spec.Containers[0].Env {
			if e.ValueFrom == nil {
				env[e.Name] = e.Value
			}
		}
		if _, ok := env[cli.EnvNativeImage]; ok {
			return d, env
		}
	}
	t.Fatalf("no Deployment carries %s, so none renders the engines", cli.EnvNativeImage)
	return appsv1.Deployment{}, nil
}

// engineRenders is every installer render the engine guards read. The chart
// is rendered under two release names because its names carry the
// fullname: a hard-coded default passes under "clustarr" and fails under
// anything else.
func engineRenders(t *testing.T) map[string]installtest.Rendered {
	t.Helper()
	root := installtest.RepoRoot(t)
	helm := installtest.FindTool(t, "helm")
	return map[string]installtest.Rendered{
		"helm template clustarr":   installtest.DecodeRendered(t, installtest.Run(t, root, helm, "template", "clustarr", "charts/clustarr")),
		"helm template media":      installtest.DecodeRendered(t, installtest.Run(t, root, helm, "template", "media", "charts/clustarr")),
		"kustomize config/default": installtest.DecodeRendered(t, installtest.BuildKustomize(t, "config/default")),
	}
}

// TestGrabEnginesMountAClaimTheInstallerCreates (plan task G1-5, found by
// E-4): the DownloadClient controller stamps a /data PersistentVolumeClaim
// onto every engine StatefulSet and Deployment it creates. That name was the
// constant "clustarr-data" -- config/'s claim -- while the chart names its
// claim "<release fullname>-data", so under any release name other than
// "clustarr" every engine pod mounted a claim that does not exist and sat in
// ContainerCreating forever. Nothing could see it: envtest schedules no pods.
//
// For each installer this takes the Deployment that renders the engines (the
// manager) and reads the claim it would stamp from its literal env, falling
// back to the flag's default as the binary does. That claim must be one the
// installer creates, and the very claim the manager itself mounts at /data
// -- engines and the controller that sizes them share one volume. The other
// half of the seam, that the variable reaches the manager's options, is
// cmd/manager's own test (W5.7).
func TestGrabEnginesMountAClaimTheInstallerCreates(t *testing.T) {
	for name, r := range engineRenders(t) {
		t.Run(name, func(t *testing.T) {
			dep, env := engineRenderer(t, r)
			claim := cmp.Or(env[cli.EnvDataClaim], downloadclient.DefaultDataClaimName)
			require.True(t, r.Claims[claim],
				"the engine workloads would mount PVC %q, which %s does not create", claim, name)

			var mounted string
			for _, v := range dep.Spec.Template.Spec.Volumes {
				if v.Name == "data" && v.PersistentVolumeClaim != nil {
					mounted = v.PersistentVolumeClaim.ClaimName
				}
			}
			require.NotEmpty(t, mounted, "the %s Deployment mounts no data claim", dep.Name)
			require.Equal(t, mounted, claim,
				"the engines would mount a different /data than the controller that sizes it")
		})
	}
}

// TestGrabEnginesRunAsAnAccountTheInstallerBinds is the engine
// ServiceAccount's half of the test above (X14, found by X9): the engine
// pods the DownloadClient controller creates named no serviceAccountName, so
// they ran as the namespace's "default" account, which nothing binds, and
// were denied their own DownloadClient on a real cluster. envtest schedules
// no pods and enforces no binding, so nothing could see it.
//
// For each installer this reads the account the engine renderer stamps onto
// every engine pod from its literal env (or the flag's default). That
// account must be one the installer creates, bound to exactly one
// ClusterRole whose grants are exactly the engines' generated role.
func TestGrabEnginesRunAsAnAccountTheInstallerBinds(t *testing.T) {
	want := installtest.SortedGrants(installtest.GrantsOf(
		installtest.ReadRole(t, installtest.RepoRoot(t), "grab_engine_role.yaml").Rules))
	for name, r := range engineRenders(t) {
		t.Run(name, func(t *testing.T) {
			_, env := engineRenderer(t, r)
			account := cmp.Or(env[cli.EnvEngineServiceAccount], downloadclient.DefaultEngineServiceAccount)
			require.True(t, r.ServiceAccounts[account],
				"engine pods would run as ServiceAccount %q, which %s does not create", account, name)
			var boundTo []string
			for _, b := range r.Bindings {
				for _, s := range b.Subjects {
					if s.Kind == "ServiceAccount" && s.Name == account {
						boundTo = append(boundTo, b.RoleRef.Name)
					}
				}
			}
			require.Len(t, boundTo, 1,
				"engine ServiceAccount %q should be bound to exactly the engine ClusterRole, and is bound to %v",
				account, boundTo)
			role, ok := r.Roles[boundTo[0]]
			require.True(t, ok, "engine ServiceAccount %q is bound to ClusterRole %q, which %s does not render",
				account, boundTo[0], name)
			require.Equal(t, want, installtest.SortedGrants(installtest.GrantsOf(role.Rules)),
				"the ClusterRole engine pods run under is not the generated engine role")
		})
	}
}
