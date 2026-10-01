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

package projection

import (
	"regexp"
	"sort"
	"strings"

	"github.com/mediactl/clustarr/pkg/release"
)

// The library's typeahead (2026-10-01, after Sonarr's and Radarr's header
// search): a few characters find a monitored item of the tab by title, and
// the reader jumps to its page. Titles go through release.TitleNorm, the
// release index's normaliser, so case, accents, punctuation and a leading
// article do not matter and a title in any script is found in its own.

// Match ranks, best first.
const (
	rankID       = iota // the item's provider id
	rankExact           // the whole title
	rankPrefix          // the title starts with the query
	rankWord            // a later word of the title starts with it
	rankAnywhere        // the title contains it
)

// providerIDQuery is Sonarr's "tvdb:81189" form: a provider name and an
// id, which matches the id alone.
var providerIDQuery = regexp.MustCompile(`^(?i)(tmdb|tvdb|imdb|mbid|musicbrainz|olid|openlibrary)\s*:\s*(\S+)$`)

// Find is the monitored items whose title or provider id matches q, best
// first, at most limit of them. A prefixed id ("tmdb:603") matches the id
// only; a bare query matches an equal provider id first, then titles,
// ranked exact, prefix, word prefix, anywhere, with ties to the shorter and
// then the alphabetically earlier title. An unmonitored item is never
// found, and a query with nothing to match finds nothing.
func Find(items []LibraryItem, q string, limit int) []LibraryItem {
	q = strings.TrimSpace(q)
	idOnly := false
	id := q
	if m := providerIDQuery.FindStringSubmatch(q); m != nil {
		idOnly, id = true, m[2]
	}
	norm := release.TitleNorm(q)
	if !idOnly && norm == "" {
		return nil
	}

	type hit struct {
		item LibraryItem
		rank int
		key  string
	}
	var hits []hit
	for _, it := range items {
		if !it.Monitored {
			continue
		}
		title := release.TitleNorm(it.Title)
		rank := -1
		switch {
		case it.ProviderID != "" && strings.EqualFold(it.ProviderID, id):
			rank = rankID
		case idOnly:
		case title == norm:
			rank = rankExact
		case strings.HasPrefix(title, norm):
			rank = rankPrefix
		case strings.Contains(" "+title, " "+norm):
			rank = rankWord
		case strings.Contains(title, norm):
			rank = rankAnywhere
		}
		if rank >= 0 {
			hits = append(hits, hit{item: it, rank: rank, key: title})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if len(a.key) != len(b.key) {
			return len(a.key) < len(b.key)
		}
		return a.key < b.key
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]LibraryItem, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.item)
	}
	return out
}
