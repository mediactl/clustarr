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

// Package downloads is the remediation loop's downloads stage (ADR-0019
// §6, A3.5): the loop's adapter of app/grab/lifecycle. On an owner key
// (Movie, Series, Album, Book, Audiobook, Comic) it gathers the evidence --
// the owner's entries, intents and nonces; each entry's transfer record and
// import records; the transfers no entry claims; the DownloadClients and
// their engine records; the MediaFiles naming each entry; the block book's
// replies -- from the cache and the records buckets only, runs
// lifecycle.Decide, and turns the plan into the pass's catalogarr
// contribution and its effects: the item finalizer, blocklist calls,
// engine commands, import tasks, payload removals, MediaFile
// materialisation, direct-grab counts and history. On an Episode or Issue
// key it renders the view of its container's entries and owes nothing.
//
// Its sources are S27 (clustarr-transfers to the owner), S28
// (clustarr-imports to the owner) and an engine's new boot (every owner
// with an entry pinned to that engine, which republishes desired state).
//
// It is OwnerGone: a key the cache has no object for, that the leader-local
// index of unclaimed transfers holds, is decided on one APIReader Get --
// NotFound (or another item reusing the name) removes the transfers with
// their claims' data rule; anything else does nothing (§6.7: no evidence,
// no action).
package downloads
