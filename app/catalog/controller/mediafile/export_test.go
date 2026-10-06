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

package mediafile

// The per-reconcile lookups, for the envtests in package mediafile_test
// that hold them to a raw client and a manager cache alike.
var (
	TranscodeJobsOf = (*Reconciler).transcodeJobsOf
	ScanSidecars    = (*Reconciler).scanSidecars
)

// MediaFileRefIndex is the field the two lookups above select on: the
// SubtitleRequest selectable field's JSONPath and both cache indexes' name.
const MediaFileRefIndex = subtitleRequestMediaFileRefIndex

// AudioGraftMediaFileRefIndex is the AudioGraft cache index's name.
const AudioGraftMediaFileRefIndex = audioGraftMediaFileRefIndex
