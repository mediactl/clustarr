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

// Package retrigger re-queues a blocked import when a person sets or
// changes a Download's import-target or import-override annotation: it
// republishes the Download's ImportTask on clustarr.work.import-file, and
// the file-import worker does the rest. It is a leader-elected manager
// reconciler that reads Downloads and publishes one message, so it
// lives outside the worker (spec §4.3 I5).
//
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
package retrigger
