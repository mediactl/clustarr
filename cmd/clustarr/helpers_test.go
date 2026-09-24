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
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// unreachableKubeconfig is a well-formed kubeconfig naming a server nothing
// listens on: enough for ctrl.GetConfig and a lazy client, never contacted.
const unreachableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
users:
- name: u
  user: {token: x}
`

// rbacGrant is one (API group, resource, verb) permission.
type rbacGrant struct{ group, resource, verb string }

func grantsOf(rules []rbacv1.PolicyRule) map[rbacGrant]bool {
	out := map[rbacGrant]bool{}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					out[rbacGrant{g, res, v}] = true
				}
			}
		}
	}
	return out
}

func sortedGrants(m map[rbacGrant]bool) []string {
	var out []string
	for g := range m {
		out = append(out, g.group+"/"+g.resource+":"+g.verb)
	}
	sort.Strings(out)
	return out
}

// rendered is one installer's output, decoded into the kinds this test reads.
type rendered struct {
	deployments     []appsv1.Deployment
	serviceAccounts map[string]bool
	claims          map[string]bool
	roles           map[string]rbacv1.ClusterRole
	bindings        []rbacv1.ClusterRoleBinding
}

func decodeRendered(t *testing.T, in []byte) rendered {
	t.Helper()
	r := rendered{serviceAccounts: map[string]bool{}, claims: map[string]bool{}, roles: map[string]rbacv1.ClusterRole{}}
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(in)), 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); errors.Is(err, io.EOF) {
			return r
		} else if err != nil {
			t.Fatalf("decode rendered manifests: %v", err)
		}
		if obj == nil {
			continue
		}
		into := func(v any) {
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj, v))
		}
		switch obj["kind"] {
		case "Deployment":
			var d appsv1.Deployment
			into(&d)
			r.deployments = append(r.deployments, d)
		case "ServiceAccount":
			var sa corev1.ServiceAccount
			into(&sa)
			r.serviceAccounts[sa.Name] = true
		case "PersistentVolumeClaim":
			var pvc corev1.PersistentVolumeClaim
			into(&pvc)
			r.claims[pvc.Name] = true
		case "ClusterRole":
			var cr rbacv1.ClusterRole
			into(&cr)
			r.roles[cr.Name] = cr
		case "ClusterRoleBinding":
			var crb rbacv1.ClusterRoleBinding
			into(&crb)
			r.bindings = append(r.bindings, crb)
		}
	}
}
