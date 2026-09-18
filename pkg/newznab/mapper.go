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

package newznab

import (
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// CategoryMapper is the reverse of ByKind: the standard Newznab tree back
// to a Clustarr MediaKind. It holds no state (categories are a fixed
// table), so the zero value is ready to use.
type CategoryMapper struct{}

// Kind returns the first MediaKind any of ids maps to, checking each id's
// Parent() against the five family roots. ok is false when nothing in ids
// is a recognised family (e.g. only a custom id, or 1000/4000/6000).
func (CategoryMapper) Kind(ids []CategoryID) (kind commonv1.MediaKind, ok bool) {
	for _, id := range ids {
		switch id.Parent() {
		case CatMovies:
			return commonv1.MediaKindMovie, true
		case CatTV:
			return commonv1.MediaKindSeries, true
		case CatAudio:
			if id == CatAudioAudiobook {
				return commonv1.MediaKindAudiobook, true
			}
			return commonv1.MediaKindAlbum, true
		case CatBooks:
			if id == CatBooksComics {
				return commonv1.MediaKindComic, true
			}
			return commonv1.MediaKindBook, true
		}
	}
	return "", false
}
