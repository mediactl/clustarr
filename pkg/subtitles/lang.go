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
	"fmt"
	"strings"
)

// LangKey is Bazarr's wanted-language identity — spec §7's exact type. "en",
// "en:forced" and "en:hi" are three distinct keys (research note §2.2,
// §13.1). Mirrors api/subtitle/v1alpha1.LanguageItem.Key's wire form
// (validated there by ^[A-Za-z]{2,8}(-[A-Za-z0-9]{2,8})*(:forced)?(:hi)?$)
// without importing that package.
type LangKey string

// ParseLangKey splits a LangKey into its BCP-47 language tag and flags.
func ParseLangKey(k LangKey) (lang string, forced, hi bool, err error) {
	s := string(k)
	if s == "" {
		return "", false, false, fmt.Errorf("subtitles: empty langKey")
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 1:
		return parts[0], false, false, nil
	case 2:
		switch parts[1] {
		case "forced":
			return parts[0], true, false, nil
		case "hi":
			return parts[0], false, true, nil
		default:
			return "", false, false, fmt.Errorf("subtitles: langKey %q: unknown suffix %q", s, parts[1])
		}
	default:
		return "", false, false, fmt.Errorf("subtitles: langKey %q: forced and hi are mutually exclusive", s)
	}
}

// FormatLangKey is ParseLangKey's inverse.
func FormatLangKey(lang string, forced, hi bool) LangKey {
	switch {
	case forced:
		return LangKey(lang + ":forced")
	case hi:
		return LangKey(lang + ":hi")
	default:
		return LangKey(lang)
	}
}
