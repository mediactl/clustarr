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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
	"github.com/mediactl/clustarr/test/nativetest"
)

// newEngine is the engine a pool pod of class builds, behind the gate.
func newEngine(t *testing.T, class transcode.Hardware) Engine {
	t.Helper()
	nativetest.FFmpeg(t, needsFor(class))
	e, err := New(class)
	require.NoError(t, err)
	return e
}

// A pool pod's engine requires its class's encoders, muxers and filters --
// the image's self-check list (spec 2026-10-06 §7.4): every class encodes
// AAC and AC-3 beside its video encoder (standard Version 2's surround
// tracks) and writes through mp4, srt and ass. New refuses a class whose
// needs this FFmpeg lacks, naming what is missing, so the pod takes no work
// it would fail.
func TestNewRequiresItsClassesEncoders(t *testing.T) {
	nativetest.FFmpeg(t, ffruntime.Needs{})
	assert.Equal(t, []string{"libx265", "aac", "ac3", "hevc_nvenc"}, needsFor(transcode.HardwareNVIDIA).Encoders)
	assert.Equal(t, []string{"libx265", "aac", "ac3"}, needsFor(transcode.HardwareCPU).Encoders)
	assert.Equal(t, []string{"mp4", "srt", "ass"}, needsFor(transcode.HardwareCPU).Muxers)
	assert.Equal(t, selfcheck.Needs[selfcheck.ClassIntel].Encoders, needsFor(transcode.HardwareIntel).Encoders)
	assert.Equal(t, selfcheck.Needs[selfcheck.ClassIntel].Filters, needsFor(transcode.HardwareIntel).Filters)
	assert.Equal(t, selfcheck.Needs[selfcheck.ClassCUDA].Filters, needsFor(transcode.HardwareNVIDIA).Filters)

	e := newEngine(t, transcode.HardwareCPU)
	assert.NotNil(t, e.prober, "New builds the in-process prober")

	old := selfcheck.Needs[selfcheck.ClassCPU]
	t.Cleanup(func() { selfcheck.Needs[selfcheck.ClassCPU] = old })
	n := old
	n.Encoders = append(append([]string{}, old.Encoders...), "no_such_encoder")
	n.Muxers = append(append([]string{}, old.Muxers...), "no_such_muxer")
	selfcheck.Needs[selfcheck.ClassCPU] = n
	_, err := New(transcode.HardwareCPU)
	require.Error(t, err, "a class whose needs this FFmpeg lacks has no engine")
	assert.Contains(t, err.Error(), "no_such_encoder")
	assert.Contains(t, err.Error(), "no_such_muxer")
}
