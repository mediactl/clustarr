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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

// identityDirs maps the app/<dir> a case name carries -- the restructure
// that nested every service under app/ (commit 0d798b3) renamed case names
// like "catalogarr/worker" to "app/catalog/worker" -- onto the RBAC
// identity it actually runs as (the Makefile's RBAC_ROLES, which the
// restructure left alone: package names and directories moved, service and
// role identifiers did not).
var identityDirs = map[string]string{
	"catalog": "catalogarr",
	"import":  "importarr",
	"indexer": "indexarr",
	"grab":    "grabarr",
	"squash":  "squasharr",
	"caption": "captionarr",
}

// caseIdentity is the RBAC identity a start-envtest case runs as: the
// service named by its case name (through identityDirs for an "app/<dir>/…"
// name, or the name's own first segment otherwise, e.g. "ui"), except
// grabarr's engine roles, which run as the engine pods do, under
// grabarr-engine.
func caseIdentity(name string) string {
	parts := strings.Split(strings.Fields(name)[0], "/")
	service := parts[0]
	if service == "app" && len(parts) >= 2 {
		service = identityDirs[parts[1]]
	}
	role := parts[len(parts)-1]
	if service == "grabarr" && strings.HasSuffix(role, "-engine") {
		return "grabarr-engine"
	}
	return service
}

// identityKubeconfigs makes the start envtest run every service under the
// RBAC it ships with -- the X14 split's proof, not only its guard.
//
// envtest's apiserver authorizes with RBAC, but its default user is in
// system:masters, which bypasses it: until X14 every case ran with every
// permission, so a service whose role lacked a grant it uses passed here and
// failed Forbidden on the first real cluster. That mattered little while
// every service shared one union role; with one role per service it is the
// whole question. So this installs every generated ClusterRole
// (config/rbac/<role>_role.yaml, and ui's hand-written ui_role.yaml) and the
// hand-written leader-election Role exactly as the installers do, binds each
// to a ServiceAccount subject of the identity's name, and returns a
// kubeconfig per identity for a client certificate authenticating as that
// ServiceAccount. A case then runs its service with KUBECONFIG pointing at
// its identity's file: an informer its role cannot list never syncs and the
// case never goes Ready, and a write its role cannot make fails the case's
// verify.
func identityKubeconfigs(t *testing.T, env *envtest.Environment, identities []string) map[string]string {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("rbac scheme: %v", err)
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("build admin client: %v", err)
	}

	roleFiles := map[string]string{"ui": "ui_role.yaml"}
	for _, r := range rbacRoles(t, root) {
		roleFiles[r.name] = r.file()
	}

	var election rbacv1.Role
	raw, err := os.ReadFile(filepath.Join(root, "config", "rbac", "leader_election_role.yaml"))
	if err != nil {
		t.Fatalf("read the leader-election Role: %v", err)
	}
	if err := yaml.Unmarshal(raw, &election); err != nil {
		t.Fatalf("decode the leader-election Role: %v", err)
	}
	election.Namespace = "default"
	if err := admin.Create(ctx, &election); err != nil {
		t.Fatalf("create the leader-election Role: %v", err)
	}

	out := map[string]string{}
	dir := t.TempDir()
	for _, identity := range identities {
		if _, done := out[identity]; done {
			continue
		}
		file, ok := roleFiles[identity]
		if !ok {
			t.Fatalf("no generated or hand-written role for identity %q", identity)
		}
		role := readRole(t, root, file)
		role.ObjectMeta = metav1.ObjectMeta{Name: "start-envtest-" + identity}
		if err := admin.Create(ctx, &role); err != nil {
			t.Fatalf("create ClusterRole for %s: %v", identity, err)
		}
		subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: identity, Namespace: "default"}
		if err := admin.Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "start-envtest-" + identity},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
			Subjects:   []rbacv1.Subject{subject},
		}); err != nil {
			t.Fatalf("bind ClusterRole for %s: %v", identity, err)
		}
		if err := admin.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "start-envtest-" + identity + "-leader-election", Namespace: "default"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: election.Name},
			Subjects:   []rbacv1.Subject{subject},
		}); err != nil {
			t.Fatalf("bind the leader-election Role for %s: %v", identity, err)
		}

		user, err := env.AddUser(envtest.User{
			Name:   "system:serviceaccount:default:" + identity,
			Groups: []string{"system:serviceaccounts", "system:serviceaccounts:default", "system:authenticated"},
		}, nil)
		if err != nil {
			t.Fatalf("add user for %s: %v", identity, err)
		}
		kc, err := user.KubeConfig()
		if err != nil {
			t.Fatalf("kubeconfig for %s: %v", identity, err)
		}
		path := filepath.Join(dir, identity+".kubeconfig")
		if err := os.WriteFile(path, kc, 0o600); err != nil {
			t.Fatalf("write kubeconfig for %s: %v", identity, err)
		}
		out[identity] = path
	}
	return out
}
