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

// BitrateForChannels applies the per-channel-count bitrate policy from
// docs/research/transcode.md §5.2: channels * perChannelKbps, with a 96kbps
// floor for mono so a single-channel track is never starved.
func BitrateForChannels(channels, perChannelKbps int32) int32 {
	b := channels * perChannelKbps
	if channels <= 1 && b < 96 {
		b = 96
	}
	return b
}
