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

// Package metadata is the normalized metadata model shared by every
// provider client in pkg/metadata/clients: a crosswalk of external ids, a
// per-kind entity model, provider interfaces, a registry that composes them
// with priority fallback, an in-process cache and rate-limit defaults. It
// imports no Kubernetes types beyond api/common/v1alpha1's MediaKind and no
// other Phase B package -- catalogarr and the metadata gateway (ADR-0007)
// depend on it, never the reverse.
package metadata

import (
	"errors"
	"fmt"
	"regexp"
)

// ExternalIDs crosswalks a media item across providers, keyed by a short
// source tag (KeyTMDB, KeyIMDb, ...). It is exactly map[string]string, not a
// typed key, so it matches api/catalog/v1alpha1's MovieMetadata.ExternalIDs
// and SeriesMetadata.ExternalIDs field for field -- the gateway copies this
// type onto a CR with no translation layer.
type ExternalIDs map[string]string

// Recognised ExternalIDs keys. A key not listed here still round-trips
// through ExternalIDs and Merge unchanged; only Validate treats the list
// below specially.
const (
	KeyIMDb               = "imdb"
	KeyTMDB               = "tmdb"
	KeyTVDB               = "tvdb"
	KeyMBArtist           = "mb-artist"
	KeyMBReleaseGroup     = "mb-release-group"
	KeyMBRelease          = "mb-release"
	KeyMBRecording        = "mb-recording"
	KeyISBN13             = "isbn13"
	KeyASIN               = "asin"
	KeyComicVine          = "comicvine"
	KeyAniList            = "anilist"
	KeyOpenLibraryWork    = "olwork"
	KeyOpenLibraryEdition = "oledition"
	KeyOpenLibraryAuthor  = "olauthor"
)

// Merge returns a copy of e with every key from o that e does not already
// hold. A key present in both keeps e's value -- Merge never lets a later,
// possibly-wrong source silently overwrite an earlier one; a real conflict
// is the caller's to log, not this method's to resolve.
func (e ExternalIDs) Merge(o ExternalIDs) ExternalIDs {
	out := make(ExternalIDs, len(e)+len(o))
	for k, v := range e {
		out[k] = v
	}
	for k, v := range o {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

var (
	imdbPattern      = regexp.MustCompile(`^tt[0-9]{7,10}$`)
	numericPattern   = regexp.MustCompile(`^[0-9]+$`)
	asinPattern      = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	comicVinePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]+$`)
	mbidPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Validate checks every key in e that this package recognises against its
// known shape. Keys it does not recognise (tvrage, wikidata, a future
// provider) pass through unchecked -- Validate catches mapping bugs in the
// ids this package itself hands out and consumes, not every id crosswalk
// entry that could ever exist.
func Validate(e ExternalIDs) error {
	var errs []error
	check := func(key string, ok bool, want string) {
		if v, present := e[key]; present && !ok {
			errs = append(errs, fmt.Errorf("metadata: %s=%q is not a valid %s", key, v, want))
		}
	}
	check(KeyIMDb, imdbPattern.MatchString(e[KeyIMDb]), "IMDb id (ttNNNNNNN)")
	check(KeyTMDB, numericPattern.MatchString(e[KeyTMDB]), "TMDB numeric id")
	check(KeyTVDB, numericPattern.MatchString(e[KeyTVDB]), "TVDB numeric id")
	check(KeyMBArtist, mbidPattern.MatchString(e[KeyMBArtist]), "MusicBrainz MBID")
	check(KeyMBReleaseGroup, mbidPattern.MatchString(e[KeyMBReleaseGroup]), "MusicBrainz MBID")
	check(KeyMBRelease, mbidPattern.MatchString(e[KeyMBRelease]), "MusicBrainz MBID")
	check(KeyMBRecording, mbidPattern.MatchString(e[KeyMBRecording]), "MusicBrainz MBID")
	check(KeyISBN13, validISBN13(e[KeyISBN13]), "ISBN-13")
	check(KeyASIN, asinPattern.MatchString(e[KeyASIN]), "ASIN")
	check(KeyComicVine, comicVinePattern.MatchString(e[KeyComicVine]), "ComicVine id (NNNN-NNNNN)")
	check(KeyAniList, numericPattern.MatchString(e[KeyAniList]), "AniList numeric id")
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// validISBN13 checks the ISBN-13 checksum: alternating weights 1,3 over the
// 13 digits must sum to a multiple of 10.
func validISBN13(s string) bool {
	if len(s) != 13 {
		return false
	}
	sum := 0
	for i, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		d := int(r - '0')
		if i%2 == 0 {
			sum += d
		} else {
			sum += d * 3
		}
	}
	return sum%10 == 0
}
