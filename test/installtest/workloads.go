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

package installtest

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// Workloads returns r's clustarr Deployments by app.kubernetes.io/component,
// skipping subchart and fixture workloads (nats-box, the e2e stubs).
func Workloads(r Rendered) map[string]appsv1.Deployment {
	out := map[string]appsv1.Deployment{}
	for _, d := range r.Deployments {
		if d.Labels["app.kubernetes.io/name"] != "clustarr" {
			continue
		}
		if c := d.Labels["app.kubernetes.io/component"]; c != "" {
			out[c] = d
		}
	}
	return out
}

// Container returns component's one container, failing when it is absent.
func Container(t testing.TB, r Rendered, component string) corev1.Container {
	t.Helper()
	d, ok := Workloads(r)[component]
	require.True(t, ok, "no Deployment for component %q", component)
	require.Len(t, d.Spec.Template.Spec.Containers, 1, component)
	return d.Spec.Template.Spec.Containers[0]
}

// SetEnvFrom sets c's environment in the test process as the kubelet
// would: literal values as written, metadata.name and metadata.namespace
// fieldRefs as podName and "clustarr-system", and anything read from a
// Secret or ConfigMap as "from-<NAME>". A command reads its flag defaults
// from the environment, so its parse of the rendered args must see it.
func SetEnvFrom(t *testing.T, c corev1.Container, podName string) {
	t.Helper()
	for _, e := range c.Env {
		v := e.Value
		if f := e.ValueFrom; f != nil {
			switch {
			case f.FieldRef != nil && f.FieldRef.FieldPath == "metadata.name":
				v = podName
			case f.FieldRef != nil && f.FieldRef.FieldPath == "metadata.namespace":
				v = "clustarr-system"
			default:
				v = "from-" + e.Name
			}
		}
		t.Setenv(e.Name, v)
	}
}
