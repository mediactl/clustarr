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

package quality

import (
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/release"
)

// remuxFloorKbps: below this an AVC/HEVC stream is an encode, not a disc remux.
const remuxFloorKbps = 20000

// AugmentFromMediaInfo corrects a name-derived Quality's resolution and a
// false remux modifier against the probe's ffprobe result, without ever
// touching the source: the probe never supplies one, so a name-derived
// source (or the lack of one) is left exactly as it was parsed. changed
// reports whether anything moved, so a caller can skip re-deriving a name
// when the probe agreed with the parse.
func AugmentFromMediaInfo(q commonv1.Quality, mi *commonv1.MediaInfo) (commonv1.Quality, bool) {
	if mi == nil || mi.Width == 0 || mi.Height == 0 {
		return q, false
	}
	out := q
	if res := mediainfo.ResolutionFromDimensions(mi.Width, mi.Height); res != 0 && res != q.Resolution {
		out.Resolution = res
	}
	if q.Modifier == commonv1.ModifierRemux && mi.VideoBitrateKbps > 0 && mi.VideoBitrateKbps < remuxFloorKbps {
		switch strings.ToLower(mi.VideoCodec) {
		case "h264", "hevc", "vc1":
			out.Modifier = commonv1.ModifierNone
		}
	}
	if out == q {
		return q, false
	}
	// qualityTable is keyed on commonv1.ModifierNone ("none"), but a Quality
	// literal that never set Modifier carries the Go zero value ("") instead
	// -- the typed-client defaulting trap CLAUDE.md warns about. Normalize
	// only for the lookup key; out.Modifier itself is left exactly as the
	// resolution and remux corrections above produced it.
	lookupMod := out.Modifier
	if lookupMod == "" {
		lookupMod = commonv1.ModifierNone
	}
	if named, ok := release.QualityFor(out.Source, out.Resolution, lookupMod); ok {
		out.Name = named.Name
	} else {
		out.Name = "Unknown"
	}
	return out, true
}
