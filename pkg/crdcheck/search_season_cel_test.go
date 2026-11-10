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

import "k8s.io/apimachinery/pkg/runtime/schema"

var gvrSearches = schema.GroupVersionResource{Group: "catalog.clustarr.io", Version: "v1alpha1", Resource: "searches"}

// searchSeasonCases are the Search CRD's spec.season rules (series search
// design §4; the 2026-11-10 non-video design D10, §9.4): a season on a
// series, planned (grabBest) or interactive; on a comic only the volume,
// season 1 (searchplan.VolumeNumber, catalogv1alpha1.VolumeSearchSeason),
// planned or interactive, where a planned one takes no spec.grab; never on
// a movie, an author or an artist, and never another season of a comic.
func searchSeasonCases() []admissionCase {
	search := func(kind string, season any, grabBest bool, grab ...string) map[string]any {
		spec := map[string]any{"mediaRef": map[string]any{"kind": kind, "name": "x"}}
		if season != nil {
			spec["season"] = season
		}
		if grabBest {
			spec["grabBest"] = true
		}
		if len(grab) > 0 {
			spec["grab"] = grab
		}
		return map[string]any{
			"apiVersion": "catalog.clustarr.io/v1alpha1",
			"kind":       "Search",
			"metadata":   map[string]any{"name": "s", "namespace": "default"},
			"spec":       spec,
		}
	}
	return []admissionCase{
		{"Search: a planned series season", gvrSearches, search("series", int64(2), true), ""},
		{"Search: an interactive series season", gvrSearches, search("series", int64(0), false), ""},
		{"Search: a planned comic volume (season 1)", gvrSearches, search("comic", int64(1), true), ""},
		{"Search: an interactive comic volume", gvrSearches, search("comic", int64(1), false), ""},
		{"Search: an interactive comic volume takes a pick", gvrSearches, search("comic", int64(1), false, "guid-a"), ""},
		{"Search: a comic search without a season fans out", gvrSearches, search("comic", nil, true), ""},
		{"Search: a comic has one volume, season 2 is refused", gvrSearches, search("comic", int64(2), true), "season needs mediaRef.kind series, or comic with season 1 (the volume)"},
		{"Search: a comic volume is never season 0", gvrSearches, search("comic", int64(0), false), "season needs mediaRef.kind series, or comic with season 1 (the volume)"},
		{"Search: a planned comic volume takes no spec.grab", gvrSearches, search("comic", int64(1), true, "guid-a"), "a comic's volume search with grabBest grabs through the comic; spec.grab needs an interactive volume search"},
		{"Search: a season on a movie is refused", gvrSearches, search("movie", int64(1), true), "season needs mediaRef.kind series, or comic with season 1 (the volume)"},
		{"Search: a season on an author is refused", gvrSearches, search("author", int64(1), false), "season needs mediaRef.kind series, or comic with season 1 (the volume)"},
	}
}
