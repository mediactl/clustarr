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
	"net/url"
	"strconv"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// handleIndexerAPI is "GET /{indexer}/api": t=caps from the Indexer's own
// status, or a live search scoped to it.
func (s *Server) handleIndexerAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("indexer")
	idx, err := s.resolveIndexer(ctx, name)
	if err != nil {
		s.writeLookupError(ctx, w, name, err)
		return
	}
	if !indexerEnabled(idx) {
		// Disabled means "the operator turned it off without deleting it"
		// (IndexerSpec.Enabled's own doc comment); a Torznab client asking
		// this facade for it gets the same answer as asking for one that
		// does not exist, not a caps document promising searches that will
		// never run.
		http.Error(w, "facade: no such indexer", http.StatusNotFound)
		return
	}

	switch t := r.URL.Query().Get("t"); t {
	case "caps":
		s.writeCaps(ctx, w, capsFromIndexer(idx))
	case "search", "tvsearch", "movie", "music", "audio", "book":
		s.handleIndexerSearch(w, r, idx, torznab.SearchMode(t))
	case "":
		s.writeTorznabError(w, http.StatusBadRequest, torznab.ErrMissingParameter, "t is required")
	default:
		s.writeTorznabError(w, http.StatusBadRequest, torznab.ErrNoSuchFunction, fmt.Sprintf("unknown function %q", t))
	}
}

// handleIndexerSearch runs a LIVE federated search scoped to exactly one
// Indexer via Config.Search -- clustarr.rpc.indexarr.search's own body,
// called in-process (see doc.go). SearchRequest.Text is set from q, the
// same field catalogarr/worker/search's BuildSearchRequest fills from the
// item's resolved title (G1-6); buildQuery still prefers ids wherever the
// indexer supports one.
func (s *Server) handleIndexerSearch(w http.ResponseWriter, r *http.Request, idx *indexv1alpha1.Indexer, mode torznab.SearchMode) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.SearchTimeout)
	defer cancel()

	req := buildSearchRequest(r.URL.Query(), mode, s.cfg.SearchTimeout)
	req.Namespace = idx.Namespace
	req.IndexerRefs = []schema.Ref{{Namespace: idx.Namespace, Name: idx.Name}}

	resp := s.cfg.Search(ctx, req)
	s.writeResults(r.Context(), w, resp.Releases)
}

// buildSearchRequest translates a Torznab query string into a
// schema.SearchRequest. Every field it can fill from the wire is filled;
// param names follow docs/research/naming.md's verified Torznab table
// (q, cat, limit, imdbid, tmdbid, tvdbid, season, ep).
func buildSearchRequest(q url.Values, mode torznab.SearchMode, timeout time.Duration) schema.SearchRequest {
	req := schema.SearchRequest{
		Kind:           kindForMode(mode),
		Text:           q.Get("q"),
		Categories:     parseInt32List(q.Get("cat")),
		Limit:          int32(parseIntDefault(q.Get("limit"), 0)),
		DeadlineMillis: timeout.Milliseconds(),
		// A facade call is, by construction, someone deliberately asking
		// right now -- there is no automatic/cron caller behind this route
		// -- so it is scored the same as catalogarr's own interactive
		// Search: EnableInteractiveSearch gates candidate selection rather
		// than EnableAutomaticSearch.
		UserInvoked: true,
	}

	ids := map[string]string{}
	if v := normalizeIMDBID(q.Get("imdbid")); v != "" {
		ids[commonv1.IDKeyIMDB] = v
	}
	if v := q.Get("tmdbid"); v != "" {
		ids[commonv1.IDKeyTMDB] = v
	}
	if v := q.Get("tvdbid"); v != "" {
		ids[commonv1.IDKeyTVDB] = v
	}
	if len(ids) > 0 {
		req.IDs = ids
	}

	if n, ok := leadingInt(q.Get("season")); ok {
		s32 := int32(n)
		req.Season = &s32
	}
	if n, ok := leadingInt(q.Get("ep")); ok {
		e32 := int32(n)
		req.Episode = &e32
	}
	return req
}

// kindForMode maps a Torznab t= mode onto the MediaKind
// indexarr/search/query.go's modeFor reads back out to pick the wire search
// mode (movie<->MediaKindMovie, tvsearch<->MediaKindEpisode). music/audio/
// search have no MediaKind of their own and fall back to modeFor's default
// (ModeSearch), same as every kind modeFor does not special-case -- that
// mapping belongs to indexarr/search, not duplicated here.
func kindForMode(mode torznab.SearchMode) commonv1.MediaKind {
	switch mode {
	case torznab.ModeMovieSearch:
		return commonv1.MediaKindMovie
	case torznab.ModeTVSearch:
		return commonv1.MediaKindEpisode
	case torznab.ModeBookSearch:
		return commonv1.MediaKindBook
	default:
		return ""
	}
}

// normalizeIMDBID adds the canonical "tt" prefix torznab.Release.IDs and
// commonv1.ReleaseInfo.IDs both expect (see torznab/release.go's Release.IDs
// doc comment) when a client sent the bare numeric id, which Sonarr/Radarr
// -alikes routinely do.
func normalizeIMDBID(v string) string {
	if v == "" || strings.HasPrefix(v, "tt") {
		return v
	}
	if _, err := strconv.ParseUint(v, 10, 64); err != nil {
		return v
	}
	return "tt" + v
}

// parseInt32List splits a comma-separated list of Newznab category ids,
// silently dropping anything that does not parse -- the same
// graceful-degradation pkg/torznab's own wire parsers use for a malformed
// value, rather than failing the whole request over one bad token.
func parseInt32List(s string) []int32 {
	if s == "" {
		return nil
	}
	var out []int32
	for tok := range strings.SplitSeq(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if n, err := strconv.ParseInt(tok, 10, 32); err == nil {
			out = append(out, int32(n))
		}
	}
	return out
}

// parseIntDefault parses s as a base-10 int, returning def on any failure
// (blank, non-numeric, out of range).
func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// leadingInt reads the leading base-10 integer off s, so a range like "5-6"
// (a season/episode pack, which some Torznab clients send) still yields a
// usable single value instead of failing to parse at all. SearchRequest has
// no way to express a range.
func leadingInt(s string) (int, bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}
