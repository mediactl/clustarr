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
// The policy pointers' defaults are read in app/transcode/worker (ReplaceSource,
// MaxOutputToSourcePercent, ...), which predates this file.

// DefaultQuality is an unset TranscodeProfileSpec.quality (no CRD default:
// see the field).
const DefaultQuality int32 = 24

// QualityOrDefault is spec.quality; unset means DefaultQuality.
func (s *TranscodeProfileSpec) QualityOrDefault() int32 {
	if s == nil || s.Quality == nil {
		return DefaultQuality
	}
	return *s.Quality
}
