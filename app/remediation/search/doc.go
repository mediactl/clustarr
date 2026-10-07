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

// Package search is the remediation loop's search stage (ADR-0019 §7.3,
// A4.2): the loop's adapter of app/catalog/searchplan on the searchable
// item keys (Movie, Episode, Album, Book, Audiobook, Issue; ruling R12).
// It runs after the downloads stage, whose outcome carries the owner's
// fresh entries for a redownload (R25).
//
// It gathers the item's sweep candidate (wantedcron.CandidateOf: wanted,
// its attempts with lastSearchedAt folded in, a donor wanted), its and its
// container's monitoring, its resolved profile, the DownloadClients'
// protocols and the Indexers' priorities (the task carries both, P7 and
// P9's finding), the record at its key in clustarr-searches, the donor want
// and the covering failures; runs searchplan.Decide under the dispatch
// ledger (catalog-search-normal) and the Pacer's live-search class; and
// returns catalog-grab's searchDispatch, lastSearchedAt and attempt
// counters as part of the one set the grab stage completes, a
// DispatchPublish of the SearchTask v2, a DispatchAnswered on
// incorporation, and its Due.
//
// Its source is S29 (clustarr-searches): a record wakes its item and its
// container. wantedcron's sweep wakes every candidate at low priority
// (ItemReconciler.WantedWakes). LedgerSource rebuilds the ledger's
// outstanding searches after a leader change.
//
// On a Series it is the season planner (Wave S, series search design §4):
// a person's series or season search -- a Search with grabBest on a series,
// found through seriessearch's index and woken by S31 -- is served from
// status.seasonSearch by searchplan.DecideSeasons, one season task in
// flight behind the namespace's live-search pace and admitted on
// catalog-search-high, its record read at the series' season key.
// seasonSearch and lastSearchedAt join catalog-grab's set beside the grab
// stage's pendingGrabs. SeasonLedgerSource rebuilds the season dispatches
// after a leader change. An Episode reads its Series' state: it waits for
// a pending season and folds an answered one once. Nothing here publishes
// an Episode search for a season.
package search
