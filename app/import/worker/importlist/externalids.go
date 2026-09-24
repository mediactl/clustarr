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

package importlist

import (
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// ToMetadataExternalIDs converts a pkg/importlist.ExternalIDs (a provider's
// typed crosswalk) into a pkg/metadata.ExternalIDs (a plain
// map[string]string keyed by metadata's short source tags), the conversion
// pkg/importlist/types.go:27-32 names this task as the owner of. Only
// non-empty fields are carried across; metadata.ExternalIDs round-trips
// through JSON onto a catalog item's own externalIDs map, so an empty
// string here would otherwise show up as a spurious key with no value.
//
// MusicBrainz is carried as metadata.KeyMBReleaseGroup rather than
// KeyMBArtist or KeyMBRelease: pkg/importlist.ExternalIDs has one generic
// MusicBrainz field because a list provider (Trakt, MDBList, ...) exposes
// one MBID per item, and an import list's only MusicBrainz-identified kind
// is album (G2 work, not reachable from this task's movie/series path) --
// release-group is the MBID a list entry for an album would carry. ISBN
// becomes KeyISBN13: every provider that emits an ISBN on a list entry
// emits ISBN-13, the modern standard: KeyIMDb, KeyTMDB, KeyTVDB, KeyASIN,
// KeyComicVine and KeyAniList need no such judgement call, since
// pkg/metadata has exactly one key for each.
func ToMetadataExternalIDs(ids importlist.ExternalIDs) metadata.ExternalIDs {
	out := make(metadata.ExternalIDs, 8)
	set := func(key, value string) {
		if value != "" {
			out[key] = value
		}
	}
	set(metadata.KeyIMDb, ids.IMDb)
	set(metadata.KeyTMDB, ids.TMDB)
	set(metadata.KeyTVDB, ids.TVDB)
	set(metadata.KeyMBReleaseGroup, ids.MusicBrainz)
	set(metadata.KeyISBN13, ids.ISBN)
	set(metadata.KeyASIN, ids.ASIN)
	set(metadata.KeyComicVine, ids.ComicVine)
	set(metadata.KeyAniList, ids.AniList)
	return out
}

// FromMetadataExternalIDs is ToMetadataExternalIDs' inverse: it reads the
// metadata keys this package's own ToMetadataExternalIDs writes (plus the
// gateway's resolve response, which uses the same key vocabulary) back into
// an importlist.ExternalIDs. A key metadata.ExternalIDs carries that this
// package does not recognise is silently dropped, the same way
// pkg/metadata.Validate ignores keys it does not recognise: this is a
// narrowing conversion by construction, not a lossless one.
func FromMetadataExternalIDs(ids metadata.ExternalIDs) importlist.ExternalIDs {
	return importlist.ExternalIDs{
		IMDb:        ids[metadata.KeyIMDb],
		TMDB:        ids[metadata.KeyTMDB],
		TVDB:        ids[metadata.KeyTVDB],
		MusicBrainz: ids[metadata.KeyMBReleaseGroup],
		ISBN:        ids[metadata.KeyISBN13],
		ASIN:        ids[metadata.KeyASIN],
		ComicVine:   ids[metadata.KeyComicVine],
		AniList:     ids[metadata.KeyAniList],
	}
}
