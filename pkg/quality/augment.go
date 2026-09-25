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
//
// The corrected (source, resolution, modifier) is placed on a quality the
// table defines (findBySourceAndResolution), never left on a triple no
// Definition holds: a quality the name gave a known name never augments to
// "Unknown", which no profile tier would admit.
func AugmentFromMediaInfo(q commonv1.Quality, mi *commonv1.MediaInfo) (commonv1.Quality, bool) {
	if mi == nil || mi.Width == 0 || mi.Height == 0 {
		return q, false
	}
	res, mod := q.Resolution, q.Modifier
	if probed := mediainfo.ResolutionFromDimensions(mi.Width, mi.Height); probed != commonv1.ResolutionUnknown {
		res = probed
	}
	if q.Modifier == commonv1.ModifierRemux && mi.VideoBitrateKbps > 0 && mi.VideoBitrateKbps < remuxFloorKbps {
		switch strings.ToLower(mi.VideoCodec) {
		case "h264", "hevc", "vc1":
			mod = commonv1.ModifierNone
		}
	}
	if res == q.Resolution && mod == q.Modifier {
		return q, false
	}
	// qualityTable is keyed on commonv1.ModifierNone ("none"), but a Quality
	// literal that never set Modifier carries the Go zero value ("") instead
	// -- the typed-client defaulting trap CLAUDE.md warns about. Normalize
	// only for the lookup key; the result keeps the modifier exactly as the
	// remux correction above left it, since every fallback keeps the
	// modifier it was asked for.
	lookupMod := mod
	if lookupMod == "" {
		lookupMod = commonv1.ModifierNone
	}
	found, ok := findBySourceAndResolution(q.Source, res, lookupMod)
	if !ok {
		return q, false
	}
	out := commonv1.Quality{Name: found.Name, Source: q.Source, Resolution: found.Resolution, Modifier: mod}
	if out == q {
		return q, false
	}
	return out, true
}

// findBySourceAndResolution places a probed (source, resolution, modifier)
// on a quality pkg/release's table defines, after Radarr's
// QualityFinder.FindBySourceAndResolution (ruling R10), which the probe's
// resolution buckets need because a source's ladder has gaps:
//
//   - the exact triple, when the table has it;
//   - else the source's one quality defined without a resolution, for this
//     modifier: DVD, SDTV, CAM, TELESYNC, TELECINE and WORKPRINT, which
//     the name parse also keys at ResolutionUnknown -- a DVD rip probed at
//     576 lines is still DVD;
//   - else the source's nearest rung at or below the probed resolution,
//     never above: a 540-line Bluray encode is Bluray-480p. A stream of
//     360 lines or fewer counts as 480p, the lowest resolution any rung is
//     defined at, as Radarr's AugmentQualityFromMediaInfo maps any frame it
//     cannot place higher to R480p;
//   - else nothing (ok false), and the caller keeps the name's quality.
func findBySourceAndResolution(src commonv1.Source, res int32, mod commonv1.Modifier) (commonv1.Quality, bool) {
	if q, ok := release.QualityFor(src, res, mod); ok {
		return q, true
	}
	rungs := release.QualityRungs(src, mod)
	if len(rungs) > 0 && rungs[0].Resolution == commonv1.ResolutionUnknown {
		return rungs[0], true
	}
	atOrBelow := max(res, commonv1.Resolution480p)
	var nearest commonv1.Quality
	found := false
	for _, r := range rungs {
		if r.Resolution <= atOrBelow {
			nearest, found = r, true
		}
	}
	return nearest, found
}
