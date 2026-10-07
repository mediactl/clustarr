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

package probe

// The probe planner reads a file's TranscodeJobs, AudioGrafts and the
// TranscodeProfile a swap ran under through the manager's cache (moved from
// the mediafile controller, F3.4). The blank line keeps the markers
// package-level.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs;audiografts,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
