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

package v1alpha1

// These accessors read the scalar fields that are pointers only so a Go
// client can send an explicit zero, with each field's +kubebuilder:default
// applied. An object built in Go and never round-tripped through the
// apiserver holds nil, which must still read as the default. Every consumer
// reads these fields through here, so the default is restated once, next to
// the marker it mirrors (api/subtitle/v1alpha1/defaults.go is the same
// convention). They are plain Go methods and generate nothing.
//
// The policy pointers' defaults are read in app/squash/worker (ReplaceSource,
// MaxOutputToSourcePercent, ...), which predates this file.

// DefaultHDROffset mirrors CRFTable.hdrOffset's +kubebuilder:default.
const DefaultHDROffset int32 = -1

// HDROffsetOrDefault is video.crf.hdrOffset; unset means DefaultHDROffset,
// and an explicit 0 applies no HDR offset.
func (t CRFTable) HDROffsetOrDefault() int32 {
	if t.HDROffset == nil {
		return DefaultHDROffset
	}
	return *t.HDROffset
}
