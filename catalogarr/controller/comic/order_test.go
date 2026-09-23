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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/mediactl/clustarr/catalogarr/controller/comic"
	"github.com/mediactl/clustarr/pkg/k8s"
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
		number    string
		want      string
	}{
		// Plain numbers keep spec §4.2's form unchanged.
		{"single digit issue", "the-walking-dead", "1", "the-walking-dead-001.0"},
		{"double digit issue", "the-walking-dead", "12", "the-walking-dead-012.0"},
		{"half issue", "the-walking-dead", "12.5", "the-walking-dead-012.5"},
		{"wide value is never truncated", "one-piece", "1092", "one-piece-1092.0"},
		{"zero", "annuals", "0", "annuals-000.0"},
		{"negative", "flashpoint", "-1", "flashpoint--01.0"},
		// Lettered numbers carry their letters.
		{"a letter suffix", "saga", "12a", "saga-012.0-a"},
		{"another letter suffix", "saga", "12b", "saga-012.0-b"},
		{"a later letter rounds up but keeps its integer", "saga", "12z", "saga-012.3-z"},
		// Everything else: readable part and digest.
		{"ComicVine suffix", "saga", "12.HU", "saga-012.1-hu-" + k8s.HashSuffix("12.HU")},
		{"text before the number", "saga", "Annual 1", "saga-000.0-annual-1-" + k8s.HashSuffix("Annual 1")},
		{"two decimals", "saga", "12.25", "saga-012.2-12.25-" + k8s.HashSuffix("12.25")},
		{"nothing readable", "saga", "½", "saga-000.5-x-" + k8s.HashSuffix("½")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, comic.IssueName(c.comicName, c.number))
		})
	}
}

// issueNameCorpus is every shape of issue number the naming has to keep
// apart, including the ones the 5.1 form alone collapses: letter suffixes
// (Kapowarr's 12.01 and 12.02 both render 012.0), spellings of one value
// ("12", "12.0", "012"), hundredths the form cannot show ("12.2", "12.25"),
// numbers whose letters push the value past the next tenth ("12z" and
// "12.3z" both render 012.3), case and separator variants, clamped
// magnitudes, and zero's two signs.
var issueNameCorpus = []string{
	"0", "-0", "0.5", "-0.5", "1", "-1", "01", "1.0", "1.5", "1.50",
	"12", "12.0", "012", "12.2", "12.25", "12.3", "12.5",
	"12a", "12b", "12z", "12.3z", "12A", "12 a", "12-a", "12.a", "12aa", "12ab", "12ba",
	"12.HU", "12.hu", "12.MU", "12.NOW", "13.1.HU", "1.2.3",
	"½", "¼", "1½", "∞", "-", "", " ", "?", "1?", "10?",
	"Annual 1", "Annual 2", "annual 1", "TPB", "HC", "1/2", "1-2", "10-11",
	"100000000", "100000001", "99999999a", "99999998a", "inf", "Infinity", "NaN",
	"1e3", "1000", "1_000", "0x10",
}

// TestIssueNameIsInjective: distinct issue numbers never share an Issue name,
// and every name is a valid object name.
func TestIssueNameIsInjective(t *testing.T) {
	seen := map[string]string{}
	for _, number := range issueNameCorpus {
		name := comic.IssueName("saga", number)
		if prev, dup := seen[name]; dup {
			t.Errorf("issue numbers %q and %q share the name %q", prev, number, name)
		}
		seen[name] = number
		assert.Empty(t, validation.IsDNS1123Subdomain(name), "%q -> %q is not a valid object name", number, name)
	}
	for i := 1; i <= 2000; i++ {
		for _, suffix := range []string{"", "a", "b", ".5", ".HU"} {
			number := strconv.Itoa(i) + suffix
			name := comic.IssueName("saga", number)
			if prev, dup := seen[name]; dup && prev != number {
				t.Errorf("issue numbers %q and %q share the name %q", prev, number, name)
			}
			seen[name] = number
		}
	}
}

// TestIssueNameFitsTheNameLimit: a comic name near the limit still yields
// valid, distinct Issue names.
func TestIssueNameFitsTheNameLimit(t *testing.T) {
	long := strings.Repeat("a", 240)
	seen := map[string]string{}
	for _, number := range []string{"1", "2", "12a", "12b", "12.HU", "Annual 1"} {
		name := comic.IssueName(long, number)
		assert.LessOrEqual(t, len(name), k8s.MaxNameLength, "%q", number)
		assert.Empty(t, validation.IsDNS1123Subdomain(name), "%q -> %q", number, name)
		if prev, dup := seen[name]; dup {
			t.Errorf("issue numbers %q and %q share the name %q", prev, number, name)
		}
		seen[name] = number
	}
}
