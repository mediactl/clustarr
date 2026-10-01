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

package cardigann

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// dotNetNamedBlocks is .NET's table of named Unicode blocks, the
// \p{IsName} / \P{IsName} escapes (RegexCharClass's block list; names are
// case-sensitive). regexp2 implements .NET's categories and scripts but
// not these, and 35 bundled definitions strip Cyrillic or CJK titles with
// \p{IsCyrillic} or \p{IsCJKUnifiedIdeographs}, so expandNamedBlocks
// rewrites each into its code-point range before compiling.
var dotNetNamedBlocks = map[string][2]rune{
	"IsAlphabeticPresentationForms":         {0xFB00, 0xFB4F},
	"IsArabic":                              {0x0600, 0x06FF},
	"IsArabicPresentationForms-A":           {0xFB50, 0xFDFF},
	"IsArabicPresentationForms-B":           {0xFE70, 0xFEFF},
	"IsArmenian":                            {0x0530, 0x058F},
	"IsArrows":                              {0x2190, 0x21FF},
	"IsBasicLatin":                          {0x0000, 0x007F},
	"IsBengali":                             {0x0980, 0x09FF},
	"IsBlockElements":                       {0x2580, 0x259F},
	"IsBopomofo":                            {0x3100, 0x312F},
	"IsBopomofoExtended":                    {0x31A0, 0x31BF},
	"IsBoxDrawing":                          {0x2500, 0x257F},
	"IsBraillePatterns":                     {0x2800, 0x28FF},
	"IsBuhid":                               {0x1740, 0x175F},
	"IsCJKCompatibility":                    {0x3300, 0x33FF},
	"IsCJKCompatibilityForms":               {0xFE30, 0xFE4F},
	"IsCJKCompatibilityIdeographs":          {0xF900, 0xFAFF},
	"IsCJKRadicalsSupplement":               {0x2E80, 0x2EFF},
	"IsCJKSymbolsandPunctuation":            {0x3000, 0x303F},
	"IsCJKUnifiedIdeographs":                {0x4E00, 0x9FFF},
	"IsCJKUnifiedIdeographsExtensionA":      {0x3400, 0x4DBF},
	"IsCherokee":                            {0x13A0, 0x13FF},
	"IsCombiningDiacriticalMarks":           {0x0300, 0x036F},
	"IsCombiningDiacriticalMarksforSymbols": {0x20D0, 0x20FF},
	"IsCombiningHalfMarks":                  {0xFE20, 0xFE2F},
	"IsCombiningMarksforSymbols":            {0x20D0, 0x20FF},
	"IsControlPictures":                     {0x2400, 0x243F},
	"IsCurrencySymbols":                     {0x20A0, 0x20CF},
	"IsCyrillic":                            {0x0400, 0x04FF},
	"IsCyrillicSupplement":                  {0x0500, 0x052F},
	"IsDevanagari":                          {0x0900, 0x097F},
	"IsDingbats":                            {0x2700, 0x27BF},
	"IsEnclosedAlphanumerics":               {0x2460, 0x24FF},
	"IsEnclosedCJKLettersandMonths":         {0x3200, 0x32FF},
	"IsEthiopic":                            {0x1200, 0x137F},
	"IsGeneralPunctuation":                  {0x2000, 0x206F},
	"IsGeometricShapes":                     {0x25A0, 0x25FF},
	"IsGeorgian":                            {0x10A0, 0x10FF},
	"IsGreek":                               {0x0370, 0x03FF},
	"IsGreekExtended":                       {0x1F00, 0x1FFF},
	"IsGreekandCoptic":                      {0x0370, 0x03FF},
	"IsGujarati":                            {0x0A80, 0x0AFF},
	"IsGurmukhi":                            {0x0A00, 0x0A7F},
	"IsHalfwidthandFullwidthForms":          {0xFF00, 0xFFEF},
	"IsHangulCompatibilityJamo":             {0x3130, 0x318F},
	"IsHangulJamo":                          {0x1100, 0x11FF},
	"IsHangulSyllables":                     {0xAC00, 0xD7AF},
	"IsHanunoo":                             {0x1720, 0x173F},
	"IsHebrew":                              {0x0590, 0x05FF},
	"IsHighPrivateUseSurrogates":            {0xDB80, 0xDBFF},
	"IsHighSurrogates":                      {0xD800, 0xDB7F},
	"IsHiragana":                            {0x3040, 0x309F},
	"IsIPAExtensions":                       {0x0250, 0x02AF},
	"IsIdeographicDescriptionCharacters":    {0x2FF0, 0x2FFF},
	"IsKanbun":                              {0x3190, 0x319F},
	"IsKangxiRadicals":                      {0x2F00, 0x2FDF},
	"IsKannada":                             {0x0C80, 0x0CFF},
	"IsKatakana":                            {0x30A0, 0x30FF},
	"IsKatakanaPhoneticExtensions":          {0x31F0, 0x31FF},
	"IsKhmer":                               {0x1780, 0x17FF},
	"IsKhmerSymbols":                        {0x19E0, 0x19FF},
	"IsLao":                                 {0x0E80, 0x0EFF},
	"IsLatin-1Supplement":                   {0x0080, 0x00FF},
	"IsLatinExtended-A":                     {0x0100, 0x017F},
	"IsLatinExtended-B":                     {0x0180, 0x024F},
	"IsLatinExtendedAdditional":             {0x1E00, 0x1EFF},
	"IsLetterlikeSymbols":                   {0x2100, 0x214F},
	"IsLimbu":                               {0x1900, 0x194F},
	"IsLowSurrogates":                       {0xDC00, 0xDFFF},
	"IsMalayalam":                           {0x0D00, 0x0D7F},
	"IsMathematicalOperators":               {0x2200, 0x22FF},
	"IsMiscellaneousMathematicalSymbols-A":  {0x27C0, 0x27EF},
	"IsMiscellaneousMathematicalSymbols-B":  {0x2980, 0x29FF},
	"IsMiscellaneousSymbols":                {0x2600, 0x26FF},
	"IsMiscellaneousSymbolsandArrows":       {0x2B00, 0x2BFF},
	"IsMiscellaneousTechnical":              {0x2300, 0x23FF},
	"IsMongolian":                           {0x1800, 0x18AF},
	"IsMyanmar":                             {0x1000, 0x109F},
	"IsNumberForms":                         {0x2150, 0x218F},
	"IsOgham":                               {0x1680, 0x169F},
	"IsOpticalCharacterRecognition":         {0x2440, 0x245F},
	"IsOriya":                               {0x0B00, 0x0B7F},
	"IsPhoneticExtensions":                  {0x1D00, 0x1D7F},
	"IsPrivateUse":                          {0xE000, 0xF8FF},
	"IsPrivateUseArea":                      {0xE000, 0xF8FF},
	"IsRunic":                               {0x16A0, 0x16FF},
	"IsSinhala":                             {0x0D80, 0x0DFF},
	"IsSmallFormVariants":                   {0xFE50, 0xFE6F},
	"IsSpacingModifierLetters":              {0x02B0, 0x02FF},
	"IsSpecials":                            {0xFFF0, 0xFFFF},
	"IsSuperscriptsandSubscripts":           {0x2070, 0x209F},
	"IsSupplementalArrows-A":                {0x27F0, 0x27FF},
	"IsSupplementalArrows-B":                {0x2900, 0x297F},
	"IsSupplementalMathematicalOperators":   {0x2A00, 0x2AFF},
	"IsSyriac":                              {0x0700, 0x074F},
	"IsTagalog":                             {0x1700, 0x171F},
	"IsTagbanwa":                            {0x1760, 0x177F},
	"IsTaiLe":                               {0x1950, 0x197F},
	"IsTamil":                               {0x0B80, 0x0BFF},
	"IsTelugu":                              {0x0C00, 0x0C7F},
	"IsThaana":                              {0x0780, 0x07BF},
	"IsThai":                                {0x0E00, 0x0E7F},
	"IsTibetan":                             {0x0F00, 0x0FFF},
	"IsUnifiedCanadianAboriginalSyllabics":  {0x1400, 0x167F},
	"IsVariationSelectors":                  {0xFE00, 0xFE0F},
	"IsYiRadicals":                          {0xA490, 0xA4CF},
	"IsYiSyllables":                         {0xA000, 0xA48F},
	"IsYijingHexagramSymbols":               {0x4DC0, 0x4DFF},
}

