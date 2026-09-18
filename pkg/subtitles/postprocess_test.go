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
	"testing"
	"unicode/utf8"

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
	raw, err := os.ReadFile("../../testdata/subtitles/postprocess/mojibake_latin1.srt")
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
	raw, err := os.ReadFile("../../testdata/subtitles/postprocess/hi_sample.ass")
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
	raw, err := os.ReadFile("../../testdata/subtitles/postprocess/hi_sample.ass")
	require.NoError(t, err)

	out, err := subtitles.PostProcess(raw, "en", nil, false)
	require.NoError(t, err)
	assert.Contains(t, string(out), "[Script Info]", "originalFormat must skip SRT conversion")
}
