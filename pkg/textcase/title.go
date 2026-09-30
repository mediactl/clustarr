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

// Package textcase cases titles.
package textcase

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// minor are the words English title case lowercases unless they open or
// close the title or follow a colon or dash: articles, short conjunctions
// and the common prepositions (Chicago lowercases every preposition; these
// are the ones book titles use). "up" and "v" are not here -- "up" is as often an
// adverb ("What It Is Up To") and "v" is the Roman numeral five.
var minor = map[string]bool{
	"a": true, "an": true, "the": true,
	"and": true, "but": true, "or": true, "nor": true, "for": true, "so": true, "yet": true,
	"as": true, "at": true, "by": true, "in": true, "of": true, "off": true, "on": true,
	"per": true, "to": true, "via": true, "vs": true, "vs.": true,
	"from": true, "into": true, "onto": true, "upon": true, "with": true, "than": true,
}

var (
	tokens = regexp.MustCompile(`\s+|\S+`)
	// roman is a strict numeral from I to XXXIX, so words made of the same
	// letters ("mix", "civil") are words.
	roman = regexp.MustCompile(`^x{0,3}(ix|iv|v?i{0,3})$`)
)

// Title returns s in English title case: the first and last words, and the
// first word after a colon or a dash, capitalised; the minor words
// lowercased everywhere else, even when s capitalised them; each part of a
// hyphenated word capitalised; a strict Roman numeral uppercased. A word
// with a capital after its first letter (NASA, McCarthy, iPhone) or a digit
// is kept exactly as it is -- unless the whole title is in capitals, which
// is cased from lower case -- and so is every space and punctuation mark.
// A colon, a dash, a full stop, a semicolon, or an opening bracket or quote
// starts a new title.
func Title(s string) string {
	// A title in capitals throughout is shouting, not acronyms: case it
	// from lower case ("WAR AND PEACE" -> "War and Peace").
	if strings.IndexFunc(s, unicode.IsLower) < 0 {
		s = strings.ToLower(s)
	}
	parts := tokens.FindAllString(s, -1)
	first, last := -1, -1
	for i, p := range parts {
		if !isSpace(p) && hasLetter(p) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}

	var b strings.Builder
	afterBreak := false
	for i, p := range parts {
		if isSpace(p) {
			b.WriteString(p)
			continue
		}
		lead, core, trail := splitPunct(p)
		// An opening bracket or quote starts a title of its own:
		// "The Adolescent (A Raw Youth)".
		force := i == first || i == last || afterBreak || strings.ContainsAny(lead, "([{\"“‘")
		b.WriteString(lead)
		b.WriteString(caseWord(core, force))
		b.WriteString(trail)
		if core == "" {
			// A bare dash ("war and peace – part one") breaks the title
			// like a colon; any other bare punctuation leaves it as it was.
			afterBreak = afterBreak || strings.ContainsAny(p, "-–—")
		} else {
			// A colon, a dash or a sentence's end ("... Man. The Meek
			// One", "The Landlady; The Gambler") starts a new title.
			afterBreak = strings.ContainsAny(trail, ":–—.;?!")
		}
	}
	return b.String()
}

// caseWord cases one word, its surrounding punctuation already removed.
func caseWord(w string, force bool) string {
	if w == "" || keep(w) {
		return w
	}
	if strings.Contains(w, "-") {
		// Each part is a word: the first capitalised, the rest by the
		// same rules ("Man-of-War", "Up-to-Date", "World-War-II").
		hs := strings.Split(w, "-")
		for i, h := range hs {
			hs[i] = caseWord(h, i == 0)
		}
		return strings.Join(hs, "-")
	}
	lower := strings.ToLower(w)
	if roman.MatchString(lower) {
		return strings.ToUpper(w)
	}
	if minor[lower] && !force {
		return lower
	}
	// An Irish O' capitalises the name after it: "O'Brien".
	if strings.HasPrefix(lower, "o'") && len(w) > 2 {
		return "O'" + capitalise(w[2:])
	}
	return capitalise(w)
}

// keep reports whether w is cased on purpose: a digit anywhere, or a
// capital after its first letter.
func keep(w string) bool {
	for i, r := range w {
		if unicode.IsDigit(r) {
			return true
		}
		if i > 0 && unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func capitalise(w string) string {
	r, n := utf8.DecodeRuneInString(w)
	return string(unicode.ToUpper(r)) + w[n:]
}

// splitPunct splits the punctuation leading and trailing a word from it.
func splitPunct(p string) (lead, core, trail string) {
	start := strings.IndexFunc(p, isWordRune)
	if start < 0 {
		return p, "", ""
	}
	end := strings.LastIndexFunc(p, isWordRune)
	_, n := utf8.DecodeRuneInString(p[end:])
	return p[:start], p[start : end+n], p[end+n:]
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func hasLetter(s string) bool { return strings.IndexFunc(s, isWordRune) >= 0 }

func isSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}
