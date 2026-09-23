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

package subdl

import (
	"strings"

	"github.com/mediactl/clustarr/pkg/lang"
)

// subdlCodes is Bazarr's SubdlConverter.from_subdl
// (custom_libs/subliminal_patch/converters/subdl.py), keyed here by the
// canonical BCP-47 base language pkg/lang.Normalize produces instead of by
// ISO 639-3. Two codes carry a region: BR_PT is Brazilian Portuguese and
// ZH_BG is Traditional Chinese (Bazarr keys it on zho-TW). Tagalog is keyed
// on "fil" because that is what golang.org/x/text canonicalises "tl" to.
var subdlCodes = map[string]string{
	"ar": "AR", "da": "DA", "nl": "NL", "en": "EN", "fa": "FA", "fi": "FI", "fr": "FR",
	"id": "ID", "it": "IT", "no": "NO", "ro": "RO", "es": "ES", "sv": "SV", "vi": "VI",
	"sq": "SQ", "az": "AZ", "be": "BE", "bn": "BN", "bs": "BS", "bg": "BG", "my": "MY",
	"ca": "CA", "zh": "ZH", "hr": "HR", "cs": "CS", "eo": "EO", "et": "ET", "ka": "KA",
	"de": "DE", "el": "EL", "kl": "KL", "he": "HE", "hi": "HI", "hu": "HU", "is": "IS",
	"ja": "JA", "ko": "KO", "ku": "KU", "lv": "LV", "lt": "LT", "mk": "MK", "ms": "MS",
	"ml": "ML", "pl": "PL", "pt": "PT", "ru": "RU", "sr": "SR", "si": "SI", "sk": "SK",
	"sl": "SL", "fil": "TL", "ta": "TA", "te": "TE", "th": "TH", "tr": "TR", "uk": "UK",
	"ur": "UR", "hy": "HY", "kk": "KK", "ky": "KY", "km": "KM", "kn": "KN", "mn": "MN",
	"eu": "EU", "gl": "GL", "ga": "GA", "jv": "JV", "su": "SU",
}

const (
	codeBrazilianPortuguese = "BR_PT"
	codeTraditionalChinese  = "ZH_BG"
)

// fromSubDL is subdlCodes reversed, plus the two regional codes.
var fromSubDL = func() map[string]string {
	m := make(map[string]string, len(subdlCodes)+2)
	for tag, code := range subdlCodes {
		m[code] = tag
	}
	m[codeBrazilianPortuguese] = "pt-BR"
	m[codeTraditionalChinese] = "zh-TW"
	return m
}()

// toSubDL converts a BCP-47 tag to SubDL's language code. Like Bazarr's
// SubdlConverter.convert, it is exact: a region other than the two SubDL
// distinguishes (pt-BR, zh-TW / zh-Hant) is not supported, rather than
// silently widened to the base language.
func toSubDL(tag string) (string, bool) {
	t, ok := lang.Normalize(tag)
	if !ok {
		return "", false
	}
	parts := strings.Split(string(t), "-")
	base := strings.ToLower(parts[0])
	switch {
	case len(parts) == 1:
		code, ok := subdlCodes[base]
		return code, ok
	case base == "pt" && len(parts) == 2 && strings.EqualFold(parts[1], "BR"):
		return codeBrazilianPortuguese, true
	case base == "zh" && len(parts) == 2 && (strings.EqualFold(parts[1], "TW") || strings.EqualFold(parts[1], "Hant")):
		return codeTraditionalChinese, true
	}
	return "", false
}

// tagOf converts a SubDL language code back to BCP-47.
func tagOf(code string) (string, bool) {
	t, ok := fromSubDL[strings.ToUpper(strings.TrimSpace(code))]
	return t, ok
}
