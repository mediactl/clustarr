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

// resolutionClass buckets a source's frame height into the three CRFTable
// tiers.
func resolutionClass(height int32) string {
	switch {
	case height <= 576:
		return "sd"
	case height <= 1080:
		return "hd"
	default:
		return "uhd"
	}
}

// CRFFor picks the x265 constant-rate-factor for a source of the given
// height, applying the table's HDROffset when hdr is true. Exported so the
// Args golden fixtures can assert against it independently of Plan.
func CRFFor(t CRFTable, height int32, hdr bool) int32 {
	var base int32
	switch resolutionClass(height) {
	case "sd":
		base = t.SD
	case "hd":
		base = t.HD
	default:
		base = t.UHD
	}
	if hdr {
		base += t.HDROffset
	}
	return base
}
