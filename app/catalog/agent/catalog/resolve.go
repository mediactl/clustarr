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

package catalog

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/delay"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// resolveQualityProfile turns a QualityProfile name into a resolved
// quality.Profile. It is the production ResolveProfile for
// app/catalog/worker/grab.Sink.
//
// QualityProfile is cluster-scoped (qualityprofile_types.go), so the lookup
// takes no namespace.
func resolveQualityProfile(ctx context.Context, c client.Client, name string, cat *catalogue.Catalogue) (quality.Profile, error) {
	var qp catalogv1alpha1.QualityProfile
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return quality.Profile{}, fmt.Errorf("catalogarr: quality profile %q not found", name)
		}
		return quality.Profile{}, err
	}
	profile, errs := quality.FromCRD(&qp, cat)
	if len(errs) > 0 {
		return quality.Profile{}, fmt.Errorf("catalogarr: resolve quality profile %q: %w", name, errs[0])
	}
	return profile, nil
}

// resolveDelayProfile runs §8.2's resolution order (item ref -> tag match ->
// lowest order) over the namespace's DelayProfiles, through
// app/catalog/delay's pure Resolve. It is the production ResolveDelay for
// app/catalog/worker/grab.Sink.
//
// A namespace with no catch-all profile yields ErrNoMatch, which is "no
// delay", not a failure: the chart installs a catch-all, and an operator who
// removed it meant grabs to be immediate. This matches what the RSS matcher
// does with the same situation, deliberately -- §8.7 requires an RSS hit and
// a search hit to take the same delay/lease/grab path.
func resolveDelayProfile(ctx context.Context, c client.Client, ns string, ref *string, tags []string) (catalogv1alpha1.DelayProfileSpec, error) {
	var list catalogv1alpha1.DelayProfileList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return catalogv1alpha1.DelayProfileSpec{}, fmt.Errorf("catalogarr: list delay profiles: %w", err)
	}
	dp, err := delay.Resolve(ref, tags, list.Items)
	if err != nil {
		logging.FromContext(ctx).Debug("catalogarr: no delay profile applies; grabbing without a delay",
			"namespace", ns, "reason", err)
		return catalogv1alpha1.DelayProfileSpec{}, nil
	}
	return dp.Spec, nil
}
