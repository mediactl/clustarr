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

// Package par2test makes PAR2 sets for tests with the par2 CLI that `make
// native-assets` installs for this purpose only: par2go has no creator by
// design (spec 2026-10-06 §8.8, OD26). It runs a program, so it lives under
// test/, outside the exec guard, and only tests import it.
//
// A missing tool or library skips the test, naming what is missing, unless
// the run says it must be there: CLUSTARR_REQUIRE_TOOLS=1 (make test, CI)
// for the CLI and the library, PAR2GO_REQUIRE=1 for the library. That is
// test/nativetest's rule (W7.3); this package keeps a copy until nativetest
// is on the batch's base, so pkg/par2child and pkg/download/usenet need not
// wait for it.
package par2test

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	par2 "github.com/mediactl/par2go"
	"github.com/stretchr/testify/require"
)

// CreateSet runs `par2 c -q -b<blocks> -r<pct> [-n<files>] -- <index> <files>` in dir.
//
// -b caps the block count: par2cmdline's default of 2000 blocks over a small
// file makes the Reed-Solomon matrix enormous and a test minutes long, a
// property of the tool and not of anything under test.
func CreateSet(t testing.TB, dir, index string, files []string, blocks, redundancyPercent, recoveryFiles uint32) {
	t.Helper()
	bin := tool(t, "par2")
	args := []string{"c", "-q", fmt.Sprintf("-b%d", blocks), fmt.Sprintf("-r%d", redundancyPercent)}
	if recoveryFiles > 0 {
		args = append(args, fmt.Sprintf("-n%d", recoveryFiles))
	}
	args = append(append(args, "--", index), files...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "par2 create: %s", out)
}

// RequireLibrary skips t when par2go cannot load libpar2shim (PAR2GO_LIB),
// or fails it when the library is required.
func RequireLibrary(t testing.TB) {
	t.Helper()
	need(t, "par2go", par2.Available())
}

// tool is name's path on PATH (make test puts $NATIVE_ASSETS/bin first).
func tool(t testing.TB, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	need(t, name, err)
	return p
}

func need(t testing.TB, what string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := fmt.Sprintf("%s is unavailable: %v (run `make native-assets` and test through make, which sets PATH and PAR2GO_LIB)", what, err)
	if required(what) {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

func required(what string) bool {
	return os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" || (what == "par2go" && os.Getenv("PAR2GO_REQUIRE") == "1")
}
