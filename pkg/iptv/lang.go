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

package iptv

import (
	"strings"

	"golang.org/x/text/language"
)

// ISO6392 is code -- a BCP-47 tag ("en", "pt-BR") or an ISO 639-2 code
// ("eng", "fre") -- as the three-letter code an MPEG-TS ISO 639 language
// descriptor carries (ISO 639-2/T, which x/text gives as the base's ISO 639-3
// code: "eng", "spa", "fra"). ok is false for an empty or unknown code and
// for the sentinels "und", "mul" and "zxx", which name no language.
func ISO6392(code string) (string, bool) {
	s := strings.TrimSpace(strings.ReplaceAll(code, "_", "-"))
	if s == "" {
		return "", false
	}
	tag, err := language.Parse(s)
	if err != nil {
		return "", false
	}
	primary, _, _ := strings.Cut(tag.String(), "-")
	switch primary {
	case "und", "mul", "zxx":
		return "", false
	}
	base, conf := tag.Base()
	if conf == language.No {
		return "", false
	}
	iso := base.ISO3()
	if len(iso) != 3 {
		return "", false
	}
	return iso, true
}
