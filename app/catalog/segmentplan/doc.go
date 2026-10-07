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

// Package segmentplan plans clustarr's own segment analysis (spec
// 2026-10-01 segment detection §4): [PlanMessage], which the remediation
// loop's markers planner publishes when a file's analysis is due, and the
// catalogarr-segments-plan [Planner] that asks segmentarr-worker for a
// season or a movie, reading the Episode and MediaFile field indexes of the
// manager's cache; and [Sweeper], which deletes the clustarr-segments
// records of files that are gone (loop spec §4.11). The analysis comes back
// as a clustarr-segments record the loop incorporates (ADR-0016).
package segmentplan
