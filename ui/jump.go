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
	"github.com/mediactl/clustarr/ui/paging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// The A-Z jump bar (design 2026-09-24, after Radarr's): a letter per
// entry, # for a title that starts with anything else, and a link to the
// page where that letter's titles begin in the tab's title order.

const jumpLetters = "#ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// jumpKey is projection.JumpLetter as the byte the bar indexes by.
func jumpKey(title string) byte { return projection.JumpLetter(title)[0] }

// jumpPage is the page of per items on which the first title filing under
// letter or later sits, given items in title order; the last page when no
// title does.
func jumpPage(items []projection.LibraryItem, letter byte, per int) int {
	idx := len(items) - 1
	for i, it := range items {
		if jumpKey(it.Title) >= letter {
			idx = i
			break
		}
	}
	return paging.Paginate(len(items), max(idx, 0)/max(per, 1)+1, per).Number
}

// jumps builds the bar for a tab at base: a link per letter some title
// files under, carrying the page's size and view (p.Params) with jump in
// place of page, and a disabled entry per letter none does.
func jumps(items []projection.LibraryItem, base string, p paging.Page) []views.Jump {
	present := map[byte]bool{}
	for _, it := range items {
		present[jumpKey(it.Title)] = true
	}
	out := make([]views.Jump, 0, len(jumpLetters))
	for i := range len(jumpLetters) {
		letter := jumpLetters[i]
		j := views.Jump{Letter: string(letter), Enabled: present[letter]}
		if j.Enabled {
			q := p.Values(1)
			q.Del("page")
			q.Set("jump", string(letter))
			j.Href = base + "?" + q.Encode()
		}
		out = append(out, j)
	}
	return out
}
