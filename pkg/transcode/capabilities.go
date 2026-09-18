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

package transcode

// Capabilities reports which of the four tiers' hardware encoders are
// present in this node's ffmpeg build.
type Capabilities struct{ Encoders map[Tier]bool }

// FallbackTier tries the one documented intel fallback (qsv -> vaapi, note
// §4.2/§4.3: QSV is preferred when the libvpl runtime is present, VAAPI is
// the vendor-neutral fallback on the same node). Any other unavailable tier
// has no fallback and FallbackTier returns (want, false).
func FallbackTier(want Tier, caps Capabilities) (Tier, bool) {
	if caps.Encoders[want] {
		return want, true
	}
	if want == TierQSV && caps.Encoders[TierVAAPI] {
		return TierVAAPI, true
	}
	return want, false
}
