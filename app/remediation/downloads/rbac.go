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

package downloads

// The downloads stage's grants (ADR-0019 A3.5 step 7): the eight item kinds
// it plans, their status (the catalogarr apply) and finalizers
// (download.clustarr.io/transfers); the DownloadClients and Indexers it
// reads; the MediaFiles it materialises and deletes (A3.8) and the
// AudioGrafts it applies until F7.1 (R26); and its Events.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;albums;books;audiobooks;comics;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status;episodes/status;albums/status;books/status;audiobooks/status;comics/status;issues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/finalizers;series/finalizers;albums/finalizers;books/finalizers;audiobooks/finalizers;comics/finalizers,verbs=update
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloadclients,verbs=get;list;watch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=audiografts,verbs=get;list;watch;create;patch;update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
