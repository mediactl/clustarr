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

package crdcheck

import (
	"os"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestCRDsInstall applies every CRD in config/crd/bases to a real apiserver.
// It is the only check that compiles the CEL rules, enforces the apiserver's
// CEL cost budget and rejects schemas the generator will happily emit but the
// apiserver refuses (recursive types, empty array items, and so on).
//
// It needs the envtest control-plane binaries and is skipped when
// KUBEBUILDER_ASSETS is unset, so `go test ./...` still passes on a bare
// checkout. `make test` sets it.
func TestCRDsInstall(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test` to install the CRDs")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("install CRDs: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	if cfg == nil {
		t.Fatal("envtest returned a nil rest config")
	}
}
