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

// Package searchplan is the item key's search planner (ADR-0019 §7.3,
// §5.1, §5.4; §3.5 P1-P7, P11, P12): pure, it decides whether a searchable
// item is due a search, dispatches it under admission and pacing, and keeps
// its bookkeeping -- the per-item half of the wanted sweep the catalog
// agent's WantedScan expansion used to run.
//
// An item is due when it is searchable, monitored (an Episode's Series
// too), has a resolved profile, is wanted (missing or below its cutoff) and
// its backoff has elapsed (6 h · 2^n capped at 7 d, wantedcron.Eligible), or
// at once while a Failed or Blocklisted entry covering it asks a
// redownload (ruling R25). A search is IndexOnly once the item was searched
// before (P12, CLAUDE.md 2026-10-06: a pool of searches is never fired at
// an indexer in one burst); a live one reserves the namespace's pacing
// class first (records.SearchClass, 20/min by default), and every one is
// admitted through the dispatch ledger on catalog-search-normal. An audio
// donor search (P4) is due on its own backoff, and only DonorSearchesPerSweep
// first-time live ones per namespace per wantedcron period (DonorBudget).
//
// The record at the item's key answers the dispatch at its seq: Decide
// incorporates it -- answeredSeq, lastSearchedAt, the attempt counter
// unless every indexer was paced (P6) -- and the item key's grab planner
// reads its candidates (grabplan, R7). A dispatch outstanding past Timeout
// closes without counting.
//
// DecideSeasons is the planner's second mode (Wave S, series search design
// §4): the Series key serves a person's series or season search -- a
// Search with grabBest on a series -- from status.seasonSearch, one request
// at a time and one season task in flight, each behind the namespace's
// live-search pace and admitted on catalog-search-high (SeasonDurable).
// Nothing here publishes an Episode search for a season: an Episode folds
// its season's answer into its lastSearchedAt and attempts once
// (View.SeasonAnsweredAt), so its next search is IndexOnly, and waits while
// its season is pending (View.SeasonPending).
package searchplan
