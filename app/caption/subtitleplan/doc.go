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

// Package subtitleplan is the subtitles remediation's pure planner (loop
// spec §6.4). The loop's subtitles adapter (app/remediation/subtitles)
// gathers its Input -- the file, its item, the SubtitleProfiles, the
// embedded extractors, the RootFolders, the clustarr-subtitles records it
// needs and, when a gate opens, the media directory's listing -- and turns
// its Decision into effects; nothing here does I/O.
//
// One profile rule (§6.4.2, §2.13) lives here too: WinningProfile, which the
// slim SubtitleProfile reconciler shares, so the file's block and the
// profile's counts never disagree about a winner.
package subtitleplan
