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
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/qualityprofile"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

// Real tier/quality names, copied verbatim from
// pkg/quality/catalogue/data/profiles/hd-bluray-web.json (the "hd-bluray-web"
// built-in) rather than invented, so a resolution failure in this test can
// only be the deliberately-broken tier below, not a typo in a made-up name.
func validVideoSpec(cutoff string) catalogv1alpha1.QualityProfileSpec {
	return catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{
			{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
			{Name: "WEB 1080p", Qualities: []string{"WEBRip-1080p", "WEBDL-1080p"}},
			{Name: "Bluray-720p", Qualities: []string{"Bluray-720p"}},
		},
		Cutoff: cutoff,
	}
}

func TestReconcileValidProfileIsReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "test-hd"},
		Spec:       validVideoSpec("Bluray-1080p"),
	}
	if err := c.Create(ctx, qp); err != nil {
		t.Fatalf("create QualityProfile: %v", err)
	}

	r := qualityprofile.NewReconciler(c, catalogue.LoadedCatalogue(), events.NewFakeRecorder(10))
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-hd"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "test-hd"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionReady) {
		t.Errorf("Ready is not True: %+v", got.Status.Conditions)
	}
	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionInvalid) {
		t.Error("Invalid is True for a valid profile")
	}
	if got.Status.Hash == "" {
		t.Error("status.hash is empty")
	}
	// QualityOrder flattens every quality Definition across every tier, not
	// one entry per tier: the status field's own MaxItems=320 cap (40 tiers x
	// 8 qualities) only makes sense under that reading, and quality.Profile.Tiers
	// is [][]Definition -- one inner slice per tier, with an entry per
	// quality name the tier lists. validVideoSpec's middle tier ("WEB 1080p")
	// names two qualities (WEBRip-1080p, WEBDL-1080p), so the flattened count
	// is 1 (Bluray-1080p) + 2 (WEB 1080p) + 1 (Bluray-720p) = 4, not the tier
	// count of 3.
	if len(got.Status.QualityOrder) != 4 {
		t.Errorf("qualityOrder has %d entries, want 4", len(got.Status.QualityOrder))
	}
	if got.Status.CatalogueVersion != catalogue.LoadedCatalogue().Version {
		t.Errorf("catalogueVersion = %q, want %q", got.Status.CatalogueVersion, catalogue.LoadedCatalogue().Version)
	}
}

func TestReconcileUnknownQualityNameIsInvalid(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	spec := validVideoSpec("Bogus")
	spec.Tiers = append(spec.Tiers, catalogv1alpha1.Tier{Name: "Bogus", Qualities: []string{"NotARealQuality"}})
	qp := &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "test-broken"}, Spec: spec}
	if err := c.Create(ctx, qp); err != nil {
		t.Fatalf("create QualityProfile: %v", err)
	}

	r := qualityprofile.NewReconciler(c, catalogue.LoadedCatalogue(), events.NewFakeRecorder(10))
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-broken"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, types.NamespacedName{Name: "test-broken"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionInvalid) {
		t.Error("Invalid is not True for a profile with an unknown quality name")
	}
	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionReady) {
		t.Error("Ready is True for an invalid profile")
	}
}
