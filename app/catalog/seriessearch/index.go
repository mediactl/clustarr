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

package seriessearch

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// IndexSearchesBySeries indexes a Search whose spec.PlannedBySeries() by
// spec.mediaRef.name.
const IndexSearchesBySeries = "remediation.search.series"

// RegisterIndexes registers IndexSearchesBySeries on idx, once per process
// (remediation.RegisterIndexes).
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.Search{}, IndexSearchesBySeries, seriesOf); err != nil {
		return fmt.Errorf("seriessearch: register %s: %w", IndexSearchesBySeries, err)
	}
	return nil
}

// seriesOf is IndexSearchesBySeries' value function.
func seriesOf(o client.Object) []string {
	s, ok := o.(*catalogv1alpha1.Search)
	if !ok || !s.Spec.PlannedBySeries() || s.Spec.MediaRef.Name == "" {
		return nil
	}
	return []string{s.Spec.MediaRef.Name}
}

// Of lists ns's planned Searches naming series, oldest first (by creation,
// then name).
func Of(ctx context.Context, r client.Reader, ns, series string) ([]catalogv1alpha1.Search, error) {
	var list catalogv1alpha1.SearchList
	if err := r.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{IndexSearchesBySeries: series}); err != nil {
		return nil, fmt.Errorf("seriessearch: list the Searches of %s/%s: %w", ns, series, err)
	}
	out := list.Items
	slices.SortFunc(out, func(a, b catalogv1alpha1.Search) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}
