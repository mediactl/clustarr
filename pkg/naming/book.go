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

package naming

// audiobookFileTemplate is the audiobook folder template plus the part:
// Readarr's "{ (PartNumber)}", zero-padded so a player that sorts by name
// plays the parts in order, and dropped (brackets and all) for a one-file
// audiobook, whose partIndex is 0 (2026-11-10 design D7, D11).
const (
	bookFileTemplate      = "{Book Title}/{Author Name}"
	audiobookFileTemplate = "{Author Name}/{Book Series}/{Book SeriesPosition - }{Release Year - }{Book Title}{ Narrator}{ (PartNumber:00)}"
)

// BookFile renders the book file path, including the book-title segment
// under the author folder (Readarr convention: no separate book folder).
func (e Engine) BookFile(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenBookFile, bookFileTemplate), c)
}

// AudiobookFile renders the audiobook file path. The trailing
// "{ Narrator}" is a single, non-nested wrapper token (space prefix, no
// suffix) -- Disagreement 3's resolution of the note's doubled-brace
// "{{Narrator}}" idiom, which this grammar's single-pass regex cannot
// parse. It is omitted entirely when Narrator is empty, exactly like
// "{-Release Group}" but with a space instead of a dash.
func (e Engine) AudiobookFile(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenAudiobookFile, audiobookFileTemplate), c)
}
