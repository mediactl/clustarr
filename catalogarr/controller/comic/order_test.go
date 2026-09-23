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

package comic_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/catalogarr/controller/comic"
)

// TestCalculatedNumberCentis pins CalculatedNumberCentis to Kapowarr's own
// answer. Every want below is Kapowarr's value for that input -- produced by
// running its normalise_number, _get_calculated_issue_number,
// extract_issue_number and force_range (Casvt/Kapowarr b51f7abb) under
// Python 3 on these exact strings -- times 100, rounded, and clamped to
// int32. The rows marked "Kapowarr raises" are the inputs its code throws
// on; there the port's own reading is pinned instead.
func TestCalculatedNumberCentis(t *testing.T) {
	cases := []struct {
		name   string
		number string
		want   int32
	}{
		// float()-parseable numbers.
		{"plain integer", "12", 1200},
		{"one decimal", "12.5", 1250},
		{"two decimals", "12.25", 1225},
		{"leading zeroes", "007", 700},
		{"negative", "-1", -100},
		{"zero", "0", 0},
		{"surrounding whitespace, as float() strips", " 7 ", 700},
		{"digit-separating underscore, as float() allows", "1_000", 100000},
		{"exponent, as float() allows", "1e3", 100000},
		{"trailing dot", "50.", 5000},
		// Kapowarr's docstring examples.
		{"docstring: 3.5", "3.5", 350},
		{"docstring: 3 ½", "3 ½", 350},
		{"docstring: -10a", "-10a", -1001},
		{"docstring: 2b", "2b", 202},
		{"docstring: range takes its start", "2½ - 4.5", 250},
		// The character-by-character reading.
		{"letter suffix is its alphabet position", "12a", 1201},
		{"ComicVine-style suffix", "12.HU", 1208},
		{"two dots, the second dropped", "1.2.3", 123},
		{"suffix after a decimal", "13.1.HU", 1311},
		{"MU suffix", "1.MU", 113},
		{"half alone", "½", 50},
		{"negative half", "-½", -50},
		{"comma decimal", "1,5", 150},
		{"unknown digit", "1?", 1000},
		{"hex is not a float to Python", "0x10", 24},
		{"text before the number", "Annual 1", 1},
		{"text only", "TPB", 20},
		{"empty string", "", 0},
		// Ranges.
		{"slash is a range", "1/2", 100},
		{"plain range", "10-11", 1000},
		{"not a range when the start is not a number", "A-1", 1},
		// Past what Kapowarr's float and int32 hundredths share.
		{"NaN is no number", "NaN", 0},
		{"inf clamps", "inf", 2147483647},
		{"Infinity clamps", "Infinity", 2147483647},
		{"infinity sign clamps", "∞", 2147483647},
		{"beyond int32 hundredths clamps", "100000000", 2147483647},
		// Kapowarr raises on these; the port reads one number.
		{"Kapowarr raises: range with an empty end", "5-", 500},
		{"Kapowarr raises: a lone minus", "-", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, comic.CalculatedNumberCentis(c.number))
		})
	}
}

func TestIssueName(t *testing.T) {
	cases := []struct {
		name      string
		comicName string
		centis    int32
		want      string
	}{
		{"single digit issue", "the-walking-dead", 100, "the-walking-dead-001.0"},
		{"double digit issue", "the-walking-dead", 1200, "the-walking-dead-012.0"},
		{"half issue", "the-walking-dead", 1250, "the-walking-dead-012.5"},
		{"wide value is never truncated", "one-piece", 109200, "one-piece-1092.0"},
		{"zero (unparseable number) still pads", "annuals", 0, "annuals-000.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, comic.IssueName(c.comicName, c.centis))
		})
	}
}
