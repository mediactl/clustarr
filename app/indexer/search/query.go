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

package search

import (
	"strconv"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// modeFor maps the request's media kind onto the Torznab t= mode.
//
// Ruling R5: these are torznab.SearchMode's real wire values, which is what
// indexer.SupportsMode compares Caps.Modes' keys against. The CRD's doc
// comment said "tv-search"/"movie-search" until D1-0 corrected it, and there
// is no enum marker on the map key, so a wrong string here would make the
// caps gate match nothing: the service would search no indexer at all while
// reporting a tidy list of "does not support mode ..." outcomes.
func modeFor(kind commonv1.MediaKind) torznab.SearchMode {
	switch kind {
	case commonv1.MediaKindMovie:
		return torznab.ModeMovieSearch
	case commonv1.MediaKindEpisode:
		return torznab.ModeTVSearch
	default:
		return torznab.ModeSearch
	}
}

// paramSupported reports whether the indexer advertises param for mode, e.g.
// "imdbid" under "movie".
//
// Caps.Modes is map[string][]string on the CRD -- mode -> supported query
// parameters -- and a nil Caps means "not probed yet", which is not the same
// as "not supported". Callers skip an unprobed indexer with its own reason
// rather than guessing; see selectCandidates.
func paramSupported(caps *indexv1alpha1.Caps, mode torznab.SearchMode, param string) bool {
	if caps == nil {
		return false
	}
	for _, p := range caps.Modes[string(mode)] {
		if p == param {
			return true
		}
	}
	return false
}

// queryCategories intersects the requested Newznab ids with what the indexer
// advertises.
//
// A requested id survives when the indexer serves it OR any of its children,
// so a parent-only request ([2000], which is exactly what
// catalogarr's BuildSearchRequest sends by default) stays [2000] instead of
// fanning out to fifty leaves: the query string stays short and the indexer
// expands the parent itself.
//
// An indexer with no advertised tree gets the request verbatim. Absence of
// caps is not evidence of absence of support, and a caps probe that has not
// run yet must not silently narrow every search to nothing.
func queryCategories(requested []int32, caps *indexv1alpha1.Caps) []newznab.CategoryID {
	out := make([]newznab.CategoryID, 0, len(requested))
	if caps == nil || len(caps.Categories) == 0 {
		for _, id := range requested {
			out = append(out, newznab.CategoryID(id))
		}
		return out
	}
	served := make(map[int32]struct{}, len(caps.Categories)*4)
	for _, c := range caps.Categories {
		served[c.ID] = struct{}{}
		for _, s := range c.Sub {
			served[s.ID] = struct{}{}
		}
	}
	for _, id := range requested {
		if _, ok := served[id]; ok {
			out = append(out, newznab.CategoryID(id))
			continue
		}
		// The indexer may advertise only leaves. A requested parent survives
		// when any served id rolls up to it.
		for sub := range served {
			if int32(newznab.CategoryID(sub).Parent()) == id {
				out = append(out, newznab.CategoryID(id))
				break
			}
		}
	}
	return out
}

// buildQuery renders one SearchRequest against one Indexer's caps. ok is
// false when the indexer advertises the mode but none of the request's id
// parameters and the request carries no text to fall back on.
//
// Ids are preferred wherever the indexer supports them: queryMode is only
// ever SearchQueryModeText when NONE of imdbid/tmdbid/tvdbid matched, so an
// indexer that supports (say) tmdbid but not imdbid still gets a pure id
// query when the request has a usable tmdbid, even if req.Text is also set --
// a title query is strictly less precise than a server-side id match, and
// mixing "q=" into an id-keyed request risks narrowing an exact match with a
// noisy keyword filter on indexers that AND the two together.
//
// G1-6 closed the gap this comment used to describe (catalogarr never set
// Text and the payload "had no field for" a resolved title): both were
// wrong. schema.SearchRequest.Text has carried a free-text query since M0
// (pkg/events/schema/index.go); the gap was app/catalog/worker/search never
// having a resolved title to put there. BuildSearchRequest now renders one
// from status.metadata via TargetIDs.Title, so Text is populated for the
// Torznab facade, an interactive Search AND an automatic search alike, and
// no new schema version was needed for an already-existing, already-optional
// field.
//
// Anime arrives as an absolute number in Episode with Season nil and no
// flag: catalogarr's BuildSearchRequest drops the season for an anime series
// and puts the absolute number in Episode. That shape is the only signal on
// the wire, so it is the inference -- an episode request with Episode set
// and Season nil also searches the indexer's spec.animeCategories, which
// newznab.ByKind cannot reach.
func buildQuery(
	req schema.SearchRequest,
	idx *indexv1alpha1.Indexer,
	mode torznab.SearchMode,
	limit int,
) (q torznab.Query, ok bool, queryMode schema.SearchQueryMode) {
	q = torznab.Query{Type: mode, Limit: limit}
	caps := idx.Status.Caps

	cats := queryCategories(req.Categories, caps)
	if isAnimeRequest(req) {
		for _, c := range idx.Spec.AnimeCategories {
			cats = append(cats, newznab.CategoryID(c))
		}
	}
	q.Categories = cats

	var anyID bool
	if v := req.IDs[commonv1.IDKeyIMDB]; v != "" && paramSupported(caps, mode, "imdbid") {
		q.IMDBID, anyID = v, true
	}
	if v := req.IDs[commonv1.IDKeyTMDB]; v != "" && paramSupported(caps, mode, "tmdbid") {
		q.TMDBID, anyID = v, true
	}
	if v := req.IDs[commonv1.IDKeyTVDB]; v != "" && paramSupported(caps, mode, "tvdbid") {
		q.TVDBID, anyID = v, true
	}
	if req.Season != nil && paramSupported(caps, mode, "season") {
		s := int(*req.Season)
		q.Season = &s
	}
	if req.Episode != nil && paramSupported(caps, mode, "ep") {
		q.Episode = strconv.Itoa(int(*req.Episode))
	}
	if anyID {
		return q, true, schema.SearchQueryModeID
	}
	if req.Text != "" {
		q.Q = req.Text
		return q, true, schema.SearchQueryModeText
	}
	return q, false, ""
}

// isAnimeRequest reports the absolute-numbering shape: an episode request
// with an episode number and no season.
func isAnimeRequest(req schema.SearchRequest) bool {
	return req.Kind == commonv1.MediaKindEpisode && req.Episode != nil && req.Season == nil
}
