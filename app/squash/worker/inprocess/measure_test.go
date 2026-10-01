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

package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
)

func ffmpeg9OrSkip(t *testing.T) {
	t.Helper()
	if _, err := New(); err != nil {
		t.Skipf("no FFmpeg 9 with its shim: %v", err)
	}
}

func TestMeasureTheCPUClass(t *testing.T) {
	ffmpeg9OrSkip(t)
	m, err := Engine{}.Measure(context.Background(), transcode.HardwareCPU)
	require.NoError(t, err)
	assert.Equal(t, transcode.TierCPUx265, m.Tier)
	assert.Nil(t, m.Limits.NVDEC, "the CPU class decodes on the CPU")
}

func TestMeasureNVIDIAOnARealDevice(t *testing.T) {
	ffmpeg9OrSkip(t)
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	if err != nil {
		t.Skipf("no CUDA device: %v", err)
	}
	_ = dev.Close()
	m, err := Engine{}.Measure(context.Background(), transcode.HardwareNVIDIA)
	require.NoError(t, err)
	assert.Equal(t, transcode.TierNVENC, m.Tier)
	require.NotNil(t, m.Limits.NVDEC)
	assert.True(t, m.Limits.NVDEC.Formats["h264:8"], "every NVDEC decodes 8-bit H.264")
	assert.True(t, m.Limits.NVDEC.Formats["hevc:10"], "Turing decodes HEVC Main 10")
	assert.False(t, m.Limits.NVDEC.Formats["h264:10"], "no NVDEC decodes H.264 Hi10P")
}

func TestADeviceThatWillNotOpenIsUnavailable(t *testing.T) {
	ffmpeg9OrSkip(t)
	saved := openDevice
	t.Cleanup(func() { openDevice = saved })
	openDevice = func(transcode.Tier) (*ffgo.HWDevice, error) { return nil, errors.New("no /dev/nvidia0") }
	_, err := Engine{}.Measure(context.Background(), transcode.HardwareNVIDIA)
	require.Error(t, err)
	assert.ErrorIs(t, err, transcode.ErrDeviceUnavailable)
	assert.Contains(t, err.Error(), "no /dev/nvidia0")
}

func TestMeasureIntelOnARealDevice(t *testing.T) {
	ffmpeg9OrSkip(t)
	node := selfcheck.IntelRenderNode()
	if node == "" {
		t.Skip("no Intel render node")
	}
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeVAAPI, node)
	if err != nil {
		t.Skipf("this host cannot open %s (no VA driver here; the transcoder image carries iHD): %v", node, err)
	}
	_ = dev.Close()
	m, err := Engine{}.Measure(context.Background(), transcode.HardwareIntel)
	require.NoError(t, err)
	assert.Contains(t, []transcode.Tier{transcode.TierQSV, transcode.TierVAAPI}, m.Tier)
	t.Logf("the Intel class encodes on %s here", m.Tier)
}
