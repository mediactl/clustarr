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

package release

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// regexTimeout is CLAUDE.md's stated safety net against catastrophic
// backtracking for every ported *arr regex in this package.
const regexTimeout = 50 * time.Millisecond

// mustCompile wraps regexp2.MustCompile and applies regexTimeout uniformly,
// so every regex in pkg/release shares the same backtracking guard.
func mustCompile(pattern string, opt regexp2.RegexOptions) *regexp2.Regexp {
	re := regexp2.MustCompile(pattern, opt)
	re.MatchTimeout = regexTimeout
	return re
}

// isRegexTimeout reports whether err originated from a regexp2 MatchTimeout.
// dlclark/regexp2 v1.12.0 has no exported sentinel or typed error for this —
// runner.go's checkTimeout returns a bare fmt.Errorf("match timeout after
// %v on input `%v`", ...) — so this is a text match on that message rather
// than errors.Is/errors.As. It still works through any number of layers of
// this package's own %w-wrapping, since fmt.Errorf's %w preserves the
// wrapped error's message text in the resulting Error() string.
//
// Every fallback stage in parseSeries's cascade (tv.go) checks this before
// deciding whether to try the next family: a timeout must propagate
// immediately, not be silently treated as "this family didn't match, try
// the next one."
func isRegexTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "match timeout")
}

// sourceRegex ports Radarr/Sonarr's QualityParser.SourceRegex named-group
// family (docs/research/quality.md §7.1). Alternatives are ordered
// most-specific-first within each token family so that, e.g., "DVDSCR"
// resolves to the scr group rather than the generic dvd group — the dvd
// alternative is deliberately last since "DVD" is a substring-prefix of
// several more specific tokens (DVDSCR, DVDR, DVDRip).
var sourceRegex = mustCompile(
	`\b(?:`+
		`(?<bluray>M?Blu[-_. ]?Ray|HD[-_. ]?DVD|UHD2?BD|BDISO|BDMux|BD25|BD50|BR[-_. ]?DISK)|`+
		`(?<webdl>WEB[-_. ]?DL(?:mux)?|AmazonHD|AmazonSD|iTunesHD|MaxdomeHD|NetflixU?HD|WebHD|HBOMaxHD|DisneyHD)|`+
		`(?<webrip>WebRip|Web-Rip|WEBMux)|`+
		`(?<hdtv>HDTV)|`+
		`(?<bdrip>BDRip|BDLight|HD[-_. ]?DVDRip|UHDBDRip)|`+
		`(?<brrip>BRRip)|`+
		`(?<dvdr>\d?x?M?DVD-?[R59])|`+
		`(?<dsr>WS[-_. ]DSR|DSR)|`+
		`(?<regional>REGIONAL)|`+
		`(?<scr>DVDSCREENER|DVDSCR|SCREENER|SCR)|`+
		`(?<ts>TELESYNCH?|HD-TS|HDTS)|`+
		`(?<tc>TELECINE|HD-TC|HDTC|TC)|`+
		`(?<cam>CAMRIP|(?:NEW)?CAM|HD-?CAM(?:Rip)?|HQCAM)|`+
		`(?<wp>WORKPRINT|WP)|`+
		`(?<pdtv>PDTV)|`+
		`(?<sdtv>SDTV)|`+
		`(?<tvrip>TVRip)|`+
		`(?<dvd>DVDRip|xvidvd|DVD)`+
		`)\b`,
	regexp2.IgnoreCase,
)

// sourcePriority is the dispatch order from docs/research/quality.md's
// ParseQualityName priority list. In practice sourceRegex's single overall
// match only ever leaves one named group populated, so this is a
// belt-and-braces dispatch rather than a load-bearing ordering, but it
// mirrors *arr's own if/elif chain.
var sourcePriority = []string{
	"bluray", "webdl", "webrip", "scr", "cam", "ts", "tc", "wp", "regional",
	"hdtv", "bdrip", "brrip", "dvdr", "dvd", "pdtv", "sdtv", "dsr", "tvrip",
}

// resolutionRegex ports ResolutionRegex; AlternativeResolutionRegex's UHD/4K
// signal is folded in as the "2160p" alternative plus the standalone
// uhdTokenRegex fallback below, rather than a second regex with duplicate
// named groups.
var resolutionRegex = mustCompile(
	`(?<R2160p>2160p|3840x2160|4k)|(?<R1080p>1080p|1920x1080|1440p|FHD|1080i)|`+
		`(?<R720p>720p|1280x720|960p)|(?<R576p>576p)|(?<R540p>540p)|`+
		`(?<R480p>480p|480i|640x480|848x480)|(?<R360p>360p)`,
	regexp2.IgnoreCase,
)

