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
	"regexp"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

// Mod* mirror api/subtitle/v1alpha1.SubtitleMod's exact enum values
// (removeHI;removeTags;ocrFixes;common;fixUppercase;reverseRTL;color) —
// mirrored, not imported.
const (
	ModRemoveHI     = "removeHI"
	ModRemoveTags   = "removeTags"
	ModOCRFixes     = "ocrFixes"
	ModCommon       = "common"
	ModFixUppercase = "fixUppercase"
	ModReverseRTL   = "reverseRTL"
	ModColor        = "color"
)

// soundCueKeywords gates HI_all_caps (research note §6.3): an all-caps line
// is only a hearing-impaired cue, not shouted dialogue, when it names a
// sound.
var soundCueKeywords = regexp.MustCompile(`(?i)LAUGH|APPLAU|CHEER|MUSIC|GASP|SIGH|GROAN|COUGH|SCREAM|SHOUT|WHISPER|PHONE|DOOR|KNOCK|FOOTSTEP|THUNDER|EXPLOSION|GUNSHOT|SIREN`)

// hiBrackets matches a bracketed sound cue anywhere in the line, e.g.
// "I heard [a noise] outside." -> "I heard  outside." (regexp2 port of
// Bazarr's HI_brackets, research note §6.3; TAG's optional-style-tag
// wrapper is dropped — this package's inputs are already-decoded plain
// text by the time mods run).
var hiBrackets = mustHI(`-?["']*\[(?=[^\[\]]{3,})[A-Za-zÀ-ž0-9\s'".:_&+-]+[)\]]["']*[\s:]*`, regexp2.None)

// hiBracketsFull matches a line that is *entirely* a bracketed cue.
var hiBracketsFull = mustHI(`^-?[([].+[)\]]$`, regexp2.Singleline)

// hiSpeakerLabel matches an upper-case "NAME:" label at the start of a line
// (a simplified, RE2-portable core of Bazarr's HI_before_colon_caps).
var hiSpeakerLabel = mustHI(`^[A-ZÀ-Ž][A-ZÀ-Ž0-9 '&+-]{1,30}:\s*`, regexp2.None)

func mustHI(pattern string, opts regexp2.RegexOptions) *regexp2.Regexp {
	re := regexp2.MustCompile(pattern, opts)
	re.MatchTimeout = 2 * time.Second // CLAUDE.md: always set a MatchTimeout on regexp2
	return re
}

func replaceAll(re *regexp2.Regexp, s, repl string) (string, error) {
	return re.Replace(s, repl, -1, -1)
}

// RemoveHI ports Bazarr's hearing-impaired stripping (research note §6.3)
// line by line. It implements the bracket, speaker-label and gated
// all-caps rules (the four rules exercised by this package's tests). The
// remaining patterns from the same note section (before-colon-noncaps,
// starting-upper-then-sentence, JP_parentheses, music symbols) are
// deliberately deferred — the same "add a real implementation + test once
// one is needed" posture PostProcess's switch below takes for
// ModRemoveTags/ModOCRFixes/ModReverseRTL/ModColor — and would port the
// same way: a new package-level *regexp2.Regexp plus a case in the loop
// below.
func RemoveHI(text string) (string, error) {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m, err := hiBracketsFull.MatchString(trimmed); err != nil {
			return "", err
		} else if m {
			continue // whole line was a bracketed cue
		}
		if isAllCapsSoundCue(trimmed) {
			continue
		}
		line, err := replaceAll(hiBrackets, line, "")
		if err != nil {
			return "", err
		}
		line, err = replaceAll(hiSpeakerLabel, line, "")
		if err != nil {
			return "", err
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), nil
}

func isAllCapsSoundCue(line string) bool {
	if line != strings.ToUpper(line) {
		return false
	}
	hasLetter := false
	for _, r := range line {
		if (r >= 'A' && r <= 'Z') || (r >= 'À' && r <= 'Ž') {
			hasLetter = true
			break
		}
	}
	return hasLetter && soundCueKeywords.MatchString(line)
}
