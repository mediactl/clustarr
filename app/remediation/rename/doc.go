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

// Package rename is the remediation loop's rename actuator (loop spec §3.9):
// it moves a file to the canonical path its applied status proposes, when
// its RootFolder allows, through specwrite.RenameFile under
// importarr-worker -- the decision importarr's rename controller made,
// run by the loop against the status it just applied instead of by a second
// MediaFile watcher.
//
// The move and the spec apply are app/import/mediafilespec's RenameFile,
// which the LibraryScan rename pass shares; this package only decides when
// to call it. Nothing here writes status: after the move, the spec apply
// wakes the file, and the loop's next pass reports it current.
package rename
