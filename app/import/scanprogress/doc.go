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

// Package scanprogress is a LibraryScan's checkpoint as the rescan worker
// writes it to the clustarr-progress bucket and the LibraryScan controller
// folds it into status: Progress, its unmatched and renamed entries, and
// the KV key. It is plain data with no Kubernetes client, so the manager's
// controller reads it without linking the worker (spec §4.3 I1).
package scanprogress
