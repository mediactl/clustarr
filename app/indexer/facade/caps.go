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

package facade

import (
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// capsFromIndexer renders idx.status.caps as a torznab.Caps. A never-probed
// Indexer (status.caps nil) answers with an empty-but-valid caps document
// rather than an error: "not probed yet" is not the same as "does not
// exist", and the indexer controller's own 12-hour reprobe (indexarr's
// caps refresh) is what fills this in, not a request against the facade.
func capsFromIndexer(idx *indexv1alpha1.Indexer) torznab.Caps {
	c := torznab.Caps{ServerTitle: idx.Name}
	st := idx.Status.Caps
	if st == nil {
		return c
	}
	c.LimitsMax = int(st.LimitsMax)
	c.LimitsDefault = int(st.LimitsDefault)
	c.Modes = modesFromIndexer(st.Modes)
	c.Categories = categoriesFromIndexer(st.Categories)
	return c
}

// modesFromIndexer converts Indexer.status.caps.modes (map[wire mode]->
// supported params) into torznab.Caps.Modes. The keys are ALREADY
// torznab.SearchMode's own wire values -- api/index/v1alpha1's Caps.Modes
// doc comment says so, and app/indexer/search/query.go's paramSupported reads
// the very same map the same way -- so this is a type change, not a
// translation. A mode present in the map is available; one absent from it is
// not, which is why this ranges over st.Modes rather than every known
// torznab.SearchMode.
func modesFromIndexer(modes map[string][]string) map[torznab.SearchMode]torznab.Searching {
	if len(modes) == 0 {
		return nil
	}
	out := make(map[torznab.SearchMode]torznab.Searching, len(modes))
	for mode, params := range modes {
		out[torznab.SearchMode(mode)] = torznab.Searching{Available: true, SupportedParams: params}
	}
	return out
}

// categoriesFromIndexer converts Indexer.status.caps.categories into
// torznab.Caps.Categories, which is already typed []newznab.Category --
// api/index/v1alpha1.Category/SubCategory carry the identical id/name shape
// (see index_types.go's Category doc comment on why the CRD cannot reuse
// pkg/newznab's type directly: a CRD schema cannot be recursive, and
// controller-gen would reject it).
func categoriesFromIndexer(cats []indexv1alpha1.Category) []newznab.Category {
	if len(cats) == 0 {
		return nil
	}
	out := make([]newznab.Category, 0, len(cats))
	for _, c := range cats {
		var sub []newznab.SubCategory
		if len(c.Sub) > 0 {
			sub = make([]newznab.SubCategory, 0, len(c.Sub))
			for _, sc := range c.Sub {
				sub = append(sub, newznab.SubCategory{ID: newznab.CategoryID(sc.ID), Name: sc.Name})
			}
		}
		out = append(out, newznab.Category{ID: newznab.CategoryID(c.ID), Name: c.Name, Sub: sub})
	}
	return out
}
