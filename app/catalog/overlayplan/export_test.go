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

package overlayplan

// InputsDigestAt is InputsDigest under an explicit renderer version, so a
// test can prove a RenderVersion bump moves every digest.
var InputsDigestAt = inputsDigest

// WithCompactAt is WithCompact under explicit renderer versions, so a test
// can show that bumping either redraws the compact rendition.
func (w Want) WithCompactAt(render, compact int, it Item, compactDigest string) Want {
	return w.withCompact(render, compact, it, compactDigest)
}
