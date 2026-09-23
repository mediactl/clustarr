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

// Package lang normalizes language codes across the vocabularies this
// project actually receives them in, into one canonical BCP-47 tag. It is
// the fix for a bug class that has recurred three times: nothing in the
// tree converted between
//
//   - ISO 639-1 two-letter codes ("en", "fr") -- TMDB, and
//     pkg/quality/catalogue's language table;
//   - BCP-47 with a region subtag ("pt-BR", "es-419") -- metadata provider
//     "original language" fields, subtitle profiles, pkg/subtitles.LangKey;
//   - ISO 639-2/B bibliographic codes ("fre", "ger", "chi", "dut", "cze",
//     ...) -- every embedded audio/subtitle stream ffprobe reports; and
//   - ISO 639-2/T and ISO 639-3 codes ("fra", "deu", "eng", "jpn", ...) --
//     ffprobe again, and TVDB.
//
// A fifth vocabulary, Radarr's English display names ("English",
// "Japanese"), is deliberately NOT handled here: composing a caller looks
// like
//
//	tag, ok := lang.Normalize(rawCode)
//	if !ok {
//	    // unknown language; the caller decides what "unknown" means for it
//	    // (pkg/decision fails open, for example)
//	}
//	name, ok := catalogue.LanguageName(string(tag))
//
// pkg/quality/catalogue.LanguageName already owns the tag-to-display-name
// step and pkg/decision/language.go already calls it; duplicating that table
// here would just be a second place for it to drift.
//
// Normalize never guesses. A code it cannot resolve to a real, specific
// language -- including the ISO 639-2 sentinels "und" (undetermined), "mul"
// (multiple languages) and "zxx" (no linguistic content), and the ISO 639-2
// private-use range ("qaa".."qtz") -- reports ok=false rather than returning
// its best guess. Callers that need "unknown" to mean something (most do)
// decide that for themselves; this package only ever reports what it is
// sure of.
package lang

import (
	"strings"

	"golang.org/x/text/language"
)

// Tag is a canonical BCP-47 language tag: the base language as an ISO 639-1
// two-letter code where one exists, an ISO 639-3 code otherwise, with any
// region subtag the input carried preserved ("pt-BR", not "pt"). It carries
// no script, variant or extension normalization beyond what
// golang.org/x/text/language.Parse already canonicalizes.
type Tag string

// Normalize converts code, in any of ISO 639-1, BCP-47-with-region, ISO
// 639-2/B, ISO 639-2/T or ISO 639-3, into a canonical Tag. It is
// case-insensitive and accepts "_" or "-" as the subtag separator.
//
// ok is false for empty or unparseable input, for "und"/"mul"/"zxx" (a
// sentinel is not a language), for the ISO 639-2 private-use range, and for
// anything else Normalize is not sure it has resolved correctly -- most
// notably Radarr's English display names ("English" is not a well-formed
// BCP-47 tag, so it already fails to parse; that is not an accident this
// package works around).
//
// Verified against every ISO 639-2/B bibliographic code that differs from
// its ISO 639-2/T equivalent (alb, arm, baq, bur, chi, cze, dut, fre, geo,
// ger, gre, ice, mac, mao, may, per, rum, slo, tib, wel; see lang_test.go's
// TestNormalizeBibliographicCodes): golang.org/x/text/language.Parse already
// resolves every one of them to the correct ISO 639-1 code via CLDR's alias
// tables, so this package carries no hand-written B-code table of its own.
func Normalize(code string) (Tag, bool) {
	s := strings.TrimSpace(code)
	s = strings.ReplaceAll(s, "_", "-")
	if s == "" {
		return "", false
	}

	tag, err := language.Parse(s)
	if err != nil {
		// Covers empty/garbage input that reaches this point some other
		// way, malformed subtags, and unknown two-letter subtags like "cn"
		// (TMDB's non-standard code for Cantonese) or "zz".
		return "", false
	}

	// language.Parse accepts (and Base() will even guess a language for)
	// the ISO 639-2 sentinel codes -- "und" resolves to a low-confidence
	// "en" guess, not to "this is not a language". Reject them by their own
	// primary subtag, not by the guess x/text makes for them.
	primary, _, _ := strings.Cut(tag.String(), "-")
	switch strings.ToLower(primary) {
	case "und", "mul", "zxx":
		return "", false
	}

	base, conf := tag.Base()
	if conf == language.No || base.IsPrivateUse() {
		// conf == No: x/text could not resolve a base language at all
		// (e.g. a private-use tag like "x-klingon"). IsPrivateUse: the ISO
		// 639-2 qaa-qtz reserved range, which by definition names no
		// specific language -- resolving it to anything would be a guess.
		return "", false
	}

	// tag.String() reconstructs the tag from only the subtags the input
	// actually carried (plus whatever alias canonicalization applied), not
	// from x/text's likely-subtag inference -- so "fre" comes back "fr", not
	// "fr-FR", and "pt_BR" comes back "pt-BR", not just "pt". Its language
	// subtag is always base.String(): 2-letter ISO 639-1 where the base
	// language has one, 3-letter ISO 639-3 otherwise.
	return Tag(tag.String()), true
}
