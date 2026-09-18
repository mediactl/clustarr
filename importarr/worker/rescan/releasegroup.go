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

package rescan

import (
	"regexp"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// TEMPORARY -- delete this file when pkg/release stops mis-parsing release
// groups. Tracked as the pkg/release group defect; Phase B work, outside
// importarr's paths.
//
// pkg/release attributes quality tokens to ParsedRelease.Group on two of the
// commonest on-disk layouts:
//
//	Heat (1995) - Bluray-1080p.mkv           -> Group "1080p"
//	Heat.1995.Bluray-1080p-RlsGrp.mkv        -> Group "1080p-RlsGrp"
//
// while the ordinary scene layouts parse correctly (SPARKS, NTb, FraMeSToR,
// TERMiNAL). Normally a parser bug would just be filed, but spec §8.4 FREEZES
// MediaFileSpec.ReleaseGroup at import, and TRaSH custom formats score on
// release group -- so a bad value becomes permanent and skews every later
// upgrade decision for that file. An empty group is merely unknown; a wrong
// one is a lie the catalog then acts on.
//
// releaseGroupOrEmpty is therefore a narrow, deliberately dumb guard: it
// drops a group that is visibly made of quality tokens, and never tries to
// salvage the real group out of one (that would be re-implementing the
// parser). It is the only place in this package that second-guesses
// pkg/release.

// resolutionToken matches a bare vertical-resolution token: 480p, 720p,
// 1080p, 1080i, 2160p.
var resolutionToken = regexp.MustCompile(`^[0-9]{3,4}[ip]$`)

// qualityWords are source, container and modifier words that turn up as
// mis-attributed group segments. They are matched whole, never as
// substrings, so a real group is not caught by containing one of them: the
// point is to recognise a segment that IS a quality token, not one that
// mentions something.
var qualityWords = map[string]bool{
	"bluray": true, "bdrip": true, "brrip": true, "bdmux": true,
	"web": true, "webdl": true, "webrip": true, "webmux": true,
	"hdtv": true, "sdtv": true, "pdtv": true, "tvrip": true,
	"dvd": true, "dvdrip": true, "dvdscr": true,
	"hdrip": true, "remux": true, "uhd": true, "hddvd": true,
	"workprint": true, "telesync": true, "telecine": true, "screener": true,
}

// releaseGroupOrEmpty returns the parsed release group, or "" when it is
// visibly a quality token rather than a group.
func releaseGroupOrEmpty(parsed *release.ParsedRelease) string {
	group := strings.TrimSpace(parsed.Group)
	if group == "" {
		return ""
	}

	// The whole group being the quality's own name is the clearest case.
	if strings.EqualFold(group, parsed.Quality.Name) {
		return ""
	}

	for _, segment := range strings.Split(group, "-") {
		if isQualityToken(segment, parsed.Quality) {
			// One contaminated segment condemns the whole group: the
			// remainder may well be the real name, but picking it out
			// is exactly the parser work this guard must not attempt.
			return ""
		}
	}
	return group
}

// isQualityToken reports whether one "-"-separated segment of a parsed group
// is really part of the quality identity.
func isQualityToken(segment string, quality commonv1.Quality) bool {
	segment = strings.ToLower(strings.TrimSpace(segment))
	if segment == "" {
		return false
	}
	switch {
	case resolutionToken.MatchString(segment):
		return true
	case qualityWords[segment]:
		return true
	// The parsed quality's own source and modifier, so the guard tracks the
	// vocabulary pkg/release actually produces rather than only the static
	// list above.
	case quality.Source != commonv1.SourceUnknown && strings.EqualFold(segment, string(quality.Source)):
		return true
	case quality.Modifier != "" && quality.Modifier != commonv1.ModifierNone &&
		strings.EqualFold(segment, string(quality.Modifier)):
		return true
	}
	return false
}