var uhdTokenRegex = mustCompile(`\bUHD\b`, regexp2.IgnoreCase)

// remuxRegex ports RemuxRegex, simplified to the literal token: every remux
// release in the fixture corpus and the *arr sample set names it plainly.
var remuxRegex = mustCompile(`\bRemux\b`, regexp2.IgnoreCase)

// brdiskRegex ports BRDISKRegex's intent (a full Blu-ray disc image, not a
// remuxed/encoded file) via two lookaheads: a disc-source token and a
// disc-completeness token, in either order.
var brdiskRegex = mustCompile(
	`(?=.*\b(?:BD|BR|UHD|BLU-?RAY)\b)(?=.*\b(?:COMPLETE|DISK|ISO)\b)`,
	regexp2.IgnoreCase,
)

// rawhdRegex ports RawHDRegex.
var rawhdRegex = mustCompile(`\bRaw[-_. ]?HD\b`, regexp2.IgnoreCase)

// properRegex, repackRegex and versionRegex port ProperRegex, RepackRegex
// and VersionRegex. repackRegex folds VersionRegex's repack(?<version>\d)
// alternative into its own optional trailing digit capture.
var properRegex = mustCompile(`\bproper\b`, regexp2.IgnoreCase)

var repackRegex = mustCompile(`\b(?:repack|rerip)(\d)?\b`, regexp2.IgnoreCase)

// realRegex ports RealRegex, which *arr deliberately leaves case-sensitive:
// scene groups signal a fixed-and-reuploaded proper with uppercase REAL, and
// lowercase "real" appearing incidentally in a title must not count.
var realRegex = mustCompile(`\bREAL\b`, regexp2.None)

// qualityKey is the (source, resolution, modifier) tuple qualityTable is
// keyed by, reproducing Radarr's Quality id table
// (docs/research/naming.md A5).
type qualityKey struct {
	Source     commonv1.Source
	Resolution int32
	Modifier   commonv1.Modifier
}

var qualityTable = map[qualityKey]string{
	{commonv1.SourceTV, commonv1.ResolutionUnknown, commonv1.ModifierNone}:        "SDTV",
	{commonv1.SourceDVD, commonv1.ResolutionUnknown, commonv1.ModifierNone}:       "DVD",
	{commonv1.SourceWebDL, commonv1.Resolution1080p, commonv1.ModifierNone}:       "WEBDL-1080p",
	{commonv1.SourceTV, commonv1.Resolution720p, commonv1.ModifierNone}:           "HDTV-720p",
	{commonv1.SourceWebDL, commonv1.Resolution720p, commonv1.ModifierNone}:        "WEBDL-720p",
	{commonv1.SourceBluray, commonv1.Resolution720p, commonv1.ModifierNone}:       "Bluray-720p",
	{commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierNone}:      "Bluray-1080p",
	{commonv1.SourceWebDL, commonv1.Resolution480p, commonv1.ModifierNone}:        "WEBDL-480p",
	{commonv1.SourceTV, commonv1.Resolution1080p, commonv1.ModifierNone}:          "HDTV-1080p",
	{commonv1.SourceTV, commonv1.Resolution1080p, commonv1.ModifierRawHD}:         "Raw-HD",
	{commonv1.SourceWebRip, commonv1.Resolution480p, commonv1.ModifierNone}:       "WEBRip-480p",
	{commonv1.SourceWebRip, commonv1.Resolution720p, commonv1.ModifierNone}:       "WEBRip-720p",
	{commonv1.SourceWebRip, commonv1.Resolution1080p, commonv1.ModifierNone}:      "WEBRip-1080p",
	{commonv1.SourceTV, commonv1.Resolution2160p, commonv1.ModifierNone}:          "HDTV-2160p",
	{commonv1.SourceWebRip, commonv1.Resolution2160p, commonv1.ModifierNone}:      "WEBRip-2160p",
	{commonv1.SourceWebDL, commonv1.Resolution2160p, commonv1.ModifierNone}:       "WEBDL-2160p",
	{commonv1.SourceBluray, commonv1.Resolution2160p, commonv1.ModifierNone}:      "Bluray-2160p",
	{commonv1.SourceBluray, commonv1.Resolution480p, commonv1.ModifierNone}:       "Bluray-480p",
	{commonv1.SourceBluray, commonv1.Resolution576p, commonv1.ModifierNone}:       "Bluray-576p",
	{commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierBRDisk}:    "BR-DISK",
	{commonv1.SourceDVD, commonv1.Resolution480p, commonv1.ModifierRemux}:         "DVD-R",
	{commonv1.SourceWorkprint, commonv1.ResolutionUnknown, commonv1.ModifierNone}: "WORKPRINT",
	{commonv1.SourceCam, commonv1.ResolutionUnknown, commonv1.ModifierNone}:       "CAM",
	{commonv1.SourceTelesync, commonv1.ResolutionUnknown, commonv1.ModifierNone}:  "TELESYNC",
	{commonv1.SourceTelecine, commonv1.ResolutionUnknown, commonv1.ModifierNone}:  "TELECINE",
	{commonv1.SourceDVD, commonv1.Resolution480p, commonv1.ModifierScreener}:      "DVDSCR",
	{commonv1.SourceDVD, commonv1.Resolution480p, commonv1.ModifierRegional}:      "REGIONAL",
	{commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierRemux}:     "Remux-1080p",
	{commonv1.SourceBluray, commonv1.Resolution2160p, commonv1.ModifierRemux}:     "Remux-2160p",
}

