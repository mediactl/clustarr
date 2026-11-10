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

// Package backlog is the backlog search's pure half
// (docs/superpowers/specs/2026-11-10-backlog-search-design.md): Select, a
// cycle's choice of the wanted items whose last live search is oldest, and
// Book, the leader-local grants the BacklogSearch controller
// (app/catalog/controller/backlogsearch) gives and the search stage
// (app/remediation/search) reads and spends.
//
// An item is searched live once, at its first automatic search; every
// later one asks only the release index its indexers' RSS feeds filled
// (searchplan, P12). A grant lets one item make one more live search,
// behind the namespace's live-search pace and the dispatch ledger like any
// other, so a backlog is worked through a few searches a cycle and never
// in a burst.
package backlog