// expandNamedBlocks rewrites every \p{IsName} and \P{IsName} escape whose
// name is in dotNetNamedBlocks into the equivalent character-class ranges:
// outside a class into a class of its own ([lo-hi] or [^lo-hi]), inside one
// into ranges (the complement's two ranges for \P). Everything else is
// copied untouched, an escaped backslash included, and an unknown name is
// left for the compiler to reject.
func expandNamedBlocks(pattern string) string {
	if !strings.Contains(pattern, "{Is") {
		return pattern
	}
	var b strings.Builder
	depth := 0 // character-class nesting; .NET subtraction nests [a-z-[aeiou]]
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern):
			next := pattern[i+1]
			if next == 'p' || next == 'P' {
				if name, end, ok := blockEscape(pattern, i+2); ok {
					r := dotNetNamedBlocks[name]
					b.WriteString(blockClass(r[0], r[1], next == 'P', depth > 0))
					i = end
					continue
				}
			}
			b.WriteByte(c)
			b.WriteByte(next)
			i++
			continue
		case c == '[':
			if depth == 0 || (i > 0 && pattern[i-1] == '-') {
				depth++
				b.WriteByte(c)
				// A ']' first in a class (after an optional '^') is a literal.
				if i+1 < len(pattern) && pattern[i+1] == '^' {
					b.WriteByte('^')
					i++
				}
				if i+1 < len(pattern) && pattern[i+1] == ']' {
					b.WriteByte(']')
					i++
				}
				continue
			}
		case c == ']' && depth > 0:
			depth--
		}
		b.WriteByte(c)
	}
	return b.String()
}

