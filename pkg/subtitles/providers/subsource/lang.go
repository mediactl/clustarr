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

package subsource

import (
	"strings"

	"github.com/mediactl/clustarr/pkg/lang"
)

// subsourceNames is Bazarr's SubsourceConverter.from_subsource
// (custom_libs/subliminal_patch/converters/subsource.py, as fixed for
// regional variants in #3481), keyed here by the canonical BCP-47 tag
// pkg/lang.Normalize produces instead of by ISO 639-3. The names are
// SubSource's own, including its misspellings of Esperanto and Northern
// Sami, which are what the API matches on; they are sent lower-cased.
// Tagalog is keyed on "fil", which is what golang.org/x/text canonicalises
// "tl" to.
var subsourceNames = map[string]string{
	"en": "English", "fa": "Farsi_persian", "ab": "Abkhazian", "af": "Afrikaans",
	"sq": "Albanian", "am": "Amharic", "ar": "Arabic", "an": "Aragonese",
	"hy": "Armenian", "as": "Assamese", "az": "Azerbaijani", "eu": "Basque",
	"be": "Belarusian", "bn": "Bengali", "bs": "Bosnian", "pt-BR": "Brazilian_portuguese",
	"br": "Breton", "bg": "Bulgarian", "my": "Burmese", "ca": "Catalan",
	"zh": "Chinese_bg_code", "hr": "Croatian", "cs": "Czech", "da": "Danish",
	"nl": "Dutch", "eo": "Espranto", "et": "Estonian", "fi": "Finnish",
	"fr": "French", "gd": "Gaelic", "ka": "Georgian", "de": "German",
	"el": "Greek", "he": "Hebrew", "hi": "Hindi", "hu": "Hungarian",
	"is": "Icelandic", "ig": "Igbo", "id": "Indonesian", "ia": "Interlingua",
	"ga": "Irish", "it": "Italian", "ja": "Japanese", "kn": "Kannada",
	"kk": "Kazakh", "km": "Khmer", "ko": "Korean", "ku": "Kurdish",
	"lv": "Latvian", "lt": "Lithuanian", "lb": "Luxembourgish", "mk": "Macedonian",
	"ms": "Malay", "ml": "Malayalam", "mr": "Marathi", "mn": "Mongolian",
	"nv": "Navajo", "ne": "Nepali", "se": "Northen Sami", "no": "Norwegian", //nolint:misspell // SubSource's spelling, matched on the wire
	"oc": "Occitan", "pl": "Polish", "pt": "Portuguese", "ps": "Pushto",
	"ro": "Romanian", "ru": "Russian", "sr": "Serbian", "sd": "Sindhi",
	"si": "Sinhala", "sk": "Slovak", "sl": "Slovenian", "so": "Somali",
	"es": "Spanish", "sw": "Swahili", "sv": "Swedish", "fil": "Tagalog",
	"ta": "Tamil", "tt": "Tatar", "te": "Telugu", "th": "Thai",
	"tr": "Turkish", "tk": "Turkmen", "uk": "Ukrainian", "ur": "Urdu",
	"uz": "Uzbek", "vi": "Vietnamese", "cy": "Welsh",
}

// tagByName is subsourceNames reversed, keyed by the lower-cased name.
var tagByName = func() map[string]string {
	m := make(map[string]string, len(subsourceNames))
	for tag, name := range subsourceNames {
		m[strings.ToLower(name)] = tag
	}
	return m
}()

// toSubSource converts a BCP-47 tag to SubSource's language name, lower-
// cased as the API takes it. Like the converter it ports, it tries the tag
// with its region first and then the base language alone, so "pt-BR" is
// Brazilian Portuguese while "es-MX" and "zh-TW" fall back to Spanish and
// SubSource's single Chinese.
func toSubSource(tag string) (string, bool) {
	t, ok := lang.Normalize(tag)
	if !ok {
		return "", false
	}
	parts := strings.Split(string(t), "-")
	base := strings.ToLower(parts[0])
	if len(parts) == 2 {
		if name, ok := subsourceNames[base+"-"+strings.ToUpper(parts[1])]; ok {
			return strings.ToLower(name), true
		}
	}
	name, ok := subsourceNames[base]
	return strings.ToLower(name), ok
}

// tagOf converts a SubSource language name back to BCP-47,
// case-insensitively.
func tagOf(name string) (string, bool) {
	t, ok := tagByName[strings.ToLower(strings.TrimSpace(name))]
	return t, ok
}
