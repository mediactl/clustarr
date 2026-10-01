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

package selfcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ffmpeg9OrSkip skips unless this host has FFmpeg 9's libraries.
func ffmpeg9OrSkip(t *testing.T) {
	t.Helper()
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg libraries: %v", err)
	}
	if _, avcodec, _ := ffgo.Version(); avcodec>>16 != 63 {
		t.Skipf("FFmpeg's libavcodec is %d, not FFmpeg 9's 63", avcodec>>16)
	}
}

func TestCheckPassesOnThisHostsFFmpeg9ForCPU(t *testing.T) {
	ffmpeg9OrSkip(t)
	r, err := Check(context.Background(), ClassCPU)
	require.NoError(t, err)
	assert.Equal(t, 9, r.FFmpegMajor)
	assert.True(t, r.ShimMatched)
	assert.True(t, r.Encoders["libx265"])
	assert.True(t, r.Encoders["aac"])
}

func TestCheckNamesTheFirstMissingEncoder(t *testing.T) {
	ffmpeg9OrSkip(t)
	old := Needs[ClassCPU]
	t.Cleanup(func() { Needs[ClassCPU] = old })
	Needs[ClassCPU] = Need{Encoders: []string{"libx265", "no_such_encoder"}}
	_, err := Check(context.Background(), ClassCPU)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no_such_encoder")
}

// Without a shim built for the loaded FFmpeg, the image is broken: the
// check must say so, by name.
func TestCheckFailsWhenTheShimDoesNotMatch(t *testing.T) {
	if os.Getenv("SELFCHECK_CHILD") == "1" {
		_, err := Check(context.Background(), ClassCPU)
		if err == nil {
			os.Exit(0)
		}
		fmt.Println(err)
		os.Exit(3)
	}
	ffmpeg9OrSkip(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckFailsWhenTheShimDoesNotMatch$", "-test.count=1")
	empty := t.TempDir()
	cmd.Env = append(os.Environ(), "SELFCHECK_CHILD=1", "FFGO_SHIM_DIR="+empty, "LD_LIBRARY_PATH="+empty)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	assert.Contains(t, string(out), "shim")
}

// Phase 2 review #1: an Intel image's VAAPI driver and QSV runtimes are
// dlopen'ed, so finding the encoders by name proves nothing about them; the
// check loads each library the class needs and the libva entry points
// BtbN's stubs call, and names the first that is missing.
func TestCheckNamesAMissingRuntimeLibrary(t *testing.T) {
	ffmpeg9OrSkip(t)
	old := RuntimeLibs[ClassCPU]
	t.Cleanup(func() { RuntimeLibs[ClassCPU] = old })
	RuntimeLibs[ClassCPU] = []RuntimeLib{{Name: "libno-such-runtime.so.9"}}
	_, err := Check(context.Background(), ClassCPU)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "libno-such-runtime.so.9")
}

func TestCheckNamesAMissingSymbol(t *testing.T) {
	ffmpeg9OrSkip(t)
	old := RuntimeLibs[ClassCPU]
	t.Cleanup(func() { RuntimeLibs[ClassCPU] = old })
	RuntimeLibs[ClassCPU] = []RuntimeLib{{Name: "libc.so.6", Symbols: []string{"vaMapBuffer2_not_in_libc"}}}
	_, err := Check(context.Background(), ClassCPU)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vaMapBuffer2_not_in_libc")
}
