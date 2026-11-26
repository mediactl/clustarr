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

// Package library is the library store (ADR-0024, design
// docs/superpowers/specs/2026-11-26-library-store-design.md): the kinds a
// controller creates -- MediaFile, Episode, Album, Book, Audiobook, Issue --
// held as rows in Postgres where they were custom resources in the
// apiserver.
//
// The storage moves and the model does not. A library object keeps its Go
// type, its name, its spec and status, its finalizers and its owners, and a
// write to one follows the rules the apiserver held it to:
//
//   - the kind's schema is the CRD controller-gen still renders for it
//     (schemas/, embedded, never installed): defaults, pruning, the value
//     checks and the validation rules come from that one file;
//   - field ownership is the apiserver's own code
//     (k8s.io/apimachinery/pkg/util/managedfields) run on the row, so an
//     apply merges, releases and conflicts exactly as it did; the ownership
//     records live in the row's managed column, read by a write and by no
//     cache;
//   - resourceVersion is the row's version, stamped by a trigger from the
//     kind's counter in library.head, and a precondition on it is a Conflict
//     where it is one today; generation, finalizers with a deletion
//     timestamp, and the status subresource's split are kept as the
//     apiserver's strategies have them.
//
// The change feed is library.change: one entry per write, in commit order by
// construction -- a write takes its sequence number from its kind's row in
// library.head and holds that row until it commits, so an entry a reader can
// see never has an unseen one below it. A write that changes nothing writes
// nothing: no version, no entry, no wake.
//
// [Store] is the database; [ListerWatcher] feeds an informer from it;
// [Client] wraps a controller-runtime client so the library kinds are read
// and written here and every other kind passes through.
package library
