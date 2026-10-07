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

package importliststate

// AnnotationSyncedAt is stamped on an ImportList by the worker when a sync
// finishes, with the checkpointed Result's SyncedAt (RFC 3339, nanoseconds).
// It is how the ImportList controller learns a sync completed: its For()
// predicate passes a change to this value, so the new Result is projected
// into status as soon as it lands rather than at the next scheduled sync
// (nextSyncAt, up to a day away). The value is only a signal; the Result in
// the clustarr-progress bucket stays the source of what status says.
const AnnotationSyncedAt = "catalog.clustarr.io/importlist-synced-at"
