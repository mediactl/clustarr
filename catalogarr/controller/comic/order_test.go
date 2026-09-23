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

func TestCalculatedNumberCentis(t *testing.T) {
	cases := []struct {
		name   string
		number string
		want   int32
	}{
		{"plain integer", "12", 1200},
		{"one decimal", "12.5", 1250},
		{"two decimals rounds", "12.25", 1225},
		{"leading zeroes", "007", 700},
		{"trailing junk after the number", "12.HU", 1200},
		{"text before the number", "Annual 1", 100},
		{"no numeric token at all", "TPB", 0},
		{"empty string", "", 0},
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
