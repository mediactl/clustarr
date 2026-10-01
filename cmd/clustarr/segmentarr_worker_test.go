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
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func objects(t *testing.T, in []byte) []*unstructured.Unstructured {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(in), 4096)
	var out []*unstructured.Unstructured
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		if len(m) > 0 {
			out = append(out, &unstructured.Unstructured{Object: m})
		}
	}
}

// segmentarr-worker reads /data and talks NATS, nothing else: its pod
// mounts no ServiceAccount token and no binding names its account, in
// either installer (spec 2026-10-01 segment detection §4.1).
func TestSegmentarrWorkerHasNoCredentials(t *testing.T) {
	helm, kustomize := findTool(t, "helm"), findTool(t, "kustomize")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for name, docs := range map[string][]byte{
		"chart":     run(t, root, helm, "template", "clustarr", "charts/clustarr"),
		"kustomize": run(t, root, kustomize, "build", "config/default"),
	} {
		t.Run(name, func(t *testing.T) {
			var dep *unstructured.Unstructured
			var sa string
			for _, o := range objects(t, docs) {
				if o.GetKind() == "Deployment" && strings.HasSuffix(o.GetName(), "segmentarr-worker") {
					dep = o
				}
			}
			require.NotNil(t, dep, "no segmentarr-worker Deployment")
			spec, _, _ := unstructured.NestedMap(dep.Object, "spec", "template", "spec")
			assert.Equal(t, false, spec["automountServiceAccountToken"], "no ServiceAccount token")
			sa, _ = spec["serviceAccountName"].(string)
			require.NotEmpty(t, sa)
			for _, o := range objects(t, docs) {
				if k := o.GetKind(); k != "RoleBinding" && k != "ClusterRoleBinding" {
					continue
				}
				subjects, _, _ := unstructured.NestedSlice(o.Object, "subjects")
				for _, s := range subjects {
					assert.NotEqual(t, sa, s.(map[string]any)["name"], "%s %s binds the worker's account", o.GetKind(), o.GetName())
				}
			}
			containers, _, _ := unstructured.NestedSlice(spec, "containers")
			require.Len(t, containers, 1)
			c := containers[0].(map[string]any)
			assert.Equal(t, []any{"/usr/local/bin/segmentarr-worker"}, c["command"])
			env := map[string]bool{}
			for _, e := range c["env"].([]any) {
				env[e.(map[string]any)["name"].(string)] = true
			}
			assert.True(t, env["NATS_URL"] && env["POD_NAME"], "NATS_URL and POD_NAME")
			for _, m := range c["volumeMounts"].([]any) {
				if mm := m.(map[string]any); mm["mountPath"] == "/data" {
					assert.Equal(t, true, mm["readOnly"], "/data is read-only")
				}
			}
		})
	}
}
