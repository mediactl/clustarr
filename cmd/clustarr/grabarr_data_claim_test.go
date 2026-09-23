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

// TestGrabarrEnginesMountAClaimTheInstallerCreates is
// TestTranscodeJobServiceAccountHoldsTheWorkerRole's data-claim half, for
// grabarr's engine workloads (plan task G1-5, found by E-4).
//
// The DownloadClient controller stamps a /data PersistentVolumeClaim onto
// every engine StatefulSet and Deployment it creates. That name was the
// constant "clustarr-data" -- config/'s claim -- while the chart names its
// claim "<release fullname>-data", so under any release name other than
// "clustarr" every engine pod mounted a claim that does not exist and sat in
// ContainerCreating forever. Nothing could see it: envtest schedules no pods.
//
// For each installer this takes the grabarr controller Deployment, runs its
// argv through the real command tree with exactly its literal env, and reads
// the --data-claim the controller would stamp. That claim must be one the
// installer creates, and the very claim the controller Deployment itself
// mounts at /data -- engines and controller share one volume. The chart is
// rendered under two release names because its names carry the fullname:
// a hard-coded default passes under "clustarr" and fails under anything else.
func TestGrabarrEnginesMountAClaimTheInstallerCreates(t *testing.T) {
	helm := findTool(t, "helm")
	kustomize := findTool(t, "kustomize")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

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
				if r.deployments[i].Spec.Template.Labels["app.kubernetes.io/component"] == "grabarr" {
					dep = &r.deployments[i]
				}
			}
			require.NotNil(t, dep, "no grabarr Deployment rendered")
			ctr := dep.Spec.Template.Spec.Containers[0]

			// Exactly the Deployment's literal env; grabarr's own variables
			// are cleared first so this process's environment cannot stand
			// in for a missing one.
			for _, e := range []string{engineImageEnv, dataClaimEnv} {
				t.Setenv(e, "")
			}
			for _, e := range ctr.Env {
				if e.ValueFrom == nil {
					t.Setenv(e.Name, e.Value)
				}
			}
			t.Setenv(namespaceEnv, "clustarr-system")
			got := stub(t, &runGrabarr)
			_, err := execute(t, ctr.Args...)
			require.NoError(t, err)
			require.NoError(t, got.Validate())

			require.True(t, r.claims[got.DataClaimName],
				"grabarr's engine workloads would mount PVC %q, which %s does not create", got.DataClaimName, name)

			var mounted string
			for _, v := range dep.Spec.Template.Spec.Volumes {
				if v.Name == "data" && v.PersistentVolumeClaim != nil {
					mounted = v.PersistentVolumeClaim.ClaimName
				}
			}
			require.NotEmpty(t, mounted, "the grabarr Deployment mounts no data claim")
			require.Equal(t, mounted, got.DataClaimName,
				"the engines would mount a different /data than the grabarr controller that sizes it")
		})
	}
}
