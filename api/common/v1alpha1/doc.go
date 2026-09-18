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

// Package v1alpha1 holds the Go-only types shared by every Clustarr API group.
//
// Nothing in this package is a CRD root type: there is no API group, scheme or
// GroupVersion here. The catalog, index, download, transcode and subtitle groups
// embed these types in their own CRDs, so they carry kubebuilder validation
// markers and have generated DeepCopy methods.
//
// Import it as:
//
//	common "github.com/mediactl/clustarr/api/common/v1alpha1"
//
// +kubebuilder:object:generate=true
package v1alpha1
