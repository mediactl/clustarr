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

package manager

import (
	"maps"
	"testing"
)

func TestParseSlotsDefaultsToTheSpecBudget(t *testing.T) {
	// §12: --slots cpu=2,nvidia=1,intel=1.
	for _, in := range []string{"", "   "} {
		got, err := ParseSlots(in)
		if err != nil {
			t.Fatalf("ParseSlots(%q): %v", in, err)
		}
		if !maps.Equal(got, DefaultSlots()) {
			t.Errorf("ParseSlots(%q) = %v, want %v", in, got, DefaultSlots())
		}
	}
	if FormatSlots(DefaultSlots()) != "cpu=2,intel=1,nvidia=1" {
		t.Errorf("FormatSlots(defaults) = %q", FormatSlots(DefaultSlots()))
	}
}

func TestParseSlots(t *testing.T) {
	got, err := ParseSlots("cpu=4,nvidia=2,intel=0")
	if err != nil {
		t.Fatalf("ParseSlots: %v", err)
	}
	want := map[string]int32{HardwareCPU: 4, HardwareNVIDIA: 2, HardwareIntel: 0}
	if !maps.Equal(got, want) {
		t.Fatalf("ParseSlots = %v, want %v", got, want)
	}

	// A partial budget names only the classes it sets; the scheduler treats
	// an absent class as zero, which is how a CPU-only cluster is configured.
	got, err = ParseSlots("cpu=1")
	if err != nil {
		t.Fatalf("ParseSlots: %v", err)
	}
	if !maps.Equal(got, map[string]int32{HardwareCPU: 1}) {
		t.Fatalf("ParseSlots(\"cpu=1\") = %v", got)
	}

	// Whitespace around entries survives a hand-edited manifest.
	got, err = ParseSlots(" cpu = 2 , nvidia = 1 ")
	if err != nil {
		t.Fatalf("ParseSlots with spaces: %v", err)
	}
	if !maps.Equal(got, map[string]int32{HardwareCPU: 2, HardwareNVIDIA: 1}) {
		t.Fatalf("ParseSlots with spaces = %v", got)
	}
}

func TestParseSlotsRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"unknown hardware": "gpu=1",
		"no budget":        "cpu",
		"non-numeric":      "cpu=many",
		"negative":         "cpu=-1",
		"duplicate":        "cpu=1,cpu=2",
	}
	for label, in := range cases {
		if _, err := ParseSlots(in); err == nil {
			t.Errorf("%s: ParseSlots(%q) returned no error", label, in)
		}
	}
}

func TestFormatSlotsRoundTrips(t *testing.T) {
	want := map[string]int32{HardwareCPU: 3, HardwareNVIDIA: 0}
	got, err := ParseSlots(FormatSlots(want))
	if err != nil {
		t.Fatalf("ParseSlots(FormatSlots(...)): %v", err)
	}
	if !maps.Equal(got, want) {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
}
