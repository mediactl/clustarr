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

// Package remediation is the MediaFile remediation loop (ADR-0016): one
// controller, "mediafile", the only writer of MediaFile status, running its
// planners in Order over one view of the file and applying their status in
// one compare-and-swap (loop spec §3).
//
// A pass (§3.4) loads the cached file, builds a View, runs every bound
// planner's Gather under isolation (a panic, an error or a transient fault
// stays the planner's own), then every Plan in Order -- each over the draft
// the planners before it left -- renders the loop's own fields, applies the
// main resource (labels and the spec takeover) when it changed, applies the
// status once by compare-and-swap at the resourceVersion the pass planned
// from, and only then runs the planners' effects (record writes, task
// publishes) and the actuators (replay, then rename). A pass that changes
// nothing applies nothing; a transient fault writes nothing at all.
package remediation
