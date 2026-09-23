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

package fileimport

import (
	"regexp"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// TEMPORARY -- delete this file when pkg/release stops mis-parsing release
// groups; see importarr/worker/rescan/releasegroup.go, which carries the
// same guard for the same reason and is this file's origin. Duplicated
// rather than shared because rescan's copy is unexported and the defect
// (pkg/release attributing quality tokens to ParsedRelease.Group on two
// common on-disk layouts) hits this worker identically: MediaFileSpec.
// ReleaseGroup is frozen at import here exactly as it is there, and
// ReleaseGroup also feeds pkg/quality/catalogue's custom-format scoring
// (spec §9), so a contaminated group would corrupt formatScore too, not
// only the displayed name.
//
// resolutionToken matches a bare vertical-resolution token: 480p, 720p,
// 1080p, 1080i, 2160p.
var resolutionToken = regexp.MustCompile(`^[0-9]{3,4}[ip]$`)

// qualityWords are source, container and modifier words that turn up as
// mis-attributed group segments, matched whole rather than as substrings.
var qualityWords = map[string]bool{
	"bluray": true, "bdrip": true, "brrip": true, "bdmux": true,
	"web": true, "webdl": true, "webrip": true, "webmux": true,
	"hdtv": true, "sdtv": true, "pdtv": true, "tvrip": true,
	"dvd": true, "dvdrip": true, "dvdscr": true,
	"hdrip": true, "remux": true, "uhd": true, "hddvd": true,
	"workprint": true, "telesync": true, "telecine": true, "screener": true,
}

// releaseGroupOrEmpty returns the parsed release group, or "" when it is
// visibly a quality token rather than a group. See
// importarr/worker/rescan/releasegroup.go for the full rationale.
func releaseGroupOrEmpty(parsed *release.ParsedRelease) string {
	group := strings.TrimSpace(parsed.Group)
	if group == "" {
		return ""
	}
	if strings.EqualFold(group, parsed.Quality.Name) {
		return ""
	}
	for _, segment := range strings.Split(group, "-") {
		if isQualityToken(segment, parsed.Quality) {
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
	case quality.Source != commonv1.SourceUnknown && strings.EqualFold(segment, string(quality.Source)):
		return true
	case quality.Modifier != "" && quality.Modifier != commonv1.ModifierNone &&
		strings.EqualFold(segment, string(quality.Modifier)):
		return true
	}
	return false
}
