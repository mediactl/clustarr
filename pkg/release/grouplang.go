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
	"context"
	"slices"
	"strings"
)

// GroupLanguages answers the language a release group's dual-audio titles
// carry beside the original: outside anime, "DUAL" is the original and the
// group's own language, not English (2026-10-20: PD's NF WEB-DL of Akira
// carried Portuguese and Japanese). The language is an English display
// name, the vocabulary of ParsedRelease.Languages; ok is false when the
// group is not known to carry one.
type GroupLanguages interface {
	GroupLanguage(group string) (language string, ok bool)
}

// builtinGroupLanguages are the groups whose files in the owner's library
// showed their language beside the original on 2026-10-20 (each file's
// probed audio against its item's original language and English). Groups
// with thinner evidence are left to the learned index (pkg/grouplang).
var builtinGroupLanguages = map[string]string{
	"pd":       "Portuguese", // Akira: Portuguese (default) and Japanese
	"bioma":    "Portuguese", // 6 of 14 files: The Sopranos, Portuguese and English
	"hdt":      "Turkish",    // 44 of 49 files: Abbott Elementary, English and Turkish
	"mircrew":  "Italian",    // 2 of 2: The Godfather Part II, Italian and English
	"lullozzo": "Italian",    // 2 of 2: Amy, Burning, Italian beside or instead of the original
	"mhdgz":    "French",     // Arrietty: French and Japanese
	"fidelio":  "French",     // Back to the Future: French and English
}

type builtin struct{}

func (builtin) GroupLanguage(group string) (string, bool) {
	l, ok := builtinGroupLanguages[NormalizeGroup(group)]
	return l, ok
}

// BuiltinGroupLanguages is the built-in table alone.
func BuiltinGroupLanguages() GroupLanguages { return builtin{} }

// NormalizeGroup is the key a release group is looked up and learned by.
func NormalizeGroup(group string) string { return strings.ToLower(strings.TrimSpace(group)) }

type groupLanguagesKey struct{}

// WithGroupLanguages is ctx carrying g, the process's group languages
// (pkg/grouplang's index), for every reader of a release's languages.
func WithGroupLanguages(ctx context.Context, g GroupLanguages) context.Context {
	return context.WithValue(ctx, groupLanguagesKey{}, g)
}

// GroupLanguagesFrom is the group languages ctx carries, else the built-in
// table.
func GroupLanguagesFrom(ctx context.Context) GroupLanguages {
	if g, ok := ctx.Value(groupLanguagesKey{}).(GroupLanguages); ok && g != nil {
		return g
	}
	return builtin{}
}

// LanguagesWith is LanguagesFor with the release group's language: a
// dual-audio title that names no language but English, from a group g knows
// to carry language L, is the original and L (L and English when L is the
// original, L alone when the original is unknown) rather than the original
// and English. Every other title reads as LanguagesFor reads it.
func (p *ParsedRelease) LanguagesWith(originalLanguage string, g GroupLanguages) []string {
	if p.DualAudio && g != nil && p.Group != "" &&
		!slices.ContainsFunc(p.Languages, func(l string) bool { return l != "English" }) {
		if l, ok := g.GroupLanguage(p.Group); ok && l != "" && l != "English" {
			switch originalLanguage {
			case "":
				return []string{l}
			case l:
				return []string{l, "English"}
			default:
				return []string{originalLanguage, l}
			}
		}
	}
	return p.LanguagesFor(originalLanguage)
}
