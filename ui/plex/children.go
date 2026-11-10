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

package plex

import (
	"net/http"
	"sort"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// handleChildren answers GET {metadata key}/{ratingKey}/children (spec
// §D.2, the tv and music roots): a show's seasons, or a season's episodes
// (research §8); an artist's albums, or an album's tracks (design §7.1),
// paged (spec §D.2's paging rules; research §8: "must honour
// X-Plex-Container-Size/-Start").
func (h *handler) handleChildren(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}

		u := h.urlsFor(r, idx)
		items, ok := h.childrenOf(root, u, idx, r.PathValue("ratingKey"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.writePage(w, r, root, u, idx, items, parsePaging(r))
	}
}

// handleGrandchildren answers GET {metadata key}/{ratingKey}/grandchildren
// (spec §D.2, the tv and music roots): a show's episodes, flattened across
// every season (research §8), or an artist's tracks across every album
// (design §7.1, built lazily per page), paged the same way as
// [handler.handleChildren]. Spec §D.2 names this "episodes of a show";
// called on a season's or an album's own ratingKey it answers 404; that
// ratingKey's own /children already serves the equivalent request.
func (h *handler) handleGrandchildren(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}
		u := h.urlsFor(r, idx)

		k, ok := resolveKey(idx, r.PathValue("ratingKey"))
		if !ok || k.IsSeason || k.Track != "" || k.IsPart {
			http.NotFound(w, r)
			return
		}
		if root.music {
			p := parsePaging(r)
			window, total, ok := h.musicGrandchildren(root, u, idx, k, p)
			if !ok {
				http.NotFound(w, r)
				return
			}
			h.writeWindow(w, r, root, u, idx, window, total, p)
			return
		}
		s, ok := idx.SeriesByUID(k.UID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		episodes := append([]*catalogv1.Episode(nil), idx.Episodes(s.UID)...)
		if u.otherOrder(s) {
			episodes = nil // no season data for an order clustarr does not store
		}
		sortEpisodes(episodes)
		items := make([]Metadata, len(episodes))
		for i, e := range episodes {
			items[i] = buildEpisodeMetadata(root, u, s, e)
		}
		h.writePage(w, r, root, u, idx, items, parsePaging(r))
	}
}

// childrenOf resolves ratingKey to a show (whose children are its seasons)
// or a season (whose children are its episodes), building each as a full
// Metadata object with no nested Children of its own -- /children is itself
// the paged listing; a season's own Children block
// ([buildSeasonChildren]/[buildEpisodeChildren]) is for the unpaged,
// includeChildren=1 case on GET .../{ratingKey} instead.
func (h *handler) childrenOf(root rootDef, u urls, idx *projection.Index, ratingKey string) ([]Metadata, bool) {
	k, ok := resolveKey(idx, ratingKey)
	if !ok {
		return nil, false
	}
	if root.music {
		return h.musicChildrenOf(root, u, idx, k)
	}
	if k.Track != "" || k.IsPart || k.Synthetic != "" {
		return nil, false
	}

	if k.IsSeason {
		s, ok := idx.SeriesByUID(k.UID)
		if !ok {
			return nil, false
		}
		if u.otherOrder(s) {
			return []Metadata{}, true
		}
		var episodes []*catalogv1.Episode
		for _, e := range idx.Episodes(s.UID) {
			if e.Spec.SeasonNumber == k.Season {
				episodes = append(episodes, e)
			}
		}
		sortEpisodes(episodes)
		out := make([]Metadata, len(episodes))
		for i, e := range episodes {
			out[i] = buildEpisodeMetadata(root, u, s, e)
		}
		return out, true
	}

	s, ok := idx.SeriesByUID(k.UID)
	if !ok {
		return nil, false
	}
	if u.otherOrder(s) {
		return []Metadata{}, true
	}
	seasons := append([]catalogv1.SeasonStatus(nil), s.Status.Seasons...)
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].Number < seasons[j].Number })
	out := make([]Metadata, 0, len(seasons))
	for _, season := range seasons {
		md, ok := buildSeasonMetadata(root, u, s, season.Number, idx, false)
		if ok {
			out = append(out, md)
		}
	}
	return out, true
}

// sortEpisodes sorts by season number then episode number, the stable
// ordering both /children (a season's episodes, where every entry shares
// one season number) and /grandchildren (a show's, across every season)
// need.
func sortEpisodes(episodes []*catalogv1.Episode) {
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].Spec.SeasonNumber != episodes[j].Spec.SeasonNumber {
			return episodes[i].Spec.SeasonNumber < episodes[j].Spec.SeasonNumber
		}
		return episodes[i].Spec.EpisodeNumber < episodes[j].Spec.EpisodeNumber
	})
}

// writePage windows items to p and writes the MediaContainer response,
// setting the paging response headers alongside the body's own
// offset/totalSize (spec §D.2).
func (h *handler) writePage(w http.ResponseWriter, r *http.Request, root rootDef, u urls, idx *projection.Index, items []Metadata, p pageRequest) {
	window, total := windowMetadata(items, p)
	h.writeWindow(w, r, root, u, idx, window, total, p)
}

// writeWindow writes one page already windowed to p -- window's items,
// out of total -- for a listing built lazily per page
// ([handler.musicGrandchildren]) as for one [writePage] windowed whole.
func (h *handler) writeWindow(w http.ResponseWriter, r *http.Request, root rootDef, u urls, idx *projection.Index, window []Metadata, total int, p pageRequest) {
	window = nonNilMetadata(window)
	// Only the page answered reads its episodes' documents.
	h.enrichEpisodes(r.Context(), u, window, idx)
	setPagingHeaders(w, p.start, total)
	writeMetadata(w, customizationOf(r, nil), MetadataContainer{
		Offset:     p.start,
		TotalSize:  total,
		Identifier: root.identifier,
		Size:       len(window),
		Metadata:   window,
	})
}
