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

// Package importliststate is what the ImportList controller and the
// list-sync worker share: the sync Result checkpointed in the
// clustarr-progress bucket, the synced-at annotation that wakes the
// controller, the kinds a list's provider can yield, and the Trakt token
// Secret store the device flow and the sync both use. It links no
// app/import/worker package, so the manager's ImportList controller does
// not link the list worker (spec §4.3 I3).
package importliststate