// classifySourceGroup maps a matched sourceRegex group name to the source,
// modifier and, where *arr treats the resolution as fixed by convention
// rather than parsed from a token (e.g. TELESYNC has no resolution concept),
// the fixed resolution to use.
func classifySourceGroup(name string) (src commonv1.Source, fixedRes int32, fixedResSet bool, mod commonv1.Modifier) {
	switch name {
	case "bluray", "bdrip", "brrip":
		return commonv1.SourceBluray, commonv1.ResolutionUnknown, false, commonv1.ModifierNone
	case "webdl":
		return commonv1.SourceWebDL, commonv1.ResolutionUnknown, false, commonv1.ModifierNone
	case "webrip":
		return commonv1.SourceWebRip, commonv1.ResolutionUnknown, false, commonv1.ModifierNone
	case "hdtv", "dsr", "pdtv", "sdtv", "tvrip":
		return commonv1.SourceTV, commonv1.ResolutionUnknown, false, commonv1.ModifierNone
	case "dvdr":
		return commonv1.SourceDVD, commonv1.Resolution480p, true, commonv1.ModifierRemux
	case "dvd":
		return commonv1.SourceDVD, commonv1.ResolutionUnknown, true, commonv1.ModifierNone
	case "regional":
		return commonv1.SourceDVD, commonv1.Resolution480p, true, commonv1.ModifierRegional
	case "scr":
		return commonv1.SourceDVD, commonv1.Resolution480p, true, commonv1.ModifierScreener
	case "ts":
		return commonv1.SourceTelesync, commonv1.ResolutionUnknown, true, commonv1.ModifierNone
	case "tc":
		return commonv1.SourceTelecine, commonv1.ResolutionUnknown, true, commonv1.ModifierNone
	case "cam":
		return commonv1.SourceCam, commonv1.ResolutionUnknown, true, commonv1.ModifierNone
	case "wp":
		return commonv1.SourceWorkprint, commonv1.ResolutionUnknown, true, commonv1.ModifierNone
	}
	return commonv1.SourceUnknown, commonv1.ResolutionUnknown, false, commonv1.ModifierNone
}

var resolutionGroups = []struct {
	name string
	val  int32
}{
	{"R2160p", commonv1.Resolution2160p},
	{"R1080p", commonv1.Resolution1080p},
	{"R720p", commonv1.Resolution720p},
	{"R576p", commonv1.Resolution576p},
	{"R540p", commonv1.Resolution540p},
	{"R480p", commonv1.Resolution480p},
	{"R360p", commonv1.Resolution360p},
}

// detectResolution runs ResolutionRegex, falling back to a bare "UHD" token
// (AlternativeResolutionRegex's intent) when no explicit resolution number
// is present.
func detectResolution(title string) (int32, error) {
	m, err := resolutionRegex.FindStringMatch(title)
	if err != nil {
		return commonv1.ResolutionUnknown, fmt.Errorf("release: quality: resolution match: %w", err)
	}
	if m != nil {
		for _, g := range resolutionGroups {
			if grp := m.GroupByName(g.name); grp != nil && len(grp.Captures) > 0 {
				return g.val, nil
			}
		}
	}
	uhd, err := uhdTokenRegex.MatchString(title)
	if err != nil {
		return commonv1.ResolutionUnknown, fmt.Errorf("release: quality: uhd match: %w", err)
	}
	if uhd {
		return commonv1.Resolution2160p, nil
	}
	return commonv1.ResolutionUnknown, nil
}

