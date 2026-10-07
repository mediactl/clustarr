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

// Package naming is the remediation loop's naming planner (loop spec §3.5):
// the file's canonical path in status.naming and the NamingCurrent
// condition, and, until F5.1 moves it to the subtitles planner, the sidecar
// listing in status.sidecars (§2.7). The decision is mediafile.RenderNaming;
// this package gathers its input and lists the file's directory through the
// loop's I/O executor.
package naming
