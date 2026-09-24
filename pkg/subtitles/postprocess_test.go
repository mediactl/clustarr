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
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asticode/go-astisub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestFixMojibakeRepairsUTF8MisdecodedAsCP1252(t *testing.T) {
	got := subtitles.FixMojibake("Welcome to the cafÃ©.")
	assert.Equal(t, "Welcome to the café.", got)
}

func TestFixMojibakeLeavesCleanUTF8Alone(t *testing.T) {
	got := subtitles.FixMojibake("Welcome to the café.")
	assert.Equal(t, "Welcome to the café.", got)
}

func TestDecodeToUTF8HandlesUTF8BOM(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...)
	out, err := subtitles.PostProcess(wrapSRT(raw), "en", nil, true)
	require.NoError(t, err)
	assert.True(t, utf8.Valid(out))
	assert.NotContains(t, string(out), string([]byte{0xEF, 0xBB, 0xBF}), "BOM must be stripped")
}

func TestPostProcessOnAMojibakeFixture(t *testing.T) {
	raw, err := os.ReadFile("../../test/data/subtitles/postprocess/mojibake_latin1.srt")
	require.NoError(t, err)

	out, err := subtitles.PostProcess(raw, "en", nil, true)
	require.NoError(t, err)
	assert.Contains(t, string(out), "café")
	assert.True(t, utf8.Valid(out))
}

// wrapSRT turns a bare BOM+text blob into a minimal one-cue SRT so
// PostProcess's SRT parser has something structurally valid to read.
func wrapSRT(text []byte) []byte {
	return append([]byte("1\n00:00:01,000 --> 00:00:02,000\n"), append(text, '\n')...)
}

func TestPostProcessConvertsASSToSRTAndAppliesRemoveHI(t *testing.T) {
	raw, err := os.ReadFile("../../test/data/subtitles/postprocess/hi_sample.ass")
	require.NoError(t, err)

	out, err := subtitles.PostProcess(raw, "en", []string{subtitles.ModRemoveHI}, true)
	require.NoError(t, err)

	text := string(out)
	assert.Contains(t, text, "-->", "output must be SRT-shaped (arrow separator), not ASS")
	assert.NotContains(t, text, "[Script Info]")
	assert.NotContains(t, text, "door slams", "removeHI must have stripped the bracketed cue")
	assert.Contains(t, text, "Welcome to the café.")
}

func TestPostProcessKeepsOriginalFormatWhenToSRTIsFalse(t *testing.T) {
	raw, err := os.ReadFile("../../test/data/subtitles/postprocess/hi_sample.ass")
	require.NoError(t, err)

	out, err := subtitles.PostProcess(raw, "en", nil, false)
	require.NoError(t, err)
	assert.Contains(t, string(out), "[Script Info]", "originalFormat must skip SRT conversion")
}

// srtCueRe is a strict structural assertion for one SRT cue block: a
// decimal index, a timestamp line, one or more non-empty text lines, no
// blank line inside the cue itself. Used to validate the *whole* document
// by splitting on the blank-line cue separator first (see
// assertWellFormedSRT).
var srtCueRe = regexp.MustCompile(`^\d+\r?\n\d{2}:\d{2}:\d{2},\d{3} --> \d{2}:\d{2}:\d{2},\d{3}\r?\n(?:[^\r\n]+\r?\n?)+$`)

// assertWellFormedSRT re-parses out with go-astisub (a second, independent
// check beyond the regex) and additionally asserts, cue by cue via regex,
// that every cue is exactly `index\ntimestamp\ntext+\n\n` (the last cue may
// lack the final trailing blank line — go-astisub's own writer trims
// exactly one trailing newline from the whole document) — this is the
// structural check a flattened, non-cue-aware mod pass could silently
// break (an emptied HI-only cue leaving an orphaned index+timestamp with no
// text and no separator).
func assertWellFormedSRT(t *testing.T, out []byte, wantCues int) *astisub.Subtitles {
	t.Helper()
	require.False(t, strings.Contains(string(out), "\n\n\n"), "no doubled blank-line separators")

	blocks := strings.Split(strings.TrimRight(string(out), "\n"), "\n\n")
	require.Len(t, blocks, wantCues, "cue-block count via blank-line splitting")
	for i, b := range blocks {
		assert.Regexp(t, srtCueRe, b+"\n", "cue %d must be index\\ntimestamp\\ntext+\\n", i+1)
	}

	subs, err := astisub.ReadFromSRT(strings.NewReader(string(out)))
	require.NoError(t, err)
	require.Len(t, subs.Items, wantCues)
	for i, item := range subs.Items {
		assert.Equal(t, i+1, item.Index, "cues must be renumbered sequentially after drops")
		assert.NotEmpty(t, strings.TrimSpace(item.String()), "no empty cue may survive")
	}
	return subs
}

