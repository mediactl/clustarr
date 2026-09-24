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
	"unicode"
	"unicode/utf8"

	"github.com/mediactl/clustarr/ui/paging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// jumpLetters is the A-Z bar's alphabet, Radarr's: # for a title that does
// not start with a letter, then A to Z.
const jumpLetters = "#ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// jumpKey is the letter a title files under: its first rune upper-cased,
// or # when that is not a letter (a digit, a bracket, nothing).
func jumpKey(title string) byte {
	r, _ := utf8.DecodeRuneInString(title)
	r = unicode.ToUpper(r)
	if r >= 'A' && r <= 'Z' {
		return byte(r)
	}
	return '#'
}

// jumpPage is the page, at per items each, holding the first title filed
// at or after letter in items, which the projection keeps sorted by title.
// A letter past every title lands on the last page; an empty list on the
// first.
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

// jumps renders the bar for items on the tab at base: one entry per
// letter, linked when a title is filed under it and disabled otherwise,
// each link keeping the page size.
func jumps(items []projection.LibraryItem, base string, per int) []views.Jump {
	present := map[byte]bool{}
	for _, it := range items {
		present[jumpKey(it.Title)] = true
	}
	out := make([]views.Jump, 0, len(jumpLetters))
	for i := range len(jumpLetters) {
		letter := jumpLetters[i]
		j := views.Jump{Letter: string(letter), Enabled: present[letter]}
		if j.Enabled {
			j.Href = base + "?jump=" + jumpQuery(letter) + "&per=" + itoa(per)
		}
		out = append(out, j)
	}
	return out
}

// jumpQuery is the letter as a query value: # must be escaped or the URL
// reads it as a fragment.
func jumpQuery(letter byte) string {
	if letter == '#' {
		return "%23"
	}
	return string(letter)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
