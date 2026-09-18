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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// forbidigoProbe is a package that violates the single-writer rule twice: once
// through Status().Update and once through Status().Patch. Both must be
// reported by .golangci.yml's forbidigo rule.
const forbidigoProbe = `package probe

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Exported so the unused linter stays out of the way: this test is about
// forbidigo and nothing else.
func StatusUpdate(ctx context.Context, c client.Client, m *catalogv1alpha1.Movie) error {
	return c.Status().Update(ctx, m)
}

func StatusPatch(ctx context.Context, c client.Client, m *catalogv1alpha1.Movie) error {
	return c.Status().Patch(ctx, m, client.Merge)
}
`

// TestForbidigoRejectsStatusWrites proves the lint gate §3 and §14 rely on is
// actually armed.
//
// It is not theoretical. The rule shipped with source-shaped patterns
// (`\.Status\(\)\.Update\(`), but with analyze-types forbidigo matches the
// RESOLVED name -- `client.SubResourceWriter.Update` -- so the patterns never
// fired and every status write outside pkg/k8s would have passed CI. Nothing
// else notices a linter that silently agrees with everything, so this test
// hands it a violation and insists it complains.
func TestForbidigoRejectsStatusWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: this test shells out to golangci-lint")
	}
	lint, err := exec.LookPath("golangci-lint-v2")
	if err != nil {
		if lint, err = exec.LookPath("golangci-lint"); err != nil {
			t.Skip("golangci-lint is not on PATH")
		}
	}

	// The probe has to live inside the module for the linter to type-check
	// it, and outside any directory .golangci.yml excludes.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the module root: %v", err)
	}
	dir, err := os.MkdirTemp(root, "forbidigoprobe")
	if err != nil {
		t.Fatalf("create the probe package: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove the probe package: %v", err)
		}
	})
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(forbidigoProbe), 0o600); err != nil {
		t.Fatalf("write the probe package: %v", err)
	}

	cmd := exec.Command(lint, "run", "./"+filepath.Base(dir)+"/...") //nolint:noctx // bounded by the test timeout
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("golangci-lint accepted two status writes outside pkg/k8s:\n%s", out)
	}
	for _, want := range []string{"Status().Update", "Status().Patch"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("golangci-lint did not report %s:\n%s", want, out)
		}
	}
	if n := strings.Count(string(out), "(forbidigo)"); n != 2 {
		t.Errorf("forbidigo reported %d issues, want 2:\n%s", n, out)
	}
}
