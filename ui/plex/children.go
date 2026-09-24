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
// §D.2, tv root only): a show's seasons, or a season's episodes (research
// §8), paged (spec §D.2's paging rules; research §8: "must honour
// X-Plex-Container-Size/-Start").
func (h *handler) handleChildren(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}

		items, ok := h.childrenOf(root, idx, r.PathValue("ratingKey"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.writePage(w, root, items, parsePaging(r))
	}
}

// handleGrandchildren answers GET {metadata key}/{ratingKey}/grandchildren
// (spec §D.2, tv root only): a show's episodes, flattened across every
// season (research §8), paged the same way as [handler.handleChildren].
// Spec §D.2 names this "episodes of a show"; called on a season's own
// ratingKey it answers 404; that ratingKey's own /children (all its
// episodes) already serves the equivalent request.
func (h *handler) handleGrandchildren(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}

		uid, _, isSeason, ok := ParseRatingKey(r.PathValue("ratingKey"))
		if !ok || isSeason {
			http.NotFound(w, r)
			return
		}
		s, ok := idx.SeriesByUID(uid)
		if !ok {
			http.NotFound(w, r)
			return
		}

		episodes := append([]*catalogv1.Episode(nil), idx.Episodes(s.UID)...)
		sortEpisodes(episodes)
		items := make([]Metadata, len(episodes))
		for i, e := range episodes {
			items[i] = buildEpisodeMetadata(root, h.opts.ExternalURL, s, e)
		}
		h.writePage(w, root, items, parsePaging(r))
	}
}

// childrenOf resolves ratingKey to a show (whose children are its seasons)
// or a season (whose children are its episodes), building each as a full
// Metadata object with no nested Children of its own -- /children is itself
// the paged listing; a season's own Children block
// ([buildSeasonChildren]/[buildEpisodeChildren]) is for the unpaged,
// includeChildren=1 case on GET .../{ratingKey} instead.
func (h *handler) childrenOf(root rootDef, idx *projection.Index, ratingKey string) ([]Metadata, bool) {
	uid, season, isSeason, ok := ParseRatingKey(ratingKey)
	if !ok {
		return nil, false
	}

	if isSeason {
		s, ok := idx.SeriesByUID(uid)
		if !ok {
			return nil, false
		}
		var episodes []*catalogv1.Episode
		for _, e := range idx.Episodes(s.UID) {
			if e.Spec.SeasonNumber == season {
				episodes = append(episodes, e)
			}
		}
		sortEpisodes(episodes)
		out := make([]Metadata, len(episodes))
		for i, e := range episodes {
			out[i] = buildEpisodeMetadata(root, h.opts.ExternalURL, s, e)
		}
		return out, true
	}

	s, ok := idx.SeriesByUID(uid)
	if !ok {
		return nil, false
	}
	seasons := append([]catalogv1.SeasonStatus(nil), s.Status.Seasons...)
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].Number < seasons[j].Number })
	out := make([]Metadata, 0, len(seasons))
	for _, season := range seasons {
		md, ok := buildSeasonMetadata(root, h.opts.ExternalURL, s, season.Number, idx, false)
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
func (h *handler) writePage(w http.ResponseWriter, root rootDef, items []Metadata, p pageRequest) {
	window, total := windowMetadata(items, p)
	window = nonNilMetadata(window)
	setPagingHeaders(w, p.start, total)
	writeJSON(w, http.StatusOK, metadataContainerResponse{MediaContainer: MetadataContainer{
		Offset:     p.start,
		TotalSize:  total,
		Identifier: root.identifier,
		Size:       len(window),
		Metadata:   window,
	}})
}
