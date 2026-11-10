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

// Package seriessearch is the manager's index of planned Searches by their
// container (Wave S, series search design §4.3): a Search on a series with
// grabBest (SearchSpec.PlannedBySeries) is the Series key's to plan, and
// the Series finds the Searches naming it, and the Search controller the
// Searches a Series serves, through IndexSearchesBySeries. Since 2026-11-10
// (the non-video design D10) a comic's volume search with grabBest
// (SearchSpec.PlannedByComic) is indexed the same way, keyed by the Comic's
// name: the Comic key serves it from status.volumeSearch and the Search
// controller mirrors that state as it mirrors a Series' seasonSearch. The
// index keys by name alone, so a reader of one kind filters a same-named
// container of the other by spec.mediaRef.kind. A leaf: it imports no
// remediation package, so the Search controller and the loop both read it.
package seriessearch
