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
// id, which matches the id alone. Since 2026-11-10 (design §9.1) it also
// takes the non-video providers' names -- comicvine, mangadex and metron
// for a comic, asin (or audible) for an audiobook, olid (or openlibrary)
// for a book or an author -- beside mbid (or musicbrainz) for an artist.
var providerIDQuery = regexp.MustCompile(`^(?i)(tmdb|tvdb|imdb|mbid|musicbrainz|olid|openlibrary|comicvine|mangadex|metron|asin|audible)\s*:\s*(\S+)$`)

// providerAliases map a query's provider name onto the prefix a prefixed
// ProviderID carries, so "audible:B0036I54I6" finds the audiobook whose
// ProviderID is "asin:B0036I54I6".
var providerAliases = map[string]string{"musicbrainz": "mbid", "openlibrary": ProviderIDOpenLibrary, "audible": ProviderIDASIN}

// Find is the monitored items whose title or provider id matches q, best
// first, at most limit of them. A prefixed id ("tmdb:603") matches the id
// only; a bare query matches an equal provider id first, then titles,
// ranked exact, prefix, word prefix, anywhere, with ties to the shorter and
// then the alphabetically earlier title. An unmonitored item is never
// found, and a query with nothing to match finds nothing.
//
// A ProviderID that carries its provider ("comicvine:4050-18166",
// "asin:B0036I54I6", "olid:OL45883W"; [describeLibraryItem]) is matched by
// its bare id, and by a prefixed query only when the query's provider is
// its own, so "mangadex:4050-18166" never finds a ComicVine volume; a
// bare ProviderID (a movie's TMDB id) is matched by any provider's prefix,
// as before.
func Find(items []LibraryItem, q string, limit int) []LibraryItem {
	q = strings.TrimSpace(q)
	idOnly := false
	id, provider := q, ""
	if m := providerIDQuery.FindStringSubmatch(q); m != nil {
		idOnly, id, provider = true, m[2], strings.ToLower(m[1])
		if alias, ok := providerAliases[provider]; ok {
			provider = alias
		}
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
		case matchesProviderID(it.ProviderID, provider, id):
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

// matchesProviderID reports whether an item's ProviderID is the id the
// query names, under provider when the query named one ("" for a bare
// query): a prefixed ProviderID must carry that provider, a bare one is
// any provider's.
func matchesProviderID(providerID, provider, id string) bool {
	if providerID == "" {
		return false
	}
	prefix, bare, prefixed := strings.Cut(providerID, ":")
	if !prefixed {
		return strings.EqualFold(providerID, id)
	}
	if strings.EqualFold(providerID, id) {
		// The whole "asin:B0036I54I6" typed as a bare query.
		return true
	}
	return strings.EqualFold(bare, id) && (provider == "" || strings.EqualFold(prefix, provider))
}
