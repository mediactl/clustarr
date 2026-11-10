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
// kind out, and false for a kind with none: the delete dialog and the bulk
// delete offer "Add import list exclusion" only where it answers true, so
// it answers true only for a kind whose exclusion is honoured on every path
// that would bring the item back (the 2026-09-30 library-delete design:
// "keep import lists -- and the library scan -- from adding it back"). A
// Book's is keyed by its Open Library work id (ExclusionIDKeyOpenLibrary),
// which the import-list sync reads since 2026-11-10 (the design's
// delete-add-exclusion over the 2026-11-10 non-video libraries design's
// §9.3 delete), and a standalone book, the one a person can delete, has no
// container to re-create it. An Album has an exclusion kind
// (ExclusionKindAlbum, keyed by its MusicBrainz release-group id,
// ExclusionIDKeyMusicBrainz, which the sync reads too and which import's
// librarydelete writes when the delete-add-exclusion annotation asks) but
// answers false here: every Album has an Artist, whose fan-out re-creates
// every listed release group it does not find and reads no exclusion, so
// the checkbox would promise a block the one re-add path an album has
// defeats. It answers true once the Artist's fan-out honours the
// exclusion. Artist, Author and every child kind have none.
func ExclusionKindFor(kind commonv1.MediaKind) (ExclusionKind, bool) {
	switch kind {
	case commonv1.MediaKindMovie:
		return ExclusionKindMovie, true
	case commonv1.MediaKindSeries:
		return ExclusionKindSeries, true
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
