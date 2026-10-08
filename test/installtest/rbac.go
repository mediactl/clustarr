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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// Grant is one (API group, resource, verb) permission.
type Grant struct{ Group, Resource, Verb string }

// GrantsOf expands rules into the set of single grants they make.
func GrantsOf(rules []rbacv1.PolicyRule) map[Grant]bool {
	out := map[Grant]bool{}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					out[Grant{g, res, v}] = true
				}
			}
		}
	}
	return out
}

// SortedGrants renders a grant set as sorted "group/resource:verb" strings,
// so two sets compare with one Equal and a difference reads in a failure.
func SortedGrants(m map[Grant]bool) []string {
	var out []string
	for g := range m {
		out = append(out, g.Group+"/"+g.Resource+":"+g.Verb)
	}
	sort.Strings(out)
	return out
}

// RBACRole is one ClusterRole `make manifests` generates: its name in the
// Makefile's RBAC_ROLES and the package patterns RBAC_PATHS_<name> hands
// controller-gen. There is one per identity (design §10).
type RBACRole struct {
	Name  string
	Paths []string
}

// File is the generated role's file under config/rbac.
func (r RBACRole) File() string { return strings.ReplaceAll(r.Name, "-", "_") + "_role.yaml" }

// RBACRoles reads RBAC_ROLES and each RBAC_PATHS_<role> out of the Makefile
// -- what controller-gen is actually pointed at -- rather than restating
// them. A second copy in a test could drift from the generator's real input,
// which is precisely the class of bug the RBAC guards exist to catch.
func RBACRoles(t testing.TB, root string) []RBACRole {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)

	vars := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, found := strings.Cut(line, ":=")
		if !found {
			continue
		}
		vars[strings.TrimSpace(name)] = strings.Fields(value)
	}
	names := vars["RBAC_ROLES"]
	require.NotEmpty(t, names, "the Makefile has no RBAC_ROLES; the RBAC guards no longer know what controller-gen is given")
	out := make([]RBACRole, 0, len(names))
	for _, name := range names {
		paths := vars["RBAC_PATHS_"+name]
		require.NotEmpty(t, paths, "RBAC_ROLES names %q but the Makefile has no RBAC_PATHS_%s", name, name)
		out = append(out, RBACRole{Name: name, Paths: paths})
	}
	return out
}

// PatternCovers reports whether a controller-gen paths= pattern covers the
// package in dir (slash-separated, relative to the repo root): "./x/..." is
// x and everything beneath it, "./x" is x alone.
func PatternCovers(pattern, dir string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if base, ok := strings.CutSuffix(pattern, "/..."); ok {
		return dir == base || strings.HasPrefix(dir, base+"/")
	}
	return dir == pattern
}

// ReadRole decodes config/rbac/<file> and requires it to grant something.
func ReadRole(t testing.TB, root, file string) rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "config", "rbac", file))
	require.NoError(t, err)
	var role rbacv1.ClusterRole
	require.NoError(t, yaml.Unmarshal(raw, &role))
	require.NotEmpty(t, role.Rules, "config/rbac/%s has no rules", file)
	return role
}
