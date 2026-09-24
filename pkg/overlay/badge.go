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

package overlay

import "image"

// Badge is one rendered rating badge in a stack: which source it is for
// (Source, one of the constants in format.go, or the string form of an
// api/catalog/v1alpha1.RatingSource), the text FormatScore already rendered
// for it, and the source's Logo. Badges are drawn bottom-up along the
// chosen corner (OverlayBadge's doc comment,
// api/catalog/v1alpha1/overlayprofile_types.go): index 0 in the slice
// Render receives is nearest the anchored corner.
//
// Render does not call FormatScore or Logo itself -- the caller (the
// artwork-render role, spec §C.6) builds each Badge from an item's ratings,
// omitting a badge whose source has no rating (FormatScore's ok==false)
// rather than passing one with an empty Score.
type Badge struct {
	// Source identifies the rating source this badge is for.
	Source string
	// Score is the pre-formatted score text (FormatScore's output),
	// rendered in bold white beneath the logo. An empty Score draws the
	// badge box and logo with no text.
	Score string
	// Logo is the source's mark, drawn centred near the top of the badge.
	// A nil Logo draws the badge box and score text with no logo.
	Logo image.Image
}
