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

// Package importtarget is the grammar of the two annotations that direct
// an import by hand -- catalog.clustarr.io/import-target and
// import-override -- on a Download or a LibraryScan: parsing, the
// root-folder fit, and the target a Download's spec names. The import
// agent's inspect, the rescan and the ui's manual assign all read the one
// grammar; it links no worker (spec §4.3 I4).
package importtarget
