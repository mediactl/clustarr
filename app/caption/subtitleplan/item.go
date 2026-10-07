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

package subtitleplan

import (
	"strconv"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ItemFromMovie is a movie file's Item: its TMDB and IMDb ids, title, year
// and original language. A nil Movie is gone (Exists false).
func ItemFromMovie(m *catalogv1alpha1.Movie) Item {
	if m == nil {
		return Item{}
	}
	it := Item{Exists: true, Query: schema.SubtitleQuery{Kind: commonv1.MediaKindMovie, IDs: map[string]string{}}}
	if m.Spec.TmdbID > 0 {
		it.Query.IDs["tmdb"] = strconv.FormatInt(m.Spec.TmdbID, 10)
	}
	if md := m.Status.Metadata; md != nil {
		it.Query.Title, it.Query.Year = md.Title, md.Year
		setID(it.Query.IDs, "imdb", imdbNumber(md.ExternalIDs["imdb"]))
		it.OriginalLanguage = md.OriginalLanguage
	}
	return it
}

// ItemFromEpisode is an episode file's Item: its season and episode, and
// from its Series the show's ids (as parent_imdb and parent_tmdb, the
// pkg/subtitles.Query keys for a show's own ids, and tvdb), title, year and
// original language. A nil Episode is gone; a nil Series leaves the parent
// ids, the title and the language empty -- a hash-only search is still a
// real search.
func ItemFromEpisode(ep *catalogv1alpha1.Episode, s *catalogv1alpha1.Series) Item {
	if ep == nil {
		return Item{}
	}
	it := Item{Exists: true, Query: schema.SubtitleQuery{
		Kind: commonv1.MediaKindEpisode, IDs: map[string]string{},
		Season: ep.Spec.SeasonNumber, Episode: ep.Spec.EpisodeNumber,
	}}
	if s == nil {
		return it
	}
	if s.Spec.TvdbID > 0 {
		it.Query.IDs["tvdb"] = strconv.FormatInt(s.Spec.TvdbID, 10)
	}
	if md := s.Status.Metadata; md != nil {
		it.Query.Title, it.Query.Year = md.Title, md.Year
		setID(it.Query.IDs, "parent_imdb", imdbNumber(md.ExternalIDs["imdb"]))
		setID(it.Query.IDs, "parent_tmdb", md.ExternalIDs["tmdb"])
		it.OriginalLanguage = md.OriginalLanguage
	}
	return it
}

// imdbNumber strips an IMDb id to the bare number OpenSubtitles expects: no
// "tt" prefix, no leading zeros. A value that is not an IMDb id at all is
// dropped rather than passed through.
func imdbNumber(id string) string {
	n := strings.TrimLeft(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "tt"), "0")
	if n == "" {
		return ""
	}
	if _, err := strconv.ParseUint(n, 10, 64); err != nil {
		return ""
	}
	return n
}

func setID(ids map[string]string, key, val string) {
	if val != "" {
		ids[key] = val
	}
}
