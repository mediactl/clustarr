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

package comic

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// CalculatedNumberCentis derives IssueSpec's sortable numeric form of a
// provider issue number, in hundredths (design spec §4.2's IssueSpec.
// CalculatedNumber, stored as a scaled int32 per api/catalog/v1alpha1's own
// "CRD schemas do not allow floats" comment on CalculatedNumberCentis).
//
// It is Kapowarr's calculated issue number, ported rule for rule: Kapowarr
// is the comic manager this project models Comic/Issue on
// (docs/research/quality.md:512), it is GPL-3.0 like Clustarr, and its
// ComicVine importer computes the value as
//
//	calculated_issue_number = force_range(extract_issue_number(issue_number))[0]
//	if calculated_issue_number is None: calculated_issue_number = 0.0
//
// (backend/implementations/comicvine.py, __format_issue_output), with
// extract_issue_number and _get_calculated_issue_number from
// backend/base/file_extraction.py and normalise_number from
// backend/base/helpers.py, all at Casvt/Kapowarr
// b51f7abb9a8438b860ec785e86183a238337976f. So:
//
//   - a number Python's float() accepts is that value ("12" 12, "12.5" 12.5,
//     "-1" -1);
//   - a range ("2½ - 4.5", and "1/2", since Kapowarr rewrites "/" to "-")
//     takes its first end;
//   - anything else is read character by character: digits are kept, the
//     first other character becomes the decimal point, "½" is 5, "¼" is 3,
//     "∞" is 9999999999999 and a letter is its two-digit alphabet position,
//     so "2b" is 2.02, "-10a" -10.01 and "12.HU" 12.0821;
//   - a number that yields nothing numeric is 0.
//
// Three places differ from Kapowarr, each where Kapowarr has no answer a
// Go int32 can hold: its float is kept to hundredths (rounded half away from
// zero -- "12.HU" is 1208), NaN is 0 like an unparseable number, and a value
// past the int32 range (an "∞" issue among them) is clamped to it. Inputs on
// which Kapowarr raises instead of answering (a range with an empty side
// such as "5-", a lone "-") are read as a single number, and so as 0 when
// nothing numeric is left. Python's float() also accepts non-ASCII decimal
// digits; this accepts ASCII digits only, the only ones ComicVine prints.
//
// Before this port, Clustarr took the first decimal token it found ("12.HU"
// was 12, "Annual 1" was 1); that rule was its own, with no precedent.
func CalculatedNumberCentis(number string) int32 {
	f, ok := calculatedIssueNumber(number)
	if !ok || math.IsNaN(f) {
		return 0
	}
	c := math.Round(f * 100)
	switch {
	case c >= math.MaxInt32:
		return math.MaxInt32
	case c <= math.MinInt32:
		return math.MinInt32
	}
	return int32(c)
}

// calculatedIssueNumber is force_range(extract_issue_number(n))[0]: the
// first end of a range, or the single number; ok is false where Kapowarr's
// result is None.
func calculatedIssueNumber(n string) (float64, bool) {
	n = strings.ReplaceAll(n, "/", "-")
	runes := []rune(n)
	if len(runes) == 0 || !strings.Contains(string(runes[1:]), "-") {
		return issueNumberFloat(n)
	}
	rest := strings.ReplaceAll(string(runes[1:]), " ", "")
	start, end, _ := strings.Cut(rest, "-")
	start = string(runes[0]) + start
	if !startsWithDigit(strings.TrimLeft(start, "-")) || !startsWithDigit(strings.TrimLeft(end, "-")) {
		// Not a range after all (or, where Kapowarr would index an empty
		// side and raise, not one this can read): one number.
		return issueNumberFloat(n)
	}
	if f, ok := issueNumberFloat(start); ok {
		return f, true
	}
	return issueNumberFloat(end)
}

func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// issueNumberFloat is Kapowarr's _get_calculated_issue_number.
func issueNumberFloat(n string) (float64, bool) {
	if f, ok := pythonFloat(n); ok {
		return f, true
	}
	// normalise_number: "," is a decimal point, "?" an unknown digit.
	n = strings.ReplaceAll(n, ",", ".")
	n = strings.ReplaceAll(n, "?", "0")
	n = strings.ToLower(strings.TrimSpace(strings.TrimRight(n, ".")))

	var b strings.Builder
	if rest, ok := strings.CutPrefix(n, "-"); ok {
		b.WriteByte('-')
		n = rest
	}
	dot := true
	for _, r := range n {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
			continue
		}
		if r == '∞' {
			b.WriteString("9999999999999")
		} else if dot {
			b.WriteByte('.')
			dot = false
		}
		switch {
		case r == '½':
			b.WriteByte('5')
		case r == '¼':
			b.WriteByte('3')
		case r >= 'a' && r <= 'z':
			fmt.Fprintf(&b, "%02d", r-'a'+1)
		}
	}
	converted := b.String()
	if strings.Trim(converted, ".") == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(converted, 64)
	if err != nil {
		// Kapowarr raises here (a lone "-"); there is no number to report.
		return 0, false
	}
	return f, true
}

// pythonFloatRegex is the literal grammar Python's float() accepts, from
// https://docs.python.org/3/library/functions.html#float: an optional sign,
// then digits with single underscores between them, an optional fraction
// and an optional exponent -- or inf, infinity or nan in any case.
var pythonFloatRegex = regexp.MustCompile(`^[+-]?(?:(?:(?:\d(?:_?\d)*)?\.\d(?:_?\d)*|\d(?:_?\d)*\.?)(?:[eE][+-]?\d(?:_?\d)*)?|(?i:inf|infinity|nan))$`)

// pythonFloat reports what Python's float(s) would return. strconv.ParseFloat
// alone disagrees at the edges: it rejects surrounding whitespace and
// digit-separating underscores, and accepts hexadecimal, none of which
// Python does.
func pythonFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if !pythonFloatRegex.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange) {
			return f, true // ±Inf, as Python's float() returns for an overflow
		}
		return 0, false
	}
	return f, true
}

// IssueName renders an Issue object's name per spec §4.2:
// "<comic>-<calculatedNumber padded 5.1>" -- a printf width.precision pair
// (width 5, one decimal digit), exactly like EpisodeName's %02d comment: a
// long-running numbering (issue 1092.0) is never truncated, only ever padded
// wider than 5 when the value itself needs more room. calculatedNumberCentis
// is IssueSpec.CalculatedNumberCentis itself (hundredths), converted back to
// the decimal the name renders.
func IssueName(comicName string, calculatedNumberCentis int32) string {
	return fmt.Sprintf("%s-%05.1f", comicName, float64(calculatedNumberCentis)/100.0)
}
