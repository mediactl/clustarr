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

// Package audioalign aligns two recordings of one programme by their audio,
// so a dub from one release can be grafted onto another release's video
// (docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md §7.1). Both
// inputs are the same-language track (the anchor) of each release, decoded
// to 8 kHz mono. It finds the speed ratio between them (PAL speed-up and
// friends), the offset, and any cut where the offset jumps. Pure Go.
package audioalign
