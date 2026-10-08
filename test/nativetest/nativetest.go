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

// Package nativetest is how a test asks for the native runtime -- FFmpeg 9
// with the ffgo shim, ONNX Runtime, par2go, the parity CLIs -- and either
// skips, naming what is missing, or fails when the run says the tools must be
// there (CLUSTARR_REQUIRE_TOOLS=1, set by make test and CI; PAR2GO_REQUIRE=1
// for par2go). Spec 2026-10-06 §10.3.1, §13 OD44. Imported only by tests.
package nativetest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// Require skips t when err is not nil, or fails it when the tool is required.
func Require(t testing.TB, what string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := fmt.Sprintf("%s is unavailable: %v (run `make native-assets` and test through make, which sets LD_LIBRARY_PATH, FFGO_SHIM_DIR, ORT_LIB_PATH and PAR2GO_LIB)", what, err)
	if required(what) {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

func required(what string) bool {
	// CLUSTARR_FFGO_REQUIRE=1 (CI; upgrade guide U2.1) makes a missing FFmpeg a
	// failure here too, as testmedia.Require does: one switch for every ffgo gate.
	return os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" || (what == "par2go" && os.Getenv("PAR2GO_REQUIRE") == "1") ||
		(strings.HasPrefix(what, "FFmpeg") && os.Getenv("CLUSTARR_FFGO_REQUIRE") == "1")
}

// FFmpeg loads FFmpeg through ffruntime and checks needs. Under
// CLUSTARR_REQUIRE_TOOLS=1 the loaded libavcodec must be the pinned build in
// $CLUSTARR_NATIVE_ASSETS/lib: a host's system FFmpeg 9 cannot be told apart
// from it by version, and the parity oracles run the pinned build.
func FFmpeg(t testing.TB, needs ffruntime.Needs) ffruntime.Report {
	t.Helper()
	rep, err := ffruntime.Load()
	if err == nil {
		err = ffruntime.Require(needs)
	}
	if err == nil {
		err = pinned(rep.AVCodec)
	}
	Require(t, "FFmpeg 9 with the ffgo shim", err)
	return rep
}

func pinned(avcodec string) error {
	if os.Getenv("CLUSTARR_REQUIRE_TOOLS") != "1" {
		return nil
	}
	dir := os.Getenv("CLUSTARR_NATIVE_ASSETS")
	if dir == "" {
		return errors.New("$CLUSTARR_NATIVE_ASSETS is unset: run the native tests through make")
	}
	if lib := filepath.Join(dir, "lib") + "/"; !strings.HasPrefix(avcodec, lib) {
		return fmt.Errorf("libavcodec was mapped from %s, not the pinned build under %s (LD_LIBRARY_PATH must be set before the test process starts)", avcodec, lib)
	}
	return nil
}

// Tool is name's path on PATH.
func Tool(t testing.TB, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	Require(t, name, err)
	return p
}

// SameFFmpeg requires cli (ffmpeg or ffprobe) to be built on the libavcodec
// major.minor ffgo loaded, so a parity comparison compares decoders, not
// FFmpeg releases (spec §7.2.10 "Gate").
func SameFFmpeg(t testing.TB, cli string) {
	t.Helper()
	FFmpeg(t, ffruntime.Needs{})
	out, err := exec.Command(cli, "-hide_banner", "-version").CombinedOutput()
	if err == nil {
		major, minor, ok := libavcodecVersion(string(out))
		_, avc, _ := ffgo.Version()
		switch {
		case !ok:
			err = fmt.Errorf("%s -version printed no libavcodec line", cli)
		case uint32(major) != avc>>16 || uint32(minor) != (avc>>8)&0xff:
			err = fmt.Errorf("%s is libavcodec %d.%d, ffgo loaded %d.%d", cli, major, minor, avc>>16, (avc>>8)&0xff)
		}
	}
	Require(t, cli+" on ffgo's FFmpeg", err)
}

var avcodecLine = regexp.MustCompile(`(?m)^\s*libavcodec\s+(\d+)\.\s*(\d+)\.`)

func libavcodecVersion(out string) (major, minor int, ok bool) {
	m := avcodecLine.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return major, minor, true
}
