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

// Package grab is the remediation loop's grab stage (ADR-0019 §6.6, A4.1):
// the loop's adapter of app/catalog/grabplan, run first in every owner
// pass.
//
// On an owner key (Movie, Series, Album, Book, Audiobook, Comic) it gathers
// the candidates -- the intake inbox's parked candidates for the owner
// (the RSS matcher's matches, a Search's picks), the owner's own search
// answer and, for a Series or Comic, the answers of its covered Episodes or
// Issues while each answers its item's current searchDispatch inside
// grabplan.CandidateWindow (ruling R7) -- and the owner's fresh state from
// the cache (its profile, delay profile, entries, pending grabs, covered
// items and their current files, the DownloadClients' protocols, the
// Indexers' seed criteria and the grab ring, read without reserving), runs
// grabplan.Decide, and returns the new Pending entries for the downloads
// stage, catalog-grab's pendingGrab (or pendingGrabs) as a complete set,
// a SettleCandidate per intake candidate, a release.grabbed history event
// per grab, and the soonest grabAt as Due. It also hands the downloads
// stage grabplan.StillWants over the same view (§6.7).
//
// On an Episode or Issue key it is a view: the container's pending grab
// covering the item becomes the item's pendingGrab, under the manager that
// owns it (§6.2).
package grab
