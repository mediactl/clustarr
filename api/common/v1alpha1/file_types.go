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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FileFingerprint identifies a specific revision of a media file on disk
// cheaply, without re-hashing its contents: a file whose size and modification
// time are unchanged is the same file, so cached probe results, existing
// subtitles, provider hashes and transcode plans stay valid and work is not
// repeated.
//
// Promoted from api/subtitle/v1alpha1; the catalog group carries the same
// size+mtime pair inline on MediaFile.spec.
type FileFingerprint struct {
	// SizeBytes is the size of the media file in bytes.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// ModTime is the modification timestamp of the media file, truncated to
	// whole seconds as Kubernetes timestamps are.
	// +optional
	ModTime *metav1.Time `json:"modTime,omitempty"`
}
