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

package starttest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/test/installtest"
)

// uiRoleFile is ui's hand-written ClusterRole, the one role `make manifests`
// does not generate.
const uiRoleFile = "ui_role.yaml"

// RolesFor names the ClusterRoles an identity runs under. Since the
// installers wave there is one per identity (spec 2026-10-06 §4.6,
// §10.2.4): each name in the Makefile's RBAC_ROLES, plus ui's hand-written
// role, is its own identity's only role.
func RolesFor(t testing.TB, identity string) []string {
	t.Helper()
	if identity == "ui" {
		return []string{identity}
	}
	for _, r := range installtest.RBACRoles(t, installtest.RepoRoot(t)) {
		if r.Name == identity {
			return []string{identity}
		}
	}
	t.Fatalf("no RBAC identity %q (the Makefile's RBAC_ROLES, or ui)", identity)
	return nil
}

// roleFile is the config/rbac file holding the role named role.
func roleFile(t testing.TB, role string) string {
	t.Helper()
	if role == "ui" {
		return uiRoleFile
	}
	return installtest.RBACRole{Name: role}.File()
}

// IdentityKubeconfigs makes a start envtest run every process under the RBAC
// it ships with -- the X14 split's proof, not only its guard.
//
// envtest's apiserver authorizes with RBAC, but its default user is in
// system:masters, which bypasses it: a case running as that user passes with
// a role that lacks a grant it uses and fails Forbidden on the first real
// cluster. So this installs, for each identity, every ClusterRole RolesFor
// names (config/rbac/<role>_role.yaml, or ui's hand-written ui_role.yaml)
// bound to a ServiceAccount subject of the identity's name; for the manager,
// which alone elects (lease manager.clustarr.io), also the hand-written
// leader-election Role and its RoleBinding, and the RoleBinding to
// kube-system's extension-apiserver-authentication-reader the External
// Metrics server needs (§10.2.2) -- exactly as the installers do. It returns
// a kubeconfig per identity for a client certificate authenticating as that
// ServiceAccount. A case then runs its process with KUBECONFIG pointing at
// its identity's file: an informer its role cannot list never syncs and the
// case never goes Ready, and a write its role cannot make fails the case's
// verify.
func IdentityKubeconfigs(t testing.TB, env *envtest.Environment, identities []string) map[string]string {
	t.Helper()
	ctx := context.Background()
	root := installtest.RepoRoot(t)
	scheme := runtime.NewScheme()
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("rbac scheme: %v", err)
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("build admin client: %v", err)
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
	electionCreated := false

	out := map[string]string{}
	dir := t.TempDir()
	for _, identity := range identities {
		if _, done := out[identity]; done {
			continue
		}
		subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: identity, Namespace: "default"}
		for _, name := range RolesFor(t, identity) {
			role := installtest.ReadRole(t, root, roleFile(t, name))
			role.ObjectMeta = metav1.ObjectMeta{Name: "start-envtest-" + identity + "-" + name}
			if err := admin.Create(ctx, &role); err != nil {
				t.Fatalf("create ClusterRole %s for %s: %v", name, identity, err)
			}
			if err := admin.Create(ctx, &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: role.Name},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
				Subjects:   []rbacv1.Subject{subject},
			}); err != nil {
				t.Fatalf("bind ClusterRole %s for %s: %v", name, identity, err)
			}
		}
		if identity == "manager" {
			if !electionCreated {
				if err := admin.Create(ctx, &election); err != nil {
					t.Fatalf("create the leader-election Role: %v", err)
				}
				electionCreated = true
			}
			if err := admin.Create(ctx, &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "start-envtest-" + identity + "-leader-election", Namespace: "default"},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: election.Name},
				Subjects:   []rbacv1.Subject{subject},
			}); err != nil {
				t.Fatalf("bind the leader-election Role for %s: %v", identity, err)
			}
			if err := admin.Create(ctx, &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "start-envtest-" + identity + "-auth-reader", Namespace: "kube-system"},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "extension-apiserver-authentication-reader"},
				Subjects:   []rbacv1.Subject{subject},
			}); err != nil {
				t.Fatalf("bind the authentication reader for %s: %v", identity, err)
			}
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

// RESTConfig loads the kubeconfig at path.
func RESTConfig(t testing.TB, kubeconfig string) *rest.Config {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err)
	return cfg
}
