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

// These accessors read the *bool fields that are pointers only so a Go
// client can send an explicit false, with each field's +kubebuilder:default
// applied. The apiserver fills an absent field on write, but an object built
// in Go and never round-tripped through it -- a unit test's fixture, a spec a
// controller renders -- holds nil, which must still read as the default.
// Every consumer reads these fields through here, so the default is restated
// once, next to the marker it mirrors. They are plain Go methods, not part of
// the API surface, and generate nothing.

// EnabledOrDefault is spec.enabled; unset means true.
func (s SubtitleProviderSpec) EnabledOrDefault() bool { return derefBool(s.Enabled, true) }

// ExtractOrDefault is spec.embedded.extract; unset means true.
func (e EmbeddedSpec) ExtractOrDefault() bool { return derefBool(e.Extract, true) }

// SkipCommentaryOrDefault is spec.embedded.skipCommentary; unset means true.
func (e EmbeddedSpec) SkipCommentaryOrDefault() bool { return derefBool(e.SkipCommentary, true) }

// EnabledOrDefault is spec.upgrade.enabled; unset means true.
func (u UpgradeSpec) EnabledOrDefault() bool { return derefBool(u.Enabled, true) }

func derefBool(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}
