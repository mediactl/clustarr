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
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Rendered is one installer's output, decoded into the kinds the installer
// guards read. APIServices stay unstructured: kube-aggregator is not a
// module dependency.
type Rendered struct {
	Deployments     []appsv1.Deployment
	ServiceAccounts map[string]bool
	Claims          map[string]bool
	Roles           map[string]rbacv1.ClusterRole
	Bindings        []rbacv1.ClusterRoleBinding
	RoleBindings    []rbacv1.RoleBinding
	Secrets         map[string]corev1.Secret
	Services        []corev1.Service
	APIServices     []unstructured.Unstructured
}

// DecodeRendered sorts a multi-document manifest stream into a Rendered.
func DecodeRendered(t testing.TB, in []byte) Rendered {
	t.Helper()
	r := Rendered{
		ServiceAccounts: map[string]bool{}, Claims: map[string]bool{},
		Roles: map[string]rbacv1.ClusterRole{}, Secrets: map[string]corev1.Secret{},
	}
	for _, obj := range Objects(t, in) {
		into := func(v any) {
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, v))
		}
		switch obj.GetKind() {
		case "Deployment":
			var d appsv1.Deployment
			into(&d)
			r.Deployments = append(r.Deployments, d)
		case "ServiceAccount":
			r.ServiceAccounts[obj.GetName()] = true
		case "PersistentVolumeClaim":
			r.Claims[obj.GetName()] = true
		case "ClusterRole":
			var cr rbacv1.ClusterRole
			into(&cr)
			r.Roles[cr.Name] = cr
		case "ClusterRoleBinding":
			var crb rbacv1.ClusterRoleBinding
			into(&crb)
			r.Bindings = append(r.Bindings, crb)
		case "RoleBinding":
			var rb rbacv1.RoleBinding
			into(&rb)
			r.RoleBindings = append(r.RoleBindings, rb)
		case "Secret":
			var sec corev1.Secret
			into(&sec)
			r.Secrets[sec.Name] = sec
		case "Service":
			var svc corev1.Service
			into(&svc)
			r.Services = append(r.Services, svc)
		case "APIService":
			r.APIServices = append(r.APIServices, *obj)
		}
	}
	return r
}

// Objects splits a multi-document manifest stream into its objects,
// skipping the empty documents helm's conditional templates leave behind.
func Objects(t testing.TB, in []byte) []*unstructured.Unstructured {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(in), 4096)
	var out []*unstructured.Unstructured
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err, "decode rendered manifests")
		if len(m) > 0 {
			out = append(out, &unstructured.Unstructured{Object: m})
		}
	}
}

// DeploymentsIn decodes every Deployment document in a manifest file.
func DeploymentsIn(t testing.TB, path string) []appsv1.Deployment {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()

	var out []appsv1.Deployment
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		var d appsv1.Deployment
		switch err := dec.Decode(&d); {
		case errors.Is(err, io.EOF):
			return out
		case err != nil:
			t.Fatalf("decode %s: %v", path, err)
		}
		if d.Kind == "Deployment" {
			out = append(out, d)
		}
	}
}
