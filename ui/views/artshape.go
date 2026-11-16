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

package views

import commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"

// squareArt reports whether kind's main image is square: an artist's
// fanart.tv thumb, an album's cover and an audiobook's cover are 1:1, which
// a 2:3 poster frame crops at the sides (owner, 2026-11-16: "use square
// image containers" for the music library and albums). Every other kind's
// is a 2:3 poster, a book's cover and an author's photo included.
func squareArt(kind commonv1.MediaKind) bool {
	switch kind {
	case commonv1.MediaKindArtist, commonv1.MediaKindAlbum, commonv1.MediaKindAudiobook:
		return true
	}
	return false
}

// artRatio is the CSS ratio kind's library card frames its image at.
func artRatio(kind commonv1.MediaKind) string {
	if squareArt(kind) {
		return "1/1"
	}
	return "2/3"
}

// artThumbClass sizes the header search's thumbnail of an item of kind:
// as tall as a poster's, as wide as it is tall for a square image.
func artThumbClass(kind commonv1.MediaKind) string {
	if squareArt(kind) {
		return "h-12 w-12 rounded-sm bg-muted"
	}
	return "h-12 w-8 rounded-sm bg-muted"
}

// addThumbClass is an Add New hit's image box for kind.
func addThumbClass(kind commonv1.MediaKind) string {
	if squareArt(kind) {
		return "aspect-square w-20 shrink-0 rounded bg-muted object-cover"
	}
	return "aspect-[2/3] w-20 shrink-0 rounded bg-muted object-cover"
}
