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

// Package rollup holds the pure functions the Movie and Episode controllers
// both need to fold a watched MediaFile and a watched Download into their
// own status: FileState and DownloadOverlay. Task C6's controller amendment
// gives this package to the movie/series/episode task specifically so this
// logic is written once, with one table test each, and imported from both
// controllers rather than duplicated between app/catalog/controller/movie and
// app/catalog/controller/episode.
//
// DownloadOverlay's result is phase-independent (Overlay, not MoviePhase or
// EpisodePhase) because Movie and Episode each have their own phase enum;
// the movie and episode packages each carry a one-switch, no-logic adapter
// that maps an Overlay onto their own phase type.
package rollup
