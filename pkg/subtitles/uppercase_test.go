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

package subtitles_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/asticode/go-astisub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

// TestCapitalizeUppercaseMatchesBazarr pins the capitalisation step to
// Bazarr's own FixUppercase.capitalize. Every expected value was produced by
// running common.py's split_upper_re and capitalize verbatim under Python 3
// -- including the quirks a faithful port keeps: "I" mid-sentence becomes
// "i", a bare line break starts no new capital, and a piece opening with a
// quote or a space gets no capital at all.
func TestCapitalizeUppercaseMatchesBazarr(t *testing.T) {
	for in, want := range map[string]string{
		"HELLO THERE. HOW ARE YOU?":       "Hello there. How are you?",
		"- WHERE ARE YOU GOING?\n- HOME.": "- Where are you going?\n- Home.",
		"I'M NOT SURE... MAYBE!":          "I'm not sure... Maybe!",
		"♪ LA LA LA ♪":                    "♪ La la la ♪",
		"WAIT-WHAT?":                      "Wait-What?",
		`"STOP RIGHT THERE," HE SAID.`:    `"stop right there," he said.`,
		"HELLO\nWORLD":                    "Hello\nworld",
		"ÉCOUTE-MOI. ÇA VA?":              "Écoute-Moi. Ça va?",
		"WHAT DID I SAY? I SAID NO.":      "What did i say? I said no.",
		"MR. SMITH IS HERE":               "Mr. Smith is here",
		"":                                "",
		"  LEADING SPACE":                 "  leading space",
		// Python's \s is Unicode-aware; RE2's is ASCII-only. A no-break
		// space or a vertical tab after the full stop is still part of the
		// separator in Bazarr, so "WORLD" starts a new capital.
		"HELLO.\u00a0WORLD": "Hello.\u00a0World",
		"HELLO.\vWORLD":     "Hello.\vWorld",
	} {
		assert.Equal(t, want, subtitles.CapitalizeUppercase(in), "input %q", in)
	}
}

// srtOf renders one cue per text, one second apart.
func srtOf(texts ...string) []byte {
	var b bytes.Buffer
	for i, text := range texts {
		fmt.Fprintf(&b, "%d\n00:00:%02d,000 --> 00:00:%02d,500\n%s\n\n", i+1, i%60, i%60, text)
	}
	return b.Bytes()
}

func repeat(text string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = text
	}
	return out
}

func postProcessItems(t *testing.T, raw []byte, mods ...string) []*astisub.Item {
	t.Helper()
	out, err := subtitles.PostProcess(raw, "en", mods, true)
	require.NoError(t, err)
	subs, err := astisub.ReadFromSRT(bytes.NewReader(out))
	require.NoError(t, err)
	return subs.Items
}

// A shouted line has 23 upper-case letters; six of them are 138, past
// detect_uppercase's "more than 100" floor.
const shouted = "WHERE ARE YOU GOING TONIGHT?"

func TestFixUppercaseCapitalisesAMostlyUppercaseSubtitle(t *testing.T) {
	cues := append(repeat(shouted, 5), "- I'M NOT SURE.\n- WELL, DECIDE!")
	items := postProcessItems(t, srtOf(cues...), subtitles.ModFixUppercase)

	require.Len(t, items, 6)
	assert.Equal(t, "Where are you going tonight?", items[0].String())
	require.Len(t, items[5].Lines, 2)
	assert.Equal(t, "- I'm not sure.", items[5].Lines[0].String())
	assert.Equal(t, "- Well, decide!", items[5].Lines[1].String())
}

// The mod is gated on the subtitle being mostly upper case: a normal
// mixed-case file with a few shouted lines is left exactly as it was
// (prepare_mods: "Skipping %s, because the subtitle isn't all uppercase").
func TestFixUppercaseLeavesAMixedCaseSubtitleAlone(t *testing.T) {
	cues := append(repeat("Where are you going tonight, my friend?", 8), "NO WAY!", "STOP RIGHT THERE, EVERYONE!")
	items := postProcessItems(t, srtOf(cues...), subtitles.ModFixUppercase)

	require.Len(t, items, 10)
	assert.Equal(t, "NO WAY!", items[8].String())
	assert.Equal(t, "STOP RIGHT THERE, EVERYONE!", items[9].String())
}

// detect_uppercase needs more than MINIMUM_UPPERCASE_COUNT (100) upper-case
// letters, not just a high percentage: three shouted lines are 69 letters,
// all upper case, and still not enough to call the file upper case.
func TestFixUppercaseNeedsMoreThanAHundredUppercaseLetters(t *testing.T) {
	items := postProcessItems(t, srtOf(repeat(shouted, 3)...), subtitles.ModFixUppercase)

	require.Len(t, items, 3)
	assert.Equal(t, shouted, items[0].String())
}

// apply_last: fix_uppercase runs after the line mods wherever the profile
// lists it. Run first, it would turn "JOHN:" into "John:", which remove_HI's
// upper-case speaker-label rule no longer recognises.
func TestFixUppercaseRunsAfterRemoveHIWhateverTheOrder(t *testing.T) {
	cues := append(repeat(shouted, 5), "JOHN: WHERE ARE YOU?")
	items := postProcessItems(t, srtOf(cues...), subtitles.ModFixUppercase, subtitles.ModRemoveHI)

	require.Len(t, items, 6)
	assert.Equal(t, "Where are you?", items[5].String())
}

// "skip HI bracket entries, those might actually be lowercase": the
// bracketed sound cues are stripped before counting. Here they hold 40
// lower-case letters against 138 upper-case ones -- 77.5%, under the 90%
// bar if they were counted -- and the file is still upper case.
func TestFixUppercaseDetectionIgnoresLowercaseHIBrackets(t *testing.T) {
	cues := append(repeat(shouted, 6), "[footsteps approaching]", "[footsteps approaching]")
	items := postProcessItems(t, srtOf(cues...), subtitles.ModFixUppercase)

	require.Len(t, items, 8)
	assert.Equal(t, "Where are you going tonight?", items[0].String())
	assert.Equal(t, "[footsteps approaching]", items[6].String(), "a piece opening with '[' is lower-cased, as Bazarr's str.capitalize does")
}

// Only the first MAXIMUM_ENTRIES (50) non-empty entries are sampled: fifty
// shouted cues followed by a long mixed-case tail still read as an
// upper-case file.
func TestFixUppercaseSamplesOnlyTheFirstFiftyEntries(t *testing.T) {
	cues := append(repeat("HELLO THERE.", 50), repeat("hello there my friend, how are you doing today?", 60)...)
	items := postProcessItems(t, srtOf(cues...), subtitles.ModFixUppercase)

	require.Len(t, items, 110)
	assert.Equal(t, "Hello there.", items[0].String())
	assert.Equal(t, "Hello there my friend, how are you doing today?", items[109].String())
}

// The rest of the postprocess suite never lists fixUppercase; this guards
// the one interaction it has with the others -- validation still rejects
// an unknown mod, and listing fixUppercase does not change that.
func TestFixUppercaseDoesNotRelaxModValidation(t *testing.T) {
	_, err := subtitles.PostProcess(srtOf(shouted), "en", []string{subtitles.ModFixUppercase, "bogus"}, true)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "unknown mod"), "got %v", err)
}
