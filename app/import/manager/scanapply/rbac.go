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

package scanapply

// The scan applier's RBAC (ADR-0019 §7.6): it creates and updates MediaFile
// spec (import-worker) and the observed-fingerprint annotation (import),
// deletes the MediaFile of a file gone from disk, creates the Movie or
// Series a scan's attribution proposes, reads every item kind a file may
// belong to (a part file's MediaFile among them), and
// creates the LibraryScan that re-observes a folder whose basis moved.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes;artists;albums;authors;books;audiobooks;comics;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=libraryscans,verbs=get;list;watch;create
