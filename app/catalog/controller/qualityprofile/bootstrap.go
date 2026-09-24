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

package qualityprofile

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// seedHashAnnotation records the resolved Profile.Hash a built-in was last
// created with, so a re-run can tell "nothing changed" from "the catalogue or
// the seed JSON moved on, re-seed this one" without ever comparing a
// builtIn:true spec for update (the CEL rule forbids that outright).
const seedHashAnnotation = "catalog.clustarr.io/builtin-seed-hash"

// SeedBuiltins ensures every embedded profile seed
// (pkg/quality/catalogue.ProfileFS, "data/profiles/*.json") exists as a
// builtIn:true QualityProfile, creating it if absent and replacing it if its
// resolved content drifted from what is stored. It is idempotent and
// side-effect free when nothing changed.
func SeedBuiltins(ctx context.Context, c client.Client, cat *catalogue.Catalogue) error {
	entries, err := catalogue.ProfileFS().ReadDir("data/profiles")
	if err != nil {
		return fmt.Errorf("qualityprofile: read profile seeds: %w", err)
	}

	for _, e := range entries {
		doc, err := catalogue.ProfileFS().ReadFile("data/profiles/" + e.Name())
		if err != nil {
			return fmt.Errorf("qualityprofile: read %s: %w", e.Name(), err)
		}
		seeds, err := quality.DecodeProfileSeeds(doc)
		if err != nil {
			return fmt.Errorf("qualityprofile: decode %s: %w", e.Name(), err)
		}
		for _, seed := range seeds {
			if err := seedOne(ctx, c, cat, seed); err != nil {
				return err
			}
		}
	}
	return nil
}

func seedOne(ctx context.Context, c client.Client, cat *catalogue.Catalogue, seed quality.ProfileSeed) error {
	log := logging.FromContext(ctx).With("builtin", seed.Name)

	spec := seed.Spec
	spec.BuiltIn = true // the seed JSON never sets this; it is a property of
	// how the object was created, not of the resolved profile content.

	want := &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: seed.Name}, Spec: spec}
	profile, errs := quality.FromCRD(want, cat)
	if len(errs) > 0 {
		return fmt.Errorf("qualityprofile: built-in %s does not resolve: %v", seed.Name, errs)
	}
	want.Annotations = map[string]string{seedHashAnnotation: profile.Hash}

	var existing catalogv1alpha1.QualityProfile
	err := c.Get(ctx, types.NamespacedName{Name: seed.Name}, &existing)
	switch {
	case apierrors.IsNotFound(err):
		log.Info("creating built-in QualityProfile")
		return client.IgnoreAlreadyExists(c.Create(ctx, want))
	case err != nil:
		return fmt.Errorf("qualityprofile: get %s: %w", seed.Name, err)
	case existing.Annotations[seedHashAnnotation] == profile.Hash:
		return nil // up to date, nothing to do
	default:
		log.Info("built-in content drifted, recreating", "oldHash", existing.Annotations[seedHashAnnotation], "newHash", profile.Hash)
		if err := c.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("qualityprofile: delete stale %s: %w", seed.Name, err)
		}
		return client.IgnoreAlreadyExists(c.Create(ctx, want))
	}
}

// Bootstrap is a one-shot manager.Runnable that calls SeedBuiltins once the
// leader is elected. It is added via mgr.Add in the wiring task, not started
// as part of the watch-driven Reconciler.
type Bootstrap struct {
	Client    client.Client
	Catalogue *catalogue.Catalogue
}

func (b *Bootstrap) Start(ctx context.Context) error {
	return SeedBuiltins(ctx, b.Client, b.Catalogue)
}

func (b *Bootstrap) NeedLeaderElection() bool { return true }
