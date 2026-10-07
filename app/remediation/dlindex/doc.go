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

// Package dlindex is the manager's one index of grab entries
// (ADR-0019 §6.11): remediation.item.download, on the six owner kinds
// (Movie, Series, Album, Book, Audiobook, Comic), whose values are every
// entry's id and uid and, per entry, "engine:<entry.engine>". It replaced
// the six .spec.target.<kind> Download indexes: history and dead letters
// resolve an entry to its owner through it (OwnerOf), the advisory intake a
// terminated task's entry UID, and the DownloadClient controller the owners
// pinned to an engine (PinnedTo), whose new boot is a resync.
//
// The remediation loop registers it once (remediation.RegisterIndexes).
package dlindex
