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

// Package seriessearch is the manager's index of planned Searches by series
// (Wave S, series search design §4.3): a Search on a series with grabBest
// (SearchSpec.PlannedBySeries) is the Series key's to plan, and the Series
// finds the Searches naming it, and the Search controller the Searches a
// Series serves, through IndexSearchesBySeries. A leaf: it imports no
// remediation package, so the Search controller and the loop both read it.
package seriessearch
