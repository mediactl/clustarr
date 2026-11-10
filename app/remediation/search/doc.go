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
// item keys (Movie, Episode, Album, Book, Audiobook, Issue; ruling R12)
// and on the two containers whose children are searched as a pack first,
// Series and Comic.
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
// An item added with searchForMissing false (2026-11-10 design D10, D11;
// addedUnsearched) -- a Movie's searchForMovie or an Audiobook's own flag,
// a Series', Comic's, Artist's or Author's for a child that was already out
// when the container was created, read whatever addOptionsApplied says --
// is searchplan.View.NoSearchOnAdd: its first automatic search asks the
// release index alone, and the season and volume facts count it toward no
// pack query (SeasonFacts.FirstSearch), so the automatic pack search is not
// asked for it and it does not wait for one. A child released after its
// container was added is new and searches as any item does, so a series
// the rescan added with the flag off still searches every episode that
// airs from then on live.
//
// Its sources are S29 (clustarr-searches): a record wakes its item and its
// container; and S32: an item's own status write that moves its sweep
// reason or its donor want wakes it at low priority, since this stage runs
// before the kind's pass that writes the phase. wantedcron's sweep wakes
// every candidate at low priority (ItemReconciler.WantedWakes). LedgerSource rebuilds the ledger's
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
//
// On a Comic it is the same planner over the one season that is the volume
// (2026-11-10 design D10, the Series' season machinery with the season
// layer removed): a Comic passing searchplan.VolumePackFirst (monitored,
// two or more monitored, released issues missing) is asked for as a
// volume pack by the automatic search -- a Comic has no planned request,
// its Search CR fanning out per Issue -- in status.volumeSearch, which
// joins catalog-grab's set beside pendingGrabs; its task names the Comic
// (MediaRef kind comic, season VolumeNumber) and is answered at the Comic's
// season key. An Issue reads its Comic's state as an Episode reads its
// Series': it waits for a pending volume, waits for the automatic volume
// search before its first live search while the Comic passes
// VolumePackFirst, and folds an answered volume into its lastSearchedAt
// and lastLiveSearchAt once. S33 wakes the Comic's Issues when its
// volumeSearch moves. Nothing here publishes an Issue search for a volume.
package search