// blockEscape reads "{IsName}" at pattern[at:], returning the name and the
// index of the closing brace when the name is a known .NET block.
func blockEscape(pattern string, at int) (string, int, bool) {
	if at >= len(pattern) || pattern[at] != '{' {
		return "", 0, false
	}
	end := strings.IndexByte(pattern[at:], '}')
	if end < 0 {
		return "", 0, false
	}
	name := pattern[at+1 : at+end]
	if _, ok := dotNetNamedBlocks[name]; !ok {
		return "", 0, false
	}
	return name, at + end, true
}

// blockClass renders the block lo-hi, negated or not, either as ranges
// inside an enclosing class or as a class of its own.
func blockClass(lo, hi rune, negate, inClass bool) string {
	if !inClass {
		if negate {
			return "[^" + classRange(lo, hi) + "]"
		}
		return "[" + classRange(lo, hi) + "]"
	}
	if !negate {
		return classRange(lo, hi)
	}
	var s string
	if lo > 0 {
		s += classRange(0, lo-1)
	}
	if hi < utf8.MaxRune {
		s += classRange(hi+1, utf8.MaxRune)
	}
	return s
}

// classRange is lo-hi in class syntax; a code point above the BMP, which
// .NET's \u cannot name, is written as the character itself.
func classRange(lo, hi rune) string {
	return classChar(lo) + "-" + classChar(hi)
}

func classChar(r rune) string {
	if r > 0xFFFF {
		return string(r)
	}
	return fmt.Sprintf(`\u%04X`, r)
}
