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

package rename

// The rename's spec apply is server-side apply, which is the patch verb; the
// item and RootFolder reads and the LibraryScan check go through the
// manager's cache. The recorder is an events.k8s.io/v1 EventRecorder
// (mgr.GetEventRecorder). The blank line keeps the markers package-level.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;rootfolders;libraryscans,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
