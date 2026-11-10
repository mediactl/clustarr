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

package metrics

// Backlog search telemetry (docs/superpowers/specs/2026-11-10-backlog-search-design.md),
// manager only.
var (
	// BacklogSearchesTotal counts the backlog searches cycles started, by
	// queue (missing, cutoffUnmet) and unit (item, season).
	BacklogSearchesTotal = newCounterVec("clustarr_backlog_searches_total",
		"Backlog searches started, by reason and unit.", "reason", "unit")
	// BacklogItems is the backlog as the last cycle found it, by queue and
	// state: candidate (wanted, searched before) or due (past the queue's
	// cooldown). Not by namespace (an unbounded label): with BacklogSearches
	// in several namespaces it is the last one to cycle, and their status
	// is the per-namespace view.
	BacklogItems = newGaugeVec("clustarr_backlog_items",
		"Backlog items at the last cycle, by reason and state.", "reason", "state")
)
