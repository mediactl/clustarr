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
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// handleAggregate is "GET /search/api": a read against indexarr's
// already-merged local release index via Config.Query, not a live fan-out
// across every indexer on every request. See doc.go.
func (s *Server) handleAggregate(w http.ResponseWriter, r *http.Request) {
	switch t := r.URL.Query().Get("t"); t {
	case "caps":
		s.writeCaps(r.Context(), w, aggregateCaps())
	case "search", "tvsearch", "movie", "music", "audio", "book":
		s.handleAggregateSearch(w, r)
	case "":
		s.writeTorznabError(w, http.StatusBadRequest, torznab.ErrMissingParameter, "t is required")
	default:
		s.writeTorznabError(w, http.StatusBadRequest, torznab.ErrNoSuchFunction, fmt.Sprintf("unknown function %q", t))
	}
}

// aggregateCaps is a static caps document for /search/api: it fronts every
// enabled indexer's already-merged local index rather than one indexer with
// its own probed capabilities, so there is no per-indexer status.caps to
// read here the way capsFromIndexer reads one. Raw free-text search is the
// one thing every mode actually does (see handleAggregateSearch), so it is
// the one mode advertised as available; the standard Newznab category tree
// (pkg/newznab.Tree) is offered for a client that wants to narrow by `cat`.
func aggregateCaps() torznab.Caps {
	return torznab.Caps{
		ServerTitle: "clustarr",
		Modes: map[torznab.SearchMode]torznab.Searching{
			// SearchEngine: "raw" is caps.go's own wire flag for "accepts
			// free-text q" (Searching's doc comment), which is exactly
			// indexarr/query's whole capability.
			torznab.ModeSearch: {Available: true, SupportedParams: []string{"q"}, SearchEngine: "raw"},
		},
		Categories: newznab.Tree(),
	}
}

// handleAggregateSearch answers every t= mode the same way: a text query
// (plus `cat`, the one Torznab-standard param indexarr/query's own filter
// vocabulary already understands -- indexarr/query/filters.go's filterKeys)
// against Config.Query. Season/ep/imdbid and the rest of the Jackett filter
// grammar §6.2 names as deferred are not translated here.
func (s *Server) handleAggregateSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.SearchTimeout)
	defer cancel()

	q := r.URL.Query()
	req := schema.QueryRequest{
		Text:  q.Get("q"),
		Limit: int32(parseIntDefault(q.Get("limit"), 0)),
	}
	if offset, err := strconv.Atoi(q.Get("offset")); err == nil && offset > 0 {
		req.Offset = int32(offset)
	}
	if cat := q.Get("cat"); cat != "" {
		req.Filters = map[string]string{"category": cat}
	}

	resp := s.cfg.Query(ctx, req)
	if resp.Error != "" {
		s.writeTorznabError(w, http.StatusBadRequest, torznab.ErrIncorrectParameter, resp.Error)
		return
	}
	s.writeResults(r.Context(), w, resp.Releases)
}
