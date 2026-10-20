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

package asssrt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles/asssrt"
)

// An event's text loses its override blocks, its vector drawings and its
// hard spaces, and \N becomes a line break: what a player that does not
// render ASS can show. Lines are trimmed and empty ones dropped.
func TestText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`Hello`, "Hello"},
		{`{\fad(524,1)\an3\bord2\pos(582.889,276.889)\u1}Rhuda Runebeck\N{\fs15\u0}Hunter level 3`, "Rhuda Runebeck\nHunter level 3"},
		{`"Argent Thunderclap"\N{\u1\fs20}Ark Rodin{\fs15\u0}\NHunter level 7`, "\"Argent Thunderclap\"\nArk Rodin\nHunter level 7"},
		{`{\k20}Ka{\k30}ra{\k25}oke`, "Karaoke"},
		{`{\p1}m 0 0 l 100 0 100 100 0 100{\p0}`, ""},
		{`{\pos(1,2)\p1}m 0 0 l 1 1{\p0}Sign`, "Sign"},
		{`Line one\nline two`, "Line one\nline two"},
		{`Non\hbreaking`, "Non breaking"},
		{`  {\i1}Spaced{\i0}  \N  `, "Spaced"},
	} {
		assert.Equal(t, tc.want, asssrt.Text(tc.in), "%q", tc.in)
	}
}

// FFmpeg's ASS packets carry "ReadOrder,Layer,Style,Name,MarginL,MarginR,
// MarginV,Effect,Text", the timing in the packet; the text is the last
// field, commas and all.
func TestPacketText(t *testing.T) {
	assert.Equal(t, "Wait, what?", asssrt.PacketText(`3,0,Default,Krai,0,0,0,,{\i1}Wait, what?{\i0}`))
	assert.Equal(t, "", asssrt.PacketText(`3,0,Sign,,0,0,0,,{\p1}m 0 0 l 1 1{\p0}`))
}

const script = "\ufeff[Script Info]\r\nScriptType: v4.00+\r\n\r\n" +
	"[V4+ Styles]\r\nFormat: Name, Fontname, Fontsize, PrimaryColour\r\nStyle: Sign,Arial,20,&H00000000\r\n\r\n" +
	"[Events]\r\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n" +
	"Dialogue: 0,0:04:01.38,0:04:05.43,Sign,Text,0,0,0,,{\\fad(700,1)\\fs30\\bord2}Ark Brave\r\n" +
	"Dialogue: 0,0:01:49.21,0:01:53.71,Sign,txt,0,0,0,,{\\an3\\pos(582.889,276.889)\\u1}Rhuda Runebeck\\N{\\fs15\\u0}Hunter level 3\r\n" +
	"Dialogue: 1,0:01:49.21,0:01:53.71,Sign,txt,0,0,0,,{\\an3\\pos(582.889,276.889)\\blur4}Rhuda Runebeck\\N{\\fs15}Hunter level 3\r\n" +
	"Comment: 0,0:02:00.00,0:02:01.00,Default,,0,0,0,,translator note\r\n" +
	"Dialogue: 0,0:02:10.00,0:02:12.00,Sign,,0,0,0,,{\\p1}m 0 0 l 100 0 100 100{\\p0}\r\n" +
	"Dialogue: 0,1:02:03.04,1:02:04.50,Default,Luke,0,0,0,,Let's go, then.\r\n"

// A script converts to plain SubRip: a cue per Dialogue event with text,
// in start order; a sign drawn in two layers is one cue; comments and
// drawings are no cues. ASS times are centiseconds.
func TestConvert(t *testing.T) {
	out, err := asssrt.Convert([]byte(script))
	require.NoError(t, err)
	assert.Equal(t, "1\n00:01:49,210 --> 00:01:53,710\nRhuda Runebeck\nHunter level 3\n\n"+
		"2\n00:04:01,380 --> 00:04:05,430\nArk Brave\n\n"+
		"3\n01:02:03,040 --> 01:02:04,500\nLet's go, then.\n\n", string(out))
}

// A script whose Format names its fields in another order, as SSA v4's
// does ("Marked" for "Layer"), is read by the names.
func TestConvertReadsTheFormatLine(t *testing.T) {
	ssa := "[Script Info]\nScriptType: v4.00\n\n[Events]\n" +
		"Format: Marked, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: Marked=0,0:00:01.50,0:00:02.00,Default,,0000,0000,0000,,Hi, there\n"
	out, err := asssrt.Convert([]byte(ssa))
	require.NoError(t, err)
	assert.Equal(t, "1\n00:00:01,500 --> 00:00:02,000\nHi, there\n\n", string(out))
}

// A script with no event that has text converts to nothing, and says so:
// a sidecar is never replaced by an empty one.
func TestConvertRefusesAScriptWithNoText(t *testing.T) {
	_, err := asssrt.Convert([]byte("[Script Info]\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.00,Sign,,0,0,0,,{\\p1}m 0 0 l 1 1{\\p0}\n"))
	require.ErrorIs(t, err, asssrt.ErrNoCues)
	_, err = asssrt.Convert([]byte("1\n00:00:01,000 --> 00:00:02,000\nnot ASS\n"))
	require.ErrorIs(t, err, asssrt.ErrNotASS)
}
