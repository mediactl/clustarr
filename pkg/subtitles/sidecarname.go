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

package subtitles

import (
	"path/filepath"
	"regexp"
	"strings"
)

// SidecarExtensions are the subtitle sidecar extensions this package
// recognises when scanning a media file's directory: research note §3.2's
// non-exotic set (Bazarr's INCLUDE_EXOTIC_SUBS off), which drops .sub,
// .smi, .txt and .mpl.
var SidecarExtensions = []string{"srt", "ass", "ssa", "vtt"}

// sidecarHIInfixes is the filename infix vocabulary ParseSidecar recognises
// as hearing-impaired, matching the profile's HIExtension enum
// (api/subtitle/v1alpha1, sdh;hi;cc) rather than Bazarr's fuller external
// tag vocabulary (normal, default, embedded, embedded-forced, custom):
// SidecarName never writes those, so round-tripping them is out of scope.
var sidecarHIInfixes = map[string]bool{"sdh": true, "hi": true, "cc": true}

// langSegmentRe matches one dot-segment as a bare language tag: the same
// shape api/subtitle/v1alpha1.LanguageItem.Language and LangKey's language
// portion (lang.go) validate, checked here only for its character class —
// this package does not validate against a real BCP-47/ISO-639 table, and
// neither does the CRD.
var langSegmentRe = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{2,8})*$`)

// normalizeHIExt maps a profile's HIExtension value to the filename infix,
// defaulting to "sdh" for anything outside the enum so a bad value still
// produces a parseable name instead of an empty infix.
func normalizeHIExt(hiExt string) string {
	if sidecarHIInfixes[hiExt] {
		return hiExt
	}
	return "sdh"
}

// normalizeLangSegment reports whether seg (one dot-segment of a sidecar
// filename) is shaped like a language tag, normalising underscores to
// hyphens first (research note §3.2: "lang = next segment with _→-").
func normalizeLangSegment(seg string) (string, bool) {
	seg = strings.ReplaceAll(seg, "_", "-")
	if !langSegmentRe.MatchString(seg) {
		return "", false
	}
	return seg, true
}

func isSidecarExt(ext string) bool {
	for _, e := range SidecarExtensions {
		if ext == "."+e {
			return true
		}
	}
	return false
}

// SidecarName renders the on-disk filename for a subtitle sidecar next to
// videoPath, for langKey key, honouring hiExt (the profile's HIExtension,
// normalised via normalizeHIExt). Spec §7's exact signature; always writes
// a ".srt" — the profile's originalFormat option is a post-processing
// concern (pkg/subtitles.PostProcess's toSRT flag), not a naming one.
//
// Forced wins over HI: research note §3.2's writing rule, and ParseLangKey
// already enforces the two are mutually exclusive on key.
func SidecarName(videoPath string, key LangKey, hiExt string) string {
	stem := strings.TrimSuffix(videoPath, filepath.Ext(videoPath))
	lang, forced, hi, err := ParseLangKey(key)
	if err != nil {
		// Defensive: an unparseable key still needs *a* name, so the
		// caller's mistake is visible in the filename instead of
		// silently discarded.
		lang = string(key)
	}
	switch {
	case forced:
		return stem + "." + lang + ".forced.srt"
	case hi:
		return stem + "." + lang + "." + normalizeHIExt(hiExt) + ".srt"
	default:
		return stem + "." + lang + ".srt"
	}
}

// ParseSidecar parses one directory entry's filename as a sidecar subtitle
// for the video whose extension-stripped base name is videoStem, right to
// left per research note §3.2 (Bazarr's _search_external_subtitles,
// match_strictness="strict"): <stem>[.<lang>][.<tag>].<ext>. It reports
// ok=false when name does not belong to this video at all (wrong stem or
// extension) and — per the "scanner never guesses" invariant — whenever the
// language cannot be determined, rather than guessing one. A tag segment
// only counts when it is the last one before the extension: "Movie.forced
// .en.srt" does not parse as forced (research note §3.2's own example).
func ParseSidecar(videoStem, name string) (LangKey, bool) {
	rawExt := filepath.Ext(name)
	if !isSidecarExt(strings.ToLower(rawExt)) {
		return "", false
	}
	base := name[:len(name)-len(rawExt)]
	if base == "" {
		return "", false
	}
	segs := strings.Split(base, ".")

	forced, hi := false, false
	rest := segs
	if len(rest) >= 2 {
		switch last := strings.ToLower(rest[len(rest)-1]); {
		case last == "forced":
			forced = true
			rest = rest[:len(rest)-1]
		case sidecarHIInfixes[last]:
			hi = true
			rest = rest[:len(rest)-1]
		}
	}

	if len(rest) < 2 {
		// No segment left to be a language: this is either the bare
		// "<stem>.<ext>" case (Bazarr assigns it a guessed or
		// single-profile language; this package never guesses) or a
		// tag with nothing before it.
		return "", false
	}
	lang, ok := normalizeLangSegment(rest[len(rest)-1])
	if !ok {
		return "", false
	}
	rest = rest[:len(rest)-1]

	if strings.Join(rest, ".") != videoStem {
		return "", false
	}
	return FormatLangKey(lang, forced, hi), true
}
