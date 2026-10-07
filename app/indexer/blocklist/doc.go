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

// Package blocklist is the body of clustarr.rpc.indexarr.blocklist
// (ADR-0019 §6.14): the release index persists the blocks and unblocks the
// manager decided, and lists them. The verb never decides: a block is the
// owner key's Blocklisted transition or a person's "remove … blocklist", an
// unblock a person's download.clustarr.io/unblock, and this package writes
// exactly what it is asked, fenced by the request's seq so a late block
// cannot undo a later unblock.
//
// It runs in the index agent, beside app/indexer/query, registered in
// app/indexer/search.Serve's verbs table under queue group indexarr.
package blocklist
