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
// applied. The apiserver fills an absent field on write, but an object built
// in Go and never round-tripped through it -- a unit test's fixture, a spec a
// controller renders -- holds nil, which must still read as the default.
// Every consumer reads these fields through here, so the default is restated
// once, next to the marker it mirrors (api/subtitle/v1alpha1/defaults.go is
// the same convention). They are plain Go methods and generate nothing.

// DefaultRecycleBinCleanupDays mirrors RecycleBin.cleanupDays'
// +kubebuilder:default.
const DefaultRecycleBinCleanupDays int32 = 7

// CleanupDaysOrDefault is recycleBin.cleanupDays; unset means
// DefaultRecycleBinCleanupDays, and an explicit 0 disables the cleanup.
func (r RecycleBin) CleanupDaysOrDefault() int32 {
	if r.CleanupDays == nil {
		return DefaultRecycleBinCleanupDays
	}
	return *r.CleanupDays
}
