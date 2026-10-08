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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/installtest"
	"github.com/mediactl/clustarr/test/starttest"
)

// The identities that run each field manager (split design §5.2 as ADR-0019
// §9.2 left it): every live manager has a writer, the pinned default owner
// is the manager's, the ui writes only as clustarr-ui, and no agent, engine
// or credential-less binary writes at all.
func TestManagersOfCoverEveryFieldManager(t *testing.T) {
	seen := map[string][]string{}
	for identity, ms := range starttest.ManagersOf {
		for _, m := range ms {
			seen[m] = append(seen[m], identity)
		}
	}
	for _, m := range k8s.FieldManagers() {
		assert.Len(t, seen[string(m)], 1, "field manager %s is written by %v; want exactly one identity", m, seen[string(m)])
	}
	assert.Equal(t, []string{"manager"}, seen[k8s.DefaultFieldOwner], "the pinned default owner")
	assert.Equal(t, []string{string(k8s.ManagerUI)}, starttest.ManagersOf["ui"])
	for _, r := range installtest.RBACRoles(t, installtest.RepoRoot(t)) {
		ms, ok := starttest.ManagersOf[r.Name]
		if !assert.True(t, ok, "RBAC identity %s has no ManagersOf entry", r.Name) || r.Name == "manager" {
			continue
		}
		assert.Empty(t, ms, "%s is an agent or engine identity; it writes nothing (ADR-0019 §9.2)", r.Name)
	}
	for identity := range starttest.ManagersOf {
		for _, m := range starttest.ManagersOf[identity] {
			if m == k8s.DefaultFieldOwner {
				continue
			}
			assert.True(t, k8s.FieldManager(m).Valid(), "%s writes as %s, which k8s.FieldManagers does not list", identity, m)
		}
	}
}
