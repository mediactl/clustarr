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

package ui

import (
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// The A-Z jump bar (design 2026-09-24, after Radarr's): a letter per
// entry, # for a title that starts with anything else, and a link to the
// card where that letter's titles begin in the tab's title order -- on the
// one page since the library stopped paginating (2026-11-10).

const jumpLetters = "#ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// jumpKey is projection.JumpLetter as the byte the bar indexes by.
func jumpKey(title string) byte { return projection.JumpLetter(title)[0] }

// jumpIndex is the index of the first title filing under letter or
// later, given items in title order; the last index when no title does,
// 0 for no items.
func jumpIndex(items []projection.LibraryItem, letter byte) int {
	for i, it := range items {
		if jumpKey(it.Title) >= letter {
			return i
		}
	}
	return max(len(items)-1, 0)
}

// jumps builds the bar over a tab's arranged items: a link per letter some
// title files under, to its first title's card (#<card id>, for a reader
// without JavaScript) with that card's index for ui/static/jump.js, and a
// disabled entry per letter none does.
func jumps(items []projection.LibraryItem) []views.Jump {
	present := map[byte]bool{}
	for _, it := range items {
		present[jumpKey(it.Title)] = true
	}
	out := make([]views.Jump, 0, len(jumpLetters))
	for i := range len(jumpLetters) {
		letter := jumpLetters[i]
		j := views.Jump{Letter: string(letter), Enabled: present[letter]}
		if j.Enabled {
			j.Index = jumpIndex(items, letter)
			j.Href = "#" + views.CardID(items[j.Index])
		}
		out = append(out, j)
	}
	return out
}
