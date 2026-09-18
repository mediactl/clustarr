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
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/asticode/go-astisub"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// languageEncodings is research note §6.1's per-language candidate table,
// abbreviated to the language families this package's provider set (OS.com,
// Gestdown, embedded) actually returns most: CJK, Cyrillic, Central
// European, Western European. Each candidate is tried in order; the first
// one that both decodes without error and round-trips to valid UTF-8 wins.
var languageEncodings = map[string][]encoding.Encoding{
	"zh": {simplifiedchinese.GBK, traditionalchinese.Big5},
	"ja": {japanese.ShiftJIS, japanese.EUCJP},
	"ko": {korean.EUCKR},
	"ru": {charmap.Windows1251, charmap.ISO8859_5},
	"bg": {charmap.Windows1251, charmap.ISO8859_5},
	"uk": {charmap.Windows1251, charmap.ISO8859_5},
	"pl": {charmap.Windows1250, charmap.ISO8859_2},
	"cs": {charmap.Windows1250, charmap.ISO8859_2},
	"el": {charmap.Windows1253, charmap.ISO8859_7},
	"tr": {charmap.Windows1254, charmap.ISO8859_9},
}

// defaultEncodings is Bazarr's Western-default candidate chain.
var defaultEncodings = []encoding.Encoding{charmap.Windows1252, charmap.ISO8859_1}

// decodeToUTF8 sniffs a BOM, accepts already-valid UTF-8 as-is, and
// otherwise tries the language's candidate encodings (falling back to the
// Western default chain), returning the first clean decode.
func decodeToUTF8(raw []byte, lang string) ([]byte, error) {
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return stripBOM(raw[3:]), nil
	}
	if bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) {
		if out, err := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder().Bytes(raw); err == nil {
			return stripBOM(out), nil
		}
	}
	if bytes.HasPrefix(raw, []byte{0xFE, 0xFF}) {
		if out, err := unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder().Bytes(raw); err == nil {
			return stripBOM(out), nil
		}
	}
	if utf8.Valid(raw) {
		return stripBOM(raw), nil
	}

	candidates := append(append([]encoding.Encoding{}, languageEncodings[baseLang(lang)]...), defaultEncodings...)
	for _, enc := range candidates {
		if out, err := enc.NewDecoder().Bytes(raw); err == nil && utf8.Valid(out) {
			return stripBOM(out), nil
		}
	}
	return nil, fmt.Errorf("subtitles: could not decode subtitle to UTF-8 (lang=%s)", lang)
}

// stripBOM removes every UTF-8 BOM byte sequence (EF BB BF) from b, not
// just a leading one: a BOM is only meaningful at a file's start, but a
// stray one anywhere in already-decoded content is noise PostProcess must
// not hand downstream (astisub, RemoveHI) as if it were real text.
func stripBOM(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{0xEF, 0xBB, 0xBF}, nil)
}

func baseLang(lang string) string {
	if i := strings.IndexByte(lang, '-'); i >= 0 {
		return lang[:i]
	}
	return lang
}

// FixMojibake repairs UTF-8 bytes that were mis-decoded and re-saved as
// Windows-1252/Latin-1 — the one mojibake pattern that is mechanically
// reversible without a statistical model (research note §6.2 found no Go
// equivalent to ftfy; this covers the single highest-value case instead of
// skipping the requirement outright). Detection: re-encoding s as
// Windows-1252 and decoding the result as UTF-8 succeeds and changes
// nothing structurally wrong — i.e. s itself is valid UTF-8 that decodes
// from a Windows-1252 byte sequence which is *also* valid UTF-8 containing
// the CP1252 tell-tale bytes (0xC3 followed by a Latin-1 continuation).
func FixMojibake(s string) string {
	cp1252 := charmap.Windows1252
	reencoded, err := cp1252.NewEncoder().String(s)
	if err != nil {
		return s // s contains characters outside Windows-1252 — not this pattern
	}
	if !utf8.ValidString(reencoded) {
		return s
	}
	if reencoded == s {
		return s // nothing changed — not mojibake
	}
	// Only accept the repair if s actually shows the CP1252-mojibake tells
	// (Ã, Â and friends) — never applied to already-clean text.
	if !bytes.ContainsAny([]byte(s), "ÃÂ") {
		return s
	}
	return reencoded
}

// PostProcess is spec §7's exact signature: decode to UTF-8, convert to SRT
// unless !toSRT (profile.originalFormat), apply mods in order, always
// return valid UTF-8 SRT (or the original format's UTF-8 bytes).
func PostProcess(raw []byte, lang string, mods []string, toSRT bool) ([]byte, error) {
	decoded, err := decodeToUTF8(raw, lang)
	if err != nil {
		return nil, err
	}

	body := decoded
	if toSRT {
		body, err = convertToSRT(decoded)
		if err != nil {
			return nil, err
		}
	}

	// Mojibake repair runs on every write, unconditionally — research note
	// §6 item 2: Bazarr's ftfy.fix_text runs on every write, it is not
	// gated by a profile mod (ModCommon covers a different, still-deferred
	// set of whitespace/punctuation fixes — see below).
	text := FixMojibake(string(body))

	for _, mod := range mods {
		switch mod {
		case ModRemoveHI:
			text, err = RemoveHI(text)
			if err != nil {
				return nil, fmt.Errorf("subtitles: removeHI: %w", err)
			}
		case ModFixUppercase:
			text = fixUppercase(text)
		case ModRemoveTags, ModOCRFixes, ModCommon, ModReverseRTL, ModColor:
			// Deferred: no fixture-verified behaviour for these yet.
			// Recognised (not an error) so a profile listing them does not
			// fail PostProcess; add a real implementation + test the same
			// way as ModRemoveHI once one is needed by the captionarr
			// worker.
		default:
			return nil, fmt.Errorf("subtitles: unknown mod %q", mod)
		}
	}
	return []byte(text), nil
}

// convertToSRT sniffs ASS/SSA by content ("[Script Info]" section header,
// research note §6 item 4) and converts via go-astisub; anything else is
// assumed to already be SRT and is returned unchanged.
func convertToSRT(raw []byte) ([]byte, error) {
	if !bytes.Contains(raw, []byte("[Script Info]")) {
		return raw, nil
	}
	subs, err := astisub.ReadFromSSA(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("subtitles: parse ASS/SSA: %w", err)
	}
	var buf bytes.Buffer
	if err := subs.WriteToSRT(&buf); err != nil {
		return nil, fmt.Errorf("subtitles: write SRT: %w", err)
	}
	return buf.Bytes(), nil
}

// fixUppercase is a placeholder for Bazarr's fix_uppercase heuristic
// (research note §6 item 3): deferred, same posture as ModRemoveTags et al.
// above, until a fixture calls for it.
func fixUppercase(s string) string { return s }
