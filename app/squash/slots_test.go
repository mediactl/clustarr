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

package squasharr

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

func TestRolesAndValidate(t *testing.T) {
	if !RoleController.RunsControllers() {
		t.Error("the controller role should run controllers")
	}
	if Role("worker").Valid() {
		t.Error("--role worker is gone: the pools run cmd/squasharr-worker")
	}
	if Role("nonsense").Valid() {
		t.Error("an unknown role reported valid")
	}
	if got := Roles(); len(got) != 1 || got[0] != RoleController {
		t.Errorf("Roles() = %v, want only %s", got, RoleController)
	}

	o := DefaultOptions()
	o.Namespace = "clustarr"
	o.WorkerImage = "ghcr.io/mediactl/clustarr/media:dev"
	if err := o.Validate(); err != nil {
		t.Fatalf("the default options plus a worker image are invalid: %v", err)
	}
	if o.WorkerServiceAccount != DefaultWorkerServiceAccount || o.DataClaimName == "" {
		t.Errorf("DefaultOptions lost the worker ServiceAccount or data claim: %+v", o)
	}

	// The controller stamps the worker image onto every pool it creates.
	o.WorkerImage = ""
	if err := o.Validate(); err == nil {
		t.Error("a controller without --worker-image was accepted")
	}
	o.WorkerImage = "ghcr.io/mediactl/clustarr/media:dev"
	o.WorkerServiceAccount = ""
	if err := o.Validate(); err == nil {
		t.Error("a controller without --worker-service-account was accepted; its pods would run as the namespace default")
	}

	// Tasks are dispatched on the bus: a controller without one could
	// dispatch nothing.
	o = DefaultOptions()
	o.Namespace = "clustarr"
	o.WorkerImage = "ghcr.io/mediactl/clustarr/media:dev"
	o.NATSURL = ""
	if err := o.Validate(); err == nil {
		t.Error("a controller without --nats-url was accepted")
	}
}
