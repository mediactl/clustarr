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

// PostProcess is spec §7's exact signature: decode to UTF-8, parse into
// cues, convert to SRT unless !toSRT (profile.originalFormat), apply mods
// per cue (never over the flattened document — see applyMods), always
// return valid UTF-8 SRT (or the original format's UTF-8 bytes).
//
// Cue-awareness matters because a mod can empty a cue's text entirely (the
// canonical case: ModRemoveHI on a cue that is nothing but a bracketed
// sound cue). Operating on the whole rendered document as flat text — the
// pre-fix-round implementation — drops blank-line cue separators wholesale
// and leaves a hollowed-out cue as an orphaned index+timestamp with no
// text, corrupting the SRT structure for every cue after it. Parsing with
// go-astisub first, applying mods to each Item's own text, dropping items
// that become empty, and letting go-astisub's writer renumber and
// re-separate cues on the way out keeps the document well-formed no matter
// which cues a mod removes.
func PostProcess(raw []byte, lang string, mods []string, toSRT bool) ([]byte, error) {
	decoded, err := decodeToUTF8(raw, lang)
	if err != nil {
		return nil, err
	}

	subs, isASS, err := parseSubtitles(decoded)
	if err != nil {
		return nil, fmt.Errorf("subtitles: parse: %w", err)
	}

	if err := applyMods(subs, mods); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if isASS && !toSRT {
		err = subs.WriteToSSA(&buf)
	} else {
		err = subs.WriteToSRT(&buf)
	}
	if err != nil {
		return nil, fmt.Errorf("subtitles: write output: %w", err)
	}
	// go-astisub's WriteToSRT unconditionally prepends a UTF-8 BOM
	// (astisub.BytesBOM); WriteToSSA does not, so this is a harmless no-op
	// on that path. PostProcess's own contract is "always return valid
	// UTF-8" content, not a BOM-prefixed stream.
	return bytes.TrimPrefix(buf.Bytes(), astisub.BytesBOM), nil
}

// parseSubtitles sniffs ASS/SSA by content ("[Script Info]" section header,
// research note §6 item 4) and parses accordingly; anything else is parsed
// as SRT.
func parseSubtitles(raw []byte) (subs *astisub.Subtitles, isASS bool, err error) {
	if bytes.Contains(raw, []byte("[Script Info]")) {
		subs, err = astisub.ReadFromSSA(bytes.NewReader(raw))
		return subs, true, err
	}
	subs, err = astisub.ReadFromSRT(bytes.NewReader(raw))
	return subs, false, err
}

// applyMods validates mods once up front (so an unknown mod name is
// rejected even when subs has zero items — validating lazily inside the
// per-item loop below would silently accept a bogus mod name whenever
// there happened to be nothing to process), then runs FixMojibake
// (unconditional, research note §6 item 2) and each requested mod over
// every item's own text independently. An item whose text becomes empty
// after mods is dropped outright — never left as a hollow cue — and
// go-astisub's writers renumber remaining items positionally, so no manual
// index bookkeeping is needed here.
//
// ModFixUppercase follows Bazarr's two extra rules for it (uppercase.go):
// it applies only when the subtitle, as parsed and before any mod ran, is
// mostly upper case, and it applies after every other mod, wherever the
// profile lists it. Because it acts on each cue on its own, running it at
// the end of each cue's chain is the same as Bazarr's whole-file pass after
// the line mods.
func applyMods(subs *astisub.Subtitles, mods []string) error {
	fixUpper := false
	for _, mod := range mods {
		switch mod {
		case ModFixUppercase:
			fixUpper = true
		case ModRemoveHI, ModRemoveTags, ModOCRFixes, ModCommon, ModReverseRTL, ModColor:
		default:
			return fmt.Errorf("subtitles: unknown mod %q", mod)
		}
	}
	if fixUpper {
		upper, err := mostlyUppercase(subs.Items)
		if err != nil {
			return fmt.Errorf("subtitles: fixUppercase: %w", err)
		}
		fixUpper = upper
	}

	kept := subs.Items[:0]
	for _, item := range subs.Items {
		text := FixMojibake(itemText(item))
		for _, mod := range mods {
			switch mod {
			case ModRemoveHI:
				var err error
				text, err = RemoveHI(text)
				if err != nil {
					return fmt.Errorf("subtitles: removeHI: %w", err)
				}
			case ModFixUppercase:
				// Applied after the loop, gated on mostlyUppercase.
			case ModRemoveTags, ModOCRFixes, ModCommon, ModReverseRTL, ModColor:
				// Deferred: no fixture-verified behaviour for these yet.
				// Recognised (not an error) so a profile listing them does
				// not fail PostProcess; add a real implementation + test
				// the same way as ModRemoveHI once one is needed by the
				// captionarr worker.
			}
		}
		if fixUpper {
			text = fixUppercase(text)
		}
		if strings.TrimSpace(text) == "" {
			continue // the cue's text vanished entirely — drop the cue itself
		}
		setItemText(item, text)
		kept = append(kept, item)
	}
	subs.Items = kept
	return nil
}

// itemText joins an item's Lines/LineItems into a single "\n"-separated
// string. RemoveHI and the other line-based mods operate on this joined
// form, scoped to one cue instead of (as before this fix round) the whole
// flattened document.
func itemText(item *astisub.Item) string {
	lines := make([]string, len(item.Lines))
	for i, l := range item.Lines {
		lines[i] = l.String()
	}
	return strings.Join(lines, "\n")
}

// setItemText replaces item's Lines with one Line per non-empty "\n"
// segment of text, each holding a single LineItem. This loses any
// per-segment InlineStyle detail go-astisub's parser attached (bold/colour
// runs) — an accepted simplification, since every mod this package
// implements (RemoveHI, FixMojibake, fixUppercase) operates on plain text.
func setItemText(item *astisub.Item, text string) {
	segments := strings.Split(text, "\n")
	lines := make([]astisub.Line, 0, len(segments))
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		lines = append(lines, astisub.Line{Items: []astisub.LineItem{{Text: seg}}})
	}
	item.Lines = lines
}
