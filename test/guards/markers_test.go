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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/test/installtest"
)

// TestMarkersHasNoCredentials: markers reads /data and talks NATS only
// (segment detection §4.1), so neither its ServiceAccount nor its pod mounts
// a token and nothing binds the account; it runs /usr/bin/markers with /data
// read-only.
func TestMarkersHasNoCredentials(t *testing.T) {
	for name, out := range map[string][]byte{
		"helm":      installtest.RenderChart(t),
		"kustomize": installtest.BuildKustomize(t, "config/default"),
	} {
		t.Run(name, func(t *testing.T) {
			objs := installtest.Objects(t, out)
			var dep, sa *unstructured.Unstructured
			for _, o := range objs {
				if o.GetKind() == "Deployment" && strings.HasSuffix(o.GetName(), "markers") {
					dep = o
				}
			}
			require.NotNil(t, dep, "no markers Deployment")
			spec, _, _ := unstructured.NestedMap(dep.Object, "spec", "template", "spec")
			assert.Equal(t, false, spec["automountServiceAccountToken"], "the pod mounts no token")
			account, _ := spec["serviceAccountName"].(string)
			require.NotEmpty(t, account)
			var bindings int
			for _, o := range objs {
				switch o.GetKind() {
				case "ServiceAccount":
					if o.GetName() == account {
						sa = o
					}
				case "RoleBinding", "ClusterRoleBinding":
					bindings++
					subjects, _, _ := unstructured.NestedSlice(o.Object, "subjects")
					for _, s := range subjects {
						assert.NotEqual(t, account, s.(map[string]any)["name"], "%s %s binds markers' account", o.GetKind(), o.GetName())
					}
				}
			}
			require.Positive(t, bindings, "%s renders no binding; this guard is reading the wrong render", name)
			require.NotNil(t, sa, "the markers ServiceAccount is rendered")
			automount, found, _ := unstructured.NestedBool(sa.Object, "automountServiceAccountToken")
			assert.True(t, found && !automount, "the markers ServiceAccount mounts no token")
			containers, _, _ := unstructured.NestedSlice(spec, "containers")
			require.Len(t, containers, 1)
			c := containers[0].(map[string]any)
			assert.Equal(t, []any{binpath.Markers}, c["command"])
			env := map[string]bool{}
			for _, e := range c["env"].([]any) {
				env[e.(map[string]any)["name"].(string)] = true
			}
			assert.True(t, env["NATS_URL"] && env["POD_NAME"], "NATS_URL and POD_NAME")
			var data bool
			for _, m := range c["volumeMounts"].([]any) {
				if mm := m.(map[string]any); mm["mountPath"] == "/data" {
					data = true
					assert.Equal(t, true, mm["readOnly"], "/data is read-only")
				}
			}
			assert.True(t, data, "markers mounts /data")
		})
	}
}
