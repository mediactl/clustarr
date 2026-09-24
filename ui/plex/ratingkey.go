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

// Package plex serves clustarr's Plex Custom Metadata Provider (spec §D,
// docs/research/plex-metadata-provider.md): two read-only roots, /plex/movies
// (type 1) and /plex/tv (types 2, 3, 4), over the same catalog Options.Reader
// already backs everywhere else in ui/. Every route in this package only
// ever reads -- it holds an IndexFunc, never a client.Client or an
// *actions.Actions -- so ui/guard_test.go's ban on writes anywhere under ui/
// holds here exactly as it does everywhere else in the tree.
package plex

import (
	"fmt"
	"regexp"
	"strconv"

	"k8s.io/apimachinery/pkg/types"
)

// Provider identifiers (spec §D.1): the "scheme" a Guid[] entry and a
// match/metadata guid's own scheme segment both carry, and what
// MediaProvider.Types[].Scheme[].scheme and the root's own "identifier"
// field are set to (research §2: "should be identical to the provider
// identifier"). Each starts with the required "tv.plex.agents.custom."
// prefix (research §2's doc-drift note: the example code and its test
// enforce the prefix even though MediaProvider.md's own JSON example omits
// it once).
const (
	MoviesIdentifier = "tv.plex.agents.custom.clustarr.movies"
	TVIdentifier     = "tv.plex.agents.custom.clustarr.tv"
)

// Metadata type strings (research §5.1's "type" field, and the metadataType
// segment of a guid, research §6).
const (
	metadataTypeMovie   = "movie"
	metadataTypeShow    = "show"
	metadataTypeSeason  = "season"
	metadataTypeEpisode = "episode"
)

// Numeric provider types (research §2's table, and the match request's own
// "type" field, research §4).
const (
	typeMovie      = 1
	typeShow       = 2
	typeSeason     = 3
	typeEpisode    = 4
	typeCollection = 18
)

// ratingKeyPattern parses a ratingKey back into the UID and, when present,
// the season number a [SeasonKey] encoded into it. Spec §D.3: ratingKey
// charset is [A-Za-z0-9_-], which excludes the periods a Kubernetes name may
// carry, so every ratingKey here is a UID (36-character UUID form) optionally
// followed by "-s<NN>". NN is two to four digits: %02d pads to two and never
// truncates, so a season numbered by year (2024, a daily show's) mints
// "-s2024", which a two-digit pattern refused and so could never round-trip.
// [ParseRatingKey] then refuses any spelling [SeasonKey] would not mint
// ("-s007"), keeping one key per season.
var ratingKeyPattern = regexp.MustCompile(`^([0-9a-f-]{36})(?:-s(\d{2,4}))?$`)

// RatingKey is a Movie, Series or Episode's ratingKey: its own UID, exactly
// (spec §D.3). It exists so every call site reads "this is a ratingKey", not
// a bare string(uid) repeated at each one.
func RatingKey(uid types.UID) string {
	return string(uid)
}

// SeasonKey is a season's ratingKey (spec §D.3): the owning Series' UID plus
// "-s<NN>", NN zero-padded to at least two digits (seasons 0-9999 parse
// back). A season has no object of its own -- it is a slice of the Series'
// status.seasons -- so this is the only place a season's identity is
// minted.
func SeasonKey(seriesUID types.UID, number int32) string {
	return fmt.Sprintf("%s-s%02d", seriesUID, number)
}

// ParseRatingKey parses key back into the UID it names and, when key is a
// [SeasonKey], the season number it carries. ok is false for anything that
// does not match [ratingKeyPattern] at all -- a malformed ratingKey a
// misbehaving client sent, which every handler in this package treats as
// "not found" rather than panicking on it.
func ParseRatingKey(key string) (uid types.UID, season int32, isSeason bool, ok bool) {
	m := ratingKeyPattern.FindStringSubmatch(key)
	if m == nil {
		return "", 0, false, false
	}
	if m[2] == "" {
		return types.UID(m[1]), 0, false, true
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || fmt.Sprintf("%02d", n) != m[2] {
		// Not a spelling SeasonKey mints: "-s007" would otherwise name
		// season 7 beside the canonical "-s07".
		return "", 0, false, false
	}
	return types.UID(m[1]), int32(n), true, true
}

// GUID builds the guid a Metadata object's own "guid" field carries, and
// what a Guid[] entry's "id" and a match request's "guid" are parsed the
// same way (research §6): "{scheme}://{metadataType}/{ratingKey}", scheme
// being the provider's own identifier.
func GUID(identifier, metadataType, ratingKey string) string {
	return fmt.Sprintf("%s://%s/%s", identifier, metadataType, ratingKey)
}

// metadataKey is a Metadata object's own "key" field (research §5.1): the
// path to re-fetch it, relative to the provider root -- exactly like a
// Feature's own "key" (research §2) -- never including the "/plex/movies" or
// "/plex/tv" mount point PMS already holds as the root URL it was given at
// registration.
func metadataKey(ratingKey string, withChildren bool) string {
	k := "/library/metadata/" + ratingKey
	if withChildren {
		k += "/children"
	}
	return k
}
