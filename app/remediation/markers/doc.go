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

// Package markers is the remediation loop's markers planner (loop spec §3.5,
// §4.12): status.markers from TheIntroDB's clustarr-markers records and the
// segment analysis's clustarr-segments records; TheIntroDB asked through a
// record and a task; the segment plan published when analysis is due. The
// decision is app/catalog/markers.Plan; the manager imports this package as
// markersplanner.
package markers
