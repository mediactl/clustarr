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

// Package markers is the catalog-markers durable's handler (spec
// 2026-09-30 plex-analyze-bypass §3; loop spec 2026-10-06 §4.12): [Handler]
// asks the gateway's markers providers (TheIntroDB) for one MediaFile's skip
// segments and answers into the file's clustarr-markers record by
// compare-and-swap. It reads no Kubernetes object and writes no status: the
// task carries the query the remediation loop built from its cache, and the
// loop's markers planner incorporates the answer into status.markers. It
// runs beside the metadata gateway, which calls [Setup] once it has built
// its registry.
package markers
