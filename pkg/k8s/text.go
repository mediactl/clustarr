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

package k8s

import (
	"strings"
	"unicode/utf8"
)

// MaxConditionMessage is the longest condition message a Clustarr status
// renderer writes (loop spec §2.11.1 rule 2). metav1.Condition admits 32
// KiB; a MediaFile carries up to 12 conditions and its size budget assumes
// 1 KiB each.
const MaxConditionMessage = 1024

// Unapplyable reports whether r cannot travel in a server-side apply. The
// apiserver decodes an application/apply-patch+yaml body with a YAML reader
// that refuses U+0080-U+009F (U+0085 excepted), U+FFFE and U+FFFF ("control
// characters are not allowed"), and encoding/json leaves those runes raw, so
// one of them refuses the whole apply.
func Unapplyable(r rune) bool {
	return (r >= 0x80 && r <= 0x9F && r != 0x85) || r == 0xFFFE || r == 0xFFFF
}

// HasUnapplyable reports whether s holds an Unapplyable rune.
func HasUnapplyable(s string) bool { return strings.IndexFunc(s, Unapplyable) >= 0 }

// SanitizeText replaces every Unapplyable rune in s, and every byte that is
// not UTF-8, with U+FFFD. Use it on text, never on a path or file name: a
// rewritten path names a file that does not exist, so a writer drops such a
// value instead (HasUnapplyable).
func SanitizeText(s string) string {
	if utf8.ValidString(s) && !HasUnapplyable(s) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if Unapplyable(r) {
			return utf8.RuneError
		}
		return r
	}, s)
}

// ClampText cuts s to at most maxBytes bytes without splitting a rune. A
// CRD's maxLength counts runes, so a value within maxBytes is within a
// MaxLength of the same number.
func ClampText(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