// detectQuality runs the full source/resolution/modifier pipeline and looks
// the result up in qualityTable.
func detectQuality(title string) (commonv1.Quality, error) {
	m, err := sourceRegex.FindStringMatch(title)
	if err != nil {
		return commonv1.Quality{}, fmt.Errorf("release: quality: source match: %w", err)
	}

	src := commonv1.SourceUnknown
	mod := commonv1.ModifierNone
	resolution := commonv1.ResolutionUnknown
	resolutionFixed := false

	if m != nil {
		for _, name := range sourcePriority {
			grp := m.GroupByName(name)
			if grp == nil || len(grp.Captures) == 0 {
				continue
			}
			var fixedRes int32
			src, fixedRes, resolutionFixed, mod = classifySourceGroup(name)
			if resolutionFixed {
				resolution = fixedRes
			}
			break
		}
	}

	if !resolutionFixed {
		detected, derr := detectResolution(title)
		if derr != nil {
			return commonv1.Quality{}, derr
		}
		resolution = detected
	}

	switch src {
	case commonv1.SourceBluray:
		brdisk, berr := brdiskRegex.MatchString(title)
		if berr != nil {
			return commonv1.Quality{}, fmt.Errorf("release: quality: brdisk match: %w", berr)
		}
		remux, rerr := remuxRegex.MatchString(title)
		if rerr != nil {
			return commonv1.Quality{}, fmt.Errorf("release: quality: remux match: %w", rerr)
		}
		switch {
		case brdisk:
			mod = commonv1.ModifierBRDisk
			resolution = commonv1.Resolution1080p
		case remux:
			mod = commonv1.ModifierRemux
			if resolution != commonv1.Resolution2160p {
				resolution = commonv1.Resolution1080p
			}
		case resolution == commonv1.ResolutionUnknown:
			resolution = commonv1.Resolution720p
		}
	case commonv1.SourceTV:
		rawhd, rerr := rawhdRegex.MatchString(title)
		if rerr != nil {
			return commonv1.Quality{}, fmt.Errorf("release: quality: rawhd match: %w", rerr)
		}
		if rawhd {
			mod = commonv1.ModifierRawHD
			resolution = commonv1.Resolution1080p
		}
	case commonv1.SourceWebDL, commonv1.SourceWebRip:
		if resolution == commonv1.ResolutionUnknown {
			resolution = commonv1.Resolution480p
		}
	}

	name, ok := qualityTable[qualityKey{Source: src, Resolution: resolution, Modifier: mod}]
	if !ok {
		return commonv1.Quality{Name: "Unknown", Source: commonv1.SourceUnknown}, nil
	}
	return commonv1.Quality{Name: name, Source: src, Resolution: resolution, Modifier: mod}, nil
}

// detectRevision runs ProperRegex, RepackRegex and the case-sensitive
// RealRegex, applying *arr's Version=2-on-proper-or-repack rule.
func detectRevision(title string) (commonv1.Revision, error) {
	rev := commonv1.Revision{Version: 1}

	properMatch, err := properRegex.FindStringMatch(title)
	if err != nil {
		return commonv1.Revision{}, fmt.Errorf("release: revision: proper match: %w", err)
	}

	repackMatch, err := repackRegex.FindStringMatch(title)
	if err != nil {
		return commonv1.Revision{}, fmt.Errorf("release: revision: repack match: %w", err)
	}

	explicitVersion := 0
	if repackMatch != nil {
		rev.Repack = true
		if digit := repackMatch.GroupByNumber(1); digit != nil && len(digit.Captures) > 0 {
			if v, convErr := strconv.Atoi(digit.String()); convErr == nil {
				explicitVersion = v
			}
		}
	}
	if properMatch != nil || repackMatch != nil {
		rev.Version = 2
	}
	if int32(explicitVersion) > rev.Version {
		rev.Version = int32(explicitVersion)
	}

	real := 0
	rm, err := realRegex.FindStringMatch(title)
	if err != nil {
		return commonv1.Revision{}, fmt.Errorf("release: revision: real match: %w", err)
	}
	for rm != nil {
		real++
		rm, err = realRegex.FindNextMatch(rm)
		if err != nil {
			return commonv1.Revision{}, fmt.Errorf("release: revision: real match: %w", err)
		}
	}
	rev.Real = int32(real)

	return rev, nil
}

// parseQualityTags extracts the video quality identity and proper/repack/
// real revision from a release title. It is unexported and whitebox-tested;
// the real release group and obfuscation hash are computed by the separate
// parseGroup (group.go) — this function always returns "" for both, per its
// four-value signature not carrying an error: a regexp2 MatchTimeout here
// degrades to the conservative "Unknown"/"Version:1" defaults rather than
// losing the parse, since there is no error channel to propagate through.
func parseQualityTags(title string) (commonv1.Quality, commonv1.Revision, string, string) {
	q, err := detectQuality(title)
	if err != nil {
		q = commonv1.Quality{Name: "Unknown", Source: commonv1.SourceUnknown}
	}
	rev, err := detectRevision(title)
	if err != nil {
		rev = commonv1.Revision{Version: 1}
	}
	return q, rev, "", ""
}
