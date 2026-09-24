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
	"math"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Tag is the shape shared by Genre[], Country[] and Network[] entries
// (research §5.2).
type Tag struct {
	Tag         string `json:"tag"`
	OriginalTag string `json:"originalTag,omitempty"`
}

// GuidRef is one Guid[] entry: an external id in "{provider}://{id}" form
// (research §5.2, §6). "Internally supported providers" are imdb, tmdb and
// tvdb; anything else in status.metadata.externalIDs (tvmaze, anidb, ...) is
// not part of Plex's closed vocabulary here and is left out.
type GuidRef struct {
	ID string `json:"id"`
}

// CollectionRef is a movie's Collection[] entry (research §5.2). Only tag
// and guid are populated: key, summary, art and thumb all belong to the
// optional "collection" feature (research §2), which this provider does not
// declare (spec §D.2 lists only match and metadata), so there is no
// endpoint of this provider's own for a "key" to point at -- guid instead
// carries the TMDB collection's own external id, the same convention
// Guid[] uses for a catalog item.
type CollectionRef struct {
	Guid string `json:"guid,omitempty"`
	Tag  string `json:"tag"`
}

// RatingObj is one Rating[] entry (research §5.2): a badge identifier from
// Plex's closed vocabulary, "audience" or "critic", and a 0-10 float.
type RatingObj struct {
	Image string  `json:"image"`
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

// ratingImage/ratingType are the exact badge identifiers ruling R4 (spec
// §D.5) sends Plex, one pair per source it accepts. Metacritic, trakt and
// letterboxd are never sent -- they exist only for the overlay poster (spec
// §D.5's own closing sentence).
const (
	ratingImageIMDb   = "imdb://image.rating"
	ratingImageTMDB   = "themoviedb://image.rating"
	ratingImageRTCrit = "rottentomatoes://image.rating.ripe"
	ratingImageRTAud  = "rottentomatoes://image.rating.upright"
)

// ratings builds Plex's Rating[] from a catalog item's status.metadata.ratings,
// per ruling R4: imdb and tmdb are /10 sources (value = valueCentis/100.0);
// the Rotten Tomatoes pair is /100 (value = valueCentis/1000.0); every other
// source (metacritic, trakt, letterboxd) is skipped. The four accepted
// sources are emitted in a fixed order -- imdb, tmdb, critic, audience --
// regardless of the input slice's own order (a +listType=map field, whose
// order the apiserver does not guarantee), so the response is deterministic
// call to call.
func ratings(rs []catalogv1.Rating) []RatingObj {
	by := make(map[catalogv1.RatingSource]catalogv1.Rating, len(rs))
	for _, r := range rs {
		by[r.Source] = r
	}

	var out []RatingObj
	if r, ok := by[catalogv1.RatingSourceIMDb]; ok {
		out = append(out, RatingObj{Image: ratingImageIMDb, Type: "audience", Value: roundOneDecimal(float64(r.ValueCentis) / 100.0)})
	}
	if r, ok := by[catalogv1.RatingSourceTMDB]; ok {
		out = append(out, RatingObj{Image: ratingImageTMDB, Type: "audience", Value: roundOneDecimal(float64(r.ValueCentis) / 100.0)})
	}
	if r, ok := by[catalogv1.RatingSourceRTCritic]; ok {
		out = append(out, RatingObj{Image: ratingImageRTCrit, Type: "critic", Value: roundOneDecimal(float64(r.ValueCentis) / 1000.0)})
	}
	if r, ok := by[catalogv1.RatingSourceRTAudience]; ok {
		out = append(out, RatingObj{Image: ratingImageRTAud, Type: "audience", Value: roundOneDecimal(float64(r.ValueCentis) / 1000.0)})
	}
	return out
}

// roundOneDecimal rounds v to one decimal place -- the brief's "formatted
// with one decimal" -- e.g. 8.734 -> 8.7. encoding/json then marshals it in
// its shortest form (8.7, or 8 for a value that rounds to a whole number:
// JSON numbers carry no trailing zero), which is what every golden file in
// testdata/ was authored against.
func roundOneDecimal(v float64) float64 {
	return math.Round(v*10) / 10
}

// genres builds Genre[] from a plain []string.
func genres(gs []string) []Tag {
	if len(gs) == 0 {
		return nil
	}
	out := make([]Tag, len(gs))
	for i, g := range gs {
		out[i] = Tag{Tag: g}
	}
	return out
}

// guidRefs builds Guid[] from status.metadata.externalIDs, in the fixed
// order research §5.2 names as internally supported: imdb, tmdb, tvdb. A
// map has no order of its own, so this fixed sequence -- not a range over
// the map -- is what keeps the output deterministic.
func guidRefs(externalIDs map[string]string) []GuidRef {
	var out []GuidRef
	for _, provider := range []string{"imdb", "tmdb", "tvdb"} {
		if id, ok := externalIDs[provider]; ok && id != "" {
			out = append(out, GuidRef{ID: provider + "://" + id})
		}
	}
	return out
}

// networkTag builds Network[] from a Series' status.metadata.network, empty
// meaning none.
func networkTag(network string) []Tag {
	if network == "" {
		return nil
	}
	return []Tag{{Tag: network}}
}
