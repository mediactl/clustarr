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

// Package subtitles is the loop's subtitles adapter (loop spec §6.4): it
// gathers app/caption/subtitleplan's Input from the manager cache,
// clustarr-subtitles and /data, and turns its Decision into effects -- the
// requested record, then the v2 fetch task -- and withdrawals. The manager
// imports it as the PlannerSubtitles planner.
package subtitles
