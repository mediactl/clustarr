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
package v1alpha1

import commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"

// DeletableKinds are the kinds the library delete acts on: every kind with a
// library card and a folder of its own, and the two leaves with a page of
// their own that the 2026-11-10 non-video libraries design (§9.2, §9.3)
// makes deletable -- an Album, whose files a delete removes from its
// artist's folder along with its own subfolder but never the artist's, and
// a Book, deletable only when standalone (no spec.authorRef): an author's
// book goes with its author, which import's librarydelete refuses on the
// Book itself and the ui's page never offers. Episode and Issue go with
// their parent.
func DeletableKinds() []commonv1.MediaKind {
	return []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist,
		commonv1.MediaKindAuthor, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
		commonv1.MediaKindAlbum, commonv1.MediaKindBook,
	}
}

// ExclusionKindFor is the ImportExclusion kind that keeps a deleted item of
// kind out, and false for a kind with none (Artist, Author and every child
// kind): the delete dialog offers "Add import list exclusion" only for
// these. An Album's exclusion is keyed by its MusicBrainz release-group id
// (ExclusionIDKeyMusicBrainz, the one MBID a list entry for an album
// carries) and a Book's by its Open Library work id
// (ExclusionIDKeyOpenLibrary), which the import-list sync reads since
// 2026-11-10 (the 2026-09-30 library-delete design's delete-add-exclusion
// over the 2026-11-10 non-video libraries design's §9.2 and §9.3 deletes);
// until then both read false, since no reader honoured either.
func ExclusionKindFor(kind commonv1.MediaKind) (ExclusionKind, bool) {
	switch kind {
	case commonv1.MediaKindMovie:
		return ExclusionKindMovie, true
	case commonv1.MediaKindSeries:
		return ExclusionKindSeries, true
	case commonv1.MediaKindAlbum:
		return ExclusionKindAlbum, true
	case commonv1.MediaKindBook:
		return ExclusionKindBook, true
	case commonv1.MediaKindAudiobook:
		return ExclusionKindAudiobook, true
	case commonv1.MediaKindComic:
		return ExclusionKindComic, true
	default:
		return "", false
	}
}
