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
	"errors"
	"slices"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// ErrKindNotYieldable is a spec.kinds entry the list's provider cannot
// produce at all: a Trakt list asked for albums, a StevenLu feed asked for
// series. Gap-fix ruling R-10 makes this an admission failure (ImportListSpec's
// CEL rules, since X14); for a list admitted before them the ImportList
// controller reports it as Ready=False and schedules nothing, and a stale task
// that reaches the worker anyway fails that kind with this error rather than
// skipping it.
var ErrKindNotYieldable = errors.New("importlist: the list's provider cannot yield this kind")

// ErrNoCatalogWriter is a kind the provider can yield but this worker has no
// catalog writer for. Only movie and series have one: no implemented
// provider yields album, book, audiobook or comic items -- the two that
// could (arr against lidarr, readarr or clustarr, and custom) are stubs the
// design spec defers (§17), whose Fetch fails first -- and creating those
// kinds needs identities no Item carries yet (an Album's artistRef, a Book's
// Open Library workID). A provider that starts returning them surfaces this
// on status rather than having its items dropped.
var ErrNoCatalogWriter = errors.New("importlist: no catalog writer for this kind")

// allKinds is every value ImportListSpec.Kinds' enum admits.
var allKinds = []commonv1.MediaKind{
	commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindAlbum,
	commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
}

var videoKinds = []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindSeries}

// YieldableKinds is every catalog kind spec's provider can produce, from
// what each upstream serves:
//
//   - trakt, plex (Discover watchlist), tmdb, mdblist and imdbCSV list films
//     and shows only: movie and series.
//   - stevenLu is a popular-movies feed: movie.
//   - arr yields what its instance manages: radarr movie, sonarr series,
//     lidarr album, readarr book or audiobook (a Readarr instance serves one
//     or the other), and another Clustarr any kind.
//   - custom is an arbitrary feed with no fixed schema, so any kind.
//
// ImportListSpec's R-10 CEL rules encode this same table, and
// importarr/controller/importlist's TestAdmissionMatchesYieldableKinds holds
// the two to each other for every provider and kind.
func YieldableKinds(spec catalogv1alpha1.ImportListSpec) []commonv1.MediaKind {
	switch {
	case spec.Trakt != nil, spec.Plex != nil, spec.Tmdb != nil, spec.Mdblist != nil, spec.ImdbCSV != nil:
		return videoKinds
	case spec.StevenLu != nil:
		return []commonv1.MediaKind{commonv1.MediaKindMovie}
	case spec.Custom != nil:
		return allKinds
	case spec.Arr != nil:
		switch spec.Arr.Kind {
		case catalogv1alpha1.ArrKindRadarr:
			return []commonv1.MediaKind{commonv1.MediaKindMovie}
		case catalogv1alpha1.ArrKindSonarr:
			return []commonv1.MediaKind{commonv1.MediaKindSeries}
		case catalogv1alpha1.ArrKindLidarr:
			return []commonv1.MediaKind{commonv1.MediaKindAlbum}
		case catalogv1alpha1.ArrKindReadarr:
			return []commonv1.MediaKind{commonv1.MediaKindBook, commonv1.MediaKindAudiobook}
		case catalogv1alpha1.ArrKindClustarr:
			return allKinds
		}
	}
	return nil
}

// CanYield reports whether spec's provider can produce kind.
func CanYield(spec catalogv1alpha1.ImportListSpec, kind commonv1.MediaKind) bool {
	return slices.Contains(YieldableKinds(spec), kind)
}

// UnyieldableKinds is every spec.kinds entry spec's provider cannot produce,
// in spec order: empty for a list admission would accept under R-10.
func UnyieldableKinds(spec catalogv1alpha1.ImportListSpec) []string {
	var out []string
	for _, k := range spec.Kinds {
		if !CanYield(spec, commonv1.MediaKind(k)) {
			out = append(out, k)
		}
	}
	return out
}

// ProviderName names spec's provider for a message: the spec field name,
// plus the instance flavour for arr.
func ProviderName(spec catalogv1alpha1.ImportListSpec) string {
	switch {
	case spec.Trakt != nil:
		return "trakt"
	case spec.Plex != nil:
		return "plex"
	case spec.Tmdb != nil:
		return "tmdb"
	case spec.Mdblist != nil:
		return "mdblist"
	case spec.StevenLu != nil:
		return "stevenLu"
	case spec.ImdbCSV != nil:
		return "imdbCSV"
	case spec.Custom != nil:
		return "custom"
	case spec.Arr != nil:
		return "arr (" + string(spec.Arr.Kind) + ")"
	default:
		return "unset"
	}
}

// hasCatalogWriter reports whether syncKind can turn kind's items into
// catalog objects. See ErrNoCatalogWriter.
func hasCatalogWriter(kind commonv1.MediaKind) bool {
	return slices.Contains(videoKinds, kind)
}
