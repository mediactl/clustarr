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
// already backs everywhere else in ui/, and, under Options.MusicProvider,
// the music and audiobook roots /plex/music and /plex/audiobooks (types 8,
// 9, 10; docs/superpowers/specs/2026-11-10-non-video-libraries-design.md
// §7). Every route in this package only ever reads -- it holds an
// IndexFunc, never a client.Client or an *actions.Actions -- so
// ui/guard_test.go's ban on writes anywhere under ui/ holds here exactly as
// it does everywhere else in the tree.
package plex

import (
	"crypto/sha256"
	"encoding/hex"
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
	// MusicIdentifier and AudiobooksIdentifier are the music and audiobook
	// roots' (2026-11-10 design §7.1), served only under
	// Options.MusicProvider: PMS through 1.43.5.11079 refuses a custom
	// provider declaring types 8, 9 or 10 (§7.6's as-run note).
	MusicIdentifier      = "tv.plex.agents.custom.clustarr.music"
	AudiobooksIdentifier = "tv.plex.agents.custom.clustarr.audiobooks"
)

// Metadata type strings (research §5.1's "type" field, and the metadataType
// segment of a guid, research §6). artist, album and track are Plex's own
// music types, which the custom-provider docs do not publish (design §7,
// inferred from PMS's own music items).
const (
	metadataTypeMovie   = "movie"
	metadataTypeShow    = "show"
	metadataTypeSeason  = "season"
	metadataTypeEpisode = "episode"
	metadataTypeArtist  = "artist"
	metadataTypeAlbum   = "album"
	metadataTypeTrack   = "track"
)

// Numeric provider types (research §2's table, and the match request's own
// "type" field, research §4). 8, 9 and 10 are Plex's own metadata_type
// numbers for artist, album and track (PMS's database and its
// tv.plex.agents.music provider list them; the custom-provider table
// publishes none).
const (
	typeMovie      = 1
	typeShow       = 2
	typeSeason     = 3
	typeEpisode    = 4
	typeArtist     = 8
	typeAlbum      = 9
	typeTrack      = 10
	typeCollection = 18
)

// ratingKeyPattern parses a ratingKey back into what it names. Spec §D.3:
// ratingKey charset is [A-Za-z0-9_-], which excludes the periods a
// Kubernetes name may carry, so every ratingKey here is a UID (36-character
// UUID form) optionally followed by one suffix, or a synthetic artist key:
//
//   - "-s<NN>", a season ([SeasonKey]). NN is two to four digits: %02d pads
//     to two and never truncates, so a season numbered by year (2024, a
//     daily show's) mints "-s2024", which a two-digit pattern refused and
//     so could never round-trip. [ParseKey] refuses any spelling SeasonKey
//     would not mint ("-s007"), keeping one key per season.
//   - "-t<recording MBID>", a track of the Album the UID names
//     ([TrackKey]; design §7.2: a recording may be on several albums, so
//     the key carries both).
//   - "-p<NN>", a part of the Audiobook the UID names ([PartKey]), padded
//     and refused as a season's number is.
//   - "a-<16 hex>", the audiobooks root's synthetic artist for an Audiobook
//     with no authorRef ([SyntheticArtistKey]: sha256 of the author's name).
var ratingKeyPattern = regexp.MustCompile(`^(?:([0-9a-f-]{36})(?:-s(\d{2,4})|-t([0-9a-f-]{36})|-p(\d{2,4}))?|a-([0-9a-f]{16}))$`)

// RatingKey is a Movie, Series, Episode, Artist, Album, Author or
// Audiobook's ratingKey: its own UID, exactly (spec §D.3). It exists so
// every call site reads "this is a ratingKey", not a bare string(uid)
// repeated at each one.
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

// TrackKey is a track's ratingKey (design §7.2): the Album's UID plus
// "-t<recording MBID>". A track has no object of its own -- it is a slice
// of the Album's status.tracks, keyed by the recording -- and a recording
// may be on several albums, so the key carries both.
func TrackKey(albumUID types.UID, recordingID string) string {
	return string(albumUID) + "-t" + recordingID
}

// PartKey is an audiobook part's ratingKey (design §7.2): the Audiobook's
// UID plus "-p<NN>", NN the part's 1-based position in status.fileRefs,
// zero-padded as [SeasonKey] pads.
func PartKey(audiobookUID types.UID, part int32) string {
	return fmt.Sprintf("%s-p%02d", audiobookUID, part)
}

// SyntheticArtistKey is the audiobooks root's artist ratingKey for an
// Audiobook with no authorRef (design §7.2): "a-" plus the first 16 hex
// digits of sha256(authorName), the author as status.metadata.authors[0]
// names them. The same name always mints the same key, so every such
// Audiobook of one author shares one artist.
func SyntheticArtistKey(authorName string) string {
	sum := sha256.Sum256([]byte(authorName))
	return "a-" + hex.EncodeToString(sum[:8])
}

// Key is a parsed ratingKey ([ParseKey]): the UID it names and, when it
// has one, its suffix -- a season number, a track's recording id or a
// part number -- or, for a synthetic artist, the key itself in Synthetic
// with no UID.
type Key struct {
	UID       types.UID
	Season    int32
	IsSeason  bool
	Track     string
	Part      int32
	IsPart    bool
	Synthetic string
}

// ParseKey parses key back into what it names, false for anything that
// does not match [ratingKeyPattern] at all -- a malformed ratingKey a
// misbehaving client sent, which every handler in this package treats as
// "not found" rather than panicking on it -- or a suffix spelled as no
// minter spells it.
func ParseKey(key string) (Key, bool) {
	m := ratingKeyPattern.FindStringSubmatch(key)
	if m == nil {
		return Key{}, false
	}
	if m[5] != "" {
		return Key{Synthetic: key}, true
	}
	k := Key{UID: types.UID(m[1])}
	switch {
	case m[2] != "":
		n, ok := paddedNumber(m[2])
		if !ok {
			return Key{}, false
		}
		k.Season, k.IsSeason = n, true
	case m[3] != "":
		k.Track = m[3]
	case m[4] != "":
		n, ok := paddedNumber(m[4])
		if !ok {
			return Key{}, false
		}
		k.Part, k.IsPart = n, true
	}
	return k, true
}

// paddedNumber parses the digits [SeasonKey] and [PartKey] mint, refusing a
// spelling they would not: "-s007" would otherwise name season 7 beside
// the canonical "-s07".
func paddedNumber(digits string) (int32, bool) {
	n, err := strconv.Atoi(digits)
	if err != nil || fmt.Sprintf("%02d", n) != digits {
		return 0, false
	}
	return int32(n), true
}

// ParseRatingKey parses key back into the UID it names and, when key is a
// [SeasonKey], the season number it carries: the video roots' reading of a
// key. A music key (a track, a part, a synthetic artist) is not one of
// theirs and does not parse here; [ParseKey] reads every grammar.
func ParseRatingKey(key string) (uid types.UID, season int32, isSeason bool, ok bool) {
	k, ok := ParseKey(key)
	if !ok || k.Track != "" || k.IsPart || k.Synthetic != "" {
		return "", 0, false, false
	}
	return k.UID, k.Season, k.IsSeason, true
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
