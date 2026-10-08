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

package starttest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/test/installtest"
	"github.com/mediactl/clustarr/test/starttest"
)

// identities is every RBAC identity a start envtest can run a process as:
// the Makefile's RBAC_ROLES plus ui's hand-written role.
func identities(t *testing.T) []string {
	out := []string{"ui"}
	for _, r := range installtest.RBACRoles(t, installtest.RepoRoot(t)) {
		out = append(out, r.Name)
	}
	return out
}

// Each identity runs under exactly its own generated role (spec §10.2.4):
// the installers wave replaced the pre-recut bridge.
func TestRolesForIsTheIdentityItself(t *testing.T) {
	roles := map[string]bool{}
	for _, id := range identities(t) {
		roles[id] = true
	}
	for _, identity := range []string{
		"manager", "agent-catalog", "agent-events", "agent-metadata",
		"agent-import", "agent-index", "agent-caption", "grab-engine", "ui",
	} {
		assert.Equal(t, []string{identity}, starttest.RolesFor(t, identity))
		assert.True(t, roles[identity], "no role named %s", identity)
	}
}

// Every role RolesFor names resolves to a file the installers apply: a
// generated config/rbac/<role>_role.yaml (RBACRole.File), or ui's
// hand-written ui_role.yaml. IdentityKubeconfigs installs exactly these, so
// a name that resolved to nothing would fail every RBAC-enforced start.
func TestRolesForBridgesToTheGeneratedRoles(t *testing.T) {
	root := installtest.RepoRoot(t)
	for _, identity := range identities(t) {
		for _, role := range starttest.RolesFor(t, identity) {
			file := installtest.RBACRole{Name: role}.File()
			if role == "ui" {
				file = "ui_role.yaml"
			}
			_, err := os.Stat(filepath.Join(root, "config", "rbac", file))
			assert.NoError(t, err, "identity %s runs under role %s, but config/rbac/%s is missing", identity, role, file)
			installtest.ReadRole(t, root, file)
		}
	}
}
