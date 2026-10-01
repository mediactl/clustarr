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
	"os"
	"path/filepath"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrialEncodesOnNVIDIA(t *testing.T) {
	ffmpeg9OrSkip(t)
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	if err != nil {
		t.Skipf("no CUDA device: %v", err)
	}
	_ = dev.Close()
	r, err := Trial(context.Background(), ClassCUDA, t.TempDir())
	require.NoError(t, err)
	require.Contains(t, r.Trials, "nvdec-nvenc")
	require.Contains(t, r.Trials, "upload-nvenc")
	assert.Empty(t, r.Trials["nvdec-nvenc"])
	assert.Empty(t, r.Trials["upload-nvenc"])
}

func TestTrialEncodesOnIntel(t *testing.T) {
	ffmpeg9OrSkip(t)
	node := IntelRenderNode()
	if node == "" {
		t.Skip("no Intel render node")
	}
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeVAAPI, node)
	if err != nil {
		t.Skipf("no VAAPI driver on this host for %s: %v", node, err)
	}
	_ = dev.Close()
	r, err := Trial(context.Background(), ClassIntel, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, r.Trials["vaapi"])
}

func TestTrialOnTheCPUIsTheCheck(t *testing.T) {
	ffmpeg9OrSkip(t)
	r, err := Trial(context.Background(), ClassCPU, t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, r.Trials)
}

// Phase 2 review #2: /sys/class/drm is the host's in a container; a node
// the container was not given must not be chosen (VAAPI then falls back to
// X11, and BtbN's libX11 stub aborts the process).
func TestIntelRenderNodeIgnoresANodeTheContainerDoesNotHave(t *testing.T) {
	sys, dev := t.TempDir(), t.TempDir()
	for _, n := range []struct{ name, vendor string }{{"renderD128", "0x8086"}, {"renderD129", "0x10de"}, {"renderD130", "0x8086"}} {
		require.NoError(t, os.MkdirAll(filepath.Join(sys, n.name, "device"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(sys, n.name, "device", "vendor"), []byte(n.vendor+"\n"), 0o644))
	}
	assert.Equal(t, "", intelRenderNode(sys, dev), "no node in /dev/dri")
	require.NoError(t, os.WriteFile(filepath.Join(dev, "renderD130"), nil, 0o666))
	assert.Equal(t, filepath.Join(dev, "renderD130"), intelRenderNode(sys, dev), "the Intel node the container has")
}
