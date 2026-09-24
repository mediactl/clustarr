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

package qualityprofile_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/qualityprofile"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func TestSeedBuiltinsCreatesAllThirteen(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	cat := catalogue.LoadedCatalogue()

	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
		t.Fatalf("SeedBuiltins: %v", err)
	}

	var list catalogv1alpha1.QualityProfileList
	if err := c.List(ctx, &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 13 {
		t.Fatalf("got %d QualityProfiles, want 13 (pkg/quality.BuiltinProfiles's own count)", len(list.Items))
	}
	for _, p := range list.Items {
		if !p.Spec.BuiltIn {
			t.Errorf("%s: spec.builtIn = false, want true", p.Name)
		}
	}

	var hd catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &hd); err != nil {
		t.Fatalf("get hd-bluray-web: %v", err)
	}
	if hd.Spec.Cutoff != "Bluray-1080p" {
		t.Errorf("hd-bluray-web cutoff = %q, want Bluray-1080p", hd.Spec.Cutoff)
	}
}

func TestSeedBuiltinsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	cat := catalogue.LoadedCatalogue()

	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
		t.Fatalf("first SeedBuiltins: %v", err)
	}
	var before catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &before); err != nil {
		t.Fatalf("get: %v", err)
	}

	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
		t.Fatalf("second SeedBuiltins: %v", err)
	}
	var after catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	if before.UID != after.UID {
		t.Error("a no-op re-seed deleted and recreated the object (UID changed)")
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Error("a no-op re-seed still wrote the object (resourceVersion changed)")
	}
}

func TestSeedBuiltinsRecreatesOnDrift(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	cat := catalogue.LoadedCatalogue()

	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
		t.Fatalf("first SeedBuiltins: %v", err)
	}
	var before catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &before); err != nil {
		t.Fatalf("get: %v", err)
	}

	// Simulate a catalogue/seed content change the only way this test can:
	// corrupt the drift-detection annotation directly, the same way a real
	// version bump would make the freshly computed hash disagree with what
	// is stored.
	before.Annotations["catalog.clustarr.io/builtin-seed-hash"] = "stale-hash-simulating-a-version-bump"
	if err := c.Update(ctx, &before); err != nil {
		t.Fatalf("corrupt annotation: %v", err)
	}

	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
		t.Fatalf("second SeedBuiltins: %v", err)
	}
	var after catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &after); err != nil {
		t.Fatalf("get after re-seed: %v", err)
	}
	if before.UID == after.UID {
		t.Error("a drifted profile was not recreated (UID unchanged)")
	}
	if after.Spec.Cutoff != "Bluray-1080p" {
		t.Errorf("recreated profile cutoff = %q, want Bluray-1080p", after.Spec.Cutoff)
	}
}
