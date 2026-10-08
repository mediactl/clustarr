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

// Package starttest holds what the start, registration and system envtests
// share (spec 2026-10-06 §10.3.1): an envtest control plane, an embedded
// JetStream server, probe and condition waits, a held Lease, and a
// kubeconfig per RBAC identity so a case runs under only the role its
// installers bind. It is imported only by tests.
package starttest

import (
	"path/filepath"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/test/installtest"
)

// StartEnv starts an envtest control plane with every CRD under
// config/crd/bases and stops it when the test ends. It skips without
// KUBEBUILDER_ASSETS, and fails instead under installtest.RequireTools.
func StartEnv(t testing.TB) *envtest.Environment {
	t.Helper()
	requireAssets(t)
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(installtest.RepoRoot(t), "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if _, err := env.Start(); err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	return env
}
