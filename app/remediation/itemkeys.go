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

package remediation

import (
	"slices"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The item kinds (loop spec §3.2): the loop's second key type, beside
// KindMediaFile. An item key reconciles one catalog item's file rollups
// (§3.12); it writes that item's status and finalizer and nothing else.
// Series and Comic joined as the owners of their episodes' and issues' grabs
// (ADR-0019 §6.1, A3.2).
const (
	KindMovie     KeyKind = "Movie"
	KindEpisode   KeyKind = "Episode"
	KindAlbum     KeyKind = "Album"
	KindBook      KeyKind = "Book"
	KindAudiobook KeyKind = "Audiobook"
	KindIssue     KeyKind = "Issue"
	KindSeries    KeyKind = "Series"
	KindComic     KeyKind = "Comic"
)

var itemKinds = map[commonv1.MediaKind]KeyKind{
	commonv1.MediaKindMovie:     KindMovie,
	commonv1.MediaKindEpisode:   KindEpisode,
	commonv1.MediaKindAlbum:     KindAlbum,
	commonv1.MediaKindBook:      KindBook,
	commonv1.MediaKindAudiobook: KindAudiobook,
	commonv1.MediaKindIssue:     KindIssue,
	commonv1.MediaKindSeries:    KindSeries,
	commonv1.MediaKindComic:     KindComic,
}

// ItemKind is the item key kind of a MediaRef kind; false for a kind the
// loop has no key for (artist, author).
func ItemKind(kind commonv1.MediaKind) (KeyKind, bool) {
	k, ok := itemKinds[kind]
	return k, ok
}

// ItemKeys are the items whose rollups mf feeds (loop spec §3.2):
// spec.mediaRef's item and every spec.mediaRef.keys entry, each once, in
// that order. keys exists only for Episode and Issue packs, so a
// multi-episode file is one file key and N Episode keys; mediaRef.track
// narrows an Album reference and makes no key.
func ItemKeys(mf *catalogv1alpha1.MediaFile) []Key {
	if mf == nil {
		return nil
	}
	kind, ok := ItemKind(mf.Spec.MediaRef.Kind)
	if !ok || mf.Spec.MediaRef.Name == "" {
		return nil
	}
	var keys []Key
	for _, name := range append([]string{mf.Spec.MediaRef.Name}, mf.Spec.MediaRef.Keys...) {
		k := Key{Kind: kind, Namespace: mf.Namespace, Name: name}
		if name != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}
