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

package indexer

import (
	"math"
	"slices"
	"sort"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// SupportsMode gates the caps check. mode MUST be a torznab.SearchMode value
// ("search", "tvsearch", "movie", "music", "audio", "book") -- ruling R5. A
// zero Caps (an Indexer whose caps have not been probed) supports nothing,
// so a caller must treat status.caps == nil as "not yet probed", not as
// "supports everything".
func SupportsMode(caps indexv1alpha1.Caps, mode string) bool {
	if len(caps.Modes) == 0 || mode == "" {
		return false
	}
	_, ok := caps.Modes[mode]
	return ok
}

// maxCapsItems mirrors the CRD's +kubebuilder:validation:MaxItems=200 on
// status.caps.categories and on each Category.Sub. Exceeding it is an
// apiserver rejection of the whole apply, so the projection truncates rather
// than letting one chatty indexer make its own status unwritable.
const maxCapsItems = 200

// projectCaps maps the wire caps onto the CRD's Caps.
//
// Only AVAILABLE modes are projected: SupportsMode treats key presence as
// availability, so projecting an unavailable mode would advertise a search
// the indexer answers with error 203. This matters more than it looks --
// torznab.ParseCaps populates all six modes on every document, with
// Available false for the elements the indexer did not send.
func projectCaps(c torznab.Caps) indexv1alpha1.Caps {
	out := indexv1alpha1.Caps{
		LimitsMax:     clampInt32(c.LimitsMax),
		LimitsDefault: clampInt32(c.LimitsDefault),
		// SupportsRawSearch is the plain t=search mode's searchEngine
		// attribute: the free-text q= fallback the search fan-out uses when
		// caps gate out every id parameter.
		SupportsRawSearch: c.Modes[torznab.ModeSearch].SearchEngine == "raw",
	}
	for mode, s := range c.Modes {
		if !s.Available {
			continue
		}
		params := slices.Clone(s.SupportedParams)
		sort.Strings(params) // a stable map value keeps the apply idempotent
		if out.Modes == nil {
			out.Modes = make(map[string][]string, len(c.Modes))
		}
		out.Modes[string(mode)] = params
	}

	cats := slices.Clone(c.Categories)
	sort.SliceStable(cats, func(i, j int) bool { return cats[i].ID < cats[j].ID })
	if len(cats) > maxCapsItems {
		cats = cats[:maxCapsItems]
	}
	for _, cat := range cats {
		sub := slices.Clone(cat.Sub)
		sort.SliceStable(sub, func(i, j int) bool { return sub[i].ID < sub[j].ID })
		if len(sub) > maxCapsItems {
			sub = sub[:maxCapsItems]
		}
		projected := indexv1alpha1.Category{ID: int32(cat.ID), Name: cat.Name}
		for _, s := range sub {
			projected.Sub = append(projected.Sub, indexv1alpha1.SubCategory{ID: int32(s.ID), Name: s.Name})
		}
		out.Categories = append(out.Categories, projected)
	}
	return out
}

// clampInt32 narrows an indexer-supplied count. The wire type is int, which
// is 64-bit here, and status.caps.limitsMax is int32: an indexer answering
// limits max="99999999999" would otherwise wrap to a negative page size.
func clampInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}
