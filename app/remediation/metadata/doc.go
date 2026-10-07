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

// Package metadata is the item keys' metadata stage (ADR-0019 §7.1, A5.1):
// it incorporates the metadata gateway's answer -- a clustarr-item-metadata
// record -- into status.metadata, together with status.artwork, as
// catalog-metadata's one complete set (app/catalog/metadataset), for the
// six loop kinds with metadata of their own: Movie, Series, Album, Book,
// Audiobook and Comic. The Artist and Author reconcilers, which are not loop
// keys, incorporate theirs through the same renderer.
//
// Due stays the kind's: each kind decides staleness and publishes its
// MetadataTask v2 itself, admitted through the pass (itempass.Admit, ruling
// R23). This stage answers the ledger when it incorporates (DispatchAnswered)
// and is woken by the record (S25).
//
// It writes nothing itself: the set rides the item pass, which applies it
// after the kind's catalog apply, chained on its resourceVersion (§7.0).
package metadata

// The stage's applies: status.metadata and status.artwork under
// catalog-metadata on the six loop kinds.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;albums;books;audiobooks;comics,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status;albums/status;books/status;audiobooks/status;comics/status,verbs=get;update;patch
