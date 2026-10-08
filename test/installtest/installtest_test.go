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

package installtest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/installtest"
)

func TestRBACRolesReadsTheMakefile(t *testing.T) {
	root := installtest.RepoRoot(t)
	roles := installtest.RBACRoles(t, root)
	require.NotEmpty(t, roles)
	names := map[string]bool{}
	for _, r := range roles {
		names[r.Name] = true
		assert.NotEmpty(t, r.Paths, r.Name)
		for _, p := range r.Paths {
			assert.Regexp(t, `^\./`, p, "%s: a controller-gen paths= pattern is relative to the repository root", r.Name)
		}
		_, err := os.Stat(filepath.Join(root, "config", "rbac", r.File()))
		assert.NoError(t, err, "RBAC_ROLES names %s but config/rbac/%s is missing", r.Name, r.File())
	}
	assert.True(t, names["manager"], "the manager's role is generated")
	assert.Equal(t, "grab_engine_role.yaml", installtest.RBACRole{Name: "grab-engine"}.File())
}

func TestDecodeRenderedSortsEveryKind(t *testing.T) {
	in := []byte(`apiVersion: apps/v1
kind: Deployment
metadata: {name: manager}
---
apiVersion: v1
kind: Service
metadata: {name: external-metrics}
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata: {name: v1beta1.external.metrics.k8s.io}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: auth-reader, namespace: kube-system}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: extension-apiserver-authentication-reader}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: clustarr-manager-role}
rules: [{apiGroups: [""], resources: [secrets], verbs: [get]}]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: clustarr-manager-rolebinding}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: clustarr-manager-role}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: manager}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data}
---
apiVersion: v1
kind: Secret
metadata: {name: art-signing-key}
---
# an empty document, as helm's conditional templates leave behind
`)
	r := installtest.DecodeRendered(t, in)
	require.Len(t, r.Deployments, 1)
	require.Len(t, r.Services, 1)
	require.Len(t, r.APIServices, 1)
	require.Len(t, r.RoleBindings, 1)
	require.Len(t, r.Bindings, 1)
	assert.Equal(t, "manager", r.Deployments[0].Name)
	assert.Equal(t, "external-metrics", r.Services[0].Name)
	assert.Equal(t, "v1beta1.external.metrics.k8s.io", r.APIServices[0].GetName())
	assert.Equal(t, "extension-apiserver-authentication-reader", r.RoleBindings[0].RoleRef.Name)
	assert.Equal(t, "clustarr-manager-role", r.Bindings[0].RoleRef.Name)
	assert.Contains(t, r.Roles, "clustarr-manager-role")
	assert.Equal(t, map[string]bool{"manager": true}, r.ServiceAccounts)
	assert.Equal(t, map[string]bool{"data": true}, r.Claims)
	assert.Contains(t, r.Secrets, "art-signing-key")
	assert.Len(t, installtest.Objects(t, in), 9, "the empty document is skipped")
}

func TestPatternCovers(t *testing.T) {
	assert.True(t, installtest.PatternCovers("./app/catalog/...", "app/catalog/controller/movie"))
	assert.True(t, installtest.PatternCovers("./app/catalog/...", "app/catalog"))
	assert.True(t, installtest.PatternCovers("./app/grab/status", "app/grab/status"))
	assert.False(t, installtest.PatternCovers("./app/grab/status", "app/grab/status/x"))
	assert.False(t, installtest.PatternCovers("./app/grab/...", "app/grabber"))
	assert.False(t, installtest.PatternCovers("./app/grab", "app/grabber"))
}

func TestWorkloadsKeepOnlyClustarrComponents(t *testing.T) {
	r := installtest.DecodeRendered(t, []byte(`apiVersion: apps/v1
kind: Deployment
metadata: {name: clustarr-manager, labels: {app.kubernetes.io/name: clustarr, app.kubernetes.io/component: manager}}
spec: {template: {spec: {containers: [{name: manager, env: [{name: A, value: a}, {name: P, valueFrom: {fieldRef: {fieldPath: metadata.name}}}, {name: S, valueFrom: {secretKeyRef: {name: s, key: k}}}]}]}}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: clustarr-nats-box, labels: {app.kubernetes.io/name: nats, app.kubernetes.io/component: nats-box}}
`))
	got := installtest.Workloads(r)
	require.Len(t, got, 1)
	c := installtest.Container(t, r, "manager")
	installtest.SetEnvFrom(t, c, "pod-0")
	assert.Equal(t, "a", os.Getenv("A"))
	assert.Equal(t, "pod-0", os.Getenv("P"))
	assert.Equal(t, "from-S", os.Getenv("S"))
}
