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

// TestSeedBuiltinsUpdatesADriftedBuiltinInPlace: a built-in whose seed
// moved on is updated, never deleted and recreated -- between the delete
// and the create every Episode and Movie on it read "profile unresolved"
// and flipped back, two bursts of status writes and Events per deploy
// (phase 2 review, 2026-10-06). spec.seedHash is what lets the seeder, and
// only a change of it, edit a built-in.
func TestSeedBuiltinsUpdatesADriftedBuiltinInPlace(t *testing.T) {
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
	if before.Spec.SeedHash == "" || before.Spec.SeedHash != before.Annotations["catalog.clustarr.io/builtin-seed-hash"] {
		t.Fatalf("spec.seedHash = %q, want the seed's hash %q", before.Spec.SeedHash, before.Annotations["catalog.clustarr.io/builtin-seed-hash"])
	}

	// A seed from an older release: another hash and other content, as an
	// upgrade finds it (written as an older seeder would have: with its own
	// seedHash, which is what admits the change).
	stale := before.DeepCopy()
	stale.Annotations["catalog.clustarr.io/builtin-seed-hash"] = "stale-hash"
	stale.Spec.SeedHash = "stale-hash"
	stale.Spec.Cutoff = stale.Spec.Tiers[len(stale.Spec.Tiers)-1].Name
	if err := c.Update(ctx, stale); err != nil {
		t.Fatalf("stale seed: %v", err)
	}

	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
		t.Fatalf("second SeedBuiltins: %v", err)
	}
	var after catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &after); err != nil {
		t.Fatalf("get after re-seed: %v", err)
	}
	if before.UID != after.UID {
		t.Error("a drifted built-in was deleted and recreated (UID changed); it must be updated in place")
	}
	if after.Spec.Cutoff != "Bluray-1080p" || after.Spec.SeedHash != before.Spec.SeedHash {
		t.Errorf("re-seeded cutoff %q seedHash %q, want Bluray-1080p and %q", after.Spec.Cutoff, after.Spec.SeedHash, before.Spec.SeedHash)
	}
	if after.Annotations["catalog.clustarr.io/builtin-seed-hash"] != before.Spec.SeedHash {
		t.Errorf("annotation = %q, want %q", after.Annotations["catalog.clustarr.io/builtin-seed-hash"], before.Spec.SeedHash)
	}

	// An owner's edit of a built-in is still refused.
	edit := after.DeepCopy()
	edit.Spec.Cutoff = edit.Spec.Tiers[len(edit.Spec.Tiers)-1].Name
	if err := c.Update(ctx, edit); err == nil {
		t.Error("an edit of a built-in that leaves spec.seedHash alone was accepted")
	}
}
