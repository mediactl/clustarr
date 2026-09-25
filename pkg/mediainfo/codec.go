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

package mediainfo

import "github.com/mediactl/clustarr/pkg/naming"

// FormatVideoCodec is Radarr's MediaInfoFormatter.FormatVideoCodec: the
// probe names the codec, and the release title says whether an AVC or
// HEVC stream was an x264/x265 encode, which the file itself cannot.
//
// It delegates to naming.VideoCodecLabel (ruling R5,
// docs/superpowers/sdd/2026-09-24-probe-driven-naming) so there is exactly
// one implementation of the switch, shared by pkg/naming's
// {MediaInfo VideoCodec}/{MediaInfo Simple}/{MediaInfo Full} tokens and by
// this package's own callers.
func FormatVideoCodec(codecName, videoProfile, releaseTitle string) string {
	return naming.VideoCodecLabel(codecName, videoProfile, releaseTitle)
}
