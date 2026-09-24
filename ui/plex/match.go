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
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/ui/projection"
)

// matchRequest is a match POST's body (research §4). Every field research
// marks optional is left `omitempty` on decode by simply being the Go zero
// when absent -- Index and ParentIndex are pointers because a request may
// legitimately mean "season/episode 0", which the zero value cannot be told
// apart from "absent" on a plain int.
type matchRequest struct {
	Type             int    `json:"type"`
	Title            string `json:"title,omitempty"`
	ParentTitle      string `json:"parentTitle,omitempty"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
	IncludeChildren  int    `json:"includeChildren,omitempty"`
	EpisodeOrder     string `json:"episodeOrder,omitempty"`
	Year             int32  `json:"year,omitempty"`
	Guid             string `json:"guid,omitempty"`
	Index            *int32 `json:"index,omitempty"`
	ParentIndex      *int32 `json:"parentIndex,omitempty"`
	Filename         string `json:"filename,omitempty"`
	Date             string `json:"date,omitempty"`
	Manual           int    `json:"manual,omitempty"`
	IncludeAdult     int    `json:"includeAdult,omitempty"`
}

// handleMatch answers POST {match key}/matches (spec §D.2, §D.4). No match
// answers 200 with an empty Metadata array, never 404: research §4's return
// codes reserve 404 for an unknown ratingKey on the metadata route, and
// "Plex falls through to the next provider in the agent" (spec §D.4) only
// works if this provider answers cleanly rather than erroring.
func (h *handler) handleMatch(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req matchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed match request body")
			return
		}

		idx, ok := h.index(w, r)
		if !ok {
			return
		}

		results := nonNilMetadata(h.match(root, idx, req))
		writeJSON(w, http.StatusOK, metadataContainerResponse{MediaContainer: MetadataContainer{
			Offset:     0,
			TotalSize:  len(results),
			Identifier: root.identifier,
			Size:       len(results),
			Metadata:   results,
		}})
	}
}

// match dispatches a decoded match request by its numeric type (research
// §4's table), building every result as a full Metadata object (spec §D.4:
// "results are full Metadata objects").
func (h *handler) match(root rootDef, idx *projection.Index, req matchRequest) []Metadata {
	if !root.declares(req.Type) {
		// A type another root declares (a show asked of the movies root):
		// no match, the same empty container as an unknown title.
		return nil
	}
	includeChildren := req.IncludeChildren == 1
	manual := req.Manual == 1

	switch req.Type {
	case typeMovie:
		movies := matchMovies(idx, req, manual)
		out := make([]Metadata, len(movies))
		for i, m := range movies {
			out[i] = buildMovieMetadata(root, h.opts.ExternalURL, m)
		}
		return out

	case typeShow:
		shows := matchShows(idx, req.Title, req.Year, req.Guid, manual, !manual)
		out := make([]Metadata, len(shows))
		for i, s := range shows {
			out[i] = buildShowMetadata(root, h.opts.ExternalURL, s, idx, includeChildren)
		}
		return out

	case typeSeason:
		s := resolveShow(idx, req.ParentTitle, req.Year, req.Guid)
		if s == nil || req.Index == nil {
			return nil
		}
		md, ok := buildSeasonMetadata(root, h.opts.ExternalURL, s, *req.Index, idx, includeChildren)
		if !ok {
			return nil
		}
		return []Metadata{md}

	case typeEpisode:
		s := resolveShow(idx, req.GrandparentTitle, req.Year, req.Guid)
		if s == nil {
			return nil
		}
		e := resolveEpisode(idx.Episodes(s.UID), req)
		if e == nil {
			return nil
		}
		return []Metadata{buildEpisodeMetadata(root, h.opts.ExternalURL, s, e)}

	default:
		return nil
	}
}

// resolveShow finds the show a season or episode match request names (D.4
// rule 3: "resolve their show by 1 or 2"): by guid first, else by title and
// year, best match only (a season or episode match never returns more than
// one show's worth of results).
func resolveShow(idx *projection.Index, title string, year int32, guid string) *catalogv1.Series {
	if guid != "" {
		if s, ok := showByGuid(idx, guid); ok {
			return s
		}
	}
	shows := matchShows(idx, title, year, "", false, false)
	if len(shows) == 0 {
		return nil
	}
	return shows[0]
}

// resolveEpisode finds one episode among a show's episodes (D.4 rule 3):
// by parentIndex (season) and index (episode number) when both are given,
// else by date against airDate.
func resolveEpisode(episodes []*catalogv1.Episode, req matchRequest) *catalogv1.Episode {
	if req.Index != nil && req.ParentIndex != nil {
		for _, e := range episodes {
			if e.Spec.SeasonNumber == *req.ParentIndex && e.Spec.EpisodeNumber == *req.Index {
				return e
			}
		}
		return nil
	}
	if req.Date != "" {
		for _, e := range episodes {
			if e.Status.AirDate != nil && e.Status.AirDate.UTC().Format("2006-01-02") == req.Date {
				return e
			}
		}
	}
	return nil
}

// showByGuid resolves a "tmdb://", "tvdb://" or "imdb://" guid against a
// Series (D.4 rule 1).
func showByGuid(idx *projection.Index, guid string) (*catalogv1.Series, bool) {
	scheme, id, ok := splitGuid(guid)
	if !ok {
		return nil, false
	}
	switch scheme {
	case "tvdb":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, false
		}
		return idx.ByTVDB(n)
	case "imdb":
		obj, ok := idx.ByIMDb(commonv1.MediaKindSeries, id)
		if !ok {
			return nil, false
		}
		s, ok := obj.(*catalogv1.Series)
		return s, ok
	default:
		return nil, false
	}
}

// movieByGuid resolves a "tmdb://", "tvdb://" or "imdb://" guid against a
// Movie (D.4 rule 1). tvdb never matches a movie -- kept for symmetry with
// showByGuid and because an unmatched scheme cleanly falls through to
// title matching rather than erroring.
func movieByGuid(idx *projection.Index, guid string) (*catalogv1.Movie, bool) {
	scheme, id, ok := splitGuid(guid)
	if !ok {
		return nil, false
	}
	switch scheme {
	case "tmdb":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, false
		}
		obj, ok := idx.ByTMDB(commonv1.MediaKindMovie, n)
		if !ok {
			return nil, false
		}
		m, ok := obj.(*catalogv1.Movie)
		return m, ok
	case "imdb":
		obj, ok := idx.ByIMDb(commonv1.MediaKindMovie, id)
		if !ok {
			return nil, false
		}
		m, ok := obj.(*catalogv1.Movie)
		return m, ok
	default:
		return nil, false
	}
}

// splitGuid splits "scheme://id" into its two parts.
func splitGuid(guid string) (scheme, id string, ok bool) {
	parts := strings.SplitN(guid, "://", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// matchMovies is the movie half of D.4: guid first (rule 1), else a
// title/alternate-title scan ordered exact-year first (rule 2).
func matchMovies(idx *projection.Index, req matchRequest, manual bool) []*catalogv1.Movie {
	if req.Guid != "" {
		if m, ok := movieByGuid(idx, req.Guid); ok {
			return []*catalogv1.Movie{m}
		}
	}
	return matchMoviesByTitle(idx, req.Title, req.Year, manual)
}

// matchMoviesByTitle is D.4 rule 2 for movies. An automatic match (manual
// absent) that names a year takes only an exact or ±1 year: the "any year"
// tier bound an automatic Dune (2021) request to Dune (1984) whenever the
// 2021 film was not in the catalogue, and Plex applies an automatic match
// without asking. The any-year tier stays for a request with no year, which
// cannot be ranked by one, and for manual=1, where Plex shows the user a
// ranked list to choose from.
func matchMoviesByTitle(idx *projection.Index, title string, year int32, manual bool) []*catalogv1.Movie {
	if title == "" {
		return nil
	}
	norm := release.TitleNorm(title)
	strict := !manual

	type candidate struct {
		movie *catalogv1.Movie
		tier  int
	}
	var candidates []candidate
	for _, m := range idx.Movies() {
		meta := m.Status.Metadata
		if meta == nil {
			continue
		}
		if !titleMatches(norm, meta.Title, meta.AlternateTitles) {
			continue
		}
		tier := yearTier(meta.Year, year)
		if !admitsTier(tier, year, strict) {
			continue
		}
		candidates = append(candidates, candidate{movie: m, tier: tier})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].tier != candidates[j].tier {
			return candidates[i].tier < candidates[j].tier
		}
		return movieTitle(candidates[i].movie) < movieTitle(candidates[j].movie)
	})

	if len(candidates) == 0 {
		return nil
	}
	n := 1
	if manual {
		n = min(5, len(candidates))
	}
	out := make([]*catalogv1.Movie, n)
	for i := range n {
		out[i] = candidates[i].movie
	}
	return out
}

func movieTitle(m *catalogv1.Movie) string {
	if m.Status.Metadata == nil {
		return ""
	}
	return m.Status.Metadata.Title
}

// matchShows is the show half of D.4, matchMovies' twin, with the same
// year rule: strict (exact or ±1 only, when a year is given) for an
// automatic type-2 match, the any-year tier kept for manual=1.
//
// strictYear is separate from manual because rule 3 resolves a season's or
// an episode's show through here too (resolveShow), and there the request's
// year is the season's or episode's own release year (research §4: "release
// year"), not the show's -- season 5 of a 2015 show arrives with 2020. It
// may order candidates but must never exclude the show.
func matchShows(idx *projection.Index, title string, year int32, guid string, manual, strictYear bool) []*catalogv1.Series {
	if guid != "" {
		if s, ok := showByGuid(idx, guid); ok {
			return []*catalogv1.Series{s}
		}
	}
	if title == "" {
		return nil
	}
	norm := release.TitleNorm(title)

	type candidate struct {
		series *catalogv1.Series
		tier   int
	}
	var candidates []candidate
	for _, s := range idx.AllSeries() {
		meta := s.Status.Metadata
		if meta == nil {
			continue
		}
		alts := make([]string, len(meta.AlternateTitles))
		for i, a := range meta.AlternateTitles {
			alts[i] = a.Title
		}
		if !titleMatches(norm, meta.Title, alts) {
			continue
		}
		tier := yearTier(meta.Year, year)
		if !admitsTier(tier, year, strictYear) {
			continue
		}
		candidates = append(candidates, candidate{series: s, tier: tier})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].tier != candidates[j].tier {
			return candidates[i].tier < candidates[j].tier
		}
		return seriesTitle(candidates[i].series) < seriesTitle(candidates[j].series)
	})

	if len(candidates) == 0 {
		return nil
	}
	n := 1
	if manual {
		n = min(5, len(candidates))
	}
	out := make([]*catalogv1.Series, n)
	for i := range n {
		out[i] = candidates[i].series
	}
	return out
}

func seriesTitle(s *catalogv1.Series) string {
	if s.Status.Metadata == nil {
		return ""
	}
	return s.Status.Metadata.Title
}

// titleMatches reports whether norm (already release.TitleNorm-ed) equals
// the normalised title or any normalised alternate title (D.4 rule 2).
func titleMatches(norm, title string, alternates []string) bool {
	if release.TitleNorm(title) == norm {
		return true
	}
	for _, alt := range alternates {
		if release.TitleNorm(alt) == norm {
			return true
		}
	}
	return false
}

// admitsTier reports whether a candidate in tier may be returned at all:
// always, unless strict and the request named a year, when only the exact
// (0) and ±1 (1) tiers are -- D.4's "then any when no year was given".
func admitsTier(tier int, requestedYear int32, strict bool) bool {
	return !strict || requestedYear == 0 || tier < 2
}

// yearTier ranks a candidate's own year against the requested one (D.4 rule
// 2): 0 exact, 1 within one year, 2 any -- including every case where the
// request gave no year at all, which cannot be ranked by proximity and so
// always falls into the catch-all tier.
func yearTier(candidateYear, requestedYear int32) int {
	if requestedYear == 0 {
		return 2
	}
	switch candidateYear {
	case requestedYear:
		return 0
	case requestedYear - 1, requestedYear + 1:
		return 1
	default:
		return 2
	}
}
