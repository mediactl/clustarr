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

// Package series implements the Series controller: episode numbering and
// naming, the add-time and steady-state monitoring decision, the season
// rollup and phase as pure functions, and a thin Reconciler that fans a
// Series out into owned Episode objects per spec §8.1.
//
// Series is the sole writer of status.phase, status.path, status.seasons,
// status.episodeCount and status.episodeFileCount; status.metadata belongs
// to the metadata gateway (Task C5, field manager
// k8s.ManagerCatalogarrWorker). The per-Episode provider fields
// (title/overview/airDate/tvdbID/absoluteNumber/runtimeMinutes) are written
// by this reconciler too, but under the same k8s.ManagerCatalogarr field
// manager as the Episode controller's own Phase/Conditions write -- the two
// stay on disjoint fields by convention, not by a field-manager split (see
// the episode package's doc comment).
package series
