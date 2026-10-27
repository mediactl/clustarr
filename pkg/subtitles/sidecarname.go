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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SidecarExtensions are the subtitle sidecar extensions this package
// recognises when scanning a media file's directory: research note §3.2's
// non-exotic set (Bazarr's INCLUDE_EXOTIC_SUBS off), which drops .sub,
// .smi, .txt and .mpl; plus "sup", the PGS sidecar the MP4 standard writes
// beside a transcode for an image subtitle no text track covers (Version
// 8), so it is renamed, recycled and deleted with its file. It is no
// subtitle a player reads: ImageSidecar, which the caption planner's
// "existing" excludes.
var SidecarExtensions = []string{"srt", "ass", "ssa", "vtt", "sup"}

// ImageSidecar reports a sidecar name that holds an image subtitle (a .sup
// PGS track), which Plex and the subtitle providers' scoring never read: it
// is kept for the OCR to come and counts as no existing subtitle.
func ImageSidecar(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".sup")
}

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
// concern (pkg/subtitles/postprocess.PostProcess's toSRT flag), not a naming one.
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

// SidecarsOf is every file beside the video at videoPath that ParseSidecar
// attributes to it: recorded or not -- transcode writes the source's own
// subtitles beside a transcode (MP4 standard §4.1), and no MediaFile lists
// them. A folder that cannot be read has none.
func SidecarsOf(videoPath string) ([]string, error) {
	dir := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if _, ok := ParseSidecar(stem, e.Name()); ok || BareSidecar(stem, e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// BareSidecar reports name as <stem>.<ext> for a sidecar extension: a
// subtitle beside the video with no language in its name, which the
// transcode writes for an untagged track and Plex lists as an unknown
// language. ParseSidecar attributes it to no language (this package never
// guesses one), but it is the video's file all the same: a rename, a
// recycle or a delete carries it (2026-10-27: three of The Sopranos'
// .sup sidecars stayed under the stem their MP4 was renamed away from).
func BareSidecar(stem, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return isSidecarExt(ext) && name == stem+name[len(name)-len(ext):]
}
