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

package rescan

// WalkOrderLess exposes walkOrderLess to the external test package.
var WalkOrderLess = walkOrderLess

// KeptOutputName exposes keptOutputName to the external test package.
var KeptOutputName = keptOutputName

// ClampRunes exposes clampRunes to the external test package.
var ClampRunes = clampRunes

// SetRenameBeforeMove installs fn as the hook RenameFile runs between its
// read of the MediaFile and its move, and returns a func that removes it.
func SetRenameBeforeMove(fn func()) (restore func()) {
	prev := renameBeforeMove
	renameBeforeMove = fn
	return func() { renameBeforeMove = prev }
}
