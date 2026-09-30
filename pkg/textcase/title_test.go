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

package textcase_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/textcase"
)

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{
		"the brothers karamazov":           "The Brothers Karamazov",
		"The Lock And Key Library":         "The Lock and Key Library",
		"notes from the underground":       "Notes from the Underground",
		"what we talk about: a life":       "What We Talk About: A Life",
		"my uncle's dream":                 "My Uncle's Dream",
		"self-portrait in a convex mirror": "Self-Portrait in a Convex Mirror",
		"NASA and the iPhone":              "NASA and the iPhone",
		"letters vol ii":                   "Letters Vol II",
		"the civil mix":                    "The Civil Mix",
		"1984":                             "1984",
		"war and peace – part one":         "War and Peace – Part One",
		"crime and punishment - a novel":   "Crime and Punishment - A Novel",
		"  of mice  and men ":              "  Of Mice  and Men ",
		"Beyaz Geceler":                    "Beyaz Geceler",
		"the idiot (penguin classics)":     "The Idiot (Penguin Classics)",
		"\"the gambler\" and other tales":  "\"The Gambler\" and Other Tales",
		"a raw youth":                      "A Raw Youth",
		"what it is up to":                 "What It Is Up To",
		"McCarthy's the road":              "McCarthy's the Road",
		"":                                 "",
	} {
		assert.Equal(t, want, textcase.Title(in), "Title(%q)", in)
	}
}