func TestPostProcessDropsAWhollyHICueAndKeepsCueStructureValid(t *testing.T) {
	const threeCueSRT = "1\n00:00:01,000 --> 00:00:02,000\nHello there.\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\n[door slams]\n\n" +
		"3\n00:00:05,000 --> 00:00:06,000\nGoodbye.\n"

	out, err := subtitles.PostProcess([]byte(threeCueSRT), "en", []string{subtitles.ModRemoveHI}, true)
	require.NoError(t, err)

	subs := assertWellFormedSRT(t, out, 2)
	assert.Equal(t, "Hello there.", subs.Items[0].String())
	assert.Equal(t, "Goodbye.", subs.Items[1].String())
	assert.NotContains(t, string(out), "door slams")
}

func TestPostProcessOnHISampleASSProducesAWellFormedSingleCueSRT(t *testing.T) {
	raw, err := os.ReadFile("../../test/data/subtitles/postprocess/hi_sample.ass")
	require.NoError(t, err)

	out, err := subtitles.PostProcess(raw, "en", []string{subtitles.ModRemoveHI}, true)
	require.NoError(t, err)

	// The [door slams] cue must be dropped entirely (research note's HI
	// stripping operates cue-by-cue, not by mangling the flattened
	// document), leaving exactly one well-formed cue.
	subs := assertWellFormedSRT(t, out, 1)
	assert.Equal(t, "Welcome to the café.", subs.Items[0].String())
	assert.False(t, strings.HasPrefix(string(out), "\xef\xbb\xbf"), "go-astisub's WriteToSRT BOM must be stripped from PostProcess output")
}

func TestPostProcessMalformedInputsDoNotPanic(t *testing.T) {
	// Truncated ASS: valid [Script Info]/[Events] header, but the second
	// Dialogue line is cut mid-field so it has fewer columns than the
	// declared Format — go-astisub's own SSA event builder rejects this
	// with a real error (verified directly against the library, not
	// guessed) rather than silently ignoring it like a fully-garbled line.
	const truncatedASS = "[Script Info]\nTitle: X\n\n[V4+ Styles]\n" +
		"Format: Name, Fontname\nStyle: Default,Arial\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello there\n" +
		"Dialogue: 0,0:00:03.00,0:00"

	tests := []struct {
		name string
		raw  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"binary garbage", []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x80, 0x81}},
		{"truncated SRT (index only, no timestamp)", []byte("1\n")},
		{"truncated SRT (mid timestamp)", []byte("1\n00:00:01,000 --")},
		{"truncated ASS (mid-Dialogue)", []byte(truncatedASS)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out []byte
			var err error
			require.NotPanics(t, func() {
				out, err = subtitles.PostProcess(tt.raw, "en", nil, true)
			})
			// Every case above parses to zero usable cues (or a genuine
			// structural parse failure for the truncated-ASS case), so
			// PostProcess must return a non-nil error rather than silently
			// fabricating output — there is no documented pass-through case
			// among these inputs.
			assert.Error(t, err)
			assert.Nil(t, out)
		})
	}
}

func TestPostProcessTruncatedSRTWithRecoverableTextDoesNotPanicAndSucceeds(t *testing.T) {
	// A cue whose text was cut off mid-line (no trailing newline) is still
	// one recoverable cue to go-astisub's scanner-based reader — this is
	// the "documented pass-through" half of the malformed-input contract,
	// verified directly against the library rather than assumed.
	var out []byte
	var err error
	require.NotPanics(t, func() {
		out, err = subtitles.PostProcess([]byte("1\n00:00:01,000 --> 00:00:02,000\nHello"), "en", nil, true)
	})
	require.NoError(t, err)
	assertWellFormedSRT(t, out, 1)
}
