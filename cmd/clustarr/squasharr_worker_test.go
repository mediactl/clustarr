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
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

// TestTranscodeJobServiceAccountHoldsTheWorkerRole answers "how do the
// Job's ServiceAccount and the worker's RBAC stay in sync" for both
// installers, from what they actually render.
//
// For each rendering it takes the squasharr Deployment, runs its argv
// through the real command tree with its env, and reads the
// --worker-service-account and --data-claim the TranscodeJob controller
// would stamp onto every Job. That account must exist, must be bound to a
// ClusterRole whose rules are exactly the generated worker Role -- and to
// nothing else, least of all the manager role -- and the claim must be one
// the installer creates. The chart is rendered under two release names,
// because its names carry the fullname and a hard-coded default would pass
// under "clustarr" and fail under anything else.
func TestTranscodeJobServiceAccountHoldsTheWorkerRole(t *testing.T) {
	helm := findTool(t, "helm")
	kustomize := findTool(t, "kustomize")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	want := sortedGrants(grantsOf(readRole(t, root, "squasharr_worker_role.yaml").Rules))

	cases := map[string][]byte{
		"helm template clustarr":   run(t, root, helm, "template", "clustarr", "charts/clustarr"),
		"helm template media":      run(t, root, helm, "template", "media", "charts/clustarr"),
		"kustomize config/default": run(t, root, kustomize, "build", "config/default"),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			r := decodeRendered(t, out)
			var dep *appsv1.Deployment
			for i := range r.deployments {
				if r.deployments[i].Spec.Template.Labels["app.kubernetes.io/component"] == "squasharr" {
					dep = &r.deployments[i]
				}
			}
			require.NotNil(t, dep, "no squasharr Deployment rendered")
			ctr := dep.Spec.Template.Spec.Containers[0]

			// Exactly the Deployment's literal env; the four squasharr
			// variables are cleared first so this process's own
			// environment cannot stand in for a missing one.
			for _, e := range []string{workerImageEnv, workerImageCUDAEnv, workerServiceAccountEnv, dataClaimEnv} {
				t.Setenv(e, "")
			}
			for _, e := range ctr.Env {
				if e.ValueFrom == nil {
					t.Setenv(e.Name, e.Value)
				}
			}
			t.Setenv(namespaceEnv, "clustarr-system")
			got := stub(t, &runSquasharr)
			_, err := execute(t, ctr.Args...)
			require.NoError(t, err)
			require.NoError(t, got.Validate())

			account := got.WorkerServiceAccount
			require.True(t, r.serviceAccounts[account],
				"transcode Jobs would run as ServiceAccount %q, which %s does not create", account, name)
			require.True(t, r.claims[got.DataClaimName],
				"transcode Jobs would mount PVC %q, which %s does not create", got.DataClaimName, name)

			var boundTo []string
			for _, b := range r.bindings {
				for _, s := range b.Subjects {
					if s.Kind == "ServiceAccount" && s.Name == account {
						boundTo = append(boundTo, b.RoleRef.Name)
					}
				}
			}
			require.Len(t, boundTo, 1,
				"ServiceAccount %q should be bound to exactly the worker ClusterRole, and is bound to %v", account, boundTo)
			role, ok := r.roles[boundTo[0]]
			require.True(t, ok, "ServiceAccount %q is bound to ClusterRole %q, which %s does not render", account, boundTo[0], name)
			require.Equal(t, want, sortedGrants(grantsOf(role.Rules)),
				"the ClusterRole transcode Jobs run under is not the generated worker Role")
		})
	}
}

// TestChartIntelRenderGroupsReachTheController follows the chart's
// squasharr.intelRenderGroups value (X14) through what the chart renders:
// the squasharr Deployment's env, run through the real command tree, must
// give squasharr the same GIDs -- and no value must give none, since the
// render GID is per host install.
func TestChartIntelRenderGroupsReachTheController(t *testing.T) {
	helm := findTool(t, "helm")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		args []string
		want []int64
	}{
		"set":   {args: []string{"--set", "squasharr.intelRenderGroups={44,109}"}, want: []int64{44, 109}},
		"unset": {},
	} {
		t.Run(name, func(t *testing.T) {
			out := run(t, root, helm, append([]string{"template", "clustarr", "charts/clustarr"}, tc.args...)...)
			r := decodeRendered(t, out)
			var dep *appsv1.Deployment
			for i := range r.deployments {
				if r.deployments[i].Spec.Template.Labels["app.kubernetes.io/component"] == "squasharr" {
					dep = &r.deployments[i]
				}
			}
			require.NotNil(t, dep, "no squasharr Deployment rendered")
			ctr := dep.Spec.Template.Spec.Containers[0]
			t.Setenv(intelRenderGroupsEnv, "")
			for _, e := range ctr.Env {
				if e.ValueFrom == nil {
					t.Setenv(e.Name, e.Value)
				}
			}
			t.Setenv(namespaceEnv, "clustarr-system")
			got := stub(t, &runSquasharr)
			_, err := execute(t, ctr.Args...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.IntelRenderGroups,
				"the chart's squasharr.intelRenderGroups did not reach squasharr's --intel-render-groups")
		})
	}
}
