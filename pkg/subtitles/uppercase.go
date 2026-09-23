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
	"unicode"
	"unicode/utf8"

	"github.com/asticode/go-astisub"
)

// This file ports Bazarr's fix_uppercase mod (ModFixUppercase), from
// morpheus65535/bazarr at ec41fe82c03ccd556d168666595bd0be1202f77b:
//
//   - custom_libs/subzero/modification/mods/common.py, class FixUppercase
//     (identifier "fix_uppercase", mostly_uppercase = True,
//     apply_last = True) and split_upper_re;
//   - custom_libs/subzero/modification/main.py,
//     SubtitleModifications.detect_uppercase and the prepare_mods gate
//     "if mod_cls.mostly_uppercase and not self.mostly_uppercase: skip".
//
// So the mod is two rules, not one: it runs only on a subtitle that is
// mostly upper case to begin with (a normal mixed-case file is never
// touched, whatever the profile asks), and when it runs it runs last, after
// every line mod -- which matters, because remove_HI's speaker-label rule
// only recognises an upper-case "JOHN:" and would miss it once the text had
// become "John:".

// Bazarr's detect_uppercase constants, verbatim.
const (
	uppercaseMaxEntries    = 50  // MAXIMUM_ENTRIES: only the first 50 non-empty entries are sampled
	uppercaseMinPercentage = 90  // MINIMUM_UPPERCASE_PERCENTAGE
	uppercaseMinCount      = 100 // MINIMUM_UPPERCASE_COUNT: strictly more than this many upper-case letters
)

// pySpace is Python 3's `\s` for str patterns (the characters for which
// str.isspace() is true). Go's RE2 `\s` is ASCII [\t\n\f\r ] only, so it is
// spelled out: the Unicode separators (\p{Z}), plus \v, the ASCII
// information separators \x1c-\x1f and NEL \x85, which Python also treats
// as whitespace.
const pySpace = `[\t\n\v\f\r\x1c-\x1f\x85\p{Z}]`

// splitUpperRe is common.py's split_upper_re, `(\s*[.!?♪\-]\s*)`, with
// Python's `\s`: the sentence (and dash, and music-note) boundaries after
// which FixUppercase starts a new capitalised run.
var splitUpperRe = regexp.MustCompile(pySpace + `*[.!?♪\-]` + pySpace + `*`)

// capitalizeUppercase is FixUppercase.capitalize: split the text on
// splitUpperRe keeping the separators (Python's re.split with a capturing
// group), apply str.capitalize to every piece, and join. Bazarr's quirks are
// kept deliberately, because a port that "improves" them is no longer the
// behaviour users of the mod know: a lone "I" mid-sentence becomes "i", a
// line break that no punctuation precedes does not start a new capital, and
// a piece that opens with a quote or a space keeps that character "first"
// and so gets no capital at all.
func capitalizeUppercase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, m := range splitUpperRe.FindAllStringIndex(s, -1) {
		b.WriteString(pyCapitalize(s[last:m[0]]))
		b.WriteString(pyCapitalize(s[m[0]:m[1]]))
		last = m[1]
	}
	b.WriteString(pyCapitalize(s[last:]))
	return b.String()
}

// pyCapitalize is Python 3.8+'s str.capitalize: the first character in
// title case, every other character lower-cased. Go's simple case mappings
// stand in for Python's full ones, which differ only for a handful of
// characters whose case mapping changes their length (e.g. U+0130).
func pyCapitalize(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToTitle(r)) + strings.ToLower(s[size:])
}

// fixUppercase is ModFixUppercase applied to one cue's text. Callers gate
// it on mostlyUppercase and apply it after every other mod (see applyMods).
func fixUppercase(s string) string { return capitalizeUppercase(s) }

// mostlyUppercase is detect_uppercase: over the first uppercaseMaxEntries
// entries that still have text after the hearing-impaired markers are
// stripped, count upper- and lower-case letters; the subtitle is mostly
// upper case when more than uppercaseMinCount letters are upper case and
// they are at least uppercaseMinPercentage percent of all cased letters.
//
// Bazarr strips HI markers first ("skip HI bracket entries, those might
// actually be lowercase") by running the first four remove_HI processors
// over each entry. The first of them, HI_brackets_full, is a
// FullBracketEntryProcessor that returns its input unchanged when called
// without an entry -- as detect_uppercase calls it -- so it is a no-op
// there. The other three are HI_before_colon_caps, HI_before_colon_noncaps
// and HI_brackets. This package ports the first and last of those
// (hiSpeakerLabel, hiBrackets, applied per line); HI_before_colon_noncaps
// is not ported (see RemoveHI), so a mixed-case speaker label counts
// towards the tally here where Bazarr would have dropped it first.
func mostlyUppercase(items []*astisub.Item) (bool, error) {
	var upper, lower, entries int
	for _, item := range items {
		text := strings.TrimSpace(itemText(item))
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			var err error
			if line, err = replaceAll(hiSpeakerLabel, line, ""); err != nil {
				return false, err
			}
			if line, err = replaceAll(hiBrackets, line, ""); err != nil {
				return false, err
			}
			lines[i] = line
		}
		text = strings.Join(lines, "\n")
		if strings.TrimSpace(text) != "" {
			for _, r := range text {
				switch {
				case unicode.IsUpper(r):
					upper++
				case unicode.IsLower(r):
					lower++
				}
			}
			entries++
		}
		if entries >= uppercaseMaxEntries {
			break
		}
	}
	total := upper + lower
	return total > 0 && upper > uppercaseMinCount && upper*100 >= uppercaseMinPercentage*total, nil
}
